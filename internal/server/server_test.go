package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

// ─── shared test fixtures ────────────────────────────────────────────

// stubReleasesBackend implements datasource.RepoBackend for tests. It
// serves a fixed tag list and records how many times ListTagHistory
// is called — an observable proxy for whether the minimumReleaseAge
// path ran. lastRepo captures the last repo argument so
// multi-segment routing can be verified end-to-end.
type stubReleasesBackend struct {
	tags      []chainguard.Tag
	listErr   error
	histCalls atomic.Int32
	lastRepo  atomic.Value // string
	readyErr  error
}

func (b *stubReleasesBackend) ListAllRepos(_ context.Context) ([]string, error) {
	return nil, nil
}
func (b *stubReleasesBackend) ListTags(_ context.Context, repo string) ([]chainguard.Tag, error) {
	b.lastRepo.Store(repo)
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.tags, nil
}
func (b *stubReleasesBackend) ListTagHistory(_ context.Context, _ string) ([]chainguard.TagHistory, error) {
	b.histCalls.Add(1)
	return nil, nil
}
func (b *stubReleasesBackend) Ready(_ context.Context) error { return b.readyErr }

// stubRepoDatasource wraps a stubReleasesBackend in a RepoDatasource
// with a single tag old enough to clear any minimumReleaseAge cutoff.
func stubRepoDatasource() *datasource.RepoDatasource {
	return datasource.NewRepoDatasource(&stubReleasesBackend{tags: []chainguard.Tag{
		{ID: "1", Name: "1", LastUpdated: time.Now().Add(-30 * 24 * time.Hour), Digest: "sha256:x"},
	}}, 0)
}

// mustNew builds a Server and t.Fatal's on error. Used by call sites
// that supply datasources and shouldn't be checking the error every
// time.
func mustNew(t *testing.T, opts ...Option) *Server {
	t.Helper()
	srv, err := New(opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

// ─── New ─────────────────────────────────────────────────────────────

func TestNew_NoDatasourcesFails(t *testing.T) {
	if _, err := New(); err == nil {
		t.Errorf("New() with no datasources: got nil error, want failure")
	}
}

// ─── /readyz ─────────────────────────────────────────────────────────

func TestReadyz_DatasourceNotReady(t *testing.T) {
	backend := &stubReleasesBackend{readyErr: errors.New("token expired")}
	h := mustNew(t, WithDatasource("repo", datasource.NewRepoDatasource(backend, 0))).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_DatasourceReady(t *testing.T) {
	h := mustNew(t, WithDatasource("repo", stubRepoDatasource())).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// ─── route registration ──────────────────────────────────────────────

// TestDatasource_RouteRegistration verifies that route registration keys
// off whether a source is attached: attached names get a handler,
// anything else stays unregistered and falls through to the mux's 404.
func TestDatasource_RouteRegistration(t *testing.T) {
	h := mustNew(t, WithDatasource("repo", stubRepoDatasource())).Handler()

	for path, want := range map[string]int{
		"/v1/repo/foo/releases":  http.StatusOK,
		"/v1/other/foo/releases": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d (body: %s)", path, rec.Code, want, rec.Body.String())
		}
	}
}

// ─── /v1/repo/{path...}/releases ─────────────────────────────────────

// TestHandleReleases_MultiSegmentPath confirms the /v1/repo/{path...}
// dispatcher passes multi-segment paths (used for Helm charts under
// `charts/…` and `iamguarded-charts/…`) straight through to the backend.
func TestHandleReleases_MultiSegmentPath(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		wantRepo   string
		wantStatus int
	}{
		{
			name:       "single-segment image",
			url:        "/v1/repo/python/releases",
			wantRepo:   "python",
			wantStatus: http.StatusOK,
		},
		{
			name:       "two-segment charts subrepo",
			url:        "/v1/repo/charts/kube-prometheus-stack/releases",
			wantRepo:   "charts/kube-prometheus-stack",
			wantStatus: http.StatusOK,
		},
		{
			name:       "two-segment iamguarded-charts subrepo",
			url:        "/v1/repo/iamguarded-charts/nginx/releases",
			wantRepo:   "iamguarded-charts/nginx",
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing /releases suffix returns 404",
			url:        "/v1/repo/charts/nginx",
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &stubReleasesBackend{tags: []chainguard.Tag{
				{ID: "1", Name: "1", LastUpdated: time.Now().Add(-30 * 24 * time.Hour), Digest: "sha256:x"},
			}}
			h := mustNew(t, WithDatasource("repo", datasource.NewRepoDatasource(backend, 0))).Handler()

			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			got, _ := backend.lastRepo.Load().(string)
			if got != tc.wantRepo {
				t.Errorf("backend saw repo=%q, want %q", got, tc.wantRepo)
			}
		})
	}
}

func TestHandleReleasesMinimumReleaseAgeQueryParam(t *testing.T) {
	// A single "fresh" tag whose current digest is newer than any
	// positive minimumReleaseAge cutoff — so minimumReleaseAge>0 will
	// trigger a history walk while minimumReleaseAge=0 will
	// short-circuit to TagsAsReleases.
	freshTag := chainguard.Tag{
		ID:          "latest",
		Name:        "latest",
		LastUpdated: time.Now(),
		Digest:      "sha256:fresh",
	}

	tests := []struct {
		name                    string
		serverMinimumReleaseAge time.Duration
		query                   string
		wantStatus              int
		wantHistCalls           int32
		wantReleases            int
	}{
		{
			name:                    "no query, server default 0 → pass-through, no history walk",
			serverMinimumReleaseAge: 0,
			query:                   "",
			wantStatus:              http.StatusOK,
			wantHistCalls:           0,
			wantReleases:            1,
		},
		{
			name:                    "query minimumReleaseAge=168h overrides server default 0 → history walk",
			serverMinimumReleaseAge: 0,
			query:                   "?minimumReleaseAge=168h",
			wantStatus:              http.StatusOK,
			wantHistCalls:           1,
			wantReleases:            0, // no history entries → tag omitted
		},
		{
			name:                    "query minimumReleaseAge=0 overrides server default 168h → pass-through",
			serverMinimumReleaseAge: 168 * time.Hour,
			query:                   "?minimumReleaseAge=0",
			wantStatus:              http.StatusOK,
			wantHistCalls:           0,
			wantReleases:            1,
		},
		{
			name:                    "invalid minimumReleaseAge returns 400",
			serverMinimumReleaseAge: 0,
			query:                   "?minimumReleaseAge=not-a-duration",
			wantStatus:              http.StatusBadRequest,
		},
		{
			name:                    "negative minimumReleaseAge returns 400",
			serverMinimumReleaseAge: 0,
			query:                   "?minimumReleaseAge=-1h",
			wantStatus:              http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &stubReleasesBackend{tags: []chainguard.Tag{freshTag}}
			h := mustNew(t,
				WithMinimumReleaseAge(tc.serverMinimumReleaseAge),
				WithDatasource("repo", datasource.NewRepoDatasource(backend, 0)),
			).Handler()

			req := httptest.NewRequest(http.MethodGet, "/v1/repo/foo/releases"+tc.query, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if got := backend.histCalls.Load(); got != tc.wantHistCalls {
				t.Errorf("ListTagHistory calls = %d, want %d", got, tc.wantHistCalls)
			}
			if tc.wantStatus == http.StatusOK {
				var resp datasource.Response
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if len(resp.Releases) != tc.wantReleases {
					t.Errorf("releases = %d, want %d (%+v)", len(resp.Releases), tc.wantReleases, resp.Releases)
				}
			}
		})
	}
}

// ─── /releases error mapping ─────────────────────────────────────────

// TestHandleReleases_UnknownPackage confirms a datasource returning
// ErrNotFound becomes a 404 rather than a 502.
func TestHandleReleases_UnknownPackage(t *testing.T) {
	backend := &stubReleasesBackend{listErr: chainguard.ErrRepoNotFound}
	h := mustNew(t, WithDatasource("repo", datasource.NewRepoDatasource(backend, 0))).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/repo/nonesuch/releases", nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestHandleReleases_MalformedName confirms a name the datasource
// rejects becomes a 400, surfacing the source's own message, rather
// than reaching the backend at all.
func TestHandleReleases_MalformedName(t *testing.T) {
	backend := &stubReleasesBackend{}
	h := mustNew(t, WithDatasource("repo", datasource.NewRepoDatasource(backend, 0))).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/repo/.escape/releases", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
	if got, _ := backend.lastRepo.Load().(string); got != "" {
		t.Errorf("backend saw repo=%q, want no call", got)
	}
}

// TestHandleReleases_UpstreamFailure confirms an unclassified backend
// error becomes a 502 rather than leaking as a 500.
func TestHandleReleases_UpstreamFailure(t *testing.T) {
	backend := &stubReleasesBackend{listErr: errors.New("grpc unavailable")}
	h := mustNew(t, WithDatasource("repo", datasource.NewRepoDatasource(backend, 0))).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/repo/python/releases", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body: %s)", rec.Code, rec.Body.String())
	}
}

// ─── parseMinimumReleaseAgeQuery ─────────────────────────────────────

func TestParseMinimumReleaseAgeQuery(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantMsg string
		wantOk  bool
	}{
		{name: "valid short", raw: "168h", want: 168 * time.Hour, wantOk: true},
		{name: "valid at max", raw: "8760h", want: maxMinimumReleaseAge, wantOk: true},
		{name: "zero", raw: "0", want: 0, wantOk: true},
		{name: "negative", raw: "-1h", wantMsg: "non-negative"},
		{name: "malformed", raw: "not-a-duration", wantMsg: "non-negative"},
		{name: "over max", raw: "8761h", wantMsg: "exceeds the maximum"},
		{name: "far over max", raw: "1000000h", wantMsg: "exceeds the maximum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg, ok := parseMinimumReleaseAgeQuery(tc.raw)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v (msg=%q)", ok, tc.wantOk, msg)
			}
			if ok && got != tc.want {
				t.Errorf("d = %v, want %v", got, tc.want)
			}
			if !ok && tc.wantMsg != "" && !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("msg = %q, want to contain %q", msg, tc.wantMsg)
			}
		})
	}
}
