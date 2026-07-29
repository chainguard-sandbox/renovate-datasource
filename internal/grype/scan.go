// Package grype wraps grype as a library (no shell-out): a DB
// downloads and refreshes grype's SQLite vulnerability DB, and a
// Scanner runs matches against SPDX-encoded SBOM bytes.
package grype

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/anchore/grype/grype"
	"github.com/anchore/grype/grype/match"
	"github.com/anchore/grype/grype/matcher"
	grypePkg "github.com/anchore/grype/grype/pkg"
	"github.com/anchore/grype/grype/vulnerability"
	"github.com/anchore/syft/syft/format"
)

// Package is the trimmed view of an installable package a Match
// refers to. Field names mirror grype's pkg.Package.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type,omitempty"` // grype/syft ecosystem: "apk", "python", "npm", …
}

// Match is one vulnerability finding.
type Match struct {
	// ID is the vulnerability identifier. Usually CVE-YYYY-N but
	// grype also emits GHSA-* IDs.
	ID       string
	Severity string
	Package  Package
	// FixVersions is every version grype reports as fixed. Callers
	// surface the whole list rather than picking one — a reader on
	// version-line X can then tell whether a fix landed there.
	FixVersions []string
	FixState    string // "fixed" / "not-fixed" / "wont-fix" / "unknown"
	Description string
	CVSS        *CVSS
	KEV         bool // CISA Known Exploited Vulnerability
	URLs        []string
}

// CVSS is the primary CVSS score grype attached to a vulnerability.
type CVSS struct {
	Score  float64 `json:"score"`
	Vector string  `json:"vector,omitempty"`
	Source string  `json:"source,omitempty"` // e.g. "nvd@nist.gov", "GitHub"
}

// Scanner runs grype matches against a supplied SBOM. Safe for
// concurrent use.
type Scanner struct {
	provider vulnerability.Provider
	matchers []match.Matcher
}

// NewScanner constructs a Scanner backed by the given provider.
func NewScanner(vp vulnerability.Provider) *Scanner {
	return &Scanner{
		provider: vp,
		matchers: matcher.NewDefaultMatchers(matcher.Config{}),
	}
}

// Scan parses an SPDX-encoded SBOM and returns the vulnerabilities
// grype finds against the loaded DB.
func (s *Scanner) Scan(ctx context.Context, sbomBytes []byte) ([]Match, error) {
	decoded, _, _, err := format.Decode(bytes.NewReader(sbomBytes))
	if err != nil {
		return nil, fmt.Errorf("decoding sbom: %w", err)
	}
	if decoded == nil {
		return nil, errors.New("sbom decoded to nil (unrecognised format?)")
	}

	pkgPtrs := grypePkg.FromCollection(decoded.Artifacts.Packages, decoded.Relationships, grypePkg.SynthesisConfig{
		GenerateMissingCPEs: true,
	})
	pkgs := make([]grypePkg.Package, 0, len(pkgPtrs))
	for _, p := range pkgPtrs {
		if p != nil {
			pkgs = append(pkgs, *p)
		}
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("no packages parsed from sbom (input decoded to %d artifacts)", decoded.Artifacts.Packages.PackageCount())
	}

	pkgCtx := grypePkg.Context{
		Source: &decoded.Source,
	}

	vm := grype.VulnerabilityMatcher{
		VulnerabilityProvider: s.provider,
		Matchers:              s.matchers,
	}
	matches, _, err := vm.FindMatchesContext(ctx, pkgs, pkgCtx)
	if err != nil {
		return nil, fmt.Errorf("matching: %w", err)
	}
	if matches == nil {
		return nil, nil
	}

	sorted := matches.Sorted()
	out := make([]Match, 0, len(sorted))
	for _, m := range sorted {
		out = append(out, toMatch(m))
	}
	return out, nil
}

func toMatch(m match.Match) Match {
	out := Match{
		ID: m.Vulnerability.ID,
		Package: Package{
			Name:    m.Package.Name,
			Version: m.Package.Version,
			Type:    string(m.Package.Type),
		},
		FixState: string(m.Vulnerability.Fix.State),
		URLs:     advisoryURLs(m),
	}
	if len(m.Vulnerability.Fix.Versions) > 0 {
		out.FixVersions = append([]string(nil), m.Vulnerability.Fix.Versions...)
	}
	if md := m.Vulnerability.Metadata; md != nil {
		out.Severity = md.Severity
		out.Description = md.Description
		out.KEV = len(md.KnownExploited) > 0
		if len(md.Cvss) > 0 {
			c := md.Cvss[0]
			out.CVSS = &CVSS{
				Score:  c.Metrics.BaseScore,
				Vector: c.Vector,
				Source: c.Source,
			}
		}
	}
	return out
}

// advisoryURLs returns reference links for a match: curated
// Advisories first, then metadata URLs, deduped in order.
func advisoryURLs(m match.Match) []string {
	var out []string
	seen := make(map[string]struct{})
	push := func(u string) {
		if u == "" {
			return
		}
		if _, ok := seen[u]; ok {
			return
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	for _, a := range m.Vulnerability.Advisories {
		push(a.Link)
	}
	if md := m.Vulnerability.Metadata; md != nil {
		for _, u := range md.URLs {
			push(u)
		}
	}
	return out
}
