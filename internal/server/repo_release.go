package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chainguard"
)

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
	if err := json.NewEncoder(w).Encode(ReleasesResponse{Releases: releases}); err != nil {
		s.log.ErrorContext(ctx, "encoding response", "repo", repo, "err", err)
	}
}
