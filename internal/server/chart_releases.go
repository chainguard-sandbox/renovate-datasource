package server

import "net/http"

// Subrepo prefixes under the configured org for the two chart
// flavors. Handlers compose `<prefix>/<name>` before dispatching.
const (
	chartsPrefix           = "charts"
	iamguardedChartsPrefix = "iamguarded-charts"
)

func (s *Server) handleChartReleases(w http.ResponseWriter, r *http.Request) {
	s.serveChartReleases(w, r, chartsPrefix)
}

func (s *Server) handleIamguardedChartReleases(w http.ResponseWriter, r *http.Request) {
	s.serveChartReleases(w, r, iamguardedChartsPrefix)
}

// serveChartReleases delegates to the shared releases handler so
// cooldown behaviour stays lockstep with images.
func (s *Server) serveChartReleases(w http.ResponseWriter, r *http.Request, prefix string) {
	name := r.PathValue("name")
	if !chartNamePattern.MatchString(name) {
		writeAPIError(w, http.StatusBadRequest, "The chart name isn't well-formed.")
		return
	}
	r.SetPathValue("repo", prefix+"/"+name)
	s.handleReleases(w, r)
}
