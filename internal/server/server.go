// Package server exposes the /releases endpoints Renovate consumes
// as a custom datasource. Datasources plug in via Options; the mux
// dispatches everything under /v1/repo and /v1/apk to a single
// shared handler body.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

// maxCooldown bounds the ?cooldown= override at one year. Beyond
// that we assume a malformed request rather than intent.
const maxCooldown = 365 * 24 * time.Hour

// Readier reports auth-layer readiness. Typically satisfied by
// *chainguard.Client so /readyz can surface a stale chainctl token
// or an unreadable identity-token file.
type Readier interface {
	Ready(ctx context.Context) error
}

type Server struct {
	readier    Readier
	repoDatasource datasource.Datasource
	apkDatasource  datasource.Datasource
	cooldown   time.Duration
	now        func() time.Time
	log        *slog.Logger
}

// New builds a Server. readier drives the /readyz auth check.
// Datasources are attached via WithRepoDatasource / WithAPKDatasource — a datasource
// is required for its endpoint to be registered.
func New(readier Readier, opts ...Option) *Server {
	o := options{
		cooldown: defaultCooldown,
		log:      slog.Default(),
		now:      time.Now,
	}
	for _, fn := range opts {
		fn(&o)
	}
	return &Server{
		readier:    readier,
		repoDatasource: o.repoDatasource,
		apkDatasource:  o.apkDatasource,
		cooldown:   o.cooldown,
		now:        o.now,
		log:        o.log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.readier != nil {
			if err := s.readier.Ready(r.Context()); err != nil {
				// Log the detail server-side; respond with a generic message so
				// unauthenticated probes can't enumerate internal filesystem
				// paths or audiences via the readiness endpoint.
				s.log.WarnContext(r.Context(), "not ready", "err", err)
				writeAPIError(w, http.StatusServiceUnavailable, "The service isn't ready yet.")
				return
			}
		}
		for name, ds := range map[string]datasource.Datasource{"repo": s.repoDatasource, "apk": s.apkDatasource} {
			if ds == nil {
				continue
			}
			if err := ds.Ready(r.Context()); err != nil {
				s.log.WarnContext(r.Context(), "not ready", "datasource", name, "err", err)
				writeAPIError(w, http.StatusServiceUnavailable, "The service isn't ready yet.")
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// Both /releases endpoints share the same body — parse cooldown,
	// look up, encode. The mux wiring differs only because repo
	// paths can be multi-segment (charts/nginx,
	// iamguarded-charts/postgresql) and http.ServeMux only allows
	// the {...} wildcard as the trailing segment.
	if s.repoDatasource != nil {
		mux.HandleFunc("GET /v1/repo/{path...}", func(w http.ResponseWriter, r *http.Request) {
			path := r.PathValue("path")
			repo, ok := strings.CutSuffix(path, "/releases")
			if !ok || repo == "" {
				writeAPIError(w, http.StatusNotFound, "Not found.")
				return
			}
			s.serveReleases(w, r, "repo", s.repoDatasource, repo)
		})
	}
	if s.apkDatasource != nil {
		mux.HandleFunc("GET /v1/apk/{name}/releases", func(w http.ResponseWriter, r *http.Request) {
			s.serveReleases(w, r, "apk", s.apkDatasource, r.PathValue("name"))
		})
	}
	return mux
}

// serveReleases is the shared /releases handler body. Same pipeline
// for every datasource: parse cooldown, ask the source for releases
// (which handles name validation itself), encode the response.
func (s *Server) serveReleases(w http.ResponseWriter, r *http.Request, kind string, ds datasource.Datasource, packageName string) {
	// ?cooldown=<dur> overrides the server-wide --cooldown default so a
	// single deployment can serve multiple Renovate configurations that
	// each want a different window.
	cooldown := s.cooldown
	if raw := r.URL.Query().Get("cooldown"); raw != "" {
		d, msg, ok := parseCooldownQuery(raw)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, msg)
			return
		}
		cooldown = d
	}
	s.log.InfoContext(r.Context(), "releases request", "kind", kind, "packageName", packageName, "cooldown", cooldown, "remote", r.RemoteAddr, "ua", r.UserAgent())

	before := cutoffFor(s.now(), cooldown)
	releases, err := ds.Releases(r.Context(), packageName, before)
	if err != nil {
		var invalid *datasource.InvalidPackageNameError
		switch {
		case errors.As(err, &invalid):
			writeAPIError(w, http.StatusBadRequest, invalid.Message)
			return
		case errors.Is(err, datasource.ErrNotFound):
			writeAPIError(w, http.StatusNotFound, "No package with that name.")
			return
		}
		s.log.ErrorContext(r.Context(), "releases lookup failed", "kind", kind, "packageName", packageName, "err", err)
		writeAPIError(w, http.StatusBadGateway, "The upstream lookup failed. Please try again in a moment.")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(datasource.Response{Releases: releases}); err != nil {
		s.log.ErrorContext(r.Context(), "encoding response", "kind", kind, "packageName", packageName, "err", err)
	}
}

// apiError is the JSON shape returned for every non-OK response
// from the /v1/* endpoints. /healthz remains plain text "ok".
type apiError struct {
	Error string `json:"error"`
}

// writeAPIError serialises a structured JSON error onto w. Used in
// place of http.Error everywhere except /healthz so clients can
// surface the message verbatim.
func writeAPIError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: msg})
}

// cutoffFor turns a cooldown duration into the "before" cutoff the
// datasource.Datasource API expects. Zero cooldown returns the zero
// time, which the Datasource treats as "no filter".
func cutoffFor(now time.Time, cooldown time.Duration) time.Time {
	if cooldown <= 0 {
		return time.Time{}
	}
	return now.Add(-cooldown)
}

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
