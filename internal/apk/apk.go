// Package apk pulls APKINDEX metadata from Chainguard repositories.
// IndexLoader downloads each repository's APKINDEX.tar.gz, parses
// it, and installs the merged release list into an IndexStore.
package apk

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"golang.org/x/oauth2"
)

// Repository describes one apk repository — an HTTP base URL plus
// optional auth. The base URL points at the repo root (e.g.
// `https://apk.cgr.dev/rob.best`); consumers append `/{arch}/…` for
// their specific asset. Name is a display label used in log lines.
type Repository struct {
	Name    string
	BaseURL string
	// Auth resolves the "user:<token>" Basic-auth credential just
	// before each request. Nil for public repos (virtualapk.cgr.dev).
	Auth AuthFunc
}

// AuthFunc returns a "user:<token>" credential for HTTP Basic auth.
// The signature threads a context so token exchanges can respect the
// caller's deadline.
type AuthFunc func(ctx context.Context) (string, error)

// TokenSourceAuth adapts an oauth2.TokenSource into an AuthFunc that
// yields "user:<accessToken>" — the shape apk.cgr.dev expects. Returns
// nil when ts is nil so callers can pass through an unconfigured
// token source without extra guarding.
func TokenSourceAuth(ts oauth2.TokenSource) AuthFunc {
	if ts == nil {
		return nil
	}
	return func(_ context.Context) (string, error) {
		tok, err := ts.Token()
		if err != nil {
			return "", err
		}
		return "user:" + tok.AccessToken, nil
	}
}

// DefaultRepositories returns the canonical Chainguard repository chain
// for a given org: the org's private repo on apk.cgr.dev plus the two
// per-org virtualapk views of chainguard and extra-packages. The
// private repo is authenticated with ts; the virtualapk feeds are
// public. Empty orgName omits the private repo (avoids a broken
// `https://apk.cgr.dev//…` URL); empty orgUIDP omits the virtualapk
// feeds (they need the UIDP in the path).
func DefaultRepositories(orgName, orgUIDP string, ts oauth2.TokenSource) []Repository {
	repos := make([]Repository, 0, 3)
	if orgName != "" {
		repos = append(repos, Repository{
			Name:    orgName,
			BaseURL: "https://apk.cgr.dev/" + orgName,
			Auth:    TokenSourceAuth(ts),
		})
	}
	if orgUIDP != "" {
		repos = append(repos,
			Repository{Name: "chainguard", BaseURL: "https://virtualapk.cgr.dev/" + orgUIDP + "/chainguard"},
			Repository{Name: "extra-packages", BaseURL: "https://virtualapk.cgr.dev/" + orgUIDP + "/extra-packages"},
		)
	}
	return repos
}

// RepositoriesFromURLs builds a Repository list from bare repo-root
// URLs. Each URL must be http(s) with a non-empty host and no arch
// suffix — the loader appends `/{arch}/APKINDEX.tar.gz` itself, so a
// path like `.../x86_64` would break the fetch. Repository names are
// derived from the URL: the last path segment if there is one, else
// the host.
//
// Every returned Repository carries an AuthFunc that consults
// HTTP_AUTH at request time; see HTTPAuthFor for the format. When the
// env var is unset or targets a different host the AuthFunc returns
// an empty credential, which the loader treats as "no auth".
func RepositoriesFromURLs(urls []string) ([]Repository, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	repos := make([]Repository, 0, len(urls))
	for _, raw := range urls {
		u, err := url.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("apk-repository %q: %w", raw, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("apk-repository %q: scheme must be http or https", raw)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("apk-repository %q: missing host", raw)
		}
		trimmed := strings.TrimRight(u.Path, "/")
		if last := path.Base(trimmed); last == "x86_64" || last == "aarch64" {
			return nil, fmt.Errorf("apk-repository %q: pass the repo root, not the per-arch path (drop /%s)", raw, last)
		}
		if u.Scheme == "http" && httpAuthTargets(u.Host) {
			return nil, fmt.Errorf("apk-repository %q: HTTP_AUTH is set for %s; refusing to send Basic credentials over plaintext http (use https)", raw, u.Host)
		}
		name := path.Base(trimmed)
		if name == "" || name == "." || name == "/" {
			name = u.Host
		}
		repos = append(repos, Repository{
			Name:    name,
			BaseURL: strings.TrimRight(raw, "/"),
			Auth:    HTTPAuthFor(u.Host),
		})
	}
	return repos, nil
}

// httpAuthTargets reports whether HTTP_AUTH is currently set to a
// well-formed Basic credential targeting host. Used to catch the
// plaintext-transport + Basic-auth combination at config time.
func httpAuthTargets(host string) bool {
	env := os.Getenv("HTTP_AUTH")
	if env == "" {
		return false
	}
	parts := strings.SplitN(env, ":", 4)
	return len(parts) == 4 && parts[0] == "basic" && parts[1] == host
}

// HTTPAuthFor returns an AuthFunc that reads the HTTP_AUTH env var on
// each call and returns a `user:password` credential when the env
// var's host matches host. Format:
//
//	HTTP_AUTH=basic:<host>:<user>:<password>
//
// Only one credential entry per variable, only Basic auth, exact host
// match. When the env var is unset or matches a different host the
// returned AuthFunc yields an empty string, which the loader treats
// as no auth.
func HTTPAuthFor(host string) AuthFunc {
	return func(_ context.Context) (string, error) {
		env := os.Getenv("HTTP_AUTH")
		if env == "" {
			return "", nil
		}
		// SplitN keeps the password intact if it contains colons.
		parts := strings.SplitN(env, ":", 4)
		if len(parts) != 4 || parts[0] != "basic" {
			return "", fmt.Errorf("HTTP_AUTH: expected basic:<host>:<user>:<password>")
		}
		if parts[1] != host {
			return "", nil
		}
		return parts[2] + ":" + parts[3], nil
	}
}

