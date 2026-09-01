// Package snapshot writes Chainguard release feeds to a directory
// tree that a plain static file server can serve as a Renovate custom
// datasource. Each configured Datasource is iterated: for every package
// name it lists, its Releases (post-minimumReleaseAge filter) are
// written to <outputDir>/<name>/<path>/releases.json.
//
// The JSON shape matches datasource.Response, the same one the live
// serve command returns, so a Renovate configuration pointed at
// either backend uses the same registryUrlTemplate.
package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

type options struct {
	datasources       map[string]datasource.Datasource
	minimumReleaseAge time.Duration
	maxReleaseAge     time.Duration
	concurrency       int
	now               func() time.Time
	log               *slog.Logger
}

// Option configures Generate.
type Option func(*options)

// WithDatasource registers ds under name. name becomes the on-disk
// subdirectory (<outputDir>/<name>/<package>/releases.json). The
// last registration wins if the same name is used twice.
func WithDatasource(name string, ds datasource.Datasource) Option {
	return func(o *options) {
		if o.datasources == nil {
			o.datasources = map[string]datasource.Datasource{}
		}
		o.datasources[name] = ds
	}
}

// WithMinimumReleaseAge sets the window applied to every source's
// Releases call. Frozen at generation time (unlike the serve
// command's per-request ?minimumReleaseAge=). 0 disables it.
func WithMinimumReleaseAge(d time.Duration) Option {
	return func(o *options) { o.minimumReleaseAge = d }
}

// WithMaxReleaseAge drops releases whose ReleaseTimestamp is older
// than now-d, and skips writing the package/repo entirely when
// nothing survives. Applies uniformly across all datasources. 0 disables it.
func WithMaxReleaseAge(d time.Duration) Option {
	return func(o *options) { o.maxReleaseAge = d }
}

// WithNow overrides the reference time used by --max-release-age.
// Nil defaults to time.Now.
func WithNow(fn func() time.Time) Option {
	return func(o *options) { o.now = fn }
}

// WithLogger sets the structured logger. Default slog.Default().
func WithLogger(log *slog.Logger) Option {
	return func(o *options) { o.log = log }
}

// WithConcurrency caps the number of per-package Releases lookups
// running in parallel per source. Actual API-call concurrency is
// governed by whatever rate-limiter the datasources' backends carry, so
// setting this above the backend's limit only costs goroutines, not
// throughput. 0 or negative disables the fan-out (sequential).
func WithConcurrency(n int) Option {
	return func(o *options) { o.concurrency = n }
}

// Generate writes <outputDir>/<name>/<package>/releases.json for
// every entry emitted by every registered source. outputDir must
// not already exist — Generate creates it fresh and refuses to
// touch anything already there. Atomic-swap policy (blue/green
// symlink flips, rsync into place, S3 sync-then-alias) is the
// caller's job.
//
// Returns an error if no datasources were registered — an empty
// snapshot is almost certainly a misconfiguration.
func Generate(ctx context.Context, outputDir string, opts ...Option) error {
	o := options{log: slog.Default(), now: time.Now}
	for _, fn := range opts {
		fn(&o)
	}
	if len(o.datasources) == 0 {
		return errors.New("snapshot.Generate: no datasources registered")
	}
	if outputDir == "" {
		return errors.New("snapshot.Generate: outputDir is required")
	}
	if _, err := os.Lstat(outputDir); err == nil {
		return fmt.Errorf("output %q already exists; pass a path that doesn't", outputDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", outputDir, err)
	}

	// Stage writes in a tmp dir adjacent to outputDir so the final
	// rename stays on the same filesystem (POSIX rename is atomic
	// within a filesystem but fails across mounts with EXDEV). If
	// generation fails or the process is interrupted we clean the
	// tmp dir up on the way out and outputDir is never observable
	// in a half-written state.
	parent := filepath.Dir(outputDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("preparing parent %q: %w", parent, err)
	}
	tmpDir, err := os.MkdirTemp(parent, filepath.Base(outputDir)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating tmp dir under %q: %w", parent, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	// Freeze "now" once so every cutoff — the minimumReleaseAge
	// "before" we pass to each Datasource.Releases and the
	// max-release-age cutoff applied afterwards — uses the same
	// reference time across the whole run.
	now := o.now()
	var minReleaseAgeBefore time.Time
	if o.minimumReleaseAge > 0 {
		minReleaseAgeBefore = now.Add(-o.minimumReleaseAge)
	}
	var maxAgeCutoff time.Time
	if o.maxReleaseAge > 0 {
		maxAgeCutoff = now.Add(-o.maxReleaseAge)
	}

	for name, ds := range o.datasources {
		o.log.InfoContext(ctx, "snapshot: starting datasource", "name", name)
		packages, err := ds.PackageNames(ctx)
		if err != nil {
			return fmt.Errorf("source %q enumeration: %w", name, err)
		}

		var written, skipped atomic.Int64
		eg, egCtx := errgroup.WithContext(ctx)
		if o.concurrency > 0 {
			eg.SetLimit(o.concurrency)
		}
		for _, pkg := range packages {
			eg.Go(func() error {
				if err := egCtx.Err(); err != nil {
					return err
				}
				releases, err := ds.Releases(egCtx, pkg, datasource.ReleasesOptions{Before: minReleaseAgeBefore})
				if err != nil {
					if errors.Is(err, datasource.ErrNotFound) {
						// Race: PackageNames listed it, Releases didn't find
						// it. Not fatal — skip and move on.
						o.log.WarnContext(egCtx, "snapshot: skipping package (not found)", "datasource", name, "package", pkg)
						skipped.Add(1)
						return nil
					}
					var invalidName *datasource.InvalidPackageNameError
					if errors.As(err, &invalidName) {
						o.log.WarnContext(egCtx, "snapshot: skipping package (invalid name)", "datasource", name, "package", pkg, "err", invalidName.Message)
						skipped.Add(1)
						return nil
					}
					return fmt.Errorf("source %q releases for %q: %w", name, pkg, err)
				}
				if !maxAgeCutoff.IsZero() {
					releases = withinMaxAge(releases, maxAgeCutoff)
				}
				if len(releases) == 0 {
					o.log.WarnContext(egCtx, "snapshot: skipping package (no releases after filtering)", "datasource", name, "package", pkg)
					skipped.Add(1)
					return nil
				}
				if err := writeResponse(tmpDir, name, pkg, datasource.Response{Releases: releases}); err != nil {
					return err
				}
				o.log.DebugContext(egCtx, "snapshot: wrote package", "datasource", name, "package", pkg, "releases", len(releases))
				written.Add(1)
				return nil
			})
		}
		if err := eg.Wait(); err != nil {
			return err
		}
		o.log.InfoContext(ctx, "snapshot: datasource complete", "name", name, "written", written.Load(), "skipped", skipped.Load())
	}

	// Atomic commit: single directory-entry swap on the parent
	// filesystem. From here on outputDir is either fully populated
	// or doesn't exist at all.
	if err := os.Rename(tmpDir, outputDir); err != nil {
		return fmt.Errorf("committing snapshot to %q: %w", outputDir, err)
	}
	committed = true
	return nil
}

// withinMaxAge drops releases whose ReleaseTimestamp is before
// cutoff. Zero-timestamp releases always pass — we have no signal
// to age them out.
func withinMaxAge(releases []datasource.Release, cutoff time.Time) []datasource.Release {
	out := make([]datasource.Release, 0, len(releases))
	for _, r := range releases {
		if r.ReleaseTimestamp.IsZero() || !r.ReleaseTimestamp.Before(cutoff) {
			out = append(out, r)
		}
	}
	return out
}

// writeResponse marshals resp to <outputDir>/<name>/<pkg>/releases.json,
// creating the enclosing directories. Errors from Close are
// propagated so a disk-full / quota flush failure at the tail of
// the write doesn't silently produce a truncated file.
//
// pkg is defence-checked with filepath.Clean + IsLocal so a source
// emitting a "../foo" path can't escape outputDir.
func writeResponse(outputDir, name, pkg string, resp datasource.Response) error {
	cleaned := filepath.Clean(pkg)
	if !filepath.IsLocal(cleaned) {
		return fmt.Errorf("refusing unsafe path %q under source %q", pkg, name)
	}
	dir := filepath.Join(outputDir, name, cleaned)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %q: %w", dir, err)
	}
	full := filepath.Join(dir, "releases.json")
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("opening %q: %w", full, err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(resp); err != nil {
		_ = f.Close()
		return fmt.Errorf("encoding %q: %w", full, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", full, err)
	}
	return nil
}
