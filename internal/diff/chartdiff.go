package diff

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chart"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diffutil"
)

// ChartRef summarises one side of a chart diff.
type ChartRef struct {
	Digest       string `json:"digest"`
	ChartVersion string `json:"chartVersion,omitempty"`
	AppVersion   string `json:"appVersion,omitempty"`
}

// ChartResponse is the JSON shape returned by the chart diff endpoints.
type ChartResponse struct {
	From              ChartRef         `json:"from"`
	To                ChartRef         `json:"to"`
	ChartYAMLDiff     string           `json:"chartYamlDiff,omitempty"`
	ValuesDiff        string           `json:"valuesDiff,omitempty"`
	Images            *ChartImagesDiff `json:"images,omitempty"`
	FromImagesMissing bool             `json:"fromImagesMissing,omitempty"`
	ToImagesMissing   bool             `json:"toImagesMissing,omitempty"`
}

// ChartFetcher is the subset of *oci.Fetcher Charts needs, kept as
// an interface for test doubles.
type ChartFetcher interface {
	ResolveDigest(ctx context.Context, repo, ref string) (string, error)
	ChartContents(ctx context.Context, repo, ref string) (*chart.Contents, error)
	ChartLock(ctx context.Context, repo, ref string) (*chart.ChartLock, error)
}

// Charts fetches both sides in parallel and returns the assembled
// diff. Images is populated only when both sides carry a chart-lock
// or helm.sh/images annotation; FromImagesMissing / ToImagesMissing
// let the UI explain its absence.
func Charts(ctx context.Context, f ChartFetcher, repo, fromRef, toRef string) (*ChartResponse, error) {
	var from, to chartSide

	fetch := func(ctx context.Context, ref, label string, out *chartSide) error {
		digest, err := f.ResolveDigest(ctx, repo, ref)
		if err != nil {
			return fmt.Errorf("%s resolve: %w", label, err)
		}
		out.digest = digest

		inner, innerCtx := errgroup.WithContext(ctx)
		inner.Go(func() error {
			c, err := f.ChartContents(innerCtx, repo, digest)
			if err != nil {
				return fmt.Errorf("%s contents: %w", label, err)
			}
			out.contents = c
			return nil
		})
		inner.Go(func() error {
			lock, err := f.ChartLock(innerCtx, repo, digest)
			if err != nil {
				return fmt.Errorf("%s chart-lock: %w", label, err)
			}
			out.chartLock = lock
			return nil
		})
		return inner.Wait()
	}

	eg, egCtx := errgroup.WithContext(ctx)
	eg.Go(func() error { return fetch(egCtx, fromRef, "from", &from) })
	eg.Go(func() error { return fetch(egCtx, toRef, "to", &to) })
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	chartDiff, err := diffutil.Unified("Chart.yaml", from.digest, to.digest, from.contents.ChartYAML, to.contents.ChartYAML)
	if err != nil {
		return nil, err
	}
	valuesDiff, err := diffutil.Unified("values.yaml", from.digest, to.digest, from.contents.ValuesYAML, to.contents.ValuesYAML)
	if err != nil {
		return nil, err
	}

	fromImgs := lockedImagesFor(from)
	toImgs := lockedImagesFor(to)

	resp := &ChartResponse{
		From:              chartRefFromSide(from),
		To:                chartRefFromSide(to),
		ChartYAMLDiff:     chartDiff,
		ValuesDiff:        valuesDiff,
		FromImagesMissing: fromImgs == nil,
		ToImagesMissing:   toImgs == nil,
	}
	if fromImgs != nil && toImgs != nil {
		d := diffChartImages(fromImgs, toImgs)
		resp.Images = &d
	}
	return resp, nil
}

// lockedImagesFor prefers the chart-lock attestation (Chainguard's
// canonical form) and falls back to the helm.sh/images annotation
// (iamguarded charts). Returns nil when the chart carries neither,
// so callers can distinguish "no image metadata" (nil, surfaced as
// FromImagesMissing / ToImagesMissing) from "attested empty" (empty
// non-nil slice, rendered as "no image changes").
func lockedImagesFor(d chartSide) []chart.LockedImage {
	if d.chartLock != nil {
		return d.chartLock.Flatten()
	}
	if d.contents != nil && len(d.contents.AnnotationImages) > 0 {
		return d.contents.AnnotationImages
	}
	return nil
}

type chartSide struct {
	digest    string
	contents  *chart.Contents
	chartLock *chart.ChartLock
}

func chartRefFromSide(d chartSide) ChartRef {
	out := ChartRef{Digest: d.digest}
	if d.contents != nil {
		out.ChartVersion = d.contents.ChartVersion
		out.AppVersion = d.contents.AppVersion
	}
	return out
}
