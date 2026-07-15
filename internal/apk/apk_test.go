package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestFetch(t *testing.T) {
	const wantToken = "tok-abc123"

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		wantMelange string
		wantPKGINFO string
		wantErrIs   error
		wantErrSub  string
	}{
		{
			name: "happy path extracts control entries",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Path; got != "/test-org/x86_64/foo-1.2.3-r0.apk" {
					t.Errorf("unexpected path: %s", got)
				}
				if !hasBasicAuth(r, "user", wantToken) {
					t.Errorf("missing/incorrect Basic auth header: %q", r.Header.Get("Authorization"))
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(buildAPK(t, []apkStream{
					{}, // signature stream (empty tar)
					{entries: map[string]entry{
						".PKGINFO":      {body: "name=foo\n"},
						".melange.yaml": {body: "package:\n  name: foo\n"},
					}},
					// Data stream included to confirm we stop before reading it;
					// the handler returns successfully even without us touching
					// these entries.
					{entries: map[string]entry{
						"usr/bin/foo": {body: "binary\n"},
					}},
				}))
			},
			wantMelange: "package:\n  name: foo\n",
			wantPKGINFO: "name=foo\n",
		},
		{
			name: "missing control entries leave fields empty",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(buildAPK(t, []apkStream{
					{entries: map[string]entry{
						"usr/bin/foo": {body: "binary\n"},
					}},
				}))
			},
		},
		{
			name: "404 returns ErrNotFound",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			wantErrIs: ErrNotFound,
		},
		{
			name: "5xx surfaces a generic error (not ErrNotFound)",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErrSub: "status 500",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			// Point at the single test-server source so behaviour is
			// deterministic and we don't hit the real fallback hosts
			// during unit tests.
			f := NewFetcher("x86_64", []Repository{
				{BaseURL: srv.URL + "/test-org", Auth: TokenSourceAuth(staticTokenSource(wantToken))},
			})

			got, err := f.Fetch(context.Background(), "foo", "1.2.3-r0")
			switch {
			case tc.wantErrIs != nil:
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("err = %v, want errors.Is(_, %v)", err, tc.wantErrIs)
				}
			case tc.wantErrSub != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErrSub)
				}
			default:
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if string(got.Melange) != tc.wantMelange {
					t.Errorf("Melange = %q, want %q", got.Melange, tc.wantMelange)
				}
				if string(got.PKGINFO) != tc.wantPKGINFO {
					t.Errorf("PKGINFO = %q, want %q", got.PKGINFO, tc.wantPKGINFO)
				}
				wantURL := srv.URL + "/test-org/x86_64/foo-1.2.3-r0.apk"
				if got.URL != wantURL {
					t.Errorf("URL = %q, want %q", got.URL, wantURL)
				}
			}
		})
	}
}

func TestFetchFallbackChain(t *testing.T) {
	// Primary 404s, second source 404s, third source returns the apk.
	// Validates that we walk past 404s, stop on first 200, and don't
	// attach Authorization to unauthenticated fallback sources.
	var hits []string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "primary "+r.URL.Path)
		if r.Header.Get("Authorization") == "" {
			t.Errorf("primary expected auth header, got none")
		}
		http.NotFound(w, r)
	}))
	defer primary.Close()

	chainguard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "chainguard "+r.URL.Path)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("chainguard fallback got unexpected auth header")
		}
		http.NotFound(w, r)
	}))
	defer chainguard.Close()

	wolfi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, "wolfi "+r.URL.Path)
		if r.Header.Get("Authorization") != "" {
			t.Errorf("wolfi fallback got unexpected auth header")
		}
		_, _ = w.Write(buildAPK(t, []apkStream{
			{entries: map[string]entry{".PKGINFO": {body: "name=foo\n"}}},
		}))
	}))
	defer wolfi.Close()

	notReached := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("sources after a 200 should not be hit; got %s", r.URL.Path)
	}))
	defer notReached.Close()

	f := NewFetcher("x86_64", []Repository{
		{BaseURL: primary.URL + "/test-org", Auth: TokenSourceAuth(staticTokenSource("tok"))},
		{BaseURL: chainguard.URL + "/chainguard"},
		{BaseURL: wolfi.URL + "/os"},
		{BaseURL: notReached.URL + "/never"},
	})

	got, err := f.Fetch(context.Background(), "foo", "1.0-r0")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(got.PKGINFO) != "name=foo\n" {
		t.Errorf("PKGINFO = %q", got.PKGINFO)
	}
	if len(hits) != 3 {
		t.Errorf("expected 3 source hits (primary, chainguard, wolfi); got %v", hits)
	}
}

func TestFetchAllSources404(t *testing.T) {
	// Every source returns 404 → ErrNotFound surfaces with the last
	// source's URL in the error message so the operator can see where
	// we ended.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	f := NewFetcher("x86_64", []Repository{
		{BaseURL: srv.URL + "/a"},
		{BaseURL: srv.URL + "/b"},
	})
	_, err := f.Fetch(context.Background(), "foo", "1.0-r0")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want errors.Is(_, ErrNotFound)", err)
	}
}

func TestFetchPropagatesNon404(t *testing.T) {
	// A 500 from the primary aborts the whole walk — we don't silently
	// fall through, since a server error here likely indicates a
	// misconfiguration we want surfaced.
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Errorf("fallback should not be reached after primary 500")
	}))
	defer fallback.Close()

	f := NewFetcher("x86_64", []Repository{
		{BaseURL: primary.URL + "/primary", Auth: TokenSourceAuth(staticTokenSource("tok"))},
		{BaseURL: fallback.URL + "/fallback"},
	})
	_, err := f.Fetch(context.Background(), "foo", "1.0-r0")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("got ErrNotFound; want non-404 error to propagate")
	}
}

func TestDefaultRepositories(t *testing.T) {
	// The default repository chain drives both the fetcher and the
	// index loader; they have to agree exactly or the diff / version
	// endpoints will 404 on packages that only live in the virtualapk
	// feeds. Assert the URLs directly.
	ts := staticTokenSource("tok")
	tests := []struct {
		name        string
		orgName     string
		orgUIDP     string
		wantURLs    []string
		wantAuthIdx []int // indexes in wantURLs that should carry an AuthFunc
	}{
		{
			name:        "full chain: private + virtualapk chainguard + virtualapk extras",
			orgName:     "myorg",
			orgUIDP:     "uidp-123",
			wantURLs:    []string{"https://apk.cgr.dev/myorg", "https://virtualapk.cgr.dev/uidp-123/chainguard", "https://virtualapk.cgr.dev/uidp-123/extra-packages"},
			wantAuthIdx: []int{0},
		},
		{
			name:     "empty orgName omits the private repo",
			orgName:  "",
			orgUIDP:  "uidp-123",
			wantURLs: []string{"https://virtualapk.cgr.dev/uidp-123/chainguard", "https://virtualapk.cgr.dev/uidp-123/extra-packages"},
		},
		{
			name:        "empty orgUIDP omits both virtualapk feeds",
			orgName:     "myorg",
			orgUIDP:     "",
			wantURLs:    []string{"https://apk.cgr.dev/myorg"},
			wantAuthIdx: []int{0},
		},
		{
			name:     "both empty yields no repositories",
			orgName:  "",
			orgUIDP:  "",
			wantURLs: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repos := DefaultRepositories(tc.orgName, tc.orgUIDP, ts)
			if len(repos) != len(tc.wantURLs) {
				t.Fatalf("repos len = %d, want %d (%+v)", len(repos), len(tc.wantURLs), repos)
			}
			for i, want := range tc.wantURLs {
				if got := repos[i].BaseURL; got != want {
					t.Errorf("repos[%d].BaseURL = %q, want %q", i, got, want)
				}
			}
			wantAuth := map[int]bool{}
			for _, i := range tc.wantAuthIdx {
				wantAuth[i] = true
			}
			for i, r := range repos {
				hasAuth := r.Auth != nil
				if hasAuth != wantAuth[i] {
					t.Errorf("repos[%d] Auth nil-ness = %v, want authed=%v", i, !hasAuth, wantAuth[i])
				}
			}
		})
	}
}

// entry is one tar entry written into a fixture .apk.
type entry struct {
	body string
}

// apkStream describes one gzip-wrapped tar archive in the fixture .apk.
type apkStream struct {
	entries map[string]entry
}

// buildAPK packs streams into the concatenated gzip-tar form that real
// .apk artifacts use.
func buildAPK(t *testing.T, streams []apkStream) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, s := range streams {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gw)
		for name, e := range s.entries {
			hdr := &tar.Header{
				Name:     name,
				Mode:     0o644,
				Typeflag: tar.TypeReg,
				Size:     int64(len(e.body)),
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatalf("write header %s: %v", name, err)
			}
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %s: %v", name, err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatalf("close tar: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("close gzip: %v", err)
		}
		out.Write(buf.Bytes())
	}
	return out.Bytes()
}

func hasBasicAuth(r *http.Request, user, pass string) bool {
	got := r.Header.Get("Authorization")
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	return got == want
}

type staticTokenSource string

func (s staticTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: string(s)}, nil
}
