package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chainguard"
)

type chartReleasesBackend struct {
	tags        []chainguard.Tag
	seenRepo    atomic.Value // string
	listCalls   atomic.Int32
}

func (b *chartReleasesBackend) ListTags(_ context.Context, repo string) ([]chainguard.Tag, error) {
	b.seenRepo.Store(repo)
	b.listCalls.Add(1)
	return b.tags, nil
}
func (b *chartReleasesBackend) ListTagHistory(_ context.Context, _ string) ([]chainguard.TagHistory, error) {
	return nil, nil
}
func (b *chartReleasesBackend) Ready(_ context.Context) error { return nil }

func TestHandleChartReleases_ComposesSubrepo(t *testing.T) {
	tag := chainguard.Tag{ID: "1.0", Name: "1.0", LastUpdated: time.Unix(0, 0), Digest: "sha256:abc"}

	tests := []struct {
		name     string
		path     string
		wantRepo string
	}{
		{
			name:     "charts flavor prepends charts/",
			path:     "/v1/charts/kube-prometheus-stack/releases",
			wantRepo: "charts/kube-prometheus-stack",
		},
		{
			name:     "iamguarded flavor prepends iamguarded-charts/",
			path:     "/v1/iamguarded-charts/kube-prometheus-stack/releases",
			wantRepo: "iamguarded-charts/kube-prometheus-stack",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &chartReleasesBackend{tags: []chainguard.Tag{tag}}
			h := New(backend, nil).Handler()

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
			}
			if got := backend.seenRepo.Load(); got != tc.wantRepo {
				t.Errorf("ListTags repo = %q, want %q", got, tc.wantRepo)
			}
			var resp ReleasesResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(resp.Releases) != 1 || resp.Releases[0].Version != tag.Name {
				t.Errorf("releases = %+v, want single release for %q", resp.Releases, tag.Name)
			}
		})
	}
}

// TestChartPrefix_MatchesRepoPattern locks in the invariant that
// serveChartReleases relies on: composing `<prefix>/<name>` where
// name passed chartNamePattern still satisfies repoNamePattern, so
// the delegation to handleReleases doesn't silently 400.
func TestChartPrefix_MatchesRepoPattern(t *testing.T) {
	names := []string{"kube-prometheus-stack", "a", "a-b", "a.b", "a_b"}
	prefixes := []string{chartsPrefix, iamguardedChartsPrefix}
	for _, p := range prefixes {
		for _, n := range names {
			if !chartNamePattern.MatchString(n) {
				t.Fatalf("test bug: %q doesn't match chartNamePattern", n)
			}
			composed := p + "/" + n
			if !repoNamePattern.MatchString(composed) {
				t.Errorf("%q composed from chart-valid name %q fails repoNamePattern", composed, n)
			}
		}
	}
}

func TestHandleChartReleases_RejectsBadName(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"slash", "/v1/charts/foo%2Fbar/releases"},
		{"uppercase", "/v1/charts/Foo/releases"},
		{"leading dot", "/v1/charts/.foo/releases"},
		{"iamguarded slash", "/v1/iamguarded-charts/foo%2Fbar/releases"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			backend := &chartReleasesBackend{}
			h := New(backend, nil).Handler()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400 (body: %s)", tc.path, rec.Code, rec.Body.String())
			}
			if backend.listCalls.Load() != 0 {
				t.Errorf("%s: backend was called; validation should have short-circuited", tc.path)
			}
		})
	}
}
