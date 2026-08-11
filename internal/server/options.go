package server

import (
	"log/slog"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

const defaultMinimumReleaseAge = 0

type options struct {
	minimumReleaseAge time.Duration
	datasources       map[string]datasource.Datasource
	log               *slog.Logger
	now               func() time.Time
}

// Option configures New.
type Option func(*options)

// WithMinimumReleaseAge sets the default window applied when a request
// doesn't provide its own ?minimumReleaseAge=<dur> query parameter.
// Default is 0 (disabled), which passes releases through unfiltered.
func WithMinimumReleaseAge(d time.Duration) Option {
	return func(o *options) { o.minimumReleaseAge = d }
}

// WithLogger sets the structured logger. Default is slog.Default().
func WithLogger(l *slog.Logger) Option {
	return func(o *options) { o.log = l }
}

// WithDatasource attaches ds under name, serving it at
// /v1/<name>/{package}/releases. Omit to leave the route unregistered.
// The last registration wins if name is reused.
func WithDatasource(name string, ds datasource.Datasource) Option {
	return func(o *options) {
		if o.datasources == nil {
			o.datasources = map[string]datasource.Datasource{}
		}
		o.datasources[name] = ds
	}
}
