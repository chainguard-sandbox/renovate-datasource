package server

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
)

// Conservative repo-name pattern: lowercase, digits, dashes, underscores,
// dots, and single internal slashes. Blocks `..`, leading dots, query strings.
var repoNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`)

// chartNamePattern is repoNamePattern restricted to a single segment.
// Chart URLs carry only the chart's short name; the subrepo prefix
// (charts/ or iamguarded-charts/) is composed server-side.
var chartNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?$`)

// apkProvidesNamePattern accepts apk package names, including apk
// capability prefixes emitted by APKINDEX (`cmd:node`, `so:libssl.so.3`,
// `pc:openssl`, `py3.14:setuptools`). Bounded length to keep URL /
// filesystem handling predictable.
var apkProvidesNamePattern = regexp.MustCompile(`^(?:[a-z][a-z0-9.]*:)?[a-z0-9][a-z0-9._+-]{0,127}$`)

// validateAPKProvidesName writes a 400 to w and returns false when
// name doesn't match apkProvidesNamePattern.
func validateAPKProvidesName(w http.ResponseWriter, name string) bool {
	if apkProvidesNamePattern.MatchString(name) {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, "The apk package name isn't well-formed.")
	return false
}

// Backend is the subset of *chainguard.Client the HTTP layer depends on for
// platform-API calls (listing tags + history, readiness).
type Backend interface {
	ListTags(ctx context.Context, repo string) ([]chainguard.Tag, error)
	ListTagHistory(ctx context.Context, tagID string) ([]chainguard.TagHistory, error)
	Ready(ctx context.Context) error
}

type Server struct {
	backend            Backend
	apkIndex           *apk.IndexStore
	cooldown           time.Duration
	historyConcurrency int
	now                func() time.Time
	log                *slog.Logger
}

// New builds a Server. backend handles platform-API calls used by every
// /releases endpoint.
func New(backend Backend, opts ...Option) *Server {
	o := options{
		cooldown:           defaultCooldown,
		historyConcurrency: defaultHistoryConcurrency,
		log:                slog.Default(),
		now:                time.Now,
	}
	for _, fn := range opts {
		fn(&o)
	}
	return &Server{
		backend:            backend,
		apkIndex:           o.apkIndex,
		cooldown:           o.cooldown,
		historyConcurrency: o.historyConcurrency,
		now:                o.now,
		log:                o.log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.backend.Ready(r.Context()); err != nil {
			// Log the detail server-side; respond with a generic message so
			// unauthenticated probes can't enumerate internal filesystem
			// paths or audiences via the readiness endpoint.
			s.log.WarnContext(r.Context(), "not ready", "err", err)
			writeAPIError(w, http.StatusServiceUnavailable, "The service isn't ready yet.")
			return
		}
		// When the apk index is wired up but the initial load failed,
		// /v1/apk/{name}/releases will 404 until the next refresh
		// succeeds — surface that as not-ready so orchestrators can
		// gate traffic.
		if s.apkIndex != nil && s.apkIndex.Len() == 0 {
			s.log.WarnContext(r.Context(), "not ready", "err", "apk index empty")
			writeAPIError(w, http.StatusServiceUnavailable, "The service isn't ready yet.")
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /v1/repo/{path...}", s.handleRepoV1)
	mux.HandleFunc("GET /v1/charts/{name}/releases", s.handleChartReleases)
	mux.HandleFunc("GET /v1/iamguarded-charts/{name}/releases", s.handleIamguardedChartReleases)
	mux.HandleFunc("GET /v1/apk/{name}/releases", s.handleAPKReleases)
	return mux
}

// handleRepoV1 dispatches the /v1/repo/{repo...}/releases endpoint.
// http.ServeMux only allows the {...} wildcard as the final segment,
// so we route on a single trailing wildcard and pick the endpoint by
// suffix (only /releases lives in this service).
func (s *Server) handleRepoV1(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if repo, ok := strings.CutSuffix(path, "/releases"); ok && repo != "" {
		r.SetPathValue("repo", repo)
		s.handleReleases(w, r)
		return
	}
	writeAPIError(w, http.StatusNotFound, "Not found.")
}
