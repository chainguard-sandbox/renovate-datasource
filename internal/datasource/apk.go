package datasource

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"time"

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
// packageName, dropping entries newer than before when it's
// non-zero. Malformed packageNames produce *InvalidPackageNameError;
// unknown but valid names produce ErrNotFound.
func (s *APKDatasource) Releases(_ context.Context, packageName string, before time.Time) ([]Release, error) {
	if !apkProvidesNamePattern.MatchString(packageName) {
		return nil, &InvalidPackageNameError{Message: "The apk package name isn't well-formed."}
	}
	entries := s.Store.Get(packageName)
	if entries == nil {
		return nil, ErrNotFound
	}
	out := make([]Release, 0, len(entries))
	for _, e := range entries {
		// Cutoff filter: skip releases whose timestamp is after the
		// cutoff. Zero-timestamp entries always pass — we have no
		// signal to hold them back.
		if !before.IsZero() && !e.Timestamp.IsZero() && e.Timestamp.After(before) {
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
