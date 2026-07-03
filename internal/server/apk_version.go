package server

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
)

//go:embed templates/apk_version.html
var apkVersionPageHTML string

//go:embed templates/apk_version.js
var apkVersionPageJS string

// apkVersionPageTemplate is the shell for /apk/.../version/... — the
// single-version snapshot view used as a click target from version
// references on the diff pages.
var apkVersionPageTemplate = template.Must(template.New("apk_version").Parse(apkVersionPageHTML))

// apkVersionPageData feeds the apk version template.
type apkVersionPageData struct {
	Name     string
	Version  string
	CSS      template.CSS
	CommonJS template.JS
	JS       template.JS
}

// handleAPKVersion serves the JSON snapshot of one apk version. Same
// data shape as a single side of the diff response; useful as a
// click-target from any place an apk version is displayed.
func (s *Server) handleAPKVersion(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("version")
	s.log.InfoContext(r.Context(), "apk version request", "name", name, "version", version, "remote", r.RemoteAddr, "ua", r.UserAgent())

	if !apkNamePattern.MatchString(name) {
		writeAPIError(w, http.StatusBadRequest, "The apk package name isn't well-formed.")
		return
	}
	if !apkVersionPattern.MatchString(version) {
		writeAPIError(w, http.StatusBadRequest, "The version isn't well-formed.")
		return
	}
	if s.apk == nil {
		writeAPIError(w, http.StatusNotImplemented, "Apk lookups aren't enabled on this deployment.")
		return
	}

	ctx := r.Context()
	resp, err := diff.ComputeAPKVersion(ctx, s.apk, name, version)
	if err != nil {
		status, msg := classifyAPKDiffError(err)
		if status >= 500 {
			s.log.ErrorContext(ctx, "ComputeAPKVersion failed", "name", name, "version", version, "err", err)
		} else {
			s.log.InfoContext(ctx, "ComputeAPKVersion client error", "name", name, "version", version, "status", status, "err", err)
		}
		writeAPIError(w, status, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.ErrorContext(ctx, "encoding apk version response", "name", name, "err", err)
	}
}

// handleAPKVersionPage serves the HTML shell that fetches
// /v1/apk/{name}/version/... client-side and renders the snapshot.
func (s *Server) handleAPKVersionPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	version := r.PathValue("version")

	if !apkNamePattern.MatchString(name) {
		writeAPIError(w, http.StatusBadRequest, "The apk package name isn't well-formed.")
		return
	}
	if !apkVersionPattern.MatchString(version) {
		writeAPIError(w, http.StatusBadRequest, "The version isn't well-formed.")
		return
	}

	data := apkVersionPageData{
		Name:     name,
		Version:  version,
		CSS:      template.CSS(diffPageCSS),
		CommonJS: template.JS(commonJS),
		JS:       template.JS(apkVersionPageJS),
	}

	writeHTMLHeaders(w)
	if err := apkVersionPageTemplate.Execute(w, data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering apk version page", "err", err)
	}
}

// apkVersionURL builds the canonical link to a single apk version's
// snapshot page. name and version have already been validated against
// the apk name/version patterns, so PathEscape just guards against
// future relaxation of those patterns.
func apkVersionURL(name, version string) string {
	return "/apk/" + url.PathEscape(name) + "/version/" + url.PathEscape(version)
}
