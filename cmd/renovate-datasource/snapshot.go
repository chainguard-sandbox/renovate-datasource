package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
	"github.com/chainguard-sandbox/renovate-datasource/internal/snapshot"
)

type snapshotOptions struct {
	org               string
	outputDir         string
	minimumReleaseAge time.Duration
	maxReleaseAge     time.Duration
	concurrency       int
	logLevel          string

	identity      string
	identityToken string
}

func newSnapshotCmd() *cobra.Command {
	opts := &snapshotOptions{}
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Snapshot Chainguard release feeds to a static folder tree",
		Long: `Write the same JSON payloads the serve command's /releases endpoints
return, but to disk instead of via HTTP. Point a static file server
at --output-dir and it becomes a Renovate custom datasource.

Layout:

  <output-dir>/
  └── repo/
      ├── charts/
      │   └── nginx/releases.json                    # helm chart
      ├── iamguarded-charts/
      │   └── postgresql/releases.json               # iamguarded helm chart
      └── python/releases.json                       # image

Examples:

  # Snapshot everything.
  renovate-datasource snapshot --org=my.org.com -d /out

  # Drop anything untouched for six months.
  renovate-datasource snapshot --org=my.org.com -d /out --max-release-age=4380h

  # Apply a week's minimum-release-age at generation time.
  renovate-datasource snapshot --org=my.org.com -d /out --min-release-age=168h`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshot(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.org, "org", "", "Chainguard org/group name (required)")
	cmd.Flags().StringVarP(&opts.outputDir, "output-dir", "d", "", "output directory; must not already exist (required)")
	cmd.Flags().DurationVar(&opts.minimumReleaseAge, "min-release-age", 0, "minimum-release-age window (Go duration, e.g. 168h). Frozen at generation time. 0 (default) disables it.")
	cmd.Flags().DurationVar(&opts.maxReleaseAge, "max-release-age", 0, "drop releases older than this (Go duration, e.g. 4380h for ~6 months) and skip packages/repos with nothing in the window. 0 (default) disables it.")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 16, "cap on concurrent platform-API calls; also drives the per-package fan-out inside each source and the per-repo history fan-out")
	cmd.Flags().StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().StringVar(&opts.identity, "identity", "", "UIDP of an assumable Chainguard identity (enables identity auth)")
	cmd.Flags().StringVar(&opts.identityToken, "identity-token", "", "OIDC token to assume the identity; either a file path or a literal JWT")
	_ = cmd.MarkFlagRequired("output-dir")

	return cmd
}

func runSnapshot(parent context.Context, opts *snapshotOptions) error {
	log, err := newLogger(opts.logLevel)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cg, err := newChainguardClient(ctx, log, opts.org, opts.identity, opts.identityToken, opts.concurrency)
	if err != nil {
		return err
	}
	defer func() {
		if err := cg.Close(); err != nil {
			log.Warn("closing Chainguard client", "err", err)
		}
	}()

	return snapshot.Generate(ctx, opts.outputDir,
		snapshot.WithLogger(log),
		snapshot.WithMinimumReleaseAge(opts.minimumReleaseAge),
		snapshot.WithMaxReleaseAge(opts.maxReleaseAge),
		snapshot.WithConcurrency(opts.concurrency),
		snapshot.WithDatasource("repo", datasource.NewRepoDatasource(cg, opts.concurrency)),
	)
}
