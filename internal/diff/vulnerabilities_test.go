package diff

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/grype"
)

func TestDiffVulnerabilities(t *testing.T) {
	apk := func(name, version string) grype.Package {
		return grype.Package{Name: name, Version: version, Type: "apk"}
	}
	pyth := func(name, version string) grype.Package {
		return grype.Package{Name: name, Version: version, Type: "python"}
	}
	match := func(id, sev string, pkg grype.Package, fixVersions []string, urls ...string) grype.Match {
		return grype.Match{ID: id, Severity: sev, Package: pkg, FixVersions: fixVersions, URLs: urls}
	}

	tests := []struct {
		name        string
		from, to    []grype.Match
		wantAdded   []string // "id|pkg=ver,pkg=ver|fixedIn"
		wantRemoved []string
	}{
		{
			name: "empty from surfaces everything as Added",
			from: nil,
			to: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.5"), []string{"3.0.8"}),
			},
			wantAdded: []string{"CVE-2024-1|openssl=3.0.5(3.0.8)"},
		},
		{
			name: "empty to surfaces everything as Removed",
			from: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.0"), nil),
			},
			to:          nil,
			wantRemoved: []string{"CVE-2024-1|openssl=3.0.0"},
		},
		{
			name: "unchanged vulnerability drops from both buckets",
			from: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.0"), nil),
				match("CVE-2024-2", "medium", apk("curl", "8.0.0"), nil),
			},
			to: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.0"), nil),
				match("CVE-2024-3", "low", apk("git", "2.40.0"), nil),
			},
			wantAdded:   []string{"CVE-2024-3|git=2.40.0"},
			wantRemoved: []string{"CVE-2024-2|curl=8.0.0"},
		},
		{
			name: "same vulnerability on a different package on the other side is still unchanged",
			from: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.0"), nil),
			},
			to: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl-dev", "3.0.0"), nil),
			},
			wantAdded:   nil,
			wantRemoved: nil,
		},
		{
			name: "same vulnerability across ecosystems is still unchanged",
			from: []grype.Match{
				match("CVE-2024-1", "high", apk("cryptography", "3.0.0"), nil),
			},
			to: []grype.Match{
				match("CVE-2024-1", "high", pyth("cryptography", "3.0.0"), nil),
			},
			wantAdded:   nil,
			wantRemoved: nil,
		},
		{
			name: "multiple packages per vulnerability fold into one entry with a merged Packages list",
			from: nil,
			to: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.5"), []string{"3.0.8"}),
				match("CVE-2024-1", "high", apk("openssl-doc", "3.0.5"), []string{"3.0.8"}),
			},
			wantAdded: []string{"CVE-2024-1|openssl=3.0.5(3.0.8),openssl-doc=3.0.5(3.0.8)"},
		},
		{
			name: "duplicate match within a scan collapses in the merged Packages list",
			from: nil,
			to: []grype.Match{
				match("CVE-2024-1", "high", apk("openssl", "3.0.5"), []string{"3.0.8"}),
				match("CVE-2024-1", "high", apk("openssl", "3.0.5"), []string{"3.0.8"}),
			},
			wantAdded: []string{"CVE-2024-1|openssl=3.0.5(3.0.8)"},
		},
		{
			name:        "both empty yields empty diff",
			from:        nil,
			to:          nil,
			wantAdded:   nil,
			wantRemoved: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffVulnerabilities(tc.from, tc.to)
			if diff := comparePartition(got.Added, tc.wantAdded); diff != "" {
				t.Errorf("Added mismatch: %s", diff)
			}
			if diff := comparePartition(got.Removed, tc.wantRemoved); diff != "" {
				t.Errorf("Removed mismatch: %s", diff)
			}
		})
	}
}

func TestMergeMatches_PackagesSortedAndURLsDeduped(t *testing.T) {
	// Order must be deterministic so JSON diffs stay stable.
	ms := []grype.Match{
		{ID: "CVE-X", Severity: "high", Package: grype.Package{Name: "z", Version: "1", Type: "apk"}, FixVersions: []string{"1.1"}, URLs: []string{"u1", "u2"}},
		{ID: "CVE-X", Severity: "high", Package: grype.Package{Name: "a", Version: "2", Type: "apk"}, FixVersions: []string{"2.1", "3.0"}, URLs: []string{"u2", "u3"}},
	}
	got := mergeMatches("CVE-X", ms)
	wantPkgs := []AffectedPackage{
		{Package: grype.Package{Name: "a", Version: "2", Type: "apk"}, FixVersions: []string{"2.1", "3.0"}},
		{Package: grype.Package{Name: "z", Version: "1", Type: "apk"}, FixVersions: []string{"1.1"}},
	}
	if !reflect.DeepEqual(got.Packages, wantPkgs) {
		t.Errorf("Packages = %+v, want %+v", got.Packages, wantPkgs)
	}
	wantURLs := []string{"u1", "u2", "u3"}
	if !reflect.DeepEqual(got.URLs, wantURLs) {
		t.Errorf("URLs = %+v, want %+v", got.URLs, wantURLs)
	}
}

func TestMergeMatches_VulnerabilityLevelMetadataFromFirstMatch(t *testing.T) {
	// First match wins for severity/description/CVSS; KEV is OR'd.
	cvss := &grype.CVSS{Score: 9.8, Vector: "AV:N", Source: "nvd@nist.gov"}
	ms := []grype.Match{
		{ID: "CVE-X", Severity: "high", Description: "First description", CVSS: cvss},
		{ID: "CVE-X", Severity: "critical", Description: "Second (should lose)", KEV: true},
	}
	got := mergeMatches("CVE-X", ms)
	if got.Severity != "high" {
		t.Errorf("Severity = %q, want high (first wins)", got.Severity)
	}
	if got.Description != "First description" {
		t.Errorf("Description = %q, want the first entry", got.Description)
	}
	if got.CVSS == nil || got.CVSS.Score != 9.8 {
		t.Errorf("CVSS = %+v, want first-seen score 9.8", got.CVSS)
	}
	if !got.KEV {
		t.Errorf("KEV = false, want true (any match with KEV flips it)")
	}
}

// comparePartition renders each vulnerability as "id|pkg=ver(fix),..."
// and compares as sorted sets so map iteration order doesn't matter.
func comparePartition(got []Vulnerability, want []string) string {
	gotKeys := make([]string, 0, len(got))
	for _, c := range got {
		gotKeys = append(gotKeys, c.ID+"|"+joinPackages(c.Packages))
	}
	sort.Strings(gotKeys)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if len(gotKeys) != len(wantSorted) {
		return fmtDiff(gotKeys, wantSorted)
	}
	for i := range gotKeys {
		if gotKeys[i] != wantSorted[i] {
			return fmtDiff(gotKeys, wantSorted)
		}
	}
	return ""
}

func joinPackages(ps []AffectedPackage) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += ","
		}
		out += p.Name + "=" + p.Version
		if len(p.FixVersions) > 0 {
			out += "(" + strings.Join(p.FixVersions, "|") + ")"
		}
	}
	return out
}

func fmtDiff(got, want []string) string {
	return "\n  got:  " + join(got) + "\n  want: " + join(want)
}

func join(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	out := "["
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out + "]"
}
