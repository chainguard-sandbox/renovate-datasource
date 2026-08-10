// Package server exposes the /releases endpoints Renovate consumes
// as a custom datasource. Datasources plug in via Options; the mux
// dispatches everything under /v1/repo and /v1/apk to a single
// shared handler body.
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

// maxMinimumReleaseAge bounds the ?minimumReleaseAge= override at one
// year. Beyond that we assume a malformed request rather than intent.
const maxMinimumReleaseAge = 365 * 24 * time.Hour

type Server struct {
	repoDatasource    datasource.Datasource
	apkDatasource     datasource.Datasource
	minimumReleaseAge time.Duration
	now               func() time.Time
	log               *slog.Logger
}

// New builds a Server. Datasources are attached via WithRepoDatasource /
// WithAPKDatasource — a datasource is required for its endpoint to be
// registered.
func New(opts ...Option) *Server {
	o := options{
		minimumReleaseAge: defaultMinimumReleaseAge,
		log:               slog.Default(),
		now:               time.Now,
	}
	for _, fn := range opts {
		fn(&o)
	}
	return &Server{
		repoDatasource:    o.repoDatasource,
		apkDatasource:     o.apkDatasource,
		minimumReleaseAge: o.minimumReleaseAge,
		now:               o.now,
		log:               o.log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
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
	// Both /releases endpoints share the same body — parse the
	// minimumReleaseAge window, look up, encode. The mux wiring
	// differs only because repo paths can be multi-segment
	// (charts/nginx, iamguarded-charts/postgresql) and http.ServeMux
	// only allows the {...} wildcard as the trailing segment.
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
// for every datasource: parse the minimumReleaseAge window, ask the
// source for releases (which handles name validation itself), encode
// the response.
func (s *Server) serveReleases(w http.ResponseWriter, r *http.Request, kind string, ds datasource.Datasource, packageName string) {
	// ?minimumReleaseAge=<dur> overrides the server-wide
	// --min-release-age default so a single deployment can serve
	// multiple Renovate configurations that each want a different
	// window.
	minimumReleaseAge := s.minimumReleaseAge
	if raw := r.URL.Query().Get("minimumReleaseAge"); raw != "" {
		d, msg, ok := parseMinimumReleaseAgeQuery(raw)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, msg)
			return
		}
		minimumReleaseAge = d
	}
	arch := r.URL.Query().Get("arch")
	s.log.InfoContext(r.Context(), "releases request", "kind", kind, "packageName", packageName, "minimumReleaseAge", minimumReleaseAge, "arch", arch, "remote", r.RemoteAddr, "ua", r.UserAgent())

	opts := datasource.ReleasesOptions{
		Before: cutoffFor(s.now(), minimumReleaseAge),
		Arch:   arch,
	}
	releases, err := ds.Releases(r.Context(), packageName, opts)
	if err != nil {
		var invalidName *datasource.InvalidPackageNameError
		var invalidArg *datasource.InvalidArgumentError
		switch {
		case errors.As(err, &invalidName):
			writeAPIError(w, http.StatusBadRequest, invalidName.Message)
			return
		case errors.As(err, &invalidArg):
			writeAPIError(w, http.StatusBadRequest, invalidArg.Message)
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

// cutoffFor turns a minimumReleaseAge duration into the "before" cutoff
// the datasource.Datasource API expects. Zero returns the zero time,
// which the Datasource treats as "no filter".
func cutoffFor(now time.Time, minimumReleaseAge time.Duration) time.Time {
	if minimumReleaseAge <= 0 {
		return time.Time{}
	}
	return now.Add(-minimumReleaseAge)
}

// parseMinimumReleaseAgeQuery parses the ?minimumReleaseAge=<dur>
// override. Returns ok=false with a client-facing error message when
// the value can't be parsed, is negative, or exceeds
// maxMinimumReleaseAge.
func parseMinimumReleaseAgeQuery(raw string) (time.Duration, string, bool) {
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, "The 'minimumReleaseAge' query parameter must be a non-negative Go duration (e.g. 168h).", false
	}
	if d > maxMinimumReleaseAge {
		return 0, "The 'minimumReleaseAge' query parameter exceeds the maximum of 8760h (365 days).", false
	}
	return d, "", true
}
