// Package apk pulls the user-visible metadata of Chainguard apk artifacts.
//
// Lookups walk a fixed chain of repositories so packages that live in
// the user's private org repo, the public chainguard repo, the extras
// repo, or the wolfi os repo can all be diffed without per-call
// configuration. Only the org-scoped lookup is authenticated.
//
// One Fetch returns the .melange.yaml and the .PKGINFO. Extraction
// stops as soon as both are captured so we never decompress the data
// stream (typically the bulk of the apk).
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
	"path"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

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
	sources []repoSource
}

// repoSource describes one repository in the lookup chain. ts is nil
// for unauthenticated public repos; non-nil sources have a Basic-auth
// header attached with `user:<token>` per Chainguard's apk.cgr.dev
// convention. limiter caps the per-source request rate so a burst of
// diffs against many packages can't hammer a single upstream (notably
// the public wolfi.dev / packages.cgr.dev hosts).
type repoSource struct {
	baseURL string
	ts      oauth2.TokenSource
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

// New constructs a Fetcher. ts must mint apk.cgr.dev-audience tokens
// for the org-scoped primary repo. Fallback repos (chainguard, extras,
// wolfi os) are hit unauthenticated. When orgName is empty the primary
// source is omitted so we don't generate broken `https://apk.cgr.dev//…`
// URLs; lookups proceed straight to the public fallbacks.
func New(orgName, arch string, ts oauth2.TokenSource) *Fetcher {
	limiter := func() *rate.Limiter {
		return rate.NewLimiter(rate.Limit(defaultSourceRPS), defaultSourceBurst)
	}
	sources := make([]repoSource, 0, 4)
	if orgName != "" {
		sources = append(sources, repoSource{
			baseURL: "https://apk.cgr.dev/" + orgName,
			ts:      ts,
			limiter: limiter(),
		})
	}
	sources = append(sources,
		repoSource{baseURL: "https://apk.cgr.dev/chainguard", limiter: limiter()},
		repoSource{baseURL: "https://packages.cgr.dev/extras", limiter: limiter()},
		repoSource{baseURL: "https://packages.wolfi.dev/os", limiter: limiter()},
	)
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

func (f *Fetcher) fetchFromSource(ctx context.Context, src repoSource, name, version string) (*Contents, error) {
	if src.limiter != nil {
		if err := src.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter wait for %s: %w", src.baseURL, err)
		}
	}
	url := fmt.Sprintf("%s/%s/%s-%s.apk", src.baseURL, f.arch, name, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building apk request: %w", err)
	}
	if src.ts != nil {
		tok, err := src.ts.Token()
		if err != nil {
			return nil, fmt.Errorf("getting apk.cgr.dev token: %w", err)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+tok.AccessToken)))
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
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
		c.URL = url
		return c, nil
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w (%s)", ErrNotFound, url)
	default:
		return nil, fmt.Errorf("apk fetch %s: status %d", url, resp.StatusCode)
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
