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
