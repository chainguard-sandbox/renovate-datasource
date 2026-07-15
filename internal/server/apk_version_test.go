package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
)

// stubAPKFetcher and newAPKServer are defined in apk_diff_test.go and
// shared with these tests since they exercise the same fetcher surface.

func TestHandleAPKVersion(t *testing.T) {
	contents := map[string]*apk.Contents{
		"foo/1.0": {
			URL:     "https://example.test/repo/foo-1.0.apk",
			Melange: []byte("package:\n  name: foo\n  version: 1.0\n"),
			PKGINFO: []byte("pkgname = foo\npkgver = 1.0\n"),
		},
	}

	tests := []struct {
		name       string
		path       string
		fetcher    diff.APKFetcher
		wantStatus int
		wantBody   func(*testing.T, []byte)
	}{
		{
			name:       "happy path returns snapshot",
			path:       "/v1/apk/foo/version/1.0",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusOK,
			wantBody: func(t *testing.T, body []byte) {
				var resp diff.APKVersionResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Name != "foo" || resp.Version != "1.0" {
					t.Errorf("metadata mismatch: %+v", resp)
				}
				if resp.URL != "https://example.test/repo/foo-1.0.apk" {
					t.Errorf("URL = %q", resp.URL)
				}
				if !strings.Contains(resp.Melange, "name: foo") {
					t.Errorf("melange missing expected content:\n%s", resp.Melange)
				}
				if !strings.Contains(resp.PKGINFO, "pkgname = foo") {
					t.Errorf("pkginfo missing expected content:\n%s", resp.PKGINFO)
				}
			},
		},
		{
			name:       "missing apk maps to 404",
			path:       "/v1/apk/foo/version/9.9",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "upstream error maps to 502",
			path:       "/v1/apk/foo/version/1.0",
			fetcher:    &stubAPKFetcher{err: errors.New("boom")},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "invalid name returns 400",
			path:       "/v1/apk/UPPERCASE/version/1.0",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid version returns 400",
			path:       "/v1/apk/foo/version/-bad",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newAPKServer(t, tc.fetcher)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantBody != nil {
				body, err := io.ReadAll(rec.Body)
				if err != nil {
					t.Fatal(err)
				}
				tc.wantBody(t, body)
			}
		})
	}
}

func TestHandleAPKVersionDisabledWhenFetcherNil(t *testing.T) {
	h := New(nil, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/version/1.0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestHandleAPKVersionPage(t *testing.T) {
	h := newAPKServer(t, &stubAPKFetcher{})
	req := httptest.NewRequest(http.MethodGet, "/apk/foo/version/1.0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"foo", "1.0", "/v1/apk/"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestHandleAPKVersion_RedirectsWhenCapabilityResolvesUniquely(t *testing.T) {
	// /apk/nodejs/version/24.14.0-r0 is a capability that resolves to a
	// single real provider (nodejs-24). Both the JSON and HTML endpoints
	// should 302 to the underlying package's URL.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	tests := []struct {
		name string
		path string
		want string
	}{
		{"JSON", "/v1/apk/nodejs/version/24.14.0-r0", "/v1/apk/nodejs-24/version/24.14.0-r0"},
		{"HTML", "/apk/nodejs/version/24.14.0-r0", "/apk/nodejs-24/version/24.14.0-r0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusFound {
				t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandleAPKVersion_MultipleChoicesWhenAmbiguous(t *testing.T) {
	// cmd:node=26.4.0-r1 has two providers in providesStore. The JSON
	// endpoint surfaces 300 Multiple Choices with the candidate list.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/cmd:node/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMultipleChoices {
		t.Fatalf("status = %d, want 300 (body: %s)", rec.Code, rec.Body.String())
	}
	var got apkResolution
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "cmd:node" || got.Version != "26.4.0-r1" {
		t.Errorf("Name/Version mismatch: %+v", got)
	}
	if len(got.Providers) != 2 {
		t.Errorf("Providers len = %d, want 2 (nodejs-26 + nodejs-26-minimal): %+v", len(got.Providers), got.Providers)
	}
}

func TestHandleAPKVersionPage_PickerWhenAmbiguous(t *testing.T) {
	// HTML counterpart: the picker page lists per-provider snapshot
	// links and mentions the capability.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/cmd:node/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/apk/nodejs-26/version/26.4.0-r1",
		"/apk/nodejs-26-minimal/version/26.4.0-r1",
		"apk capability",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("picker page missing %q\n---\n%s", want, body)
		}
	}
}

func TestHandleAPKVersion_PrefixedNameAllowed(t *testing.T) {
	// The validator was loosened for the version endpoint so URLs like
	// /apk/cmd:node/version/24.14.0-r0 route correctly instead of 400'ing.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/cmd:node/version/22.14.0-r0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// Single provider → 302 (see providesStore).
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/apk/nodejs-22/version/22.14.0-r0" {
		t.Errorf("Location = %q, want /apk/nodejs-22/version/22.14.0-r0", got)
	}
}
