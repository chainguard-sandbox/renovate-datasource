package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chainguard"
)

// DefaultHistoryConcurrency is the default fan-out used when applyCooldown is
// given concurrency <= 0.
const DefaultHistoryConcurrency = 16

// Release is one entry in the Renovate custom-datasource response.
type Release struct {
	Version          string    `json:"version"`
	ReleaseTimestamp time.Time `json:"releaseTimestamp"`
	Digest           string    `json:"digest"`
}

// Response is the top-level shape returned by /v1/repo/{repo}/releases.
type Response struct {
	Releases []Release `json:"releases"`
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if !repoNamePattern.MatchString(repo) {
		writeAPIError(w, http.StatusBadRequest, "The repo name isn't a valid OCI repository path.")
		return
	}

	// The ?cooldown=<dur> query param overrides the server-wide default
	// (--cooldown) on a per-request basis so a single deployment can serve
	// multiple Renovate configurations that each want a different window.
	cooldown := s.cooldown
	if raw := r.URL.Query().Get("cooldown"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			writeAPIError(w, http.StatusBadRequest, "The 'cooldown' query parameter must be a non-negative Go duration (e.g. 168h).")
			return
		}
		cooldown = d
	}
	s.log.InfoContext(r.Context(), "request", "repo", repo, "cooldown", cooldown, "remote", r.RemoteAddr, "ua", r.UserAgent())

	ctx := r.Context()
	tags, err := s.backend.ListTags(ctx, repo)
	if err != nil {
		if errors.Is(err, chainguard.ErrRepoNotFound) {
			writeAPIError(w, http.StatusNotFound, "No repository with that name in this org.")
			return
		}
		s.log.ErrorContext(ctx, "ListTags failed", "repo", repo, "err", err)
		writeAPIError(w, http.StatusBadGateway, "The upstream registry returned an error. Please try again in a moment.")
		return
	}

	var releases []Release
	if cooldown <= 0 {
		// Cooldown disabled — emit each tag's current state straight through,
		// no history walk. Matches the behaviour Renovate would see if it
		// hit cgr.dev directly, but keeps the changelog/diff plumbing the
		// rest of the service provides.
		releases = tagsAsReleases(tags)
	} else {
		cutoff := s.now().Add(-cooldown)
		releases, err = applyCooldown(ctx, tags, cutoff, s.backend.ListTagHistory, s.historyConcurrency)
		if err != nil {
			s.log.ErrorContext(ctx, "applyCooldown failed", "repo", repo, "err", err)
			writeAPIError(w, http.StatusBadGateway, "The upstream registry returned an error. Please try again in a moment.")
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(Response{Releases: releases}); err != nil {
		s.log.ErrorContext(ctx, "encoding response", "repo", repo, "err", err)
	}
}

// tagsAsReleases emits each tag's current state as a Release, without any
// cooldown rewind. Used by the releases handler when the cooldown is set to
// 0 (disabled), so the datasource behaves as a thin pass-through of the
// upstream tag list.
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
// History lookups are dispatched concurrently with a bound of `concurrency`
// (DefaultHistoryConcurrency if <= 0). Output order matches input order.
func applyCooldown(ctx context.Context, tags []chainguard.Tag, cutoff time.Time, history historyFn, concurrency int) ([]Release, error) {
	if concurrency <= 0 {
		concurrency = DefaultHistoryConcurrency
	}

	// Pre-allocate output slots so we can preserve input order regardless of
	// the order in which the concurrent history calls complete. A nil slot
	// means "omit from response".
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
