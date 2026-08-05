package chart

// ChartLockPredicateType is the in-toto predicateType Chainguard uses
// for the chart-lock attestation attached to chart artifacts.
const ChartLockPredicateType = "https://chainguard.dev/attestation/chart-lock/v1"

// ChartLock is the decoded chart-lock predicate.
type ChartLock struct {
	Chart  ChartRef      `json:"chart"`
	Images ImagesSection `json:"images"`
}

// ChartRef identifies the chart artifact the attestation covers.
type ChartRef struct {
	Package string `json:"package"`
	Ref     string `json:"ref"`
}

// ImagesSection is one node in the chart-lock's image tree: Refs
// carry the pinned digests, Template carries per-image metadata,
// Subcharts recurses.
type ImagesSection struct {
	Refs      map[string]LockedRef     `json:"refs,omitempty"`
	Template  ImageTemplate            `json:"template,omitempty"`
	Subcharts map[string]ImagesSection `json:"subcharts,omitempty"`
}

// LockedRef is a chart-lock image reference. RepoName is the leaf
// repo name under the same org as the chart.
type LockedRef struct {
	Digest   string `json:"digest"`
	RepoName string `json:"repoName"`
	Tag      string `json:"tag"`
}

type ImageTemplate struct {
	Images map[string]ImageTemplateEntry `json:"images,omitempty"`
}

// ImageTemplateEntry is indexed by logical name in ImagesSection.Refs.
type ImageTemplateEntry struct {
	Requirement string         `json:"requirement,omitempty"` // "required" | "optional"
	Values      map[string]any `json:"values,omitempty"`
}

// LockedImage is the flattened view of one image entry produced by
// ChartLock.Flatten. Path + LogicalName locate it in the subchart
// tree; RepoName + Digest + Tag identify the image.
type LockedImage struct {
	Path        []string `json:"path,omitempty"`
	LogicalName string   `json:"logicalName"`
	RepoName    string   `json:"repoName"`
	Digest      string   `json:"digest"`
	Tag         string   `json:"tag,omitempty"`
	Requirement string   `json:"requirement,omitempty"`
}

// Flatten walks a ChartLock's image tree and returns one LockedImage
// per (subchart path, logical name) entry. Returns nil only when the
// receiver is nil — a non-nil ChartLock with no images returns an
// empty slice, so callers can distinguish "attested to be empty"
// from "no attestation".
func (l *ChartLock) Flatten() []LockedImage {
	if l == nil {
		return nil
	}
	out := []LockedImage{}
	flattenSection(l.Images, nil, &out)
	return out
}

func flattenSection(s ImagesSection, path []string, out *[]LockedImage) {
	for name, ref := range s.Refs {
		*out = append(*out, LockedImage{
			Path:        append([]string(nil), path...),
			LogicalName: name,
			RepoName:    ref.RepoName,
			Digest:      ref.Digest,
			Tag:         ref.Tag,
			Requirement: s.Template.Images[name].Requirement,
		})
	}
	for name, sub := range s.Subcharts {
		flattenSection(sub, append(path, name), out)
	}
}
