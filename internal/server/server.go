package server

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chainguard"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
)

// Conservative repo-name pattern: lowercase, digits, dashes, underscores,
// dots, and single internal slashes. Blocks `..`, leading dots, query strings.
var repoNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]*[a-z0-9])?(/[a-z0-9]([a-z0-9._-]*[a-z0-9])?)*$`)

// tagPattern matches the OCI distribution tag spec: 1–128 chars from
// [A-Za-z0-9_.-], starting with [A-Za-z0-9_] (no leading dot or dash).
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// digestPattern matches the only OCI digest form we accept: sha256 + 64 hex.
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// apkNamePattern matches apk package names: lowercase alnum + dot/underscore/
// plus/hyphen, no leading punctuation. Bounded length to keep URL handling
// predictable.
var apkNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,127}$`)

// apkProvidesNamePattern additionally accepts an apk capability prefix
// (e.g. `cmd:node`, `so:libssl.so.3`, `pc:openssl`, `py3.14:setuptools`).
// Used by /v1/apk/{name}/releases so callers can look up versions via
// any name apk-tools would resolve against, including the prefixed
// capabilities emitted by parseIndex's addProvides. The diff/version
// endpoints intentionally stay on apkNamePattern since prefixed
// capabilities aren't installable — there's no .apk file to fetch.
var apkProvidesNamePattern = regexp.MustCompile(`^(?:[a-z][a-z0-9.]*:)?[a-z0-9][a-z0-9._+-]{0,127}$`)

// apkVersionPattern matches apk versions in the format `<upstream>-r<rev>`
// (e.g. `3.6.3-r2`, `20241121-r1`). Permissive but bounded; we don't
// try to enforce the full apk version grammar at the boundary.
var apkVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// validRef reports whether ref is a well-formed OCI tag or a sha256 digest.
// Validating at the server boundary lets us 400 fast on bad input rather
// than passing it to the registry and reporting a generic 502.
func validRef(ref string) bool {
	if strings.HasPrefix(ref, "sha256:") {
		return digestPattern.MatchString(ref)
	}
	return tagPattern.MatchString(ref)
}

// validateAPKName writes a 400 to w and returns false when name doesn't
// match apkNamePattern. Centralised so the four apk handlers stay in
// sync on both the pattern and the client-facing error message.
func validateAPKName(w http.ResponseWriter, name string) bool {
	if apkNamePattern.MatchString(name) {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, "The apk package name isn't well-formed.")
	return false
}

// validateAPKProvidesName is the releases-endpoint counterpart to
// validateAPKName: same rules plus an optional capability prefix like
// `cmd:` or `so:`. See apkProvidesNamePattern for the full rationale.
func validateAPKProvidesName(w http.ResponseWriter, name string) bool {
	if apkProvidesNamePattern.MatchString(name) {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, "The apk package name isn't well-formed.")
	return false
}

// validateAPKVersion writes a 400 to w and returns false when value
// doesn't match apkVersionPattern. msg is inlined into the response so
// callers can tag which version failed ("'from' version", "'to'
// version", or just "version") without each handler repeating the
// regex check.
func validateAPKVersion(w http.ResponseWriter, msg, value string) bool {
	if apkVersionPattern.MatchString(value) {
		return true
	}
	writeAPIError(w, http.StatusBadRequest, msg)
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
	fetcher            diff.Fetcher
	apk                diff.APKFetcher
	apkIndex           *apk.IndexStore
	cooldown           time.Duration
	historyConcurrency int
	orgName            string
	now                func() time.Time
	log                *slog.Logger
}

// New builds a Server. backend handles platform-API calls (releases endpoint);
// fetcher handles direct cgr.dev access (diff endpoint). Both are required;
// everything else has a default.
func New(backend Backend, fetcher diff.Fetcher, opts ...Option) *Server {
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
		fetcher:            fetcher,
		apk:                o.apk,
		apkIndex:           o.apkIndex,
		cooldown:           o.cooldown,
		historyConcurrency: o.historyConcurrency,
		orgName:            o.orgName,
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
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /v1/repo/{path...}", s.handleRepoV1)
	mux.HandleFunc("GET /v1/apk/{name}/releases", s.handleAPKReleases)
	mux.HandleFunc("GET /v1/apk/{fromName}/version/{fromVersion}/diff/{toName}/version/{toVersion}", s.handleAPKDiff)
	mux.HandleFunc("GET /v1/apk/{name}/version/{version}", s.handleAPKVersion)
	mux.HandleFunc("GET /repo/{repo}/diff/{oldRef}/{newRef}", s.handleDiffPage)
	mux.HandleFunc("GET /apk/{fromName}/version/{fromVersion}/diff/{toName}/version/{toVersion}", s.handleAPKDiffPage)
	mux.HandleFunc("GET /apk/{name}/version/{version}", s.handleAPKVersionPage)
	return mux
}

// handleRepoV1 dispatches the /v1/repo/{repo...}/{op...} endpoints.
// http.ServeMux only allows the {...} wildcard as the final segment, so we
// route on a single trailing wildcard and pick the sub-endpoint by suffix:
//
//   - <repo>/releases        → handleReleases
//   - <repo>/diff/{from}/{to} → handleDiff
//
// SetPathValue populates the same keys the underlying handlers already read,
// so they stay agnostic to the routing shape.
func (s *Server) handleRepoV1(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if repo, ok := strings.CutSuffix(path, "/releases"); ok && repo != "" {
		r.SetPathValue("repo", repo)
		s.handleReleases(w, r)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) >= 4 && parts[len(parts)-3] == "diff" {
		r.SetPathValue("repo", strings.Join(parts[:len(parts)-3], "/"))
		r.SetPathValue("from", parts[len(parts)-2])
		r.SetPathValue("to", parts[len(parts)-1])
		s.handleDiff(w, r)
		return
	}
	writeAPIError(w, http.StatusNotFound, "Not found.")
}
