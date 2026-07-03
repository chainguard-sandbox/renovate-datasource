package diff

import (
	"slices"
	"strings"
)

// collectAPKEntries returns every apk-ecosystem package in the SBOM.
func collectAPKEntries(s *sbom) []sbomPackage {
	out := make([]sbomPackage, 0, len(s.Packages))
	for _, p := range s.Packages {
		if ecosystemFromPurl(p.Purl) != "apk" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// diffAPKPackages joins from/to apk entries on package name and emits the
// add/remove/update buckets.
func diffAPKPackages(from, to []sbomPackage) Packages {
	fromByName := indexAPK(from)
	toByName := indexAPK(to)

	names := make([]string, 0, len(fromByName)+len(toByName))
	for n := range fromByName {
		names = append(names, n)
	}
	for n := range toByName {
		if _, seen := fromByName[n]; !seen {
			names = append(names, n)
		}
	}
	slices.Sort(names)

	out := Packages{
		Added:   []PackageEntry{},
		Removed: []PackageEntry{},
		Updated: []PackageDelta{},
	}
	for _, n := range names {
		fe, fok := fromByName[n]
		te, tok := toByName[n]
		switch {
		case !fok && tok:
			out.Added = append(out.Added, PackageEntry{
				Name:      te.Name,
				Version:   te.Version,
				Ecosystem: "apk",
				Purl:      te.Purl,
			})
		case fok && !tok:
			out.Removed = append(out.Removed, PackageEntry{
				Name:      fe.Name,
				Version:   fe.Version,
				Ecosystem: "apk",
				Purl:      fe.Purl,
			})
		case fok && tok && fe.Version != te.Version:
			out.Updated = append(out.Updated, PackageDelta{
				Name:      n,
				From:      fe.Version,
				To:        te.Version,
				Ecosystem: "apk",
				Purl:      te.Purl,
			})
		}
	}
	return out
}

// indexAPK keys apk entries by package name. Chainguard SBOMs list each apk
// twice — once as the canonical install entry (purl qualifier `distro=`) and
// once as an origin/subpackage entry (`origin=`). We prefer the canonical
// entry when both are present so subsequent joins line up with the same
// variant that carries GENERATED_FROM relationships in collectSources.
func indexAPK(entries []sbomPackage) map[string]sbomPackage {
	m := make(map[string]sbomPackage, len(entries))
	for _, e := range entries {
		existing, exists := m[e.Name]
		if exists && isCanonicalAPKPurl(existing.Purl) && !isCanonicalAPKPurl(e.Purl) {
			continue
		}
		m[e.Name] = e
	}
	return m
}

// isCanonicalAPKPurl reports whether the purl is the canonical apk install
// entry (carries the `distro=` qualifier) rather than the origin/subpackage
// duplicate (`origin=`).
func isCanonicalAPKPurl(purl string) bool {
	return strings.Contains(purl, "distro=")
}
