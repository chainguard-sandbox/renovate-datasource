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
			path:       "/v1/apk/foo/version/1.0/diff/foo/version/2.0",
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
			path:       "/v1/apk/bar/version/1.0/diff/bar/version/1.1",
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
			path: "/v1/apk/legacy/version/1.0/diff/legacy/version/2.0",
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
			path:       "/v1/apk/foo/version/1.0/diff/foo/version/9.9",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "upstream error maps to 502",
			path:       "/v1/apk/foo/version/1.0/diff/foo/version/2.0",
			fetcher:    &stubAPKFetcher{err: errors.New("boom")},
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "invalid package name returns 400",
			path:       "/v1/apk/UPPERCASE/version/1.0/diff/UPPERCASE/version/2.0",
			fetcher:    &stubAPKFetcher{contents: contents},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid version returns 400",
			path:       "/v1/apk/foo/version/-bad/diff/foo/version/2.0",
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
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/foo/version/1.0/diff/foo/version/2.0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestHandleAPKDiffPage(t *testing.T) {
	h := newAPKServer(t, &stubAPKFetcher{})
	req := httptest.NewRequest(http.MethodGet, "/apk/foo/version/1.0/diff/foo/version/2.0", nil)
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

// providesStore builds an apk.IndexStore wired for the provider-
// resolution cases the diff handler has to handle:
//
//   - Unique real-package pair: nodejs/24.x resolves to nodejs-24 on
//     both sides → the handler redirects to /apk/nodejs-24/…
//   - Cross-package with a unique provider each side: cmd:node/22.14.0
//     → cmd:node/26.4.0 resolves to nodejs-22 → nodejs-26 → redirect
//     straight to the cross-package diff (no chooser needed).
//   - Ambiguous side: cmd:node=26.4.0-r1 has TWO providers (nodejs-26
//     and nodejs-26-minimal), matching the real Chainguard data — this
//     is what triggers the chooser page.
func providesStore(t *testing.T) *apk.IndexStore {
	t.Helper()
	s := apk.NewIndexStore()
	s.Replace(
		map[string][]apk.Release{
			"nodejs-22":         {{Version: "22.14.0-r0"}},
			"nodejs-24":         {{Version: "24.14.0-r0"}, {Version: "24.18.0-r2"}},
			"nodejs-26":         {{Version: "26.4.0-r1"}},
			"nodejs-26-minimal": {{Version: "26.4.0-r1"}},
			"nodejs":            {{Version: "24.14.0-r0"}, {Version: "24.18.0-r2"}},
			"cmd:node":          {{Version: "22.14.0-r0"}, {Version: "26.4.0-r1"}},
		},
		map[string]struct{}{
			"nodejs-22=22.14.0-r0":         {},
			"nodejs-24=24.14.0-r0":         {},
			"nodejs-24=24.18.0-r2":         {},
			"nodejs-26=26.4.0-r1":          {},
			"nodejs-26-minimal=26.4.0-r1":  {},
		},
		map[string][]apk.PackageVersion{
			"nodejs=24.14.0-r0":   {{Name: "nodejs-24", Version: "24.14.0-r0"}},
			"nodejs=24.18.0-r2":   {{Name: "nodejs-24", Version: "24.18.0-r2"}},
			"cmd:node=22.14.0-r0": {{Name: "nodejs-22", Version: "22.14.0-r0"}},
			"cmd:node=26.4.0-r1": {
				{Name: "nodejs-26", Version: "26.4.0-r1"},
				{Name: "nodejs-26-minimal", Version: "26.4.0-r1"},
			},
		},
	)
	return s
}

func TestHandleAPKDiff_RedirectsWhenProvidesResolveUniquely(t *testing.T) {
	// nodejs=24.14.0-r0 → nodejs=24.18.0-r2 both come from nodejs-24, so
	// the JSON endpoint 302s to the nodejs-24 diff.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/nodejs/version/24.14.0-r0/diff/nodejs/version/24.18.0-r2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	want := "/v1/apk/nodejs-24/version/24.14.0-r0/diff/nodejs-24/version/24.18.0-r2"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandleAPKDiff_MultipleChoicesWhenSideIsAmbiguous(t *testing.T) {
	// cmd:node=26.4.0-r1 has two providers (nodejs-26, nodejs-26-minimal)
	// so the to side is ambiguous. The JSON endpoint surfaces 300
	// Multiple Choices with the candidate lists on both sides.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/cmd:node/version/22.14.0-r0/diff/cmd:node/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMultipleChoices {
		t.Fatalf("status = %d, want 300 (body: %s)", rec.Code, rec.Body.String())
	}
	var cands apkDiffCandidates
	if err := json.Unmarshal(rec.Body.Bytes(), &cands); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cands.From.Name != "cmd:node" || cands.From.Version != "22.14.0-r0" ||
		cands.To.Name != "cmd:node" || cands.To.Version != "26.4.0-r1" {
		t.Errorf("From/To metadata mismatch: %+v", cands)
	}
	if len(cands.From.Providers) != 1 || cands.From.Providers[0].Name != "nodejs-22" {
		t.Errorf("From.Providers = %+v, want [nodejs-22 22.14.0-r0]", cands.From.Providers)
	}
	if len(cands.To.Providers) != 2 {
		t.Errorf("To.Providers len = %d, want 2 (nodejs-26 + nodejs-26-minimal): %+v", len(cands.To.Providers), cands.To.Providers)
	}
}

func TestHandleAPKDiff_RedirectsCrossPackageWithUniqueProviders(t *testing.T) {
	// A cross-package request where each side resolves to a single real
	// provider goes straight to the resolved cross-package diff — no
	// chooser detour required.
	s := apk.NewIndexStore()
	s.Replace(
		map[string][]apk.Release{"cmd:node": {{Version: "22.14.0-r0"}, {Version: "24.14.0-r0"}}},
		map[string]struct{}{
			"nodejs-22=22.14.0-r0": {},
			"nodejs-24=24.14.0-r0": {},
		},
		map[string][]apk.PackageVersion{
			"cmd:node=22.14.0-r0": {{Name: "nodejs-22", Version: "22.14.0-r0"}},
			"cmd:node=24.14.0-r0": {{Name: "nodejs-24", Version: "24.14.0-r0"}},
		},
	)
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(s)).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/cmd:node/version/22.14.0-r0/diff/cmd:node/version/24.14.0-r0", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	want := "/v1/apk/nodejs-22/version/22.14.0-r0/diff/nodejs-24/version/24.14.0-r0"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandleAPKDiff_RedirectsWhenOneSideIsProvideAndOtherIsReal(t *testing.T) {
	// Mixed request: from = capability (nodejs) → resolves to nodejs-24;
	// to = already the real package (nodejs-24). The handler resolves
	// the from side and 302s so both URL segments end up as real names.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/apk/nodejs/version/24.14.0-r0/diff/nodejs-24/version/24.18.0-r2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	want := "/v1/apk/nodejs-24/version/24.14.0-r0/diff/nodejs-24/version/24.18.0-r2"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandleAPKDiffPage_RedirectsWhenProvidesResolveUniquely(t *testing.T) {
	// HTML counterpart: an unprefixed provide with a unique real package
	// on both sides should redirect the browser to the real package's
	// diff page.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/nodejs/version/24.14.0-r0/diff/nodejs/version/24.18.0-r2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (body: %s)", rec.Code, rec.Body.String())
	}
	want := "/apk/nodejs-24/version/24.14.0-r0/diff/nodejs-24/version/24.18.0-r2"
	if got := rec.Header().Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestHandleAPKDiffPage_ChooserWhenSideIsAmbiguous(t *testing.T) {
	// cmd:node=26.4.0-r1 has two providers (nodejs-26 + nodejs-26-minimal)
	// so the to side is ambiguous. The HTML page renders the chooser
	// template with snapshot links for each side rather than the diff
	// view.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/cmd:node/version/22.14.0-r0/diff/cmd:node/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/apk/nodejs-22/version/22.14.0-r0",
		"/apk/nodejs-26/version/26.4.0-r1",
		"/apk/nodejs-26-minimal/version/26.4.0-r1",
		"resolves to more than one installable package",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chooser page missing %q\n---\n%s", want, body)
		}
	}
	// No same-real-name pair is possible (from side is only nodejs-22),
	// so no shortcut diff link.
	if strings.Contains(body, "/apk/nodejs-22/version/22.14.0-r0/diff/nodejs-22/") {
		t.Errorf("chooser page should not offer a diff link when providers differ:\n%s", body)
	}
}

func TestHandleAPKDiffPage_ChooserOffersDiffWhenSameRealName(t *testing.T) {
	// Two providers for both sides that all share a real name: the
	// Cartesian product yields a same-real-name pair, which the chooser
	// surfaces as a direct diff link on the shortcut list (a UX escape
	// hatch alongside the radio-driven pair form).
	s := apk.NewIndexStore()
	s.Replace(
		map[string][]apk.Release{
			"cmd:foo":   {{Version: "1"}, {Version: "2"}},
			"foo-alpha": {{Version: "1"}, {Version: "2"}},
			"foo-beta":  {{Version: "1"}, {Version: "2"}},
		},
		nil,
		map[string][]apk.PackageVersion{
			"cmd:foo=1": {
				{Name: "foo-alpha", Version: "1"},
				{Name: "foo-beta", Version: "1"},
			},
			"cmd:foo=2": {
				{Name: "foo-alpha", Version: "2"},
				{Name: "foo-beta", Version: "2"},
			},
		},
	)
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(s)).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/cmd:foo/version/1/diff/cmd:foo/version/2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"/apk/foo-alpha/version/1/diff/foo-alpha/version/2",
		"/apk/foo-beta/version/1/diff/foo-beta/version/2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chooser page missing shortcut link %q", want)
		}
	}
}

func TestHandleAPKDiffPage_ChooserDisablesDiffWhenSideHasNoProviders(t *testing.T) {
	// One side has multiple providers (ambiguous → chooser fires), the
	// other side is unknown to the store (0 providers). The chooser
	// renders but the Diff button is disabled — there's no valid pair
	// to submit.
	s := apk.NewIndexStore()
	s.Replace(
		map[string][]apk.Release{"cmd:foo": {{Version: "1"}, {Version: "2"}}},
		nil,
		map[string][]apk.PackageVersion{
			"cmd:foo=2": {
				{Name: "foo-a", Version: "2"},
				{Name: "foo-b", Version: "2"},
			},
			// cmd:foo=1 has no providers in the store.
		},
	)
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(s)).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/cmd:foo/version/1/diff/cmd:foo/version/2", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `type="submit" id="chooser-diff" disabled>`) {
		t.Errorf("Diff button should be disabled when a side has no providers:\n%s", body)
	}
}

func TestHandleAPKDiffPage_ChooserFormHasRadiosAndButton(t *testing.T) {
	// The chooser page always renders a form that navigates to a
	// symmetric diff URL, regardless of whether same-package shortcuts
	// exist. Assert the shape of that form.
	h := New(nil, nil, WithAPKFetcher(&stubAPKFetcher{}), WithAPKIndex(providesStore(t))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/apk/cmd:node/version/22.14.0-r0/diff/cmd:node/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		// The JS handler builds the URL, so no action= attribute — we
		// just want to confirm the radios + submit exist.
		`id="chooser-form"`,
		// Radios use `from-choice` / `to-choice` (not `from` / `to`) so
		// they persist across bfcache back-nav without a name-attribute
		// mutation on submit.
		`name="from-choice"`,
		`name="to-choice"`,
		`value="nodejs-22=22.14.0-r0"`,
		`value="nodejs-26=26.4.0-r1"`,
		`type="submit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chooser form missing %q\n---\n%s", want, body)
		}
	}
}

func TestHandleAPKDiff_CrossPackage(t *testing.T) {
	// The symmetric endpoint diffs two arbitrary (name, version) pairs.
	// When the names differ, provider resolution is bypassed and both
	// sides fetch directly under the requested real names.
	contents := map[string]*apk.Contents{
		"nodejs-22/22.14.0-r0": {
			URL:     "https://example.test/repo/nodejs-22-22.14.0-r0.apk",
			Melange: []byte("package:\n  name: nodejs-22\n  version: 22.14.0-r0\n"),
			PKGINFO: []byte("pkgname = nodejs-22\npkgver = 22.14.0-r0\n"),
		},
		"nodejs-26/26.4.0-r1": {
			URL:     "https://example.test/repo/nodejs-26-26.4.0-r1.apk",
			Melange: []byte("package:\n  name: nodejs-26\n  version: 26.4.0-r1\n"),
			PKGINFO: []byte("pkgname = nodejs-26\npkgver = 26.4.0-r1\n"),
		},
	}
	h := newAPKServer(t, &stubAPKFetcher{contents: contents})
	req := httptest.NewRequest(http.MethodGet,
		"/v1/apk/nodejs-22/version/22.14.0-r0/diff/nodejs-26/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp diff.APKDiffResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "" {
		t.Errorf("Name should be empty for cross-package diff, got %q", resp.Name)
	}
	if resp.FromName != "nodejs-22" || resp.ToName != "nodejs-26" {
		t.Errorf("FromName/ToName = %q/%q, want nodejs-22/nodejs-26", resp.FromName, resp.ToName)
	}
	if !strings.Contains(resp.Melange, "nodejs-22 22.14.0-r0") || !strings.Contains(resp.Melange, "nodejs-26 26.4.0-r1") {
		t.Errorf("melange diff missing cross-package labels:\n%s", resp.Melange)
	}
}

func TestHandleAPKDiffPage_CrossPackage(t *testing.T) {
	// HTML counterpart of TestHandleAPKDiff_CrossPackage. Verifies the
	// page loads with both real names on the h1 and the JS fetch URL
	// pointing at the JSON symmetric endpoint.
	h := newAPKServer(t, &stubAPKFetcher{})
	req := httptest.NewRequest(http.MethodGet,
		"/apk/nodejs-22/version/22.14.0-r0/diff/nodejs-26/version/26.4.0-r1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"nodejs-22",
		"nodejs-26",
		"22.14.0-r0",
		"26.4.0-r1",
		"/v1/apk/nodejs-22/version/22.14.0-r0/diff/nodejs-26/version/26.4.0-r1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pair diff page missing %q", want)
		}
	}
}

func TestHandleAPKDiff_ValidatesParams(t *testing.T) {
	h := newAPKServer(t, &stubAPKFetcher{})
	tests := []struct {
		name string
		path string
	}{
		{"bad fromName", "/v1/apk/BAD/version/1/diff/foo/version/2"},
		{"bad toName", "/v1/apk/foo/version/1/diff/BAD/version/2"},
		{"bad fromVersion", "/v1/apk/foo/version/-nope/diff/foo/version/2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
			}
		})
	}
}
