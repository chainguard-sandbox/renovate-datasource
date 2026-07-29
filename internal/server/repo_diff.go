package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"

	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/oci"
)

//go:embed templates/diff.html
var diffPageHTML string

//go:embed templates/diff.js
var diffPageJS string

// diffPageTemplate is parsed once at startup. The template uses html/template's
// JS-context escaping, so the per-request repo/ref values we substitute into
// the inline <script> block come out as properly-quoted JavaScript string
// literals. The CSS and JS bodies are embedded at build time and rendered as
// template.CSS / template.JS so html/template treats them as already-safe
// content rather than re-escaping them.
var diffPageTemplate = template.Must(template.New("diff").Parse(diffPageHTML))

type diffPageData struct {
	Repo, OldRef, NewRef string
	// Title is the fully-qualified image path displayed in the page header
	// and the browser tab — "cgr.dev/<org>/<repo>" when the org is known,
	// just "<repo>" otherwise.
	Title string
	// ConsoleURL is the Chainguard console "versions" page for this repo,
	// rendered as a link wrapping Title in the page header. Empty if the
	// server wasn't configured with an org name.
	ConsoleURL string
	CSS        template.CSS
	// CommonJS is the shared utility script that every page emits before
	// its own page-specific JS body.
	CommonJS template.JS
	JS       template.JS
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	from := r.PathValue("from")
	to := r.PathValue("to")
	s.log.InfoContext(r.Context(), "diff request", "repo", repo, "from", from, "to", to, "remote", r.RemoteAddr, "ua", r.UserAgent())

	if !repoNamePattern.MatchString(repo) {
		writeAPIError(w, http.StatusBadRequest, "The repo name isn't a valid OCI repository path.")
		return
	}
	if !validRef(from) {
		writeAPIError(w, http.StatusBadRequest, "The 'from' ref isn't a valid OCI tag or sha256 digest.")
		return
	}
	if !validRef(to) {
		writeAPIError(w, http.StatusBadRequest, "The 'to' ref isn't a valid OCI tag or sha256 digest.")
		return
	}

	ctx := r.Context()
	resp, err := diff.Compute(ctx, s.fetcher, s.grype, repo, from, to)
	if err != nil {
		status, msg := classifyDiffError(err)
		// 5xx errors deserve an ERROR log line — the server is the source
		// of the problem (or its upstream). 4xx are client problems; we
		// log them at Info so they don't pollute the error stream.
		if status >= 500 {
			s.log.ErrorContext(ctx, "diff.Compute failed", "repo", repo, "from", from, "to", to, "err", err)
		} else {
			s.log.InfoContext(ctx, "diff.Compute client error", "repo", repo, "from", from, "to", to, "status", status, "err", err)
		}
		writeAPIError(w, status, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.ErrorContext(ctx, "encoding diff response", "repo", repo, "err", err)
	}
}

// classifyDiffError maps an error from diff.Compute into the HTTP status and
// client-facing message we want to return. The two interesting cases are:
//
//   - the registry told us the image/tag doesn't exist (transport.Error 404)
//     → 404 with a clear message, not the generic upstream-error 502;
//   - the image exists but has no SBOM attestation (oci.ErrNoSBOM) → 422,
//     since the request is well-formed but undiffable.
//
// Everything else falls through to 502.
func classifyDiffError(err error) (int, string) {
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusNotFound:
			return http.StatusNotFound, "This tag or digest doesn't exist in the registry."
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusBadGateway, "Failed to authenticate with the upstream registry."
		}
	}
	if errors.Is(err, oci.ErrNoSBOM) {
		return http.StatusUnprocessableEntity, "This image has no SBOM attestation, so a diff can't be computed."
	}
	return http.StatusBadGateway, "The upstream registry returned an error. Please try again in a moment."
}

// handleDiffPage serves the HTML shell that fetches
// /v1/repo/{repo}/diff/{from}/{to} client-side and renders it with a
// spinner while the API call is in flight.
func (s *Server) handleDiffPage(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	oldRef := r.PathValue("oldRef")
	newRef := r.PathValue("newRef")

	if !repoNamePattern.MatchString(repo) {
		writeAPIError(w, http.StatusBadRequest, "The repo name isn't a valid OCI repository path.")
		return
	}
	if !validRef(oldRef) {
		writeAPIError(w, http.StatusBadRequest, "The 'from' ref isn't a valid OCI tag or sha256 digest.")
		return
	}
	if !validRef(newRef) {
		writeAPIError(w, http.StatusBadRequest, "The 'to' ref isn't a valid OCI tag or sha256 digest.")
		return
	}

	data := diffPageData{
		Repo:       repo,
		OldRef:     oldRef,
		NewRef:     newRef,
		Title:      pageTitle(s.orgName, repo),
		ConsoleURL: consoleURL(s.orgName, repo),
		CSS:        template.CSS(diffPageCSS),
		CommonJS:   template.JS(commonJS),
		JS:         template.JS(diffPageJS),
	}

	writeHTMLHeaders(w)
	if err := diffPageTemplate.Execute(w, data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering diff page", "err", err)
	}
}

// pageTitle returns the displayed image path. When an org is configured we
// surface the full pullable reference; otherwise we fall back to the bare
// repo name (the diff page still works, the link to the console is just
// omitted upstream).
func pageTitle(orgName, repo string) string {
	if orgName == "" {
		return repo
	}
	return "cgr.dev/" + orgName + "/" + repo
}

// consoleURL builds the Chainguard console URL for a repo within an org.
// Returns "" when orgName is empty so the template can omit the link.
// repo is already validated against repoNamePattern (lowercase + slashes
// only) and orgName is the value the operator configured at startup, so
// neither needs additional escaping for the path segments.
func consoleURL(orgName, repo string) string {
	if orgName == "" {
		return ""
	}
	return "https://console.chainguard.dev/org/" + orgName + "/images/organization/image/" + repo + "/versions"
}
