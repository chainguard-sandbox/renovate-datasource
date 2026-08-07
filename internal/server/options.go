package server

import (
	"log/slog"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
)

const (
	defaultCooldown           = 0
	defaultHistoryConcurrency = 16
)

type options struct {
	cooldown           time.Duration
	historyConcurrency int
	apkIndex           *apk.IndexStore
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

// WithAPKIndex attaches the in-memory apk index used by the per-package
// releases endpoint. When nil, /v1/apk/{name}/releases returns 501 Not
// Implemented.
func WithAPKIndex(s *apk.IndexStore) Option {
	return func(o *options) { o.apkIndex = s }
}
