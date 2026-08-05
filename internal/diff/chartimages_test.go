package diff

import (
	"reflect"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chart"
)

func TestDiffChartImages(t *testing.T) {
	img := func(name, repo, digest string) chart.LockedImage {
		return chart.LockedImage{LogicalName: name, RepoName: repo, Digest: digest, Tag: "latest", Requirement: "required"}
	}
	subImg := func(sub, name, repo, digest string) chart.LockedImage {
		return chart.LockedImage{Path: []string{sub}, LogicalName: name, RepoName: repo, Digest: digest, Tag: "latest", Requirement: "required"}
	}

	tests := []struct {
		name     string
		from, to []chart.LockedImage
		want     ChartImagesDiff
	}{
		{
			name: "digest change on same slot → updated",
			from: []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")},
			to:   []chart.LockedImage{img("prometheus", "prometheus", "sha256:bbb")},
			want: ChartImagesDiff{
				Updated: []ChartUpdatedImage{{
					LogicalName:  "prometheus",
					Requirement:  "required",
					FromRepoName: "prometheus", ToRepoName: "prometheus",
					FromDigest: "sha256:aaa", ToDigest: "sha256:bbb",
					FromTag: "latest", ToTag: "latest",
				}},
			},
		},
		{
			name: "same digest and repo → unchanged (nothing surfaced)",
			from: []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")},
			to:   []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")},
			want: ChartImagesDiff{},
		},
		{
			name: "new slot on the to side → added",
			from: nil,
			to:   []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")},
			want: ChartImagesDiff{Added: []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")}},
		},
		{
			name: "slot only on the from side → removed",
			from: []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")},
			to:   nil,
			want: ChartImagesDiff{Removed: []chart.LockedImage{img("prometheus", "prometheus", "sha256:aaa")}},
		},
		{
			name: "same logical name in different subcharts stay separate",
			from: []chart.LockedImage{
				img("busybox", "busybox", "sha256:1"),
				subImg("grafana", "busybox", "busybox", "sha256:2"),
			},
			to: []chart.LockedImage{
				img("busybox", "busybox", "sha256:1"),
				subImg("grafana", "busybox", "busybox", "sha256:3"),
			},
			want: ChartImagesDiff{
				Updated: []ChartUpdatedImage{{
					Path:         []string{"grafana"},
					LogicalName:  "busybox",
					Requirement:  "required",
					FromRepoName: "busybox", ToRepoName: "busybox",
					FromDigest: "sha256:2", ToDigest: "sha256:3",
					FromTag: "latest", ToTag: "latest",
				}},
			},
		},
		{
			name: "repo rename on same slot → updated (repo names differ)",
			from: []chart.LockedImage{img("agent", "old-agent", "sha256:aaa")},
			to:   []chart.LockedImage{img("agent", "new-agent", "sha256:aaa")},
			want: ChartImagesDiff{
				Updated: []ChartUpdatedImage{{
					LogicalName:  "agent",
					Requirement:  "required",
					FromRepoName: "old-agent", ToRepoName: "new-agent",
					FromDigest: "sha256:aaa", ToDigest: "sha256:aaa",
					FromTag: "latest", ToTag: "latest",
				}},
			},
		},
		{
			// Annotation-sourced images carry no digest — the tag
			// alone must be enough to identify a version bump.
			name: "tag change with no digest → updated",
			from: []chart.LockedImage{{LogicalName: "nginx", RepoName: "nginx", Tag: "1.29.5", Requirement: "required"}},
			to:   []chart.LockedImage{{LogicalName: "nginx", RepoName: "nginx", Tag: "1.31.3", Requirement: "required"}},
			want: ChartImagesDiff{
				Updated: []ChartUpdatedImage{{
					LogicalName:  "nginx",
					Requirement:  "required",
					FromRepoName: "nginx", ToRepoName: "nginx",
					FromTag: "1.29.5", ToTag: "1.31.3",
				}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := diffChartImages(tc.from, tc.to)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("diff mismatch\n got:  %+v\n want: %+v", got, tc.want)
			}
		})
	}
}
