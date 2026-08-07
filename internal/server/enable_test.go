package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestEnable_RouteRegistration verifies that WithRepoEnabled and
// WithAPKEnabled gate route registration. A disabled route responds 404
// (mux has no entry), an enabled route responds with whatever its handler
// produces — status 200 for a repo probe against the nil-tag fake backend,
// 501 for the apk probe with no index attached.
func TestEnable_RouteRegistration(t *testing.T) {
	tests := []struct {
		name       string
		opts       []Option
		wantRepo   int
		wantAPK    int
	}{
		{
			name:     "both enabled by default",
			opts:     nil,
			wantRepo: http.StatusOK,
			wantAPK:  http.StatusNotImplemented,
		},
		{
			name:     "apk disabled",
			opts:     []Option{WithAPKEnabled(false)},
			wantRepo: http.StatusOK,
			wantAPK:  http.StatusNotFound,
		},
		{
			name:     "repo disabled",
			opts:     []Option{WithRepoEnabled(false)},
			wantRepo: http.StatusNotFound,
			wantAPK:  http.StatusNotImplemented,
		},
		{
			name:     "both disabled",
			opts:     []Option{WithRepoEnabled(false), WithAPKEnabled(false)},
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
