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

//go:embed templates/chart.html
var chartPageHTML string

//go:embed templates/chart.js
var chartPageJS string

var chartPageTemplate = template.Must(template.New("chart").Parse(chartPageHTML))

type chartPageData struct {
	// APIPrefix is the chart-flavor subrepo (charts / iamguarded-charts).
	// The JS composes /v1/{APIPrefix}/{Name}/diff/{oldRef}/{newRef}.
	APIPrefix      string
	Name           string
	OldRef, NewRef string
	Title          string
	ConsoleURL     string
	CSS            template.CSS
	CommonJS, JS   template.JS
}

func (s *Server) handleChartDiff(w http.ResponseWriter, r *http.Request) {
	s.serveChartDiff(w, r, chartsPrefix)
}

func (s *Server) handleIamguardedChartDiff(w http.ResponseWriter, r *http.Request) {
	s.serveChartDiff(w, r, iamguardedChartsPrefix)
}

func (s *Server) serveChartDiff(w http.ResponseWriter, r *http.Request, prefix string) {
	name := r.PathValue("name")
	from := r.PathValue("from")
	to := r.PathValue("to")
	if !chartNamePattern.MatchString(name) {
		writeAPIError(w, http.StatusBadRequest, "The chart name isn't well-formed.")
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
	if s.chart == nil {
		writeAPIError(w, http.StatusNotImplemented, "The server was not configured with a chart fetcher.")
		return
	}

	repo := prefix + "/" + name
	ctx := r.Context()
	s.log.InfoContext(ctx, "chart diff request", "repo", repo, "from", from, "to", to, "remote", r.RemoteAddr, "ua", r.UserAgent())

	resp, err := diff.Charts(ctx, s.chart, repo, from, to)
	if err != nil {
		status, msg := classifyChartDiffError(err)
		if status >= 500 {
			s.log.ErrorContext(ctx, "diff.Charts failed", "repo", repo, "from", from, "to", to, "err", err)
		} else {
			s.log.InfoContext(ctx, "diff.Charts client error", "repo", repo, "from", from, "to", to, "status", status, "err", err)
		}
		writeAPIError(w, status, msg)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.ErrorContext(ctx, "encoding chart diff response", "repo", repo, "err", err)
	}
}

func classifyChartDiffError(err error) (int, string) {
	var te *transport.Error
	if errors.As(err, &te) {
		switch te.StatusCode {
		case http.StatusNotFound, http.StatusForbidden:
			// Map 403 the same as 404 so the caller can't
			// distinguish "you're not allowed to see this chart"
			// from "no such chart".
			return http.StatusNotFound, "This tag or digest doesn't exist in the registry."
		case http.StatusUnauthorized:
			return http.StatusBadGateway, "Failed to authenticate with the upstream registry."
		}
	}
	if errors.Is(err, oci.ErrChartLayerMissing) {
		return http.StatusUnprocessableEntity, "This artifact carries no chart layer, so a diff can't be computed."
	}
	return http.StatusBadGateway, "The upstream registry returned an error. Please try again in a moment."
}

func (s *Server) handleChartDiffPage(w http.ResponseWriter, r *http.Request) {
	s.serveChartDiffPage(w, r, chartsPrefix)
}

func (s *Server) handleIamguardedChartDiffPage(w http.ResponseWriter, r *http.Request) {
	s.serveChartDiffPage(w, r, iamguardedChartsPrefix)
}

func (s *Server) serveChartDiffPage(w http.ResponseWriter, r *http.Request, prefix string) {
	name := r.PathValue("name")
	oldRef := r.PathValue("oldRef")
	newRef := r.PathValue("newRef")
	if !chartNamePattern.MatchString(name) {
		writeAPIError(w, http.StatusBadRequest, "The chart name isn't well-formed.")
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

	repo := prefix + "/" + name
	data := chartPageData{
		APIPrefix:  prefix,
		Name:       name,
		OldRef:     oldRef,
		NewRef:     newRef,
		Title:      pageTitle(s.orgName, repo),
		ConsoleURL: chartConsoleURL(s.orgName, prefix, name),
		CSS:        template.CSS(diffPageCSS),
		CommonJS:   template.JS(commonJS),
		JS:         template.JS(chartPageJS),
	}

	writeHTMLHeaders(w)
	if err := chartPageTemplate.Execute(w, data); err != nil {
		s.log.ErrorContext(r.Context(), "rendering chart diff page", "err", err)
	}
}

// chartConsoleURL builds the Chainguard console overview URL for a
// chart. Note the console's inverted naming: our `charts` prefix
// (community-supported charts) maps to `community-chart`, and
// `iamguarded-charts` (fully-supported) maps to `chart`.
func chartConsoleURL(orgName, prefix, name string) string {
	if orgName == "" {
		return ""
	}
	var kind string
	switch prefix {
	case chartsPrefix:
		kind = "community-chart"
	case iamguardedChartsPrefix:
		kind = "chart"
	default:
		return ""
	}
	return "https://console.chainguard.dev/org/" + orgName + "/helm/organization/" + kind + "/" + name + "/overview"
}
