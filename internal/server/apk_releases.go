package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
)

// APKRelease is one entry in the /v1/apk/{name}/releases response.
// Shape mirrors Release from repo_release.go (minus the digest, which
// apk pins don't use) so Renovate consumers can reuse the same
// custom-datasource config for both endpoints.
type APKRelease struct {
	Version          string    `json:"version"`
	ReleaseTimestamp time.Time `json:"releaseTimestamp"`
}

// APKReleasesResponse is the top-level shape for /v1/apk/{name}/releases.
type APKReleasesResponse struct {
	Releases []APKRelease `json:"releases"`
}

// handleAPKReleases serves the list of known versions of an apk package.
// The index is loaded at startup (and, optionally, refreshed on a
// timer) into an in-memory Store; this handler just filters and sorts
// the pre-parsed slice.
func (s *Server) handleAPKReleases(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validateAPKProvidesName(w, name) {
		return
	}
	if s.apkIndex == nil {
		writeAPIError(w, http.StatusNotImplemented, "Apk release lookups aren't enabled on this deployment.")
		return
	}

	// The ?cooldown=<dur> query param mirrors /v1/repo/{repo}/releases:
	// it overrides the server-wide default so a single deployment can
	// serve multiple Renovate configurations with different windows.
	cooldown := s.cooldown
	if raw := r.URL.Query().Get("cooldown"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < 0 {
			writeAPIError(w, http.StatusBadRequest, "The 'cooldown' query parameter must be a non-negative Go duration (e.g. 168h).")
			return
		}
		cooldown = d
	}
	s.log.InfoContext(r.Context(), "apk releases request", "name", name, "cooldown", cooldown, "remote", r.RemoteAddr, "ua", r.UserAgent())

	entries := s.apkIndex.Get(name)
	if entries == nil {
		writeAPIError(w, http.StatusNotFound, "No apk package with that name in the loaded indexes.")
		return
	}

	cutoff := s.now().Add(-cooldown)
	out := make([]APKRelease, 0, len(entries))
	for _, e := range entries {
		// Cooldown filter: skip releases whose timestamp is fresher
		// than now-cooldown. Entries without a timestamp are treated
		// as "old enough" since we have no signal to hold them back.
		if cooldown > 0 && !e.Timestamp.IsZero() && e.Timestamp.After(cutoff) {
			continue
		}
		out = append(out, apkReleaseFromEntry(e))
	}

	// Sort by timestamp descending (newest first), falling back to
	// version string for stable ordering when timestamps tie or are
	// zero. Renovate does its own version parsing, so this ordering
	// is a UX/log-readability choice rather than a functional one.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].ReleaseTimestamp.Equal(out[j].ReleaseTimestamp) {
			return out[i].ReleaseTimestamp.After(out[j].ReleaseTimestamp)
		}
		return out[i].Version > out[j].Version
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(APKReleasesResponse{Releases: out}); err != nil {
		s.log.ErrorContext(r.Context(), "encoding apk releases response", "name", name, "err", err)
	}
}

func apkReleaseFromEntry(e apk.Release) APKRelease {
	return APKRelease{
		Version:          e.Version,
		ReleaseTimestamp: e.Timestamp,
	}
}
