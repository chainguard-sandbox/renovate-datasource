package server

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
)

// maxCooldown bounds the ?cooldown= override at one year. Beyond
// that we assume a malformed request rather than intent.
const maxCooldown = 365 * 24 * time.Hour

// parseCooldownQuery parses the ?cooldown=<dur> override. Returns
// ok=false with a client-facing error message when the value can't
// be parsed, is negative, or exceeds maxCooldown.
func parseCooldownQuery(raw string) (time.Duration, string, bool) {
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, "The 'cooldown' query parameter must be a non-negative Go duration (e.g. 168h).", false
	}
	if d > maxCooldown {
		return 0, "The 'cooldown' query parameter exceeds the maximum of 8760h (365 days).", false
	}
	return d, "", true
}

// Release is one entry in the Renovate custom-datasource response.
type Release struct {
	Version          string    `json:"version"`
	ReleaseTimestamp time.Time `json:"releaseTimestamp"`
	Digest           string    `json:"digest"`
}

// ReleasesResponse is the top-level shape returned by the releases
// endpoints (images, charts, iamguarded-charts).
type ReleasesResponse struct {
	Releases []Release `json:"releases"`
}

// tagsAsReleases emits each tag's current state as a Release, without
// any cooldown rewind. Used when cooldown is disabled, so the
// datasource behaves as a thin pass-through of the upstream tag list.
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

// historyFn returns the historical iterations of the tag identified by tagID.
type historyFn func(ctx context.Context, tagID string) ([]chainguard.TagHistory, error)

// applyCooldown produces the Renovate releases view for a set of tags.
//
// For each tag:
//   - if the tag's current digest is on or before the cutoff, emit it as-is;
//   - if it's after the cutoff, walk history and emit the most recent entry
//     whose UpdateTimestamp is on or before the cutoff;
//   - if no such history entry exists, omit the tag.
//
// History lookups run concurrently, bounded by `concurrency`
// (defaultHistoryConcurrency if <= 0). Output order matches input order.
func applyCooldown(ctx context.Context, tags []chainguard.Tag, cutoff time.Time, history historyFn, concurrency int) ([]Release, error) {
	if concurrency <= 0 {
		concurrency = defaultHistoryConcurrency
	}

	slots := make([]*Release, len(tags))
	var needHistory []int

	for i, t := range tags {
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
					if e.UpdateTimestamp.After(cutoff) {
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
