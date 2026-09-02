package apk

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepositoriesFromURLs(t *testing.T) {
	tests := []struct {
		name     string
		urls     []string
		env      string
		wantErr  string
		wantName []string
		wantBase []string
	}{
		{
			name:     "single https URL uses last path segment as name",
			urls:     []string{"https://mirror.example.com/apk/chainguard"},
			wantName: []string{"chainguard"},
			wantBase: []string{"https://mirror.example.com/apk/chainguard"},
		},
		{
			name: "multiple URLs preserve order",
			urls: []string{
				"https://a.example/apk/chainguard",
				"https://b.example/apk/extra-packages",
			},
			wantName: []string{"chainguard", "extra-packages"},
			wantBase: []string{"https://a.example/apk/chainguard", "https://b.example/apk/extra-packages"},
		},
		{
			name:     "trailing slash trimmed",
			urls:     []string{"https://mirror.example/apk/chainguard/"},
			wantName: []string{"chainguard"},
			wantBase: []string{"https://mirror.example/apk/chainguard"},
		},
		{
			name:     "host-only URL falls back to host as name",
			urls:     []string{"https://mirror.example"},
			wantName: []string{"mirror.example"},
			wantBase: []string{"https://mirror.example"},
		},
		{
			name:    "arch suffix rejected",
			urls:    []string{"https://mirror.example/apk/chainguard/x86_64"},
			wantErr: "drop /x86_64",
		},
		{
			name:    "non-http scheme rejected",
			urls:    []string{"ftp://mirror.example/apk"},
			wantErr: "scheme must be http or https",
		},
		{
			name:    "missing host rejected",
			urls:    []string{"https:///apk"},
			wantErr: "missing host",
		},
		{
			name:    "http URL with matching HTTP_AUTH is rejected",
			urls:    []string{"http://mirror.example/apk"},
			env:     "basic:mirror.example:alice:s3cret",
			wantErr: "plaintext http",
		},
		{
			name:     "http URL with non-matching HTTP_AUTH is fine",
			urls:     []string{"http://mirror.example/apk"},
			env:      "basic:other.example:alice:s3cret",
			wantName: []string{"apk"},
			wantBase: []string{"http://mirror.example/apk"},
		},
		{
			name:     "http URL with no HTTP_AUTH is fine",
			urls:     []string{"http://mirror.example/apk"},
			wantName: []string{"apk"},
			wantBase: []string{"http://mirror.example/apk"},
		},
		{
			name:     "https URL with matching HTTP_AUTH is fine",
			urls:     []string{"https://mirror.example/apk"},
			env:      "basic:mirror.example:alice:s3cret",
			wantName: []string{"apk"},
			wantBase: []string{"https://mirror.example/apk"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_AUTH", tc.env)
			got, err := RepositoriesFromURLs(tc.urls)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RepositoriesFromURLs: %v", err)
			}
			if len(got) != len(tc.wantName) {
				t.Fatalf("got %d repos, want %d", len(got), len(tc.wantName))
			}
			for i, r := range got {
				if r.Name != tc.wantName[i] {
					t.Errorf("[%d].Name = %q, want %q", i, r.Name, tc.wantName[i])
				}
				if r.BaseURL != tc.wantBase[i] {
					t.Errorf("[%d].BaseURL = %q, want %q", i, r.BaseURL, tc.wantBase[i])
				}
				if r.Auth == nil {
					t.Errorf("[%d].Auth is nil, want an HTTP_AUTH resolver", i)
				}
			}
		})
	}
}

func TestHTTPAuthFor(t *testing.T) {
	tests := []struct {
		name     string
		env      string
		host     string
		wantCred string
		wantErr  string
	}{
		{name: "unset env returns empty", env: "", host: "mirror.example"},
		{
			name:     "matching host returns user:password",
			env:      "basic:mirror.example:alice:s3cret",
			host:     "mirror.example",
			wantCred: "alice:s3cret",
		},
		{
			name: "non-matching host returns empty (no auth applied)",
			env:  "basic:mirror.example:alice:s3cret",
			host: "other.example",
		},
		{
			name:     "password containing colon is preserved (SplitN)",
			env:      "basic:mirror.example:alice:pass:with:colons",
			host:     "mirror.example",
			wantCred: "alice:pass:with:colons",
		},
		{
			name:    "malformed env is a hard error",
			env:     "digest:mirror.example:alice:s3cret",
			host:    "mirror.example",
			wantErr: "expected basic",
		},
		{
			name:    "too few fields is a hard error",
			env:     "basic:mirror.example:alice",
			host:    "mirror.example",
			wantErr: "expected basic",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTTP_AUTH", tc.env)
			got, err := HTTPAuthFor(tc.host)(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("HTTPAuthFor: %v", err)
			}
			if got != tc.wantCred {
				t.Errorf("cred = %q, want %q", got, tc.wantCred)
			}
		})
	}
}

// TestRepositoriesFromURLs_LoadsThroughLiveServer confirms the flag
// wiring end-to-end: RepositoriesFromURLs builds a Repository the
// loader can fetch through, and HTTP_AUTH populates the Authorization
// header when the host matches.
//
// httptest picks an ephemeral port so we bypass RepositoriesFromURLs
// for the auth-header assertion (HTTP_AUTH's colon delimiter can't
// carry a port). The RepositoriesFromURLs path is exercised by the
// no-auth fetch below.
func TestRepositoriesFromURLs_LoadsThroughLiveServer(t *testing.T) {
	body := "P:foo\nV:1.2.3-r0\nt:1700000000\n"
	index := makeIndex(t, body)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/x86_64/APKINDEX.tar.gz" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write(index.Bytes())
	}))
	defer srv.Close()

	// End-to-end via RepositoriesFromURLs; no HTTP_AUTH set, so the
	// request must go out unauthenticated and the store must load.
	repos, err := RepositoriesFromURLs([]string{srv.URL})
	if err != nil {
		t.Fatalf("RepositoriesFromURLs: %v", err)
	}
	store, err := NewIndexStoreWithRefresh(context.Background(), []string{"x86_64"},repos, 0, slog.Default())
	if err != nil {
		t.Fatalf("NewIndexStoreWithRefresh: %v", err)
	}
	if got := store.Get("foo", ""); len(got) == 0 || got[0].Version != "1.2.3-r0" {
		t.Errorf("Get(foo) = %+v, want a 1.2.3-r0 entry", got)
	}
	if gotAuth != "" {
		t.Errorf("Authorization header = %q, want empty (no HTTP_AUTH)", gotAuth)
	}

	// Auth-header wiring: a Repository carrying a fixed AuthFunc must
	// produce a Basic header the server sees. Uses a manual
	// Repository (not RepositoriesFromURLs) because HTTP_AUTH can't
	// address the ephemeral test port.
	authRepos := []Repository{{
		Name:    "custom",
		BaseURL: srv.URL,
		Auth:    func(_ context.Context) (string, error) { return "alice:s3cret", nil },
	}}
	gotAuth = ""
	if _, err := NewIndexStoreWithRefresh(context.Background(), []string{"x86_64"},authRepos, 0, slog.Default()); err != nil {
		t.Fatalf("auth load: %v", err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))
	if gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}

// TestNewIndexStoreWithRefresh_MultiArch verifies that both configured
// archs are fetched, each entry is tagged with its arch, and the
// store's Archs() reports the set installed.
func TestNewIndexStoreWithRefresh_MultiArch(t *testing.T) {
	x86Body := "P:foo\nV:1.0.0-r0\nt:1700000000\n\nP:x86only\nV:9.0.0-r0\nt:1700000000\n"
	armBody := "P:foo\nV:1.0.0-r0\nt:1700000000\n\nP:armonly\nV:2.0.0-r0\nt:1700000000\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x86_64/APKINDEX.tar.gz":
			_, _ = w.Write(makeIndex(t, x86Body).Bytes())
		case "/aarch64/APKINDEX.tar.gz":
			_, _ = w.Write(makeIndex(t, armBody).Bytes())
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	repos, err := RepositoriesFromURLs([]string{srv.URL})
	if err != nil {
		t.Fatalf("RepositoriesFromURLs: %v", err)
	}
	store, err := NewIndexStoreWithRefresh(context.Background(), []string{"x86_64", "aarch64"}, repos, 0, slog.Default())
	if err != nil {
		t.Fatalf("NewIndexStoreWithRefresh: %v", err)
	}
	if archs := store.Archs(); len(archs) != 2 {
		t.Errorf("Archs = %v, want two entries", archs)
	}
	if got := store.Get("foo", ""); len(got) != 2 {
		t.Errorf("Get(foo, merged) len = %d, want 2 (one per arch)", len(got))
	}
	if got := store.Get("foo", "x86_64"); len(got) != 1 || got[0].Arch != "x86_64" {
		t.Errorf("Get(foo, x86_64) = %+v, want one x86_64 entry", got)
	}
	if got := store.Get("x86only", "aarch64"); got != nil {
		t.Errorf("Get(x86only, aarch64) = %+v, want nil", got)
	}
	if got := store.Get("armonly", "aarch64"); len(got) != 1 {
		t.Errorf("Get(armonly, aarch64) = %+v, want one entry", got)
	}
}

// TestNewIndexStoreWithRefresh_MissingArchHardFails verifies that a
// (repo, arch) pair returning 404 causes the initial Load to error
// out — Chainguard's contract guarantees every arch on every repo, so
// a missing one is surfaced rather than silently ignored.
func TestNewIndexStoreWithRefresh_MissingArchHardFails(t *testing.T) {
	body := "P:foo\nV:1.0.0-r0\nt:1700000000\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x86_64/APKINDEX.tar.gz" {
			_, _ = w.Write(makeIndex(t, body).Bytes())
			return
		}
		http.Error(w, "no such arch", http.StatusNotFound)
	}))
	defer srv.Close()

	repos, err := RepositoriesFromURLs([]string{srv.URL})
	if err != nil {
		t.Fatalf("RepositoriesFromURLs: %v", err)
	}
	store, err := NewIndexStoreWithRefresh(context.Background(), []string{"x86_64", "aarch64"}, repos, 0, slog.Default())
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if store.Len() != 0 {
		t.Errorf("store should be empty on hard-fail, got %d packages", store.Len())
	}
}
