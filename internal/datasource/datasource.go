// Package datasource holds the JSON shapes and per-datasource
// implementations shared by the HTTP server and the snapshot
// generator, so their outputs can't drift.
package datasource

import (
	"context"
	"errors"
	"time"
)

// Release is one entry in a /releases response. Digest is omitted for
// datasources that don't have one (e.g. apk pins), which is why it
// carries omitempty.
type Release struct {
	Version          string    `json:"version"`
	ReleaseTimestamp time.Time `json:"releaseTimestamp"`
	Digest           string    `json:"digest,omitempty"`
}

// Response is the top-level shape of a /releases response.
type Response struct {
	Releases []Release `json:"releases"`
}

// ErrNotFound is returned by Datasource.Releases when name isn't known.
// Callers distinguish this from real failures so a 404 (or a
// snapshot skip) is possible without conflating it with backend
// errors.
var ErrNotFound = errors.New("datasource: package not found")

// InvalidPackageNameError is returned by Datasource.Releases when name doesn't
// match the source's own naming rules. Message is safe to surface
// as an HTTP 400 body.
type InvalidPackageNameError struct{ Message string }

// Error returns the client-facing message verbatim.
func (e *InvalidPackageNameError) Error() string { return e.Message }

// Datasource is the read-only view onto one datasource. Both the HTTP
// server and the snapshot generator go through this — server builds
// per-request responses, snapshot iterates every entry.
type Datasource interface {
	// PackageNames returns every package name this source knows
	// about. Callers pass each name back to Releases to fetch its
	// releases (e.g. "curl", "cmd:node" for apk; "python",
	// "charts/nginx" for repo).
	PackageNames(ctx context.Context) ([]string, error)

	// Releases returns the releases for packageName. If before is
	// non-zero, releases whose timestamp is after that instant are
	// filtered out; callers precompute before =
	// now.Add(-minimumReleaseAge) so the same cutoff can be shared
	// across every Releases call in a batch. Zero before disables
	// the filter entirely.
	//
	// Malformed packageNames produce a *InvalidPackageNameError so a
	// bad URL segment turns into a 400 rather than falling through
	// to a lookup. Unknown but well-formed packageNames produce
	// ErrNotFound so callers can distinguish 404 from real backend
	// failures.
	Releases(ctx context.Context, packageName string, before time.Time) ([]Release, error)

	// Ready reports whether the source is usable. Implementations
	// should keep this cheap — no network calls — so /readyz can
	// probe it on every request.
	Ready(ctx context.Context) error
}
