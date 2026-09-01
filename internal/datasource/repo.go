package datasource

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
)

// repoNamePattern is a conservative repo-name matcher: lowercase, digits,
// dashes, underscores, dots, and single internal slashes. Multi-segment
// paths cover subrepos — e.g. `charts/nginx` and `iamguarded-charts/postgresql`
// resolve to Helm charts under those subgroups. Blocks `..`, leading dots,
// query strings.
var repoNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`)

// RepoBackend is the subset of *chainguard.Client the repo source
// needs. Keeping it narrow so tests can wire a fake without
// implementing the whole gRPC surface. Ready surfaces auth-layer
// readiness (e.g. token freshness) without a live API call so
// /readyz can poll it on every request.
type RepoBackend interface {
	ListAllRepos(ctx context.Context) ([]string, error)
	ListTags(ctx context.Context, repo string) ([]chainguard.Tag, error)
	ListTagHistory(ctx context.Context, tagID string) ([]chainguard.TagHistory, error)
	Ready(ctx context.Context) error
}

// RepoDatasource resolves repo paths to release lists via the platform
// registry API. A non-zero before triggers a per-tag history walk
// that rewinds the tag to the newest historical digest at or before
// that instant.
type RepoDatasource struct {
	Backend            RepoBackend
	HistoryConcurrency int
}

// NewRepoDatasource builds a RepoDatasource with the given history-fanout limit.
func NewRepoDatasource(backend RepoBackend, historyConcurrency int) *RepoDatasource {
	return &RepoDatasource{Backend: backend, HistoryConcurrency: historyConcurrency}
}

// PackageNames returns every repo path in the org, walking subgroups.
func (s *RepoDatasource) PackageNames(ctx context.Context) ([]string, error) {
	return s.Backend.ListAllRepos(ctx)
}

// Ready delegates to the backend's cheap readiness check — typically
// a token-freshness probe on *chainguard.Client. A live API call
// would be too expensive to run per /readyz hit.
func (s *RepoDatasource) Ready(ctx context.Context) error { return s.Backend.Ready(ctx) }

// Releases returns the release list for the repo at packageName.
//
// Options:
//
//   - opts.Before: when non-zero, tags newer than that instant are
//     rewound to the newest historical digest at or before it (or
//     dropped if none exists).
//   - opts.Arch: ignored — images and helm charts aren't arch-scoped at the
//     tag level.
//
// Malformed packageNames produce *InvalidPackageNameError; unknown repos
// surface as ErrNotFound so callers can distinguish 404 from real
// backend failures.
func (s *RepoDatasource) Releases(ctx context.Context, packageName string, opts ReleasesOptions) ([]Release, error) {
	if !repoNamePattern.MatchString(packageName) {
		return nil, &InvalidPackageNameError{Message: "The repo name isn't a valid OCI repository path."}
	}
	tags, err := s.Backend.ListTags(ctx, packageName)
	if err != nil {
		if errors.Is(err, chainguard.ErrRepoNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if opts.Before.IsZero() {
		return tagsAsReleases(tags), nil
	}
	return applyMinimumReleaseAge(ctx, tags, opts.Before, s.Backend.ListTagHistory, s.HistoryConcurrency)
}

// historyFn returns the historical iterations of the tag identified by tagID.
type historyFn func(ctx context.Context, tagID string) ([]chainguard.TagHistory, error)

// tagsAsReleases emits each tag's current state as a Release, without
// any history rewind. Used when minimumReleaseAge is disabled.
func tagsAsReleases(tags []chainguard.Tag) []Release {
	out := make([]Release, 0, len(tags))
	for _, t := range tags {
		out = append(out, Release{
			Version:          t.Name,
			ReleaseTimestamp: t.LastUpdated,
			Digest:           t.Digest,
		})
	}
	return out
}

// applyMinimumReleaseAge produces the Renovate releases view for a set
// of tags subject to a minimumReleaseAge cutoff.
//
// For each tag:
//   - if the tag has no LastUpdated, omit it (we can't prove it sits
//     outside the window);
//   - if the tag's current digest is on or before the cutoff, emit it as-is;
//   - if it's after the cutoff, walk history and emit the most recent entry
//     whose UpdateTimestamp is on or before the cutoff;
//   - if no such history entry exists, omit the tag.
//
// History lookups run concurrently, bounded by `concurrency` (16 if <= 0).
// Output order matches input order.
func applyMinimumReleaseAge(ctx context.Context, tags []chainguard.Tag, cutoff time.Time, history historyFn, concurrency int) ([]Release, error) {
	if concurrency <= 0 {
		concurrency = 16
	}

	slots := make([]*Release, len(tags))
	var needHistory []int

	for i, t := range tags {
		if t.LastUpdated.IsZero() {
			continue
		}
		if !t.LastUpdated.After(cutoff) {
			slots[i] = &Release{
				Version:          t.Name,
				ReleaseTimestamp: t.LastUpdated,
				Digest:           t.Digest,
			}
			continue
		}
		needHistory = append(needHistory, i)
	}

	if len(needHistory) > 0 {
		eg, egCtx := errgroup.WithContext(ctx)
		eg.SetLimit(concurrency)

		for _, idx := range needHistory {
			t := tags[idx]
			eg.Go(func() error {
				hist, err := history(egCtx, t.ID)
				if err != nil {
					return fmt.Errorf("history(%s): %w", t.Name, err)
				}
				var best *chainguard.TagHistory
				for j := range hist {
					e := &hist[j]
					// Drop zero-timestamp entries: we can't prove they
					// sit outside the freshness window.
					if e.UpdateTimestamp.IsZero() || e.UpdateTimestamp.After(cutoff) {
						continue
					}
					if best == nil || e.UpdateTimestamp.After(best.UpdateTimestamp) {
						best = e
					}
				}
				if best == nil {
					return nil
				}
				slots[idx] = &Release{
					Version:          t.Name,
					ReleaseTimestamp: best.UpdateTimestamp,
					Digest:           best.Digest,
				}
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return nil, err
		}
	}

	out := make([]Release, 0, len(tags))
	for _, r := range slots {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}
