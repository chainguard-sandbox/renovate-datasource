package server

import (
	"log/slog"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

const defaultCooldown = 0

type options struct {
	cooldown   time.Duration
	repoDatasource datasource.Datasource
	apkDatasource  datasource.Datasource
	log        *slog.Logger
	now        func() time.Time
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

// WithLogger sets the structured logger. Default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.log = l }
}

// WithRepoDatasource attaches the source that serves /v1/repo/{path}/releases.
// Omitting it leaves the route unregistered (404).
func WithRepoDatasource(ds datasource.Datasource) Option {
	return func(o *options) { o.repoDatasource = ds }
}

// WithAPKDatasource attaches the source that serves /v1/apk/{name}/releases.
// Omitting it leaves the route unregistered (404).
func WithAPKDatasource(ds datasource.Datasource) Option {
	return func(o *options) { o.apkDatasource = ds }
}
