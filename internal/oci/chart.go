package oci

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"gopkg.in/yaml.v3"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chart"
)

// helmChartLayerMediaType is the OCI media type Helm uses for the
// tar+gzip layer holding a chart's files.
const helmChartLayerMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"

// ErrChartLayerMissing is returned by ChartContents when the manifest
// carries no helm-chart layer.
var ErrChartLayerMissing = errors.New("no helm chart layer in manifest")

// ChartLock returns the parsed chart-lock predicate attached to
// repo @ ref, or (nil, nil) if none is present. ref should be a
// per-manifest digest from ResolveDigest.
func (f *Fetcher) ChartLock(ctx context.Context, repo, ref string) (*chart.ChartLock, error) {
	stmts, err := f.AttestationStatements(ctx, repo, ref)
	if err != nil {
		return nil, err
	}
	for _, s := range stmts {
		if s.PredicateType != chart.ChartLockPredicateType {
			continue
		}
		var lock chart.ChartLock
		if err := json.Unmarshal(s.Predicate, &lock); err != nil {
			return nil, fmt.Errorf("parsing chart-lock predicate: %w", err)
		}
		return &lock, nil
	}
	return nil, nil
}

// ChartContents fetches the chart layer for repo @ ref and returns
// its parsed contents. ref should be a per-manifest digest from
// ResolveDigest. Returns ErrChartLayerMissing when the manifest
// isn't a helm chart artifact.
func (f *Fetcher) ChartContents(ctx context.Context, repo, ref string) (*chart.Contents, error) {
	r, err := f.refFor(repo, ref)
	if err != nil {
		return nil, err
	}
	img, err := remote.Image(r, f.remoteOpts(ctx)...)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", r, err)
	}
	manifest, err := img.Manifest()
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", r, err)
	}
	layer, err := findChartLayer(img)
	if err != nil {
		return nil, fmt.Errorf("locating chart layer in %s: %w", r, err)
	}
	rc, err := layer.Compressed()
	if err != nil {
		return nil, fmt.Errorf("opening chart layer %s: %w", r, err)
	}
	defer rc.Close()

	c, err := extractTarball(rc)
	if err != nil {
		return nil, err
	}
	if v, ok := manifest.Annotations[chart.HelmShImagesAnnotation]; ok {
		c.AnnotationImages = parseHelmShImagesAnnotation(f.orgName, v)
	}
	return c, nil
}

// findChartLayer returns the first layer bearing the helm chart
// media type; helm charts we've seen carry exactly one.
func findChartLayer(img v1.Image) (v1.Layer, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}
	for _, l := range layers {
		mt, err := l.MediaType()
		if err != nil {
			return nil, err
		}
		if string(mt) == helmChartLayerMediaType {
			return l, nil
		}
	}
	return nil, ErrChartLayerMissing
}

// maxChartLayerSize caps the decompressed chart tarball size to guard
// against gunzip bombs / malformed layers. Real charts we've seen are
// well under 10 MiB decompressed.
const maxChartLayerSize = 64 << 20 // 64 MiB

// maxChartFileSize caps a single Chart.yaml / values.yaml read.
const maxChartFileSize = 4 << 20 // 4 MiB

// extractTarball reads a gzipped helm chart tarball and picks out
// Chart.yaml and values.yaml. Helm packs the chart under a single
// top-level directory named for the chart (e.g.
// `kube-prometheus-stack/Chart.yaml`), so we match by basename.
func extractTarball(r io.Reader) (*chart.Contents, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gz.Close()

	out := &chart.Contents{}
	tr := tar.NewReader(io.LimitReader(gz, maxChartLayerSize))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading chart tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		clean := path.Clean(hdr.Name)
		// Two-segment paths only: skip nested files like
		// charts/foo/Chart.yaml (a subchart's own Chart.yaml).
		segs := strings.Split(clean, "/")
		if len(segs) != 2 {
			continue
		}
		var dst *[]byte
		switch segs[1] {
		case "Chart.yaml":
			dst = &out.ChartYAML
		case "values.yaml":
			dst = &out.ValuesYAML
		default:
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxChartFileSize))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", clean, err)
		}
		*dst = b
	}

	if len(out.ChartYAML) > 0 {
		out.ChartVersion, out.AppVersion = parseChartMeta(out.ChartYAML)
	}
	return out, nil
}

// parseChartMeta extracts version and appVersion from Chart.yaml
// bytes. Missing or malformed input returns empty strings rather
// than failing so the diff still renders.
func parseChartMeta(chartYAML []byte) (version, appVersion string) {
	var meta struct {
		Version    string `yaml:"version"`
		AppVersion string `yaml:"appVersion"`
	}
	if err := yaml.Unmarshal(chartYAML, &meta); err != nil {
		return "", ""
	}
	return meta.Version, meta.AppVersion
}

// parseHelmShImagesAnnotation parses the helm.sh/images annotation
// YAML: [{image: cgr.dev/<org>/<repo>[:<tag>][@<digest>], name: <logical>}].
// Refs outside orgName are dropped since the diff page can't
// cross-link to them.
func parseHelmShImagesAnnotation(orgName, yamlStr string) []chart.LockedImage {
	var entries []struct {
		Image string `yaml:"image"`
		Name  string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(yamlStr), &entries); err != nil {
		return nil
	}
	var out []chart.LockedImage
	for _, e := range entries {
		repo, tag, digest, ok := splitImageRef(orgName, e.Image)
		if !ok {
			slog.Debug("helm.sh/images: dropping ref outside org", "org", orgName, "image", e.Image, "name", e.Name)
			continue
		}
		out = append(out, chart.LockedImage{
			LogicalName: e.Name,
			RepoName:    repo,
			Digest:      digest,
			Tag:         tag,
		})
	}
	return out
}

// splitImageRef splits `cgr.dev/<org>/<repo>[:<tag>][@<digest>]`
// into its components. Returns ok=false when the ref isn't under
// cgr.dev/<orgName>/.
func splitImageRef(orgName, ref string) (repo, tag, digest string, ok bool) {
	prefix := "cgr.dev/" + orgName + "/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", "", false
	}
	body := ref[len(prefix):]
	if idx := strings.Index(body, "@"); idx != -1 {
		digest = body[idx+1:]
		body = body[:idx]
	}
	if idx := strings.LastIndex(body, ":"); idx != -1 {
		return body[:idx], body[idx+1:], digest, true
	}
	return body, "", digest, true
}
