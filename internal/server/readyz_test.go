package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chainguard"
)

type readyBackend struct{ err error }

func (r *readyBackend) ListTags(context.Context, string) ([]chainguard.Tag, error) {
	return nil, nil
}
func (r *readyBackend) ListTagHistory(context.Context, string) ([]chainguard.TagHistory, error) {
	return nil, nil
}
func (r *readyBackend) Ready(context.Context) error { return r.err }

func TestReadyz_NoAPKIndex(t *testing.T) {
	h := New(&readyBackend{}, nil).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_APKIndexEmpty(t *testing.T) {
	// An unpopulated index simulates the case where NewIndexStoreWithRefresh
	// returned an empty store because the initial load failed.
	empty := apk.NewIndexStore()
	h := New(&readyBackend{}, nil, WithAPKIndex(empty)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body: %s", rec.Code, rec.Body.String())
	}
}

func TestReadyz_APKIndexPopulated(t *testing.T) {
	populated := apk.NewIndexStore()
	populated.Replace(
		map[string][]apk.Release{"foo": {{Version: "1.0.0"}}},
		map[string]struct{}{"foo": {}},
		nil,
	)
	h := New(&readyBackend{}, nil, WithAPKIndex(populated)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
}
