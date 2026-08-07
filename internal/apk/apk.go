// Package apk pulls APKINDEX metadata from Chainguard repositories.
// IndexLoader downloads each repository's APKINDEX.tar.gz, parses
// it, and installs the merged release list into an IndexStore.
package apk

import (
	"context"

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

