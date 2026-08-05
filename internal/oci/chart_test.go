package oci

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"reflect"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chart"
)

func TestExtractTarball(t *testing.T) {
	files := map[string][]byte{
		"kube-prometheus-stack/Chart.yaml":                []byte("apiVersion: v2\nname: kube-prometheus-stack\nversion: 87.4.0\nappVersion: 0.86.1\n"),
		"kube-prometheus-stack/values.yaml":               []byte("alertmanager:\n  enabled: true\n"),
		"kube-prometheus-stack/templates/deployment.yaml": []byte("nested — must be ignored"),
		"kube-prometheus-stack/charts/grafana/Chart.yaml": []byte("nested — must be ignored"),
	}
	tgz := makeTGZ(t, files)

	c, err := extractTarball(bytes.NewReader(tgz))
	if err != nil {
		t.Fatalf("extractTarball: %v", err)
	}
	if got := string(c.ChartYAML); got != string(files["kube-prometheus-stack/Chart.yaml"]) {
		t.Errorf("ChartYAML = %q, want %q", got, files["kube-prometheus-stack/Chart.yaml"])
	}
	if got := string(c.ValuesYAML); got != string(files["kube-prometheus-stack/values.yaml"]) {
		t.Errorf("ValuesYAML = %q, want %q", got, files["kube-prometheus-stack/values.yaml"])
	}
	if c.ChartVersion != "87.4.0" {
		t.Errorf("ChartVersion = %q, want 87.4.0", c.ChartVersion)
	}
	if c.AppVersion != "0.86.1" {
		t.Errorf("AppVersion = %q, want 0.86.1", c.AppVersion)
	}
}

func TestExtractTarball_MissingValues(t *testing.T) {
	// Chart.yaml present, no values.yaml — extract should succeed with
	// empty ValuesYAML rather than error.
	files := map[string][]byte{
		"foo/Chart.yaml": []byte("apiVersion: v2\nname: foo\nversion: 1.0.0\n"),
	}
	c, err := extractTarball(bytes.NewReader(makeTGZ(t, files)))
	if err != nil {
		t.Fatalf("extractTarball: %v", err)
	}
	if len(c.ValuesYAML) != 0 {
		t.Errorf("ValuesYAML = %q, want empty", c.ValuesYAML)
	}
	if c.ChartVersion != "1.0.0" {
		t.Errorf("ChartVersion = %q, want 1.0.0", c.ChartVersion)
	}
}

func TestExtractTarball_PerFileCap(t *testing.T) {
	// A single Chart.yaml that exceeds maxChartFileSize should be
	// truncated to the cap rather than swallowing the whole file.
	oversized := bytes.Repeat([]byte("x"), maxChartFileSize+1024)
	files := map[string][]byte{
		"big/Chart.yaml": oversized,
	}
	c, err := extractTarball(bytes.NewReader(makeTGZ(t, files)))
	if err != nil {
		t.Fatalf("extractTarball: %v", err)
	}
	if got := len(c.ChartYAML); got != maxChartFileSize {
		t.Errorf("ChartYAML length = %d; want %d (maxChartFileSize)", got, maxChartFileSize)
	}
}

func TestExtractTarball_TarballCap(t *testing.T) {
	// Total decompressed stream over maxChartLayerSize should
	// terminate cleanly (tar reader hits EOF at the cap) without
	// hanging or erroring.
	junk := bytes.Repeat([]byte("x"), maxChartLayerSize+1024)
	files := map[string][]byte{
		"big/Chart.yaml":    []byte("apiVersion: v2\nname: big\nversion: 1.0.0\n"),
		"big/junk-payload":  junk,
	}
	c, err := extractTarball(bytes.NewReader(makeTGZ(t, files)))
	// tar.Next may return io.ErrUnexpectedEOF when LimitReader
	// truncates mid-record; both a clean EOF and that error are
	// acceptable outcomes — the key property is we don't OOM.
	if err != nil {
		t.Logf("extractTarball on oversized layer returned %v (acceptable)", err)
		return
	}
	// map iteration order is nondeterministic; only assert the
	// per-file cap on whatever Chart.yaml we managed to read.
	if len(c.ChartYAML) > maxChartFileSize {
		t.Errorf("ChartYAML length = %d exceeds cap %d", len(c.ChartYAML), maxChartFileSize)
	}
}

func TestParseChartMeta_Malformed(t *testing.T) {
	// Malformed YAML yields empty strings, not a panic.
	v, av := parseChartMeta([]byte("not: [ yaml"))
	if v != "" || av != "" {
		t.Errorf("parseChartMeta on malformed input = (%q, %q), want empty", v, av)
	}
}

func TestSplitImageRef(t *testing.T) {
	tests := []struct {
		name, org, ref    string
		repo, tag, digest string
		ok                bool
	}{
		{"tagged", "chainguard-private", "cgr.dev/chainguard-private/nginx:1.31.3", "nginx", "1.31.3", "", true},
		{"tagged and digested", "chainguard-private", "cgr.dev/chainguard-private/nginx:1.31.3@sha256:aaa", "nginx", "1.31.3", "sha256:aaa", true},
		{"digest only", "chainguard-private", "cgr.dev/chainguard-private/nginx@sha256:aaa", "nginx", "", "sha256:aaa", true},
		{"no tag or digest", "chainguard-private", "cgr.dev/chainguard-private/nginx", "nginx", "", "", true},
		{"multi-segment repo", "chainguard-private", "cgr.dev/chainguard-private/extras/nginx:1", "extras/nginx", "1", "", true},
		{"different org", "chainguard-private", "cgr.dev/other-org/nginx:1", "", "", "", false},
		{"non-cgr registry", "chainguard-private", "docker.io/nginx:1", "", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, ta, d, ok := splitImageRef(tc.org, tc.ref)
			if r != tc.repo || ta != tc.tag || d != tc.digest || ok != tc.ok {
				t.Errorf("splitImageRef(%q, %q) = (%q, %q, %q, %v); want (%q, %q, %q, %v)",
					tc.org, tc.ref, r, ta, d, ok, tc.repo, tc.tag, tc.digest, tc.ok)
			}
		})
	}
}

func TestFindChartLayer_Missing(t *testing.T) {
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	if _, err := findChartLayer(img); !errors.Is(err, ErrChartLayerMissing) {
		t.Errorf("findChartLayer on non-chart image: err = %v, want ErrChartLayerMissing", err)
	}
}

func TestParseHelmShImagesAnnotation(t *testing.T) {
	yaml := `- "image": "cgr.dev/chainguard-private/nginx-iamguarded:1.31.3"
  "name": "nginx-iamguarded"
- "image": "cgr.dev/chainguard-private/nginx-prometheus-exporter-iamguarded:1.5.1"
  "name": "nginx-prometheus-exporter-iamguarded"
- "image": "cgr.dev/other-org/skipped:1"
  "name": "skipped"
`
	got := parseHelmShImagesAnnotation("chainguard-private", yaml)
	want := []chart.LockedImage{
		{LogicalName: "nginx-iamguarded", RepoName: "nginx-iamguarded", Tag: "1.31.3"},
		{LogicalName: "nginx-prometheus-exporter-iamguarded", RepoName: "nginx-prometheus-exporter-iamguarded", Tag: "1.5.1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestParseHelmShImagesAnnotation_WithDigests(t *testing.T) {
	yaml := `- "image": "cgr.dev/chainguard-private/nginx@sha256:aaa"
  "name": "digest-only"
- "image": "cgr.dev/chainguard-private/nginx:1.31.3@sha256:bbb"
  "name": "tag-and-digest"
`
	got := parseHelmShImagesAnnotation("chainguard-private", yaml)
	want := []chart.LockedImage{
		{LogicalName: "digest-only", RepoName: "nginx", Digest: "sha256:aaa"},
		{LogicalName: "tag-and-digest", RepoName: "nginx", Digest: "sha256:bbb", Tag: "1.31.3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v; want %+v", got, want)
	}
}

func TestParseHelmShImagesAnnotation_Malformed(t *testing.T) {
	if got := parseHelmShImagesAnnotation("x", "not: [ yaml"); got != nil {
		t.Errorf("malformed input should return nil, got %+v", got)
	}
}

func makeTGZ(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader: %v", err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("Write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}
