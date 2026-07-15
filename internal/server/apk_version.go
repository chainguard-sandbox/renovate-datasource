package server

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
)

//go:embed templates/apk_version.html
var apkVersionPageHTML string

//go:embed templates/apk_version.js
var apkVersionPageJS string

//go:embed templates/apk_version_picker.html
var apkVersionPickerPageHTML string

// apkVersionPageTemplate is the shell for /apk/.../version/... — the
// single-version snapshot view used as a click target from version
// references on the diff pages.
var apkVersionPageTemplate = template.Must(template.New("apk_version").Parse(apkVersionPageHTML))

// apkVersionPickerPageTemplate renders the picker shown when a
// /apk/{name}/version/{version} URL resolves through a `p:` capability
// to more than one real package (e.g. `cmd:node=26.4.0-r1` provided by
// both nodejs-26 and nodejs-26-minimal).
var apkVersionPickerPageTemplate = template.Must(template.New("apk_version_picker").Parse(apkVersionPickerPageHTML))

// apkVersionPageData feeds the apk version template.
type apkVersionPageData struct {
	Name     string
	Version  string
	CSS      template.CSS
	CommonJS template.JS
	JS       template.JS
}

// apkVersionPickerPageData feeds apk_version_picker.html.
type apkVersionPickerPageData struct {
	Name    string
	Version string
	Cands   []apkChooserProvider
	CSS     template.CSS
}

// handleAPKVersion serves the JSON snapshot of one apk version. Same
// data shape as a single side of the diff response; useful as a
// click-target from any place an apk version is displayed. When the
// requested (name, version) is a `p:` capability the handler runs the
// provider resolver: unique → 302, ambiguous → 300 Multiple Choices,
// real → fall through to the direct fetch.
func (s *Server) handleAPKVersion(w http.ResponseWriter, r *http.Request) {
	pv := apk.PackageVersion{Name: r.PathValue("name"), Version: r.PathValue("version")}
	s.log.InfoContext(r.Context(), "apk version request", "name", pv.Name, "version", pv.Version, "remote", r.RemoteAddr, "ua", r.UserAgent())

	if !validateAPKProvidesName(w, pv.Name) {
		return
	}
	if !validateAPKVersion(w, "The version isn't well-formed.", pv.Version) {
		return
	}
	if s.apk == nil {
		writeAPIError(w, http.StatusNotImplemented, "Apk lookups aren't enabled on this deployment.")
		return
	}

	if redirect, cands := s.resolveAPKVersion(pv); redirect != nil {
		http.Redirect(w, r, "/v1"+apkVersionURL(*redirect), http.StatusFound)
		return
	} else if cands != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMultipleChoices)
		if err := json.NewEncoder(w).Encode(apkResolution{PackageVersion: pv, Providers: cands}); err != nil {
			s.log.ErrorContext(r.Context(), "encoding apk version candidates", "name", pv.Name, "err", err)
		}
		return
	}

	ctx := r.Context()
	resp, err := diff.ComputeAPKVersion(ctx, s.apk, pv.Name, pv.Version)
	if err != nil {
		status, msg := classifyAPKDiffError(err)
		if status >= 500 {
			s.log.ErrorContext(ctx, "ComputeAPKVersion failed", "name", pv.Name, "version", pv.Version, "err", err)
		} else {
			s.log.InfoContext(ctx, "ComputeAPKVersion client error", "name", pv.Name, "version", pv.Version, "status", status, "err", err)
		}
		writeAPIError(w, status, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.ErrorContext(ctx, "encoding apk version response", "name", pv.Name, "err", err)
	}
}

// handleAPKVersionPage serves the HTML shell that fetches
// /v1/apk/{name}/version/... client-side and renders the snapshot. Same
// resolver semantics as the JSON endpoint — unique capability
// resolution 302s to the underlying package's page, ambiguous ones
// render the picker.
func (s *Server) handleAPKVersionPage(w http.ResponseWriter, r *http.Request) {
	pv := apk.PackageVersion{Name: r.PathValue("name"), Version: r.PathValue("version")}

	if !validateAPKProvidesName(w, pv.Name) {
		return
	}
	if !validateAPKVersion(w, "The version isn't well-formed.", pv.Version) {
		return
	}

	if redirect, cands := s.resolveAPKVersion(pv); redirect != nil {
		http.Redirect(w, r, apkVersionURL(*redirect), http.StatusFound)
		return
	} else if cands != nil {
		s.renderAPKVersionPickerPage(w, r, pv, cands)
		return
	}

	data := apkVersionPageData{
		Name:     pv.Name,
		Version:  pv.Version,
		CSS:      template.CSS(diffPageCSS),
		CommonJS: template.JS(commonJS),
		JS:       template.JS(apkVersionPageJS),
	}

	writeHTMLHeaders(w)
	if err := apkVersionPageTemplate.Execute(w, data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering apk version page", "err", err)
	}
}

// resolveAPKVersion inspects the store for the requested (name,
// version) and reports one of three outcomes:
//
//   - redirect != nil: the request resolves uniquely to a different
//     real package; the caller should 302 there.
//   - cands != nil (with len > 1): multiple providers; the caller
//     renders the picker (HTML) or returns 300 (JSON).
//   - both nil: no resolution needed; the caller falls through to the
//     direct ComputeAPKVersion path.
func (s *Server) resolveAPKVersion(pv apk.PackageVersion) (redirect *apk.PackageVersion, cands []apk.PackageVersion) {
	if s.apkIndex == nil {
		return nil, nil
	}
	cands = s.apkIndex.Providers(pv.Name, pv.Version)
	switch {
	case len(cands) > 1:
		return nil, cands
	case len(cands) == 1 && cands[0] != pv:
		return &cands[0], nil
	default:
		return nil, nil
	}
}

// renderAPKVersionPickerPage lists candidate providers for a version
// URL that resolved to more than one real package. Each row is a link
// to the specific package's /apk/{realName}/version/{realVersion}
// snapshot.
func (s *Server) renderAPKVersionPickerPage(w http.ResponseWriter, r *http.Request, pv apk.PackageVersion, cands []apk.PackageVersion) {
	d := apkVersionPickerPageData{
		Name:    pv.Name,
		Version: pv.Version,
		CSS:     template.CSS(diffPageCSS),
	}
	for _, p := range cands {
		d.Cands = append(d.Cands, apkChooserProvider{
			Name: p.Name, Version: p.Version, URL: apkVersionURL(p),
		})
	}
	writeHTMLHeaders(w)
	if err := apkVersionPickerPageTemplate.Execute(w, d); err != nil {
		s.log.ErrorContext(r.Context(), "rendering apk version picker page", "err", err)
	}
}

// apkVersionURL builds the canonical link to a single apk version's
// snapshot page. name and version have already been validated against
// the apk name/version patterns, so PathEscape just guards against
// future relaxation of those patterns.
func apkVersionURL(pv apk.PackageVersion) string {
	return "/apk/" + url.PathEscape(pv.Name) + "/version/" + url.PathEscape(pv.Version)
}

// apkDiffPageURL builds the canonical link to the diff page between two
// (name, version) pairs. Same shape on both sides so the URL is
// structurally identical for same-name diffs (from == to.Name) and
// cross-package diffs. Only the from-side carries the /apk/ prefix —
// the to-side is just `/{name}/version/{version}` glued on after
// `/diff/`.
func apkDiffPageURL(from, to apk.PackageVersion) string {
	return apkVersionURL(from) + "/diff/" + url.PathEscape(to.Name) + "/version/" + url.PathEscape(to.Version)
}

// apkDiffAPIURL mirrors apkDiffPageURL for the JSON endpoint under /v1.
func apkDiffAPIURL(from, to apk.PackageVersion) string {
	return "/v1" + apkDiffPageURL(from, to)
}
