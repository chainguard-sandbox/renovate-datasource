// Package apk pulls .apk artifacts and their APKINDEX metadata from
// Chainguard repositories.
//
// Two entry points share the same repository chain (see Repository and
// DefaultRepositories):
//
//   - Fetcher downloads a single .apk artifact, walking the chain on
//     404, and extracts the .melange.yaml + .PKGINFO from its control
//     section.
//   - IndexLoader downloads each repository's APKINDEX.tar.gz, parses
//     it, and installs the merged release list into an IndexStore.
//
// Keeping the two consumers on a single Repository type means anything
// the index surfaces is also fetchable — no surprise "listed but not
// fetchable" gaps.
package apk

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
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

// PackageVersion identifies an installable apk (P:/V: from an APKINDEX
// record) — either as a real package's own identity or as one of the
// packages that emit a `p:` capability. The diff/version handlers use
// it to map a capability pin back to the real package whose .apk they
// need to fetch. JSON tags feed the candidates payload the handlers
// return on ambiguous resolution.
type PackageVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
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

// ErrNotFound is returned when no repository in the fallback chain has
// the requested apk. Distinct from "apk exists but is missing some
// entry"; the latter shows up as empty fields in Contents.
var ErrNotFound = errors.New("apk artifact not found")

// fileLimit caps the in-memory extraction of any single control entry
// (.melange.yaml / .PKGINFO). Real Chainguard files are well under
// 1 MiB; this is generous headroom.
const fileLimit = 4 << 20

// maxStreams caps how many gzip streams extractContents will walk
// before giving up. Real apks are signature + control + data → 3
// streams; anything wildly beyond that is either malformed or a
// malicious artifact trying to burn CPU by burying the entries we
// want behind thousands of empty streams.
const maxStreams = 32

// Contents is the trimmed view of an apk that the diff package consumes.
// URL is the fully-qualified location the apk was resolved at — useful
// for surfacing "fetched from X" provenance in the UI when the fallback
// chain lands somewhere other than the org-scoped primary repo.
// Either of the data fields can be empty independently — an apk may
// carry .PKGINFO but no .melange.yaml, or vice versa.
type Contents struct {
	URL     string
	Melange []byte
	PKGINFO []byte
}

// Fetcher pulls Contents from apk artifacts, walking a fixed chain of
// repositories on 404 so packages outside the configured org's private
// repo still resolve. arch is the apk-style architecture string
// ("x86_64", "aarch64").
type Fetcher struct {
	arch    string
	client  *http.Client
	sources []fetchSource
}

// fetchSource is the per-request state Fetcher keeps around each
// Repository — the repo itself plus a per-source rate limiter so a
// burst of diffs against many packages can't hammer a single upstream.
type fetchSource struct {
	repo    Repository
	limiter *rate.Limiter
}

// defaultSourceRPS / defaultSourceBurst are the rate-limiter settings
// applied to each source in the fallback chain. They're generous
// enough that an interactive user is never throttled but tight enough
// that a runaway loop can't generate dozens of requests per second.
const (
	defaultSourceRPS   = 10
	defaultSourceBurst = 20
)

// NewFetcher wires up a Fetcher against the given repository chain.
// repos should typically be the same slice fed to NewIndexLoader so
// the index and the artifact fetcher stay in sync.
func NewFetcher(arch string, repos []Repository) *Fetcher {
	sources := make([]fetchSource, 0, len(repos))
	for _, r := range repos {
		sources = append(sources, fetchSource{
			repo:    r,
			limiter: rate.NewLimiter(rate.Limit(defaultSourceRPS), defaultSourceBurst),
		})
	}
	return &Fetcher{
		arch:    arch,
		client:  newHTTPClient(),
		sources: sources,
	}
}

// newHTTPClient bounds every stage of an apk fetch so a hung upstream
// can't pin a goroutine for longer than ~apkRequestTimeout. The values
// are deliberately tighter than the server's WriteTimeout so an
// individual fetch failing leaves time to return an error to the client.
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       60 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   20 * time.Second,
	}
}

// Fetch downloads the apk identified by name and version, walking the
// repository chain and returning the first hit's parsed Contents.
// Returns ErrNotFound only when every source 404s; other errors
// propagate immediately so transient or auth failures aren't silently
// covered by a subsequent 404.
func (f *Fetcher) Fetch(ctx context.Context, name, version string) (*Contents, error) {
	var lastErr error
	for _, src := range f.sources {
		c, err := f.fetchFromSource(ctx, src, name, version)
		if err == nil {
			return c, nil
		}
		if errors.Is(err, ErrNotFound) {
			lastErr = err
			continue
		}
		return nil, err
	}
	if lastErr == nil {
		lastErr = ErrNotFound
	}
	return nil, lastErr
}

func (f *Fetcher) fetchFromSource(ctx context.Context, src fetchSource, name, version string) (*Contents, error) {
	if err := src.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limiter wait for %s: %w", src.repo.BaseURL, err)
	}
	// name and version are already validated against apkNamePattern /
	// apkVersionPattern at the HTTP boundary, so PathEscape here is
	// belt-and-braces — it keeps the URL well-formed even if either
	// pattern is ever relaxed. Matches the same convention used by the
	// apk version-page URL builder in the server package.
	fetchURL := fmt.Sprintf("%s/%s/%s-%s.apk", src.repo.BaseURL, f.arch, url.PathEscape(name), url.PathEscape(version))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building apk request: %w", err)
	}
	if src.repo.Auth != nil {
		cred, err := src.repo.Auth(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving auth for %s: %w", src.repo.BaseURL, err)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", fetchURL, err)
	}
	// Close without draining: extractContents returns as soon as the
	// control entries are captured, and for large apks (nodejs, glibc,
	// …) the unread tail is tens of megabytes of compressed data
	// stream. Draining for keepalive would mean downloading that entire
	// tail on every request, which is far more expensive than a fresh
	// TCP dial on the next lookup.
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		c, err := extractContents(resp.Body)
		if err != nil {
			return nil, err
		}
		c.URL = fetchURL
		return c, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w (%s)", ErrNotFound, fetchURL)
	default:
		return nil, fmt.Errorf("apk fetch %s: status %d", fetchURL, resp.StatusCode)
	}
}

// extractContents walks an apk's concatenated gzip-stream tar archives
// and pulls out the .melange.yaml and the .PKGINFO. Returns as soon as
// both have been captured so the (potentially very large) data stream
// is never decompressed.
//
// apks are not a single tarball — they're a sequence of independent
// gzip-wrapped tar archives (signature, control, data) concatenated on
// the wire. We wrap r in a bufio.Reader once and spin up a fresh
// gzip.Reader per stream, so the buffered position carries across
// streams. Multistream is disabled so each archive ends deterministically.
func extractContents(r io.Reader) (*Contents, error) {
	br := bufio.NewReader(r)
	out := &Contents{}
	for streams := 0; ; streams++ {
		if out.Melange != nil && out.PKGINFO != nil {
			return out, nil
		}
		if streams >= maxStreams {
			return nil, fmt.Errorf("apk has more than %d gzip streams", maxStreams)
		}
		gr, err := gzip.NewReader(br)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		gr.Multistream(false)

		if err := scanTarStream(gr, out); err != nil {
			_ = gr.Close()
			return nil, err
		}
		// Drain any trailing bytes of this stream so the bufio reader is
		// positioned at the start of the next gzip header before we loop.
		if _, err := io.Copy(io.Discard, gr); err != nil {
			_ = gr.Close()
			return nil, fmt.Errorf("draining gzip stream: %w", err)
		}
		if err := gr.Close(); err != nil {
			return nil, fmt.Errorf("closing gzip stream: %w", err)
		}
	}
}

// scanTarStream reads tar entries from r and captures the contents of
// .melange.yaml / .PKGINFO when they appear. Other entries are skipped
// (tar.Reader.Next implicitly advances past their bodies).
func scanTarStream(r io.Reader, out *Contents) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar entry: %w", err)
		}
		switch path.Base(hdr.Name) {
		case ".melange.yaml":
			b, err := io.ReadAll(io.LimitReader(tr, fileLimit))
			if err != nil {
				return fmt.Errorf("reading .melange.yaml: %w", err)
			}
			out.Melange = b
		case ".PKGINFO":
			b, err := io.ReadAll(io.LimitReader(tr, fileLimit))
			if err != nil {
				return fmt.Errorf("reading .PKGINFO: %w", err)
			}
			out.PKGINFO = b
		}
	}
}
