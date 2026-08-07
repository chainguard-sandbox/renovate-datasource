package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
)

// buildStore stamps a synthetic index into an apk.IndexStore so the
// handler tests can exercise cooldown / sorting / 404s without touching
// the network.
func buildStore(entries map[string][]apk.Release) *apk.IndexStore {
	s := apk.NewIndexStore()
	s.Replace(entries)
	return s
}

func TestHandleAPKReleases_UnknownPackage(t *testing.T) {
	store := buildStore(nil)
	h := New(nil, WithAPKIndex(store)).Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/nonesuch/releases", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestHandleAPKReleases_NotImplemented(t *testing.T) {
	// No index attached → 501, so orchestrators can detect a deployment
	// where the apk feature is intentionally disabled.
	h := New(nil).Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestHandleAPKReleases_CooldownFilterAndSort(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }

	store := buildStore(map[string][]apk.Release{
		"foo": {
			{Version: "1.0.0-r0", Timestamp: day(30)},
			{Version: "1.1.0-r0", Timestamp: day(15)},
			// Within a 7d cooldown, this one is too fresh and should be filtered out.
			{Version: "1.2.0-r0", Timestamp: day(1)},
			// Zero timestamp — treated as old enough and always included.
			{Version: "0.9.0-r0", Timestamp: time.Time{}},
		},
	})

	srv := New(nil, WithAPKIndex(store))
	srv.now = func() time.Time { return now }
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases?cooldown=168h", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp APKReleasesResponse
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
	store := buildStore(map[string][]apk.Release{
		"foo": {
			{Version: "1.0.0-r0", Timestamp: now.AddDate(0, 0, -1)},
			{Version: "1.1.0-r0", Timestamp: now},
		},
	})
	srv := New(nil, WithAPKIndex(store))
	srv.now = func() time.Time { return now }
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp APKReleasesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Releases) != 2 {
		t.Errorf("expected all releases without cooldown, got %d", len(resp.Releases))
	}
}

func TestHandleAPKReleases_BadCooldown(t *testing.T) {
	store := buildStore(map[string][]apk.Release{
		"foo": {{Version: "1.0.0-r0"}},
	})
	h := New(nil, WithAPKIndex(store)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/releases?cooldown=whatever", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAPKReleases_MalformedName(t *testing.T) {
	store := buildStore(nil)
	h := New(nil, WithAPKIndex(store)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/BAD..NAME/releases", nil)
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
	store := buildStore(map[string][]apk.Release{
		"cmd:node": {{Version: "24.14.0-r0", Timestamp: time.Unix(1_700_000_000, 0).UTC()}},
	})
	h := New(nil, WithAPKIndex(store)).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/apk/cmd:node/releases", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp APKReleasesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Releases) != 1 || resp.Releases[0].Version != "24.14.0-r0" {
		t.Errorf("releases = %+v, want one entry at 24.14.0-r0", resp.Releases)
	}
}
