package server

import (
	"context"
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

// stubAPKFetcher returns configured Contents per (name, version) or the
// configured error. Shared with apk_version_test.go — both endpoints
// exercise the same fetcher surface.
type stubAPKFetcher struct {
	contents map[string]*apk.Contents
	err      error
}

func (s *stubAPKFetcher) Fetch(_ context.Context, name, version string) (*apk.Contents, error) {
	if s.err != nil {
		return nil, s.err
	}
	if c, ok := s.contents[name+"/"+version]; ok {
		return c, nil
	}
	return nil, apk.ErrNotFound
}

// newAPKServer assembles a Server with just enough wiring to exercise
// the /v1/apk/* handlers (diff + version).
func newAPKServer(t *testing.T, f diff.APKFetcher) http.Handler {
	t.Helper()
	srv := New(nil, nil, WithAPKFetcher(f))
	return srv.Handler()
}

func TestHandleAPKDiff(t *testing.T) {
	contents := map[string]*apk.Contents{
		"foo/1.0": {
			URL:     "https://example.test/repo/foo-1.0.apk",
			Melange: []byte("package:\n  name: foo\n  version: 1.0\n"),
			PKGINFO: []byte("pkgname = foo\npkgver = 1.0\n"),
		},
		"foo/2.0": {
			URL:     "https://example.test/repo/foo-2.0.apk",
			Melange: []byte("package:\n  name: foo\n  version: 2.0\n"),
			PKGINFO: []byte("pkgname = foo\npkgver = 2.0\n"),
		},
		"bar/1.0": {
			Melange: []byte("package: bar\n"),
			PKGINFO: []byte("pkgname = bar\n"),
		},
		"bar/1.1": {
			Melange: []byte("package: bar\n"),
			PKGINFO: []byte("pkgname = bar\n"),
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
			name:       "happy path returns metadata diffs",
			path:       "/v1/apk/foo/diff/1.0/2.0",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusOK,
			wantBody: func(t *testing.T, body []byte) {
				var resp diff.APKDiffResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Name != "foo" || resp.From != "1.0" || resp.To != "2.0" {
					t.Errorf("metadata mismatch: %+v", resp)
				}
				if !strings.Contains(resp.Melange, "-  version: 1.0") {
					t.Errorf("melange diff missing expected lines:\n%s", resp.Melange)
				}
				if !strings.Contains(resp.PKGINFO, "+pkgver = 2.0") {
					t.Errorf("pkginfo diff missing expected lines:\n%s", resp.PKGINFO)
				}
			},
		},
		{
			name:       "identical versions return empty diffs",
			path:       "/v1/apk/bar/diff/1.0/1.1",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusOK,
			wantBody: func(t *testing.T, body []byte) {
				var resp diff.APKDiffResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if resp.Melange != "" || resp.PKGINFO != "" {
					t.Errorf("expected all-empty diffs, got %+v", resp)
				}
			},
		},
		{
			name: "missing melange flagged on the from side",
			path: "/v1/apk/legacy/diff/1.0/2.0",
			fetcher: &stubAPKFetcher{contents: map[string]*apk.Contents{
				"legacy/1.0": {
					// Older apk: only .PKGINFO, no .melange.yaml.
					PKGINFO: []byte("pkgname = legacy\npkgver = 1.0\n"),
				},
				"legacy/2.0": {
					Melange: []byte("package:\n  name: legacy\n  version: 2.0\n"),
					PKGINFO: []byte("pkgname = legacy\npkgver = 2.0\n"),
				},
			}},
			wantStatus: http.StatusOK,
			wantBody: func(t *testing.T, body []byte) {
				var resp diff.APKDiffResponse
				if err := json.Unmarshal(body, &resp); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if !resp.FromMelangeMissing || resp.ToMelangeMissing {
					t.Errorf("expected fromMelangeMissing=true, toMelangeMissing=false; got from=%v to=%v",
						resp.FromMelangeMissing, resp.ToMelangeMissing)
				}
			},
		},
		{
			name:       "missing apk maps to 404",
			path:       "/v1/apk/foo/diff/1.0/9.9",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "upstream error maps to 502",
			path:       "/v1/apk/foo/diff/1.0/2.0",
			fetcher:    &stubAPKFetcher{err: errors.New("boom")},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "invalid package name returns 400",
			path:       "/v1/apk/UPPERCASE/diff/1.0/2.0",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid version returns 400",
			path:       "/v1/apk/foo/diff/-bad/2.0",
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

func TestHandleAPKDiffDisabledWhenFetcherNil(t *testing.T) {
	// No WithAPKFetcher → endpoint refuses with 501.
	h := New(nil, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/diff/1.0/2.0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestHandleAPKDiffPage(t *testing.T) {
	h := newAPKServer(t, &stubAPKFetcher{})
	req := httptest.NewRequest(http.MethodGet, "/apk/foo/diff/1.0/2.0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"foo", "1.0", "2.0", "/v1/apk/"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}
