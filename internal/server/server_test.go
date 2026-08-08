package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

// ─── shared test fixtures ────────────────────────────────────────────

// readyBackend implements Readier for tests. Zero-value returns nil
// (ready); set err to simulate a not-ready backend.
type readyBackend struct{ err error }

func (r *readyBackend) Ready(context.Context) error { return r.err }

// stubReleasesBackend implements datasource.RepoBackend for tests. It
// serves a fixed tag list and records how many times ListTagHistory
// is called — an observable proxy for whether the cooldown path ran.
// lastRepo captures the last repo argument so multi-segment routing
// can be verified end-to-end.
type stubReleasesBackend struct {
	tags      []chainguard.Tag
	histCalls atomic.Int32
	lastRepo  atomic.Value // string
}

func (b *stubReleasesBackend) ListAllRepos(_ context.Context) ([]string, error) {
	return nil, nil
}
func (b *stubReleasesBackend) ListTags(_ context.Context, repo string) ([]chainguard.Tag, error) {
	b.lastRepo.Store(repo)
	return b.tags, nil
}
func (b *stubReleasesBackend) ListTagHistory(_ context.Context, _ string) ([]chainguard.TagHistory, error) {
	b.histCalls.Add(1)
	return nil, nil
}

// buildAPKDatasource stamps a synthetic index into an APKDatasource so
// handler tests can exercise cooldown / sorting / 404s without
// touching the network.
func buildAPKDatasource(entries map[string][]apk.PackageVersion) *datasource.APKDatasource {
	store := apk.NewIndexStore()
	store.Replace(entries)
	return datasource.NewAPKDatasource(store)
}

// serverWithFixedNow builds a Server with the given options and pins
// its clock to frozen so cooldown cutoffs are deterministic.
func serverWithFixedNow(frozen time.Time, opts ...Option) *Server {
	srv := New(nil, opts...)
	srv.now = func() time.Time { return frozen }
	return srv
}

// ─── /readyz ─────────────────────────────────────────────────────────

func TestReadyz_NoAPKDatasource(t *testing.T) {
	h := New(&readyBackend{}).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_APKDatasourceEmpty(t *testing.T) {
	// An unpopulated index simulates the case where NewIndexStoreWithRefresh
	// returned an empty store because the initial load failed. APKDatasource.Ready
	// surfaces that via its Len() == 0 check.
	empty := datasource.NewAPKDatasource(apk.NewIndexStore())
	h := New(&readyBackend{}, WithAPKDatasource(empty)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_APKDatasourcePopulated(t *testing.T) {
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{"foo": {{Version: "1.0.0"}}})
	h := New(&readyBackend{}, WithAPKDatasource(datasource.NewAPKDatasource(store))).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

// ─── route registration ──────────────────────────────────────────────

// TestDatasource_RouteRegistration verifies that route registration keys
// off whether a source is attached: no source → route unregistered
// (404); source attached → handler runs.
func TestDatasource_RouteRegistration(t *testing.T) {
	apkDS := datasource.NewAPKDatasource(func() *apk.IndexStore {
		s := apk.NewIndexStore()
		s.Replace(map[string][]apk.PackageVersion{"foo": {{Version: "1.0.0", Timestamp: time.Unix(1, 0).UTC()}}})
		return s
	}())
	repoDS := datasource.NewRepoDatasource(&stubReleasesBackend{tags: []chainguard.Tag{
		{ID: "1", Name: "1", LastUpdated: time.Now().Add(-30 * 24 * time.Hour), Digest: "sha256:x"},
	}}, 0)

	tests := []struct {
		name     string
		opts     []Option
		wantRepo int
		wantAPK  int
	}{
		{
			name:     "both datasources attached",
			opts:     []Option{WithRepoDatasource(repoDS), WithAPKDatasource(apkDS)},
			wantRepo: http.StatusOK,
			wantAPK:  http.StatusOK,
		},
		{
			name:     "repo only",
			opts:     []Option{WithRepoDatasource(repoDS)},
			wantRepo: http.StatusOK,
			wantAPK:  http.StatusNotFound,
		},
		{
			name:     "apk only",
			opts:     []Option{WithAPKDatasource(apkDS)},
			wantRepo: http.StatusNotFound,
			wantAPK:  http.StatusOK,
		},
		{
			name:     "neither",
			opts:     nil,
			wantRepo: http.StatusNotFound,
			wantAPK:  http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := New(&readyBackend{}, tc.opts...).Handler()

			for path, want := range map[string]int{
				"/v1/repo/foo/releases": tc.wantRepo,
				"/v1/apk/foo/releases":  tc.wantAPK,
			} {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != want {
					t.Errorf("%s: status = %d, want %d (body: %s)", path, rec.Code, want, rec.Body.String())
				}
			}
		})
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
			h := New(&readyBackend{}, WithRepoDatasource(datasource.NewRepoDatasource(backend, 0))).Handler()

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

func TestHandleReleasesCooldownQueryParam(t *testing.T) {
	// A single "fresh" tag whose current digest is newer than any positive
	// cooldown cutoff — so cooldown>0 will trigger a history walk while
	// cooldown=0 will short-circuit to TagsAsReleases.
	freshTag := chainguard.Tag{
		ID:          "latest",
		Name:        "latest",
		LastUpdated: time.Now(),
		Digest:      "sha256:fresh",
	}

	tests := []struct {
		name           string
		serverCooldown time.Duration
		query          string
		wantStatus     int
		wantHistCalls  int32
		wantReleases   int
	}{
		{
			name:           "no query, server default 0 → pass-through, no history walk",
			serverCooldown: 0,
			query:          "",
			wantStatus:     http.StatusOK,
			wantHistCalls:  0,
			wantReleases:   1,
		},
		{
			name:           "query cooldown=168h overrides server default 0 → history walk",
			serverCooldown: 0,
			query:          "?cooldown=168h",
			wantStatus:     http.StatusOK,
			wantHistCalls:  1,
			wantReleases:   0, // no history entries → tag omitted
		},
		{
			name:           "query cooldown=0 overrides server default 168h → pass-through",
			serverCooldown: 168 * time.Hour,
			query:          "?cooldown=0",
			wantStatus:     http.StatusOK,
			wantHistCalls:  0,
			wantReleases:   1,
		},
		{
			name:           "invalid cooldown returns 400",
			serverCooldown: 0,
			query:          "?cooldown=not-a-duration",
			wantStatus:     http.StatusBadRequest,
		},
		{
			name:           "negative cooldown returns 400",
			serverCooldown: 0,
			query:          "?cooldown=-1h",
			wantStatus:     http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &stubReleasesBackend{tags: []chainguard.Tag{freshTag}}
			h := New(&readyBackend{},
				WithCooldown(tc.serverCooldown),
				WithRepoDatasource(datasource.NewRepoDatasource(backend, 0)),
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

// ─── /v1/apk/{name}/releases ─────────────────────────────────────────

func TestHandleAPKReleases_UnknownPackage(t *testing.T) {
	h := New(nil, WithAPKDatasource(buildAPKDatasource(map[string][]apk.PackageVersion{
		"known": {{Version: "1.0.0"}},
	}))).Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/nonesuch/releases", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestHandleAPKReleases_NoDatasource(t *testing.T) {
	// No APK source attached → the route isn't registered → 404.
	// Callers relying on 501 to detect intentional disablement should
	// probe /readyz instead.
	h := New(nil).Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHandleAPKReleases_CooldownFilterAndSort(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }

	ds := buildAPKDatasource(map[string][]apk.PackageVersion{
		"foo": {
			{Version: "1.0.0-r0", Timestamp: day(30)},
			{Version: "1.1.0-r0", Timestamp: day(15)},
			// Within a 7d cooldown, this one is too fresh and should be filtered out.
			{Version: "1.2.0-r0", Timestamp: day(1)},
			// Zero timestamp — treated as old enough and always included.
			{Version: "0.9.0-r0", Timestamp: time.Time{}},
		},
	})
	h := serverWithFixedNow(now, WithAPKDatasource(ds)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases?cooldown=168h", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp datasource.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Expect 1.2.0-r0 filtered out; remaining three ordered newest-first
	// (zero-timestamp last since Time{} sorts as the oldest possible time).
	wantVersions := []string{"1.1.0-r0", "1.0.0-r0", "0.9.0-r0"}
	if len(resp.Releases) != len(wantVersions) {
		t.Fatalf("got %d releases, want %d (%+v)", len(resp.Releases), len(wantVersions), resp.Releases)
	}
	for i, want := range wantVersions {
		if resp.Releases[i].Version != want {
			t.Errorf("release[%d].Version = %q, want %q", i, resp.Releases[i].Version, want)
		}
	}
}

func TestHandleAPKReleases_NoCooldownReturnsAll(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	ds := buildAPKDatasource(map[string][]apk.PackageVersion{
		"foo": {
			{Version: "1.0.0-r0", Timestamp: now.AddDate(0, 0, -1)},
			{Version: "1.1.0-r0", Timestamp: now},
		},
	})
	h := serverWithFixedNow(now, WithAPKDatasource(ds)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp datasource.Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Releases) != 2 {
		t.Errorf("expected all releases without cooldown, got %d", len(resp.Releases))
	}
}

func TestHandleAPKReleases_BadCooldown(t *testing.T) {
	h := New(nil, WithAPKDatasource(buildAPKDatasource(map[string][]apk.PackageVersion{
		"foo": {{Version: "1.0.0-r0"}},
	}))).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases?cooldown=whatever", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAPKReleases_MalformedName(t *testing.T) {
	h := New(nil, WithAPKDatasource(buildAPKDatasource(nil))).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/.escape/releases", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAPKReleases_PrefixedProvidesName(t *testing.T) {
	// A caller pinning against a capability (e.g. `apk add cmd:node=…`)
	// hits /v1/apk/cmd:node/releases. The releases endpoint accepts the
	// prefixed form so those lookups don't 400 at the routing layer.
	ds := buildAPKDatasource(map[string][]apk.PackageVersion{
		"cmd:node": {{Version: "24.14.0-r0", Timestamp: time.Unix(1_700_000_000, 0).UTC()}},
	})
	h := New(nil, WithAPKDatasource(ds)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/cmd:node/releases", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp datasource.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Releases) != 1 || resp.Releases[0].Version != "24.14.0-r0" {
		t.Errorf("releases = %+v, want one entry at 24.14.0-r0", resp.Releases)
	}
}

// ─── parseCooldownQuery ──────────────────────────────────────────────

func TestParseCooldownQuery(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantMsg string
		wantOk  bool
	}{
		{name: "valid short", raw: "168h", want: 168 * time.Hour, wantOk: true},
		{name: "valid at max", raw: "8760h", want: maxCooldown, wantOk: true},
		{name: "zero", raw: "0", want: 0, wantOk: true},
		{name: "negative", raw: "-1h", wantMsg: "non-negative"},
		{name: "malformed", raw: "not-a-duration", wantMsg: "non-negative"},
		{name: "over max", raw: "8761h", wantMsg: "exceeds the maximum"},
		{name: "far over max", raw: "1000000h", wantMsg: "exceeds the maximum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg, ok := parseCooldownQuery(tc.raw)
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
