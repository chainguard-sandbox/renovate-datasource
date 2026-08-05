package server

import (
	"log/slog"
	"time"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/diff"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/grype"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/oci"
)

const (
	defaultCooldown           = 0
	defaultHistoryConcurrency = 16
)

type options struct {
	cooldown           time.Duration
	historyConcurrency int
	orgName            string
	apk                diff.APKFetcher
	apkIndex           *apk.IndexStore
	chart              *oci.Fetcher
	grype              *grype.DB
	log                *slog.Logger
	now                func() time.Time
}

// Option configures New.
type Option func(*options)

// WithCooldown sets the default cooldown window applied when a request
// doesn't provide its own ?cooldown=<dur> query parameter. Default is 0
// (disabled), in which case /v1/repo/{repo}/releases serves the upstream
// tag list as-is, skipping the per-tag history rewind.
func WithCooldown(d time.Duration) Option {
	return func(o *options) { o.cooldown = d }
}

// WithHistoryConcurrency caps the parallel ListTagHistory calls per request.
// Default is 16.
func WithHistoryConcurrency(n int) Option {
	return func(o *options) { o.historyConcurrency = n }
}

// WithLogger sets the structured logger. Default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.log = l }
}

// WithOrgName sets the Chainguard org name used to build the "view in
// console" link on the diff page. When empty, the link is omitted.
func WithOrgName(name string) Option {
	return func(o *options) { o.orgName = name }
}

// WithAPKFetcher attaches the apk Fetcher used by the per-apk endpoints
// to produce diffs and single-version snapshots (.melange.yaml,
// .PKGINFO, and the parsed source-pipeline entries). When nil, the
// per-apk diff and version endpoints return 501 Not Implemented.
func WithAPKFetcher(f diff.APKFetcher) Option {
	return func(o *options) { o.apk = f }
}

// WithAPKIndex attaches the in-memory apk index used by the per-package
// releases endpoint. When nil, /v1/apk/{name}/releases returns 501 Not
// Implemented.
func WithAPKIndex(s *apk.IndexStore) Option {
	return func(o *options) { o.apkIndex = s }
}

// WithGrypeScanner attaches the vulnerability scanner used on the
// image diff page. Unset omits the Vulnerabilities section.
func WithGrypeScanner(s *grype.DB) Option {
	return func(o *options) { o.grype = s }
}

// WithChartFetcher attaches the fetcher used by the chart diff
// endpoints. Unset returns 501 from those endpoints.
func WithChartFetcher(f *oci.Fetcher) Option {
	return func(o *options) { o.chart = f }
}
