package diff

import (
	"sort"
	"strings"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chart"
)

// ChartImagesDiff buckets a chart's referenced images between two
// revisions.
type ChartImagesDiff struct {
	Added   []chart.LockedImage `json:"added,omitempty"`
	Removed []chart.LockedImage `json:"removed,omitempty"`
	Updated []ChartUpdatedImage `json:"updated,omitempty"`
}

// ChartUpdatedImage is one image slot (same subchart path + logical
// name) whose digest, repo, or tag changed between revisions.
type ChartUpdatedImage struct {
	Path         []string `json:"path,omitempty"`
	LogicalName  string   `json:"logicalName"`
	Requirement  string   `json:"requirement,omitempty"`
	FromRepoName string   `json:"fromRepoName"`
	ToRepoName   string   `json:"toRepoName"`
	FromDigest   string   `json:"fromDigest"`
	ToDigest     string   `json:"toDigest"`
	FromTag      string   `json:"fromTag,omitempty"`
	ToTag        string   `json:"toTag,omitempty"`
}

// diffChartImages compares two flat image lists by (subchart path,
// logical name). Sorted for stable output.
func diffChartImages(from, to []chart.LockedImage) ChartImagesDiff {
	fromByKey := indexChartImages(from)
	toByKey := indexChartImages(to)

	var out ChartImagesDiff
	for k, f := range fromByKey {
		t, ok := toByKey[k]
		if !ok {
			out.Removed = append(out.Removed, f)
			continue
		}
		// Tag is part of the identity for annotation-sourced images
		// (which have no digest); ignoring it there would misread a
		// version bump as unchanged.
		if f.Digest == t.Digest && f.RepoName == t.RepoName && f.Tag == t.Tag {
			continue
		}
		out.Updated = append(out.Updated, ChartUpdatedImage{
			Path:         t.Path,
			LogicalName:  t.LogicalName,
			Requirement:  t.Requirement,
			FromRepoName: f.RepoName,
			ToRepoName:   t.RepoName,
			FromDigest:   f.Digest,
			ToDigest:     t.Digest,
			FromTag:      f.Tag,
			ToTag:        t.Tag,
		})
	}
	for k, t := range toByKey {
		if _, ok := fromByKey[k]; !ok {
			out.Added = append(out.Added, t)
		}
	}

	sortLockedImages(out.Added)
	sortLockedImages(out.Removed)
	sortChartUpdatedImages(out.Updated)
	return out
}

func indexChartImages(imgs []chart.LockedImage) map[string]chart.LockedImage {
	out := make(map[string]chart.LockedImage, len(imgs))
	for _, i := range imgs {
		out[chartImageKey(i.Path, i.LogicalName)] = i
	}
	return out
}

func chartImageKey(path []string, name string) string {
	return strings.Join(path, "/") + "\x00" + name
}

func sortLockedImages(imgs []chart.LockedImage) {
	sort.Slice(imgs, func(i, j int) bool {
		pi, pj := strings.Join(imgs[i].Path, "/"), strings.Join(imgs[j].Path, "/")
		if pi != pj {
			return pi < pj
		}
		return imgs[i].LogicalName < imgs[j].LogicalName
	})
}

func sortChartUpdatedImages(imgs []ChartUpdatedImage) {
	sort.Slice(imgs, func(i, j int) bool {
		pi, pj := strings.Join(imgs[i].Path, "/"), strings.Join(imgs[j].Path, "/")
		if pi != pj {
			return pi < pj
		}
		return imgs[i].LogicalName < imgs[j].LogicalName
	})
}
