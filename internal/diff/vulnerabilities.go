package diff

import (
	"context"
	"sort"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/grype"
)

// GrypeScanner runs a vulnerability scan against an SBOM. *grype.DB
// satisfies it structurally.
type GrypeScanner interface {
	Scan(ctx context.Context, sbom []byte) ([]grype.Match, error)
}

// Vulnerabilities groups the vulnerability changes between two image
// refs, keyed on vulnerability ID alone: a vulnerability present on
// either side — even against a different package — is treated as
// unchanged and dropped from both buckets.
type Vulnerabilities struct {
	Added   []Vulnerability `json:"added"`
	Removed []Vulnerability `json:"removed"`
}

// Vulnerability is one vulnerability aggregated across every affected
// package on the affected side.
type Vulnerability struct {
	ID          string            `json:"id"`
	Severity    string            `json:"severity,omitempty"`
	Description string            `json:"description,omitempty"`
	CVSS        *grype.CVSS       `json:"cvss,omitempty"`
	KEV         bool              `json:"kev,omitempty"`
	Packages    []AffectedPackage `json:"packages"`
	URLs        []string          `json:"urls,omitempty"`
}

// AffectedPackage is one package grype flagged for a given
// vulnerability, embedding grype.Package so its fields flatten into
// the JSON alongside the per-package fix detail.
type AffectedPackage struct {
	grype.Package
	FixVersions []string `json:"fixVersions,omitempty"`
	FixState    string   `json:"fixState,omitempty"`
}

// diffVulnerabilities computes the added/removed sets between two
// scans, keyed by vulnerability ID. Matches for the same ID within a
// scan are folded via mergeMatches.
func diffVulnerabilities(from, to []grype.Match) Vulnerabilities {
	fromByID := indexMatchesByID(from)
	toByID := indexMatchesByID(to)
	out := Vulnerabilities{}
	for id, matches := range fromByID {
		if _, ok := toByID[id]; !ok {
			out.Removed = append(out.Removed, mergeMatches(id, matches))
		}
	}
	for id, matches := range toByID {
		if _, ok := fromByID[id]; !ok {
			out.Added = append(out.Added, mergeMatches(id, matches))
		}
	}
	return out
}

func indexMatchesByID(ms []grype.Match) map[string][]grype.Match {
	out := make(map[string][]grype.Match)
	for _, m := range ms {
		out[m.ID] = append(out[m.ID], m)
	}
	return out
}

// mergeMatches folds every match for one vulnerability ID into a
// single Vulnerability. Vuln-level metadata is taken from the first
// match that supplies it; duplicate packages (grype can emit the
// same package via multiple purl/CPE routes) and URLs are deduped.
func mergeMatches(id string, matches []grype.Match) Vulnerability {
	out := Vulnerability{ID: id}
	seenPkg := make(map[grype.Package]struct{}, len(matches))
	seenURL := make(map[string]struct{})
	for _, m := range matches {
		if out.Severity == "" {
			out.Severity = m.Severity
		}
		if out.Description == "" {
			out.Description = m.Description
		}
		if out.CVSS == nil && m.CVSS != nil {
			cvss := *m.CVSS
			out.CVSS = &cvss
		}
		if m.KEV {
			out.KEV = true
		}
		if _, ok := seenPkg[m.Package]; !ok {
			seenPkg[m.Package] = struct{}{}
			out.Packages = append(out.Packages, AffectedPackage{
				Package:     m.Package,
				FixVersions: append([]string(nil), m.FixVersions...),
				FixState:    m.FixState,
			})
		}
		for _, u := range m.URLs {
			if _, ok := seenURL[u]; !ok {
				seenURL[u] = struct{}{}
				out.URLs = append(out.URLs, u)
			}
		}
	}
	sort.Slice(out.Packages, func(i, j int) bool {
		if out.Packages[i].Name != out.Packages[j].Name {
			return out.Packages[i].Name < out.Packages[j].Name
		}
		return out.Packages[i].Version < out.Packages[j].Version
	})
	return out
}
