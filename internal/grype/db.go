package grype

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/anchore/clio"
	"github.com/anchore/grype/grype"
	"github.com/anchore/grype/grype/db/v6/distribution"
	"github.com/anchore/grype/grype/db/v6/installation"
	"github.com/anchore/grype/grype/match"
	"github.com/anchore/grype/grype/matcher"
	"github.com/anchore/grype/grype/vulnerability"
)

// ErrDBNotLoaded is returned by (*DB).Scan when the vulnerability DB
// hasn't loaded yet. Callers can distinguish this transient state
// from a real scan failure via errors.Is.
var ErrDBNotLoaded = errors.New("grype db not loaded")

// DB holds the currently-loaded grype vulnerability provider. Safe
// for concurrent use.
type DB struct {
	mu       sync.RWMutex
	provider vulnerability.Provider
	matchers []match.Matcher

	dbRootDir string
	log       *slog.Logger
}

// Scan runs a vulnerability scan against the currently-loaded DB.
// Returns ErrDBNotLoaded when no DB has loaded yet.
func (s *DB) Scan(ctx context.Context, sbomBytes []byte) ([]Match, error) {
	s.mu.RLock()
	vp := s.provider
	ms := s.matchers
	s.mu.RUnlock()
	if vp == nil {
		return nil, ErrDBNotLoaded
	}
	return (&Scanner{provider: vp, matchers: ms}).Scan(ctx, sbomBytes)
}

// Option configures a DB constructed by NewDB.
type Option func(*dbConfig)

type dbConfig struct {
	dbRootDir string
	refresh   time.Duration
	log       *slog.Logger
}

// WithDBRootDir overrides the on-disk root for the grype DB. Empty
// (the default) uses grype's XDG cache scoped to this app.
func WithDBRootDir(dir string) Option {
	return func(c *dbConfig) { c.dbRootDir = dir }
}

// WithRefresh enables a background refresh at the given interval.
// Zero (the default) disables refresh.
func WithRefresh(d time.Duration) Option {
	return func(c *dbConfig) { c.refresh = d }
}

// WithLogger sets the logger used for refresh warnings. Defaults to
// slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(c *dbConfig) { c.log = log }
}

// NewDB returns a DB populated by an initial synchronous load and,
// when WithRefresh is set, refreshed in the background. On
// initial-load failure the DB is still returned; Scan will return
// ErrDBNotLoaded until a refresh succeeds.
func NewDB(ctx context.Context, opts ...Option) (*DB, error) {
	cfg := &dbConfig{log: slog.Default()}
	for _, o := range opts {
		o(cfg)
	}
	s := &DB{dbRootDir: cfg.dbRootDir, log: cfg.log}
	err := s.reload(ctx, true)
	go s.refresh(ctx, cfg.refresh)
	return s, err
}

// reload opens (and, when update=true, downloads) the DB and swaps
// the loaded provider in.
func (s *DB) reload(ctx context.Context, update bool) error {
	distCfg := distribution.DefaultConfig()
	distCfg.ID = clio.Identification{Name: "renovate-datasource"}

	instCfg := installation.DefaultConfig(distCfg.ID)
	if s.dbRootDir != "" {
		instCfg.DBRootDir = s.dbRootDir
	}

	vp, _, err := grype.LoadVulnerabilityDB(distCfg, instCfg, update)
	if err != nil {
		return fmt.Errorf("loading grype db: %w", err)
	}

	s.mu.Lock()
	old := s.provider
	s.provider = vp
	if s.matchers == nil {
		s.matchers = matcher.NewDefaultMatchers(matcher.Config{})
	}
	s.mu.Unlock()

	// Close the previous provider outside the lock — it wraps a
	// SQLite handle and file-backed indexes, so leaking one per
	// refresh accumulates fds over the life of the process.
	if old != nil {
		if err := old.Close(); err != nil {
			s.log.WarnContext(ctx, "closing previous grype db provider", "err", err)
		}
	}
	return nil
}

// refresh re-runs reload on the given interval until ctx cancels.
// Errors are logged; the previously-loaded provider is kept.
func (s *DB) refresh(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.reload(ctx, true); err != nil {
				s.log.WarnContext(ctx, "grype db refresh failed; keeping previous snapshot", "err", err)
			}
		}
	}
}
