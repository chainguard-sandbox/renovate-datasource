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

//go:embed templates/apk_chooser.html
var apkChooserPageHTML string

// apkPageTemplate is the shell for the dedicated /apk/.../diff/... view.
// The CSS body is shared with the image diff page (see web.go) so the two
// pages render in the same visual style.
var apkPageTemplate = template.Must(template.New("apk").Parse(apkPageHTML))

// apkChooserPageTemplate renders the fallback picker shown when
// either side of a diff request maps to more than one real package
// via a `p:` entry (e.g. `cmd:node=26.4.0-r1` provided by both
// `nodejs-26` and `nodejs-26-minimal`).
var apkChooserPageTemplate = template.Must(template.New("apk_chooser").Parse(apkChooserPageHTML))

// apkPageData feeds the apk diff template. Versions are emitted in a
// JS context so html/template quotes them for us. FromURL/ToURL link
// each side to its single-version page so visitors can drill in.
// APIURL is the JSON endpoint the client-side JS fetches.
type apkPageData struct {
	FromName string
	ToName   string
	From     string
	To       string
	FromURL  string
	ToURL    string
	APIURL   string
	CSS      template.CSS
	CommonJS template.JS
	JS       template.JS
}

// handleAPKDiff serves the JSON diff between two apk (name, version)
// pairs (.melange.yaml + .PKGINFO unified diffs, plus structured
// source-pipeline changes). URL shape is symmetric with the snapshot
// endpoint — two /apk/{name}/version/{version} paths joined by /diff.
// Each side runs through the `p:` provider resolver independently, so
// mixed URLs (one capability + one real package, or two different
// capabilities) resolve just as well as symmetric ones.
func (s *Server) handleAPKDiff(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseAPKDiffPathParams(w, r)
	if !ok {
		return
	}
	s.log.InfoContext(r.Context(), "apk diff request", "fromName", from.Name, "from", from.Version, "toName", to.Name, "to", to.Version, "remote", r.RemoteAddr, "ua", r.UserAgent())
	if s.apk == nil {
		writeAPIError(w, http.StatusNotImplemented, "Apk diffs aren't enabled on this deployment.")
		return
	}

	fromCands, toCands := s.resolveAPKSides(from, to)
	if len(fromCands) > 1 || len(toCands) > 1 {
		cands := apkDiffCandidates{
			From: apkResolution{PackageVersion: from, Providers: fromCands},
			To:   apkResolution{PackageVersion: to, Providers: toCands},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMultipleChoices)
		if err := json.NewEncoder(w).Encode(cands); err != nil {
			s.log.ErrorContext(r.Context(), "encoding apk diff candidates", "fromName", from.Name, "toName", to.Name, "err", err)
		}
		return
	}
	if rFrom, rTo, redirected := redirectTargetIfResolvedElsewhere(from, to, fromCands, toCands); redirected {
		http.Redirect(w, r, apkDiffAPIURL(rFrom, rTo), http.StatusFound)
		return
	}
	s.serveAPKDiffJSON(w, r, from, to)
}

// serveAPKDiffJSON is the shared tail of the JSON handler — fetches
// both sides, computes the diff, and encodes the response. Callers are
// responsible for input validation and provider resolution.
func (s *Server) serveAPKDiffJSON(w http.ResponseWriter, r *http.Request, from, to apk.PackageVersion) {
	ctx := r.Context()
	resp, err := diff.APKs(ctx, s.apk, from, to)
	if err != nil {
		status, msg := classifyAPKDiffError(err)
		if status >= 500 {
			s.log.ErrorContext(ctx, "diff.APKs failed", "fromName", from.Name, "from", from.Version, "toName", to.Name, "to", to.Version, "err", err)
		} else {
			s.log.InfoContext(ctx, "diff.APKs client error", "fromName", from.Name, "from", from.Version, "toName", to.Name, "to", to.Version, "status", status, "err", err)
		}
		writeAPIError(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.ErrorContext(ctx, "encoding apk diff response", "fromName", from.Name, "err", err)
	}
}

// parseAPKDiffPathParams reads and validates the four path values the
// symmetric diff routes share ({fromName, fromVersion, toName,
// toVersion}). Writes a 400 on failure and returns ok=false so the
// caller stops.
func parseAPKDiffPathParams(w http.ResponseWriter, r *http.Request) (from, to apk.PackageVersion, ok bool) {
	from = apk.PackageVersion{Name: r.PathValue("fromName"), Version: r.PathValue("fromVersion")}
	to = apk.PackageVersion{Name: r.PathValue("toName"), Version: r.PathValue("toVersion")}
	if !validateAPKProvidesName(w, from.Name) {
		return
	}
	if !validateAPKProvidesName(w, to.Name) {
		return
	}
	if !validateAPKVersion(w, "The 'from' version isn't well-formed.", from.Version) {
		return
	}
	if !validateAPKVersion(w, "The 'to' version isn't well-formed.", to.Version) {
		return
	}
	ok = true
	return
}

// handleAPKDiffPage serves the HTML shell that fetches the JSON diff
// endpoint client-side and renders the unified diff once the API call
// returns. Same URL shape and per-side resolver semantics as
// handleAPKDiff. When either side is ambiguous the chooser page steps
// in; unambiguous resolutions produce a 302 to the resolved URL, and
// fully-real pairs fall through to the diff page.
func (s *Server) handleAPKDiffPage(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseAPKDiffPathParams(w, r)
	if !ok {
		return
	}
	fromCands, toCands := s.resolveAPKSides(from, to)
	if len(fromCands) > 1 || len(toCands) > 1 {
		s.renderAPKChooserPage(w, r, from, to, fromCands, toCands)
		return
	}
	if rFrom, rTo, redirected := redirectTargetIfResolvedElsewhere(from, to, fromCands, toCands); redirected {
		http.Redirect(w, r, apkDiffPageURL(rFrom, rTo), http.StatusFound)
		return
	}
	s.renderAPKDiffPage(w, r, from, to)
}

// renderAPKDiffPage is the shared render step for the HTML diff route.
func (s *Server) renderAPKDiffPage(w http.ResponseWriter, r *http.Request, from, to apk.PackageVersion) {
	data := apkPageData{
		FromName: from.Name,
		ToName:   to.Name,
		From:     from.Version,
		To:       to.Version,
		FromURL:  apkVersionURL(from),
		ToURL:    apkVersionURL(to),
		APIURL:   apkDiffAPIURL(from, to),
		CSS:      template.CSS(diffPageCSS),
		CommonJS: template.JS(commonJS),
		JS:       template.JS(apkPageJS),
	}
	writeHTMLHeaders(w)
	if err := apkPageTemplate.Execute(w, data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering apk diff page", "err", err)
	}
}

// apkResolution names a requested (name, version) pin alongside the
// installable packages that satisfy it. The version endpoint's 300
// Multiple Choices payload is a bare apkResolution; the diff endpoint's
// nests two under from/to. Providers embeds the PackageVersion fields
// via anonymous inclusion so the JSON shape is a flat
// {name, version, providers} object.
type apkResolution struct {
	apk.PackageVersion
	Providers []apk.PackageVersion `json:"providers"`
}

// apkDiffCandidates is the 300 Multiple Choices JSON payload returned
// when either side of a diff request maps to more than one real
// package. Consumers enumerate the per-side providers and decide which
// specific-package pair to fetch next; the HTML page uses the same
// data to render the chooser view.
type apkDiffCandidates struct {
	From apkResolution `json:"from"`
	To   apkResolution `json:"to"`
}

// resolveAPKSides returns the store's provider lists for each side of
// the diff independently. Each returned slice mirrors what Providers()
// yields — nil when the store has no opinion, [self] when the pair is
// itself a real package, or a PackageVersion list when the pair is a
// `p:` capability. Callers combine the two to decide between direct
// fetch, redirect, and chooser rendering.
func (s *Server) resolveAPKSides(from, to apk.PackageVersion) (fromCands, toCands []apk.PackageVersion) {
	if s.apkIndex == nil {
		return nil, nil
	}
	return s.apkIndex.Providers(from.Name, from.Version), s.apkIndex.Providers(to.Name, to.Version)
}

// redirectTargetIfResolvedElsewhere folds each side's single-candidate
// resolution into a redirect target. Returns redirected=true only when
// the resolved (name, version) differs from the requested one on
// either side; a fully-real request that Providers() already reports
// as self is left alone so the caller falls through to the direct
// fetch. No-candidate sides pass through unchanged so an unindexed
// real package still gets fetched under its requested name.
func redirectTargetIfResolvedElsewhere(from, to apk.PackageVersion, fromCands, toCands []apk.PackageVersion) (rFrom, rTo apk.PackageVersion, redirected bool) {
	rFrom, rTo = from, to
	if len(fromCands) == 1 {
		rFrom = fromCands[0]
	}
	if len(toCands) == 1 {
		rTo = toCands[0]
	}
	redirected = rFrom != from || rTo != to
	return
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

// apkChooserPageData feeds apk_chooser.html. Both sides carry their own
// (name, version) so mixed requests (one capability, one real package)
// render sensibly — no assumption that the two sides share a name.
type apkChooserPageData struct {
	FromName    string
	FromVersion string
	ToName      string
	ToVersion   string
	Diffs       []apkChooserDiff
	FromCands   []apkChooserProvider
	ToCands     []apkChooserProvider
	CSS         template.CSS
}

type apkChooserProvider struct {
	Name    string
	Version string
	URL     string
}

type apkChooserDiff struct {
	Name string
	From string
	To   string
	URL  string
}

// renderAPKChooserPage lists candidate providers for a diff request
// that resolved to more than one real package on at least one side.
// Any (fromProvider, toProvider) pair whose names match becomes a
// "same-package diff" row linking straight through to the resolved
// diff URL; the rest surface as per-version snapshot links so the
// operator can inspect each side individually.
func (s *Server) renderAPKChooserPage(w http.ResponseWriter, r *http.Request, from, to apk.PackageVersion, fromCands, toCands []apk.PackageVersion) {
	d := apkChooserPageData{
		FromName:    from.Name,
		FromVersion: from.Version,
		ToName:      to.Name,
		ToVersion:   to.Version,
		CSS:         template.CSS(diffPageCSS),
	}
	for _, p := range fromCands {
		d.FromCands = append(d.FromCands, apkChooserProvider{
			Name: p.Name, Version: p.Version, URL: apkVersionURL(p),
		})
	}
	for _, p := range toCands {
		d.ToCands = append(d.ToCands, apkChooserProvider{
			Name: p.Name, Version: p.Version, URL: apkVersionURL(p),
		})
	}
	for _, fp := range fromCands {
		for _, tp := range toCands {
			if fp.Name != tp.Name {
				continue
			}
			d.Diffs = append(d.Diffs, apkChooserDiff{
				Name: fp.Name,
				From: fp.Version,
				To:   tp.Version,
				URL:  apkDiffPageURL(fp, apk.PackageVersion{Name: fp.Name, Version: tp.Version}),
			})
		}
	}
	writeHTMLHeaders(w)
	if err := apkChooserPageTemplate.Execute(w, d); err != nil {
		s.log.ErrorContext(r.Context(), "rendering apk chooser page", "err", err)
	}
}
