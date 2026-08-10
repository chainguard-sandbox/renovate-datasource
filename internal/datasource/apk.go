package datasource

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
)

// apkProvidesNamePattern accepts real apk package names and every
// capability name apk-tools may emit — `cmd:node`, `so:libssl.so.3`,
// `pc:openssl`, or oddities like `cmd:[` (bash's `test` builtin).
//
// Rather than enumerate the allowed shape, reject only what would be
// unsafe or ambiguous: control characters, whitespace, path
// separators, and a leading dot (which would collide with `..` and
// hidden-file conventions on the snapshot filesystem). Bounded
// length keeps URL / filesystem handling predictable.
var apkProvidesNamePattern = regexp.MustCompile(`^[!-,0-~][!-.0-~]{0,127}$`)

// APKDatasource resolves apk package names against an in-memory index.
type APKDatasource struct {
	Store *apk.IndexStore
}

// NewAPKDatasource wraps store in an APKDatasource.
func NewAPKDatasource(store *apk.IndexStore) *APKDatasource {
	return &APKDatasource{Store: store}
}

// PackageNames returns every known package name from the index, sorted.
func (s *APKDatasource) PackageNames(_ context.Context) ([]string, error) {
	return s.Store.All(), nil
}

// Ready reports the source usable when the index has at least one
// package loaded. An empty store typically means the initial fetch
// hasn't succeeded yet; /readyz treats that as not-ready so
// orchestrators can gate traffic.
func (s *APKDatasource) Ready(_ context.Context) error {
	if s.Store.Len() == 0 {
		return errors.New("apk index empty")
	}
	return nil
}

// Releases returns the timestamp-descending release list for
// packageName.
//
// Options:
//
//   - opts.Before: when non-zero, entries newer than the
//     cutoff (and entries with no timestamp at all) are dropped.
//   - opts.Arch: when non-empty, only entries built for that arch are
//     returned; an arch the store wasn't loaded with produces
//     *InvalidArgumentError.
//
// Malformed packageNames produce *InvalidPackageNameError; unknown
// but valid names produce ErrNotFound.
func (s *APKDatasource) Releases(_ context.Context, packageName string, opts ReleasesOptions) ([]Release, error) {
	if !apkProvidesNamePattern.MatchString(packageName) {
		return nil, &InvalidPackageNameError{Message: "The apk package name isn't well-formed."}
	}
	if opts.Arch != "" && !slices.Contains(s.Store.Archs(), opts.Arch) {
		return nil, &InvalidArgumentError{Message: fmt.Sprintf("The arch %q isn't served by this instance.", opts.Arch)}
	}
	entries := s.Store.Get(packageName, opts.Arch)
	if entries == nil {
		return nil, ErrNotFound
	}
	// When opts.Arch is empty the store returns entries from every
	// loaded arch; collapse to one entry per version, keeping the
	// latest timestamp. When opts.Arch is set the store already
	// scoped to that arch so the merge is a no-op.
	if opts.Arch == "" {
		entries = mergeAcrossArchs(entries)
	}
	out := make([]Release, 0, len(entries))
	for _, e := range entries {
		// When a minimumReleaseAge cutoff is set, drop entries we
		// can't prove sit outside the window: anything with a
		// timestamp after the cutoff, and anything with no timestamp
		// at all (we have no evidence it isn't fresh).
		if !opts.Before.IsZero() && (e.Timestamp.IsZero() || e.Timestamp.After(opts.Before)) {
			continue
		}
		out = append(out, Release{
			Version:          e.Version,
			ReleaseTimestamp: e.Timestamp,
		})
	}

	// Newest timestamp first, tie-break by version desc for stable
	// ordering. Renovate parses versions itself so this is a
	// readability choice, not a correctness one.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ReleaseTimestamp.Equal(out[j].ReleaseTimestamp) {
			return out[i].ReleaseTimestamp.After(out[j].ReleaseTimestamp)
		}
		return out[i].Version > out[j].Version
	})
	return out, nil
}

// mergeAcrossArchs collapses entries with the same Version, keeping
// the one with the latest Timestamp. Used when opts.Arch is empty so
// a package present on multiple archs shows up once in the response.
func mergeAcrossArchs(entries []apk.PackageVersion) []apk.PackageVersion {
	byVersion := make(map[string]apk.PackageVersion, len(entries))
	for _, e := range entries {
		existing, ok := byVersion[e.Version]
		if !ok || e.Timestamp.After(existing.Timestamp) {
			byVersion[e.Version] = e
		}
	}
	if len(byVersion) == len(entries) {
		return entries
	}
	out := make([]apk.PackageVersion, 0, len(byVersion))
	for _, e := range byVersion {
		out = append(out, e)
	}
	return out
}
