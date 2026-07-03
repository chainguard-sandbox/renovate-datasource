package diff

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
	"golang.org/x/sync/errgroup"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
)

// diffContext matches GNU diff -u: three lines of unchanged context
// before and after each hunk.
const diffContext = 3

// APKDiffResponse is the JSON shape returned by /v1/apk/{name}/diff. The
// text diff fields are unified-diff strings between the from and to
// apks' respective entries (empty when byte-identical). Sources is a
// structured diff of pipeline steps extracted from the melange.yaml —
// nil when no source pins changed.
//
// The *Missing booleans flag the case where one side's apk didn't
// carry the corresponding entry (older packages predate melange.yaml
// embedding, for instance). When set, an empty Melange/PKGINFO field
// means "absent" rather than "byte-identical", and the UI surfaces
// that distinction.
type APKDiffResponse struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
	// FromURL / ToURL are the fully-qualified locations each side's apk
	// was resolved at. Displayed in the diff-page metadata header so
	// users can see when the fallback chain served the two versions
	// from different repositories.
	FromURL string `json:"fromUrl,omitempty"`
	ToURL   string `json:"toUrl,omitempty"`
	// FromMetadata / ToMetadata are the structured .PKGINFO subsets
	// used to render the metadata header on the diff page. Either may
	// be nil when the corresponding apk has no .PKGINFO.
	FromMetadata       *PKGINFOMetadata `json:"fromMetadata,omitempty"`
	ToMetadata         *PKGINFOMetadata `json:"toMetadata,omitempty"`
	Melange            string           `json:"melange,omitempty"`
	PKGINFO            string           `json:"pkginfo,omitempty"`
	FromMelangeMissing bool             `json:"fromMelangeMissing,omitempty"`
	ToMelangeMissing   bool             `json:"toMelangeMissing,omitempty"`
	FromPKGINFOMissing bool             `json:"fromPkginfoMissing,omitempty"`
	ToPKGINFOMissing   bool             `json:"toPkginfoMissing,omitempty"`
	Sources            *SourceChanges   `json:"sources,omitempty"`
}

// SourceChanges aggregates structured diffs across the different kinds
// of pipeline steps that pull source material into a melange build.
// Either sub-field may be nil independently.
type SourceChanges struct {
	GitCheckouts *GitCheckoutChanges `json:"gitCheckouts,omitempty"`
	Fetches      *FetchChanges       `json:"fetches,omitempty"`
}

// APKFetcher loads the contents of an apk by name and version. One call
// is expected to return everything ComputeAPKDiff needs so we don't
// re-download the apk for each diff section.
type APKFetcher interface {
	Fetch(ctx context.Context, name, version string) (*apk.Contents, error)
}

// ComputeAPKDiff fetches both versions' apk contents in parallel and
// returns a unified diff of .melange.yaml and .PKGINFO plus a
// structured diff of the source-pipeline entries (git-checkouts and
// fetches). Errors from either fetch are surfaced verbatim so the HTTP
// layer can map them to appropriate status codes (e.g. apk.ErrNotFound
// → 404).
func ComputeAPKDiff(ctx context.Context, f APKFetcher, name, fromVer, toVer string) (*APKDiffResponse, error) {
	var fromC, toC *apk.Contents
	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		c, err := f.Fetch(egCtx, name, fromVer)
		if err != nil {
			return fmt.Errorf("from %s: %w", fromVer, err)
		}
		fromC = c
		return nil
	})
	eg.Go(func() error {
		c, err := f.Fetch(egCtx, name, toVer)
		if err != nil {
			return fmt.Errorf("to %s: %w", toVer, err)
		}
		toC = c
		return nil
	})
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	resp := &APKDiffResponse{
		Name:               name,
		From:               fromVer,
		To:                 toVer,
		FromURL:            fromC.URL,
		ToURL:              toC.URL,
		FromMelangeMissing: len(fromC.Melange) == 0,
		ToMelangeMissing:   len(toC.Melange) == 0,
		FromPKGINFOMissing: len(fromC.PKGINFO) == 0,
		ToPKGINFOMissing:   len(toC.PKGINFO) == 0,
	}

	if len(fromC.PKGINFO) > 0 {
		if md := parsePKGINFO(fromC.PKGINFO); md != (PKGINFOMetadata{}) {
			resp.FromMetadata = &md
		}
	}
	if len(toC.PKGINFO) > 0 {
		if md := parsePKGINFO(toC.PKGINFO); md != (PKGINFOMetadata{}) {
			resp.ToMetadata = &md
		}
	}

	// Only compute the .melange.yaml diff when both sides carry one;
	// otherwise leave Melange empty and let the UI render the missing-
	// side notice via FromMelangeMissing / ToMelangeMissing.
	if len(fromC.Melange) > 0 && len(toC.Melange) > 0 {
		melangeDiff, err := unifiedDiff(name+" .melange.yaml", fromVer, toVer, fromC.Melange, toC.Melange)
		if err != nil {
			return nil, err
		}
		resp.Melange = melangeDiff
	}

	pkginfoDiff, err := unifiedDiff(name+" .PKGINFO", fromVer, toVer, fromC.PKGINFO, toC.PKGINFO)
	if err != nil {
		return nil, err
	}
	resp.PKGINFO = pkginfoDiff

	resp.Sources = diffSourcePipelines(fromC.Melange, toC.Melange)

	return resp, nil
}

// diffSourcePipelines aggregates the git-checkout and fetch pipeline
// diffs and returns nil when neither kind of source changed, so the
// JSON field can be omitted entirely. The yaml is parsed once per side
// and shared between extractors.
func diffSourcePipelines(fromYAML, toYAML []byte) *SourceChanges {
	from := parseMelange(fromYAML)
	to := parseMelange(toYAML)
	git := diffGitCheckouts(from, to)
	fetch := diffFetches(from, to)
	if git == nil && fetch == nil {
		return nil
	}
	return &SourceChanges{GitCheckouts: git, Fetches: fetch}
}

// unifiedDiff returns a unified diff of from/to. Both empty inputs → "".
// Identical inputs → "". Otherwise → standard `diff -u` output with the
// supplied label embedded in the FromFile/ToFile headers.
func unifiedDiff(label, fromVer, toVer string, from, to []byte) (string, error) {
	if bytes.Equal(from, to) {
		return "", nil
	}
	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitLines(string(from)),
		B:        splitLines(string(to)),
		FromFile: label + " " + fromVer,
		ToFile:   label + " " + toVer,
		Context:  diffContext,
	})
	if err != nil {
		return "", fmt.Errorf("rendering unified diff for %s: %w", label, err)
	}
	return out, nil
}

// splitLines preserves trailing newlines per line so the unified-diff
// output stays consistent with what `diff -u` produces. strings.Split on
// "\n" would drop them and produce a degenerate "\ No newline at end of
// file" marker on every hunk.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if last := len(parts) - 1; last >= 0 && parts[last] == "" {
		parts = parts[:last]
	}
	return parts
}
