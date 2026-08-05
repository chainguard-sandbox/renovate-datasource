package diff

import (
	"context"
)

// APKVersionResponse is the JSON shape returned by /v1/apk/{name}/version
// — a single-version snapshot of the same pieces APKs would
// surface, but without comparison. Fields are populated independently;
// any may be empty when the underlying apk doesn't carry that entry.
type APKVersionResponse struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// URL is the fully-qualified location the apk was resolved at — the
	// first repository in the fallback chain that served the artifact.
	URL string `json:"url,omitempty"`
	// Metadata is the structured subset of .PKGINFO the UI renders as a
	// header. Nil when .PKGINFO wasn't parseable or carried none of the
	// fields we surface.
	Metadata     *PKGINFOMetadata   `json:"metadata,omitempty"`
	Melange      string             `json:"melange,omitempty"`
	PKGINFO      string             `json:"pkginfo,omitempty"`
	GitCheckouts []GitCheckoutEntry `json:"gitCheckouts,omitempty"`
	Fetches      []FetchEntry       `json:"fetches,omitempty"`
}

// ComputeAPKVersion fetches one apk and returns its raw .melange.yaml,
// .PKGINFO, and the parsed source-pipeline entries. Reuses the diff
// package's extractors so the version page and the diff page see the
// same data shapes.
func ComputeAPKVersion(ctx context.Context, f APKFetcher, name, version string) (*APKVersionResponse, error) {
	c, err := f.Fetch(ctx, name, version)
	if err != nil {
		return nil, err
	}
	resp := &APKVersionResponse{
		Name:    name,
		Version: version,
		URL:     c.URL,
		Melange: string(c.Melange),
		PKGINFO: string(c.PKGINFO),
	}

	if len(c.PKGINFO) > 0 {
		if md := parsePKGINFO(c.PKGINFO); md != (PKGINFOMetadata{}) {
			resp.Metadata = &md
		}
	}

	// Parse the melange yaml once and feed it to both extractors so we
	// don't pay for yaml.Unmarshal twice on a single-version request.
	parsed := parseMelange(c.Melange)

	git := extractGitCheckouts(parsed)
	for i := range git {
		// decorate populates TagURL / CommitURL from (host, path) so the
		// UI can hyperlink each tag and commit without re-deriving the
		// URL on the client.
		git[i] = decorate(git[i])
	}
	if len(git) > 0 {
		resp.GitCheckouts = git
	}

	if fetches := extractFetches(parsed); len(fetches) > 0 {
		resp.Fetches = fetches
	}

	return resp, nil
}
