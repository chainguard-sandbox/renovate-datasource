package diff

import (
	"context"
	"strings"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chart"
)

type stubChartFetcher struct {
	digests    map[string]string
	contents   map[string]*chart.Contents
	chartLocks map[string]*chart.ChartLock
}

func (s stubChartFetcher) ResolveDigest(_ context.Context, _, ref string) (string, error) {
	return s.digests[ref], nil
}
func (s stubChartFetcher) ChartContents(_ context.Context, _, digest string) (*chart.Contents, error) {
	return s.contents[digest], nil
}
func (s stubChartFetcher) ChartLock(_ context.Context, _, digest string) (*chart.ChartLock, error) {
	return s.chartLocks[digest], nil
}

func TestCharts(t *testing.T) {
	from := &chart.Contents{
		ChartYAML:    []byte("apiVersion: v2\nname: foo\nversion: 1.0.0\nappVersion: 0.5.0\n"),
		ValuesYAML:   []byte("replicaCount: 1\n"),
		ChartVersion: "1.0.0",
		AppVersion:   "0.5.0",
	}
	to := &chart.Contents{
		ChartYAML:    []byte("apiVersion: v2\nname: foo\nversion: 1.1.0\nappVersion: 0.6.0\n"),
		ValuesYAML:   []byte("replicaCount: 2\n"),
		ChartVersion: "1.1.0",
		AppVersion:   "0.6.0",
	}

	f := stubChartFetcher{
		digests: map[string]string{
			"1.0.0": "sha256:aaa",
			"1.1.0": "sha256:bbb",
		},
		contents: map[string]*chart.Contents{
			"sha256:aaa": from,
			"sha256:bbb": to,
		},
	}

	resp, err := Charts(context.Background(), f, "charts/foo", "1.0.0", "1.1.0")
	if err != nil {
		t.Fatalf("Charts: %v", err)
	}
	if resp.From.Digest != "sha256:aaa" || resp.To.Digest != "sha256:bbb" {
		t.Errorf("digests = (%q, %q); want (sha256:aaa, sha256:bbb)", resp.From.Digest, resp.To.Digest)
	}
	if resp.From.ChartVersion != "1.0.0" || resp.To.ChartVersion != "1.1.0" {
		t.Errorf("chart versions = (%q, %q); want (1.0.0, 1.1.0)", resp.From.ChartVersion, resp.To.ChartVersion)
	}
	if resp.From.AppVersion != "0.5.0" || resp.To.AppVersion != "0.6.0" {
		t.Errorf("app versions = (%q, %q); want (0.5.0, 0.6.0)", resp.From.AppVersion, resp.To.AppVersion)
	}
	if !strings.Contains(resp.ChartYAMLDiff, "-version: 1.0.0") || !strings.Contains(resp.ChartYAMLDiff, "+version: 1.1.0") {
		t.Errorf("ChartYAMLDiff missing expected lines:\n%s", resp.ChartYAMLDiff)
	}
	if !strings.Contains(resp.ValuesDiff, "-replicaCount: 1") || !strings.Contains(resp.ValuesDiff, "+replicaCount: 2") {
		t.Errorf("ValuesDiff missing expected lines:\n%s", resp.ValuesDiff)
	}
}

func TestCharts_ImagesFromChartLock(t *testing.T) {
	lockA := &chart.ChartLock{Images: chart.ImagesSection{
		Refs: map[string]chart.LockedRef{
			"prometheus": {Digest: "sha256:aaa", RepoName: "prometheus", Tag: "latest"},
		},
		Template: chart.ImageTemplate{Images: map[string]chart.ImageTemplateEntry{
			"prometheus": {Requirement: "required"},
		}},
	}}
	lockB := &chart.ChartLock{Images: chart.ImagesSection{
		Refs: map[string]chart.LockedRef{
			"prometheus": {Digest: "sha256:bbb", RepoName: "prometheus", Tag: "latest"},
		},
		Template: chart.ImageTemplate{Images: map[string]chart.ImageTemplateEntry{
			"prometheus": {Requirement: "required"},
		}},
	}}
	c := &chart.Contents{ChartYAML: []byte("version: 1.0.0\n"), ValuesYAML: []byte("k: v\n"), ChartVersion: "1.0.0"}
	f := stubChartFetcher{
		digests:    map[string]string{"1.0.0": "sha256:a", "1.1.0": "sha256:b"},
		contents:   map[string]*chart.Contents{"sha256:a": c, "sha256:b": c},
		chartLocks: map[string]*chart.ChartLock{"sha256:a": lockA, "sha256:b": lockB},
	}

	resp, err := Charts(context.Background(), f, "charts/foo", "1.0.0", "1.1.0")
	if err != nil {
		t.Fatalf("Charts: %v", err)
	}
	if resp.Images == nil {
		t.Fatalf("Images should be populated when both sides carry a chart-lock")
	}
	if resp.FromImagesMissing || resp.ToImagesMissing {
		t.Errorf("chart-lock missing flags = (%v, %v); want both false", resp.FromImagesMissing, resp.ToImagesMissing)
	}
	if len(resp.Images.Updated) != 1 || resp.Images.Updated[0].FromDigest != "sha256:aaa" || resp.Images.Updated[0].ToDigest != "sha256:bbb" {
		t.Errorf("Updated = %+v, want one prometheus digest bump", resp.Images.Updated)
	}
}

func TestCharts_ImagesSkippedWhenLockMissing(t *testing.T) {
	c := &chart.Contents{ChartYAML: []byte("v: 1\n"), ValuesYAML: []byte("v: 1\n")}
	f := stubChartFetcher{
		digests:  map[string]string{"1.0.0": "sha256:a", "1.1.0": "sha256:b"},
		contents: map[string]*chart.Contents{"sha256:a": c, "sha256:b": c},
		// only from side has a chart-lock
		chartLocks: map[string]*chart.ChartLock{"sha256:a": {}},
	}
	resp, err := Charts(context.Background(), f, "charts/foo", "1.0.0", "1.1.0")
	if err != nil {
		t.Fatalf("Charts: %v", err)
	}
	if resp.Images != nil {
		t.Errorf("Images should be nil when one side has no chart-lock; got %+v", resp.Images)
	}
	if !resp.ToImagesMissing || resp.FromImagesMissing {
		t.Errorf("missing flags = (from=%v, to=%v); want (false, true)", resp.FromImagesMissing, resp.ToImagesMissing)
	}
}

func TestCharts_Identical(t *testing.T) {
	// Same contents on both sides — diff fields come back empty.
	same := &chart.Contents{
		ChartYAML:    []byte("apiVersion: v2\nname: foo\nversion: 1.0.0\n"),
		ValuesYAML:   []byte("replicaCount: 1\n"),
		ChartVersion: "1.0.0",
	}
	f := stubChartFetcher{
		digests: map[string]string{"1.0.0": "sha256:aaa"},
		contents: map[string]*chart.Contents{
			"sha256:aaa": same,
		},
	}
	resp, err := Charts(context.Background(), f, "charts/foo", "1.0.0", "1.0.0")
	if err != nil {
		t.Fatalf("Charts: %v", err)
	}
	if resp.ChartYAMLDiff != "" || resp.ValuesDiff != "" {
		t.Errorf("diffs should be empty for identical contents; got chart=%q values=%q", resp.ChartYAMLDiff, resp.ValuesDiff)
	}
}
