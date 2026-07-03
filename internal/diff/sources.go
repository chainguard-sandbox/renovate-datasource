package diff

import (
	"slices"
)

// sourceAggregate is one source repo as observed in a single SBOM: its host,
// path, version, and the apk packages that point at it via GENERATED_FROM.
// The apk list deduplicates the case where many apks share an upstream
// (e.g. python-3.14 and python-3.14-base both vendor cpython).
type sourceAggregate struct {
	ref  sourceRef
	apks []string
}

// collectSources scans the SBOM for github/gitlab packages that are
// GENERATED_FROM targets of apk packages. The result is keyed by
// "host|path" so the same source repo across multiple apks gets a single
// entry. If two apks happen to reference different versions of the same
// repo (rare/unusual), the first occurrence wins.
func collectSources(s *sbom) map[string]sourceAggregate {
	pkgByID := make(map[string]sbomPackage, len(s.Packages))
	for _, p := range s.Packages {
		pkgByID[p.ID] = p
	}
	// Pre-index GENERATED_FROM edges by their From ID so the per-package
	// loop below does an O(1) map lookup rather than an O(R) scan of all
	// relationships. On Chainguard SBOMs the two are typically in the
	// hundreds and low thousands respectively, so the previous nested
	// scan was O(P*R) per side per diff.
	edgesByFrom := make(map[string][]string, len(s.Packages))
	for _, r := range s.Relationships {
		if r.Type != "GENERATED_FROM" {
			continue
		}
		edgesByFrom[r.From] = append(edgesByFrom[r.From], r.To)
	}

	out := map[string]sourceAggregate{}
	for _, p := range s.Packages {
		if ecosystemFromPurl(p.Purl) != "apk" {
			continue
		}
		for _, toID := range edgesByFrom[p.ID] {
			src, ok := pkgByID[toID]
			if !ok || !isSourcePurl(src.Purl) {
				continue
			}
			sr, ok := parseSourcePurl(src.Purl)
			if !ok {
				continue
			}
			key := sr.host + "|" + sr.path
			agg, exists := out[key]
			if !exists {
				agg.ref = sr
			}
			if !slices.Contains(agg.apks, p.Name) {
				agg.apks = append(agg.apks, p.Name)
			}
			out[key] = agg
		}
	}
	return out
}

// diffSources joins the per-side source aggregates and classifies movements.
// Same scheme as packages — added / removed / updated — and consumers can
// follow the `packages` field on each entry back to the apks involved.
// Sources whose version didn't change are omitted even if their apk set
// shifted; that movement is already visible in the packages diff.
func diffSources(fromMap, toMap map[string]sourceAggregate) Sources {
	out := Sources{
		Added:   []SourceEntry{},
		Removed: []SourceEntry{},
		Updated: []SourceDelta{},
	}

	keys := make(map[string]struct{}, len(fromMap)+len(toMap))
	for k := range fromMap {
		keys[k] = struct{}{}
	}
	for k := range toMap {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	slices.Sort(sorted)

	for _, k := range sorted {
		from, fok := fromMap[k]
		to, tok := toMap[k]
		switch {
		case !fok && tok:
			slices.Sort(to.apks)
			out.Added = append(out.Added, SourceEntry{
				Name:     to.ref.path,
				Host:     to.ref.host,
				Version:  to.ref.version,
				URL:      to.ref.sourceURL(),
				Packages: to.apks,
			})
		case fok && !tok:
			slices.Sort(from.apks)
			out.Removed = append(out.Removed, SourceEntry{
				Name:     from.ref.path,
				Host:     from.ref.host,
				Version:  from.ref.version,
				URL:      from.ref.sourceURL(),
				Packages: from.apks,
			})
		case fok && tok && from.ref.version != to.ref.version:
			slices.Sort(to.apks)
			out.Updated = append(out.Updated, SourceDelta{
				Name:       to.ref.path,
				Host:       to.ref.host,
				From:       from.ref.version,
				To:         to.ref.version,
				FromURL:    from.ref.sourceURL(),
				ToURL:      to.ref.sourceURL(),
				CompareURL: from.ref.compareURLTo(to.ref.version),
				Packages:   to.apks,
			})
		}
	}
	return out
}
