package server

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
)

//go:embed templates/apk.html
var apkPageHTML string

//go:embed templates/apk.js
var apkPageJS string

// apkPageTemplate is the shell for the dedicated /apk/.../diff/... view.
// The CSS body is shared with the image diff page (see web.go) so the two
// pages render in the same visual style.
var apkPageTemplate = template.Must(template.New("apk").Parse(apkPageHTML))

// apkPageData feeds the apk diff template. Versions are emitted in a JS
// context so html/template quotes them for us. FromURL/ToURL link each
// version reference to its single-version page so visitors can drill in.
type apkPageData struct {
	Name     string
	From     string
	To       string
	FromURL  string
	ToURL    string
	CSS      template.CSS
	CommonJS template.JS
	JS       template.JS
}

// handleAPKDiff serves the JSON diff between two versions of one apk
// (.melange.yaml + .PKGINFO unified diffs, plus structured source-
// pipeline changes). Decoupled from the image-diff endpoint so callers
// (and the HTML page) can fetch one package without paying for the SBOM
// retrieval and full image diff.
func (s *Server) handleAPKDiff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	from := r.PathValue("from")
	to := r.PathValue("to")
	s.log.InfoContext(r.Context(), "apk diff request", "name", name, "from", from, "to", to, "remote", r.RemoteAddr, "ua", r.UserAgent())

	if !validateAPKName(w, name) {
		return
	}
	if !validateAPKVersion(w, "The 'from' version isn't well-formed.", from) {
		return
	}
	if !validateAPKVersion(w, "The 'to' version isn't well-formed.", to) {
		return
	}
	if s.apk == nil {
		writeAPIError(w, http.StatusNotImplemented, "Apk diffs aren't enabled on this deployment.")
		return
	}

	ctx := r.Context()
	resp, err := diff.ComputeAPKDiff(ctx, s.apk, name, from, to)
	if err != nil {
		status, msg := classifyAPKDiffError(err)
		if status >= 500 {
			s.log.ErrorContext(ctx, "ComputeAPKDiff failed", "name", name, "from", from, "to", to, "err", err)
		} else {
			s.log.InfoContext(ctx, "ComputeAPKDiff client error", "name", name, "from", from, "to", to, "status", status, "err", err)
		}
		writeAPIError(w, status, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.ErrorContext(ctx, "encoding apk diff response", "name", name, "err", err)
	}
}

// handleAPKDiffPage serves the HTML shell that fetches /v1/apk/{name}/diff
// client-side and renders the unified diff once the API call returns.
func (s *Server) handleAPKDiffPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	from := r.PathValue("from")
	to := r.PathValue("to")

	if !validateAPKName(w, name) {
		return
	}
	if !validateAPKVersion(w, "The 'from' version isn't well-formed.", from) {
		return
	}
	if !validateAPKVersion(w, "The 'to' version isn't well-formed.", to) {
		return
	}

	data := apkPageData{
		Name:     name,
		From:     from,
		To:       to,
		FromURL:  apkVersionURL(name, from),
		ToURL:    apkVersionURL(name, to),
		CSS:      template.CSS(diffPageCSS),
		CommonJS: template.JS(commonJS),
		JS:       template.JS(apkPageJS),
	}

	writeHTMLHeaders(w)
	if err := apkPageTemplate.Execute(w, data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering apk diff page", "err", err)
	}
}

// classifyAPKDiffError maps an apk-fetch error into the HTTP status and
// client-facing message we want to surface. Missing artifacts (ErrNotFound)
// become 404; the diff payload itself can carry empty fields so a 200
// response with partial sections is still meaningful. Shared with the apk
// version handler since both surface the same fetch errors.
func classifyAPKDiffError(err error) (int, string) {
	if errors.Is(err, apk.ErrNotFound) {
		return http.StatusNotFound, "No apk artifact is available for one or both versions."
	}
	return http.StatusBadGateway, "The apk registry returned an error. Please try again in a moment."
}
