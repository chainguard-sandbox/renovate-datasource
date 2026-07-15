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
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Release is one version of a package as it appears in an APKINDEX.
type Release struct {
	// Version is the raw apk version, e.g. "1.2.3-r0".
	Version string
	// Timestamp is the package's build/commit time (`t:` field). Zero
	// when the index doesn't carry one.
	Timestamp time.Time
}

// IndexStore holds the parsed indexes indexed by package name. Safe
// for concurrent reads; a single writer at a time via Replace.
type IndexStore struct {
	mu       sync.RWMutex
	releases map[string][]Release
	// real records which (name, version) pairs came from a P:/V: field
	// (as opposed to a p: entry). Consulted by Providers() to fold in
	// the self-provider when the pair is itself installable.
	real map[string]struct{}
	// provides maps every `p:` entry (keyed as "name=version") to the
	// real packages that emit it. Real packages don't appear as their
	// own providers here; Providers() folds that self-entry in.
	provides map[string][]PackageVersion
}

// providesKey builds the composite key used for the `real` set and the
// `provides` reverse index. Package names can't contain `=` so this
// mapping is unambiguous.
func providesKey(name, version string) string { return name + "=" + version }

// NewIndexStore returns an empty IndexStore. Get on an empty store
// always yields nil, matching the "no releases known" case the handler
// treats as 404. Callers wiring up a live datasource typically want
// NewIndexStoreWithRefresh instead — this bare constructor is for
// tests and any manually-populated fixtures.
func NewIndexStore() *IndexStore {
	return &IndexStore{
		releases: map[string][]Release{},
		real:     map[string]struct{}{},
		provides: map[string][]PackageVersion{},
	}
}

// NewIndexStoreWithRefresh returns an IndexStore that's been populated
// from repos (blocking on the initial Load so a boot-time
// misconfiguration surfaces immediately) and kept up to date by a
// background goroutine tied to ctx. On initial-load failure the store
// is still returned — empty but usable — so the caller can decide
// whether to keep serving until the next refresh succeeds or bail.
// interval <= 0 skips the background refresh; the store keeps the
// initial snapshot for the process's lifetime.
func NewIndexStoreWithRefresh(ctx context.Context, arch string, repos []Repository, interval time.Duration, log *slog.Logger) (*IndexStore, error) {
	store := NewIndexStore()
	loader := NewIndexLoader(store, arch, repos, log)
	err := loader.Load(ctx)
	go loader.Refresh(ctx, interval)
	return store, err
}

// Get returns the releases for name, or nil if the package isn't known.
// The returned slice is a shallow copy so callers can sort/filter
// without holding the lock.
func (s *IndexStore) Get(name string) []Release {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rs := s.releases[name]
	if len(rs) == 0 {
		return nil
	}
	out := make([]Release, len(rs))
	copy(out, rs)
	return out
}

// Providers returns the real packages that make name+version resolvable
// at install time. If the pair is itself a real package it appears
// first in the slice (as a self-provider), so callers can treat the
// result as authoritative without a separate "is this real?" check.
// Nil when nothing in the store provides (name, version).
func (s *IndexStore) Providers(name, version string) []PackageVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := providesKey(name, version)
	_, isReal := s.real[key]
	others := s.provides[key]
	if !isReal && len(others) == 0 {
		return nil
	}
	out := make([]PackageVersion, 0, len(others)+1)
	if isReal {
		out = append(out, PackageVersion{Name: name, Version: version})
	}
	out = append(out, others...)
	return out
}

// Len returns the number of packages currently indexed. Useful for
// startup log lines and /readyz-style diagnostics.
func (s *IndexStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.releases)
}

// Replace atomically swaps the store's contents. IndexLoader calls
// this once per refresh with the fully-rebuilt maps so readers never
// observe a partial update. All three arguments are installed together.
func (s *IndexStore) Replace(releases map[string][]Release, real map[string]struct{}, provides map[string][]PackageVersion) {
	if real == nil {
		real = map[string]struct{}{}
	}
	if provides == nil {
		provides = map[string][]PackageVersion{}
	}
	s.mu.Lock()
	s.releases = releases
	s.real = real
	s.provides = provides
	s.mu.Unlock()
}

// IndexLoader fetches APKINDEX.tar.gz feeds from a Repository chain
// and installs the merged result into an IndexStore. A single loader
// is safe for concurrent Load calls but that isn't necessary in
// practice — main.go serialises them behind a ticker.
type IndexLoader struct {
	repos  []Repository
	arch   string
	client *http.Client
	store  *IndexStore
	log    *slog.Logger
}

// NewIndexLoader wires up an IndexLoader with a client whose timeouts
// bound the worst-case index fetch. The indexes are on the order of
// 10-20 MiB today so the read timeout is deliberately generous. arch
// is the apk-style architecture string; the loader appends
// /{arch}/APKINDEX.tar.gz to each Repository's BaseURL.
func NewIndexLoader(store *IndexStore, arch string, repos []Repository, log *slog.Logger) *IndexLoader {
	return &IndexLoader{
		repos:  repos,
		arch:   arch,
		client: &http.Client{Timeout: 60 * time.Second},
		store:  store,
		log:    log,
	}
}

// indexData is the mutable scratch space parseIndex/parseAPKINDEX write
// into. Bundling the three maps into one struct keeps callers (parser,
// dedupe, IndexLoader) from threading them individually and makes it
// easy to swap the whole thing atomically when Load finishes.
type indexData struct {
	releases map[string][]Release
	real     map[string]struct{}
	provides map[string][]PackageVersion
}

func newIndexData() *indexData {
	return &indexData{
		releases: map[string][]Release{},
		real:     map[string]struct{}{},
		provides: map[string][]PackageVersion{},
	}
}

// Refresh re-runs Load on the given interval until ctx cancels.
// Errors are logged and don't clear the existing store, so a transient
// upstream outage between refreshes just means the served data is a
// bit stale. Blocks — call in a goroutine. interval <= 0 returns
// immediately so the caller can toggle background refresh by config
// without a wrapping conditional.
func (l *IndexLoader) Refresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := l.Load(ctx); err != nil {
				l.log.WarnContext(ctx, "apk index refresh failed; keeping previous snapshot", "err", err)
			}
		}
	}
}

// Load fetches every configured repository, parses each index, merges
// the releases into a single map, and atomically installs it into the
// store. Per-source failures are logged and skipped so a transient
// outage on one repo (or a misconfigured URL) doesn't take down the
// whole index — but if every source fails Load returns the last error
// so callers can decide whether to keep the previous snapshot.
func (l *IndexLoader) Load(ctx context.Context) error {
	merged := newIndexData()
	var lastErr error
	var loaded int
	for _, repo := range l.repos {
		indexURL := fmt.Sprintf("%s/%s/APKINDEX.tar.gz", repo.BaseURL, l.arch)
		n, err := l.loadOne(ctx, repo, indexURL, merged)
		if err != nil {
			l.log.WarnContext(ctx, "apk index fetch failed", "source", repo.Name, "url", indexURL, "err", err)
			lastErr = err
			continue
		}
		loaded++
		l.log.InfoContext(ctx, "apk index loaded", "source", repo.Name, "packages", n)
	}
	if loaded == 0 && lastErr != nil {
		return fmt.Errorf("all apk index sources failed; last error: %w", lastErr)
	}
	dedupe(merged)
	l.store.Replace(merged.releases, merged.real, merged.provides)
	l.log.InfoContext(ctx, "apk index installed", "sources", loaded, "packages", len(merged.releases), "provides", len(merged.provides))
	return nil
}

// dedupe collapses per-name release lists so each Version appears at
// most once, keeping the entry with the latest Timestamp. Chainguard's
// org-scoped apk repo mirrors chainguard/extra-packages, so before this
// pass the merged map typically carries every version 2-3× with
// identical or near-identical build times; deduping cuts the resident
// footprint of the store substantially and avoids returning duplicate
// versions to Renovate.
//
// PackageVersion slices go through the same treatment: multiple sources
// carrying the same real package emit a PackageVersion entry per
// source, so without dedup a `cmd:node=X` lookup would surface the
// same underlying nodejs-N package 2-3× in the chooser UI.
func dedupe(d *indexData) {
	for name, releases := range d.releases {
		if len(releases) < 2 {
			continue
		}
		byVersion := make(map[string]Release, len(releases))
		for _, r := range releases {
			existing, ok := byVersion[r.Version]
			if !ok || r.Timestamp.After(existing.Timestamp) {
				byVersion[r.Version] = r
			}
		}
		if len(byVersion) == len(releases) {
			continue // nothing to collapse
		}
		out := make([]Release, 0, len(byVersion))
		for _, r := range byVersion {
			out = append(out, r)
		}
		d.releases[name] = out
	}
	for key, providers := range d.provides {
		if len(providers) < 2 {
			continue
		}
		seen := make(map[PackageVersion]struct{}, len(providers))
		out := providers[:0]
		for _, p := range providers {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
		d.provides[key] = out
	}
}

func (l *IndexLoader) loadOne(ctx context.Context, repo Repository, indexURL string, out *indexData) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}
	if repo.Auth != nil {
		cred, err := repo.Auth(ctx)
		if err != nil {
			return 0, fmt.Errorf("resolving auth: %w", err)
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetching: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	return parseIndex(resp.Body, out)
}

// parseIndex walks an APKINDEX.tar.gz stream and appends every package
// entry it contains into out, keyed by package name. The tarball
// typically contains signature files plus the APKINDEX text blob plus a
// DESCRIPTION; only APKINDEX is parsed, the rest are skipped.
//
// The APKINDEX format is line-oriented "K:value" fields with blank
// lines separating package records. See
// https://wiki.alpinelinux.org/wiki/Apk_spec#APKINDEX_Format for the
// canonical grammar; we only look at the fields we care about (P, V,
// t, C).
func parseIndex(r io.Reader, out *indexData) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("tar entry: %w", err)
		}
		if hdr.Name != "APKINDEX" {
			continue
		}
		return parseAPKINDEX(tr, out)
	}
	return 0, errors.New("APKINDEX not found in archive")
}

func parseAPKINDEX(r io.Reader, out *indexData) (int, error) {
	br := bufio.NewScanner(r)
	// Individual APKINDEX lines are short (metadata), but a package
	// record can list many dependencies. Bump the buffer well past the
	// default 64 KiB so long D:/p: lines don't overflow.
	br.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cur Release
	var name string
	var provides string
	added := 0

	flush := func() {
		if name != "" && cur.Version != "" {
			out.releases[name] = append(out.releases[name], cur)
			out.real[providesKey(name, cur.Version)] = struct{}{}
			added++
			addProvides(provides, name, cur.Version, cur.Timestamp, out)
		}
		cur = Release{}
		name = ""
		provides = ""
	}

	for br.Scan() {
		line := br.Text()
		if line == "" {
			flush()
			continue
		}
		// Field lines look like `K:value` where K is a single letter.
		if len(line) < 2 || line[1] != ':' {
			continue
		}
		key, val := line[0], line[2:]
		switch key {
		case 'P':
			name = val
		case 'V':
			cur.Version = val
		case 't':
			if secs, err := strconv.ParseInt(val, 10, 64); err == nil {
				cur.Timestamp = time.Unix(secs, 0).UTC()
			}
		case 'p':
			provides = val
		}
	}
	if err := br.Err(); err != nil {
		return added, fmt.Errorf("scanning APKINDEX: %w", err)
	}
	// Trailing package with no blank line after it.
	flush()
	return added, nil
}

// addProvides emits a Release for each `p:` entry on a package record
// so pins like `apk add nodejs=24.14.0-r0` (which resolve via
// `nodejs-24`'s `p:nodejs=24.14.0-r0`) return a real releases list from
// the datasource. Prefixed capabilities (`cmd:foo`, `so:libbar.so.1`,
// `pc:openssl`, and the like) are indexed too so callers can look up
// versions against the same names apk-tools resolves against.
//
// realName / realVer are the P:/V: values of the record emitting the
// `p:` entries; they're stashed in the provides reverse index so the
// diff handler can trace a provides pin (e.g. `cmd:node=24.14.0-r0`)
// back to the installable package (`nodejs-24 24.14.0-r0`) whose .apk
// it actually needs to fetch.
//
// The `p:` field is a space-separated list of `name[=version]` entries.
// Nameonly entries (no `=`) are skipped since there's no version to
// update against, and the `=0` "unversioned provide" idiom (e.g.
// python-3.14 declaring `p:python-3=0`) is skipped for the same reason
// — surfacing it would just add a bogus "version 0" to the response.
//
// Timestamp is the package's build time. When multiple packages provide
// the same name at the same version, dedupe() at merge time collapses
// both the release entries (by newest timestamp) and the provider list
// (uniquing by PackageVersion identity across sources).
func addProvides(list string, realName, realVer string, ts time.Time, out *indexData) {
	if list == "" {
		return
	}
	for _, tok := range strings.Fields(list) {
		eq := strings.IndexByte(tok, '=')
		if eq <= 0 {
			continue
		}
		name, ver := tok[:eq], tok[eq+1:]
		if ver == "0" {
			continue
		}
		out.releases[name] = append(out.releases[name], Release{Version: ver, Timestamp: ts})
		key := providesKey(name, ver)
		out.provides[key] = append(out.provides[key], PackageVersion{Name: realName, Version: realVer})
	}
}
