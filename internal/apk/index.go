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
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Bounds on the `t:` field: time.Time.MarshalJSON only accepts years
// 0001..9999, so seconds outside this range would fail JSON encoding
// in the live handler or during snapshot generation.
const (
	minJSONUnixSec int64 = -62135596800 // 0001-01-01T00:00:00Z
	maxJSONUnixSec int64 = 253402300799 // 9999-12-31T23:59:59Z
)

// PackageVersion is one version of a package as it appears in an APKINDEX.
type PackageVersion struct {
	// Version is the raw apk version, e.g. "1.2.3-r0".
	Version string
	// Timestamp is the package's build/commit time (`t:` field). Zero
	// when the index doesn't carry one.
	Timestamp time.Time
	// Arch is the architecture the entry was fetched under (e.g.
	// "x86_64", "aarch64"). Set by the loader from the arch it was
	// serving when the entry was parsed.
	Arch string
}

// IndexStore holds the parsed indexes indexed by package name. Safe
// for concurrent reads; a single writer at a time via Replace.
type IndexStore struct {
	mu       sync.RWMutex
	releases map[string][]PackageVersion
	archs    map[string]struct{}
}

// NewIndexStore returns an empty IndexStore. Get on an empty store
// always yields nil, matching the "no releases known" case the handler
// treats as 404. Callers wiring up a live datasource typically want
// NewIndexStoreWithRefresh instead — this bare constructor is for
// tests and any manually-populated fixtures.
func NewIndexStore() *IndexStore {
	return &IndexStore{
		releases: map[string][]PackageVersion{},
		archs:    map[string]struct{}{},
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
func NewIndexStoreWithRefresh(ctx context.Context, archs []string, repos []Repository, interval time.Duration, log *slog.Logger) (*IndexStore, error) {
	store := NewIndexStore()
	loader, err := NewIndexLoader(store, archs, repos, log)
	if err != nil {
		return store, err
	}
	if err := loader.Load(ctx); err != nil {
		return store, err
	}
	go loader.Refresh(ctx, interval)
	return store, nil
}

// Get returns the releases for name, filtered to arch when non-empty.
// The returned slice is a shallow copy so callers can sort/filter
// without holding the lock.
func (s *IndexStore) Get(name, arch string) []PackageVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rs := s.releases[name]
	if len(rs) == 0 {
		return nil
	}
	if arch == "" {
		out := make([]PackageVersion, len(rs))
		copy(out, rs)
		return out
	}
	out := make([]PackageVersion, 0, len(rs))
	for _, r := range rs {
		if r.Arch == arch {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Archs returns the sorted list of architectures the store was loaded
// with. Used by the datasource layer to validate ?arch= query values
// before hitting the store.
func (s *IndexStore) Archs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.archs))
	for a := range s.archs {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Len returns the number of packages currently indexed. Useful for
// startup log lines and /readyz-style diagnostics.
func (s *IndexStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.releases)
}

// All returns every known package name, sorted. Callers iterate the
// result and pass each name back through Get to fetch the releases.
// The snapshot generator uses this to enumerate every feed to write.
func (s *IndexStore) All() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.releases))
	for name := range s.releases {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Replace atomically swaps the store's contents. IndexLoader calls
// this once per refresh with the fully-rebuilt map so readers never
// observe a partial update. archs records the set of architectures
// present in this snapshot so Archs() can report it back.
func (s *IndexStore) Replace(releases map[string][]PackageVersion, archs []string) {
	set := make(map[string]struct{}, len(archs))
	for _, a := range archs {
		set[a] = struct{}{}
	}
	s.mu.Lock()
	s.releases = releases
	s.archs = set
	s.mu.Unlock()
}

// IndexLoader fetches APKINDEX.tar.gz feeds from a Repository chain
// and installs the merged result into an IndexStore. A single loader
// is safe for concurrent Load calls but that isn't necessary in
// practice — main.go serialises them behind a ticker.
type IndexLoader struct {
	repos  []Repository
	archs  []string
	client *http.Client
	store  *IndexStore
	log    *slog.Logger
}

// NewIndexLoader wires up an IndexLoader with a client whose timeouts
// bound the worst-case index fetch. The indexes are on the order of
// 10-20 MiB today so the read timeout is deliberately generous. archs
// is the list of apk-style architecture strings to fetch; the loader
// appends /{arch}/APKINDEX.tar.gz to each Repository's BaseURL for
// every arch. An empty archs list is rejected.
func NewIndexLoader(store *IndexStore, archs []string, repos []Repository, log *slog.Logger) (*IndexLoader, error) {
	if len(archs) == 0 {
		return nil, errors.New("apk: at least one architecture is required")
	}
	return &IndexLoader{
		repos:  repos,
		archs:  archs,
		client: newIndexHTTPClient(),
		store:  store,
		log:    log,
	}, nil
}

// newIndexHTTPClient mirrors the bounded transport in apk.go's
// newHTTPClient but with longer timeouts sized for 10-20 MiB index
// downloads. Without the explicit transport, a slow TLS peer could
// hang initial load past the 60s client timeout.
func newIndexHTTPClient() *http.Client {
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
		Timeout:   60 * time.Second,
	}
}

// indexData is the mutable scratch space parseIndex/parseAPKINDEX write
// into. Wrapping the map in a struct keeps room for future scratch state
// (e.g. per-source counters) without threading extra arguments.
type indexData struct {
	releases map[string][]PackageVersion
}

func newIndexData() *indexData {
	return &indexData{
		releases: map[string][]PackageVersion{},
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

// Load fetches every (arch, repo) pair, parses each index, merges the
// releases into a single map, and atomically installs it into the
// store. Any fetch failure returns an error and the store is left
// untouched — Chainguard's contract is that every repo carries every
// supported arch, so a missing feed is a real problem worth
// surfacing rather than silently serving partial data.
func (l *IndexLoader) Load(ctx context.Context) error {
	merged := newIndexData()
	var loaded int
	for _, arch := range l.archs {
		for _, repo := range l.repos {
			indexURL := fmt.Sprintf("%s/%s/APKINDEX.tar.gz", repo.BaseURL, arch)
			n, err := l.loadOne(ctx, repo, arch, indexURL, merged)
			if err != nil {
				l.log.WarnContext(ctx, "apk index fetch failed", "source", repo.Name, "arch", arch, "url", indexURL, "err", err)
				return fmt.Errorf("apk index fetch failed for %s (%s): %w", repo.Name, arch, err)
			}
			loaded++
			l.log.InfoContext(ctx, "apk index loaded", "source", repo.Name, "arch", arch, "packages", n)
		}
	}
	dedupe(merged)
	l.store.Replace(merged.releases, l.archs)
	l.log.InfoContext(ctx, "apk index installed", "sources", loaded, "archs", len(l.archs), "packages", len(merged.releases))
	return nil
}

// dedupe collapses per-name release lists so each (arch, Version)
// pair appears at most once, keeping the entry with the latest
// Timestamp. Chainguard's org-scoped apk repo mirrors
// chainguard/extra-packages so before this pass the merged map
// typically carries every version 2-3× per arch with identical or
// near-identical build times.
func dedupe(d *indexData) {
	type key struct{ arch, version string }
	for name, releases := range d.releases {
		if len(releases) < 2 {
			continue
		}
		byKey := make(map[key]PackageVersion, len(releases))
		for _, r := range releases {
			k := key{arch: r.Arch, version: r.Version}
			existing, ok := byKey[k]
			if !ok || r.Timestamp.After(existing.Timestamp) {
				byKey[k] = r
			}
		}
		if len(byKey) == len(releases) {
			continue
		}
		out := make([]PackageVersion, 0, len(byKey))
		for _, r := range byKey {
			out = append(out, r)
		}
		d.releases[name] = out
	}
}

func (l *IndexLoader) loadOne(ctx context.Context, repo Repository, arch, indexURL string, out *indexData) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}
	if repo.Auth != nil {
		cred, err := repo.Auth(ctx)
		if err != nil {
			return 0, fmt.Errorf("resolving auth: %w", err)
		}
		if cred != "" {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(cred)))
		}
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetching: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}
	return parseIndex(resp.Body, arch, out)
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
func parseIndex(r io.Reader, arch string, out *indexData) (int, error) {
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
		return parseAPKINDEX(tr, arch, out)
	}
	return 0, errors.New("APKINDEX not found in archive")
}

func parseAPKINDEX(r io.Reader, arch string, out *indexData) (int, error) {
	br := bufio.NewScanner(r)
	// Individual APKINDEX lines are short (metadata), but a package
	// record can list many dependencies. Bump the buffer well past the
	// default 64 KiB so long D:/p: lines don't overflow.
	br.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cur PackageVersion
	var name string
	var provides string
	added := 0

	flush := func() {
		if name != "" && cur.Version != "" {
			cur.Arch = arch
			out.releases[name] = append(out.releases[name], cur)
			added++
			addProvides(provides, arch, cur.Timestamp, out)
		}
		cur = PackageVersion{}
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
			// `t:0` (Unix epoch) is what apk-tools emits when no build
			// timestamp is available. Treat it as absent so downstream
			// filters can distinguish "no signal" from a real
			// timestamp. Values outside the year range time.Time can
			// serialise to JSON are also dropped so a poisoned mirror
			// can't break encoding downstream.
			if secs, err := strconv.ParseInt(val, 10, 64); err == nil && secs != 0 && secs >= minJSONUnixSec && secs <= maxJSONUnixSec {
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

// addProvides emits a PackageVersion for each `p:` entry on a package record
// so pins like `apk add nodejs=24.14.0-r0` (which resolve via
// `nodejs-24`'s `p:nodejs=24.14.0-r0`) return a real releases list from
// the datasource. Prefixed capabilities (`cmd:foo`, `so:libbar.so.1`,
// `pc:openssl`, and the like) are indexed too so callers can look up
// versions against the same names apk-tools resolves against.
//
// The `p:` field is a space-separated list of `name[=version]` entries.
// Nameonly entries (no `=`) are skipped since there's no version to
// update against, and the `=0` "unversioned provide" idiom (e.g.
// python-3.14 declaring `p:python-3=0`) is skipped for the same reason
// — surfacing it would just add a bogus "version 0" to the response.
//
// Timestamp is the package's build time. When multiple packages provide
// the same name at the same version, dedupe() collapses the resulting
// release entries by newest timestamp.
func addProvides(list, arch string, ts time.Time, out *indexData) {
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
		out.releases[name] = append(out.releases[name], PackageVersion{Version: ver, Timestamp: ts, Arch: arch})
	}
}
