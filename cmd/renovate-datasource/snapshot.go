package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
	"github.com/chainguard-sandbox/renovate-datasource/internal/snapshot"
)

type snapshotOptions struct {
	org               string
	outputDir         string
	minimumReleaseAge time.Duration
	maxReleaseAge     time.Duration
	concurrency       int
	apkRepositories   []string
	datasources       []string
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
  ├── apk/
  │   ├── cmd:node/releases.json                     # prefixed capability
  │   ├── curl/releases.json                         # apk package
  │   └── nodejs/releases.json                       # unprefixed provider
  └── repo/
      ├── charts/
      │   └── nginx/releases.json                    # helm chart
      ├── iamguarded-charts/
      │   └── postgresql/releases.json               # iamguarded helm chart
      └── python/releases.json                       # image

Examples:

  # Snapshot everything.
  renovate-datasource snapshot --org=my.org.com -d /out

  # Only apk packages, dropping anything untouched for six months.
  renovate-datasource snapshot --org=my.org.com -d /out \
      --datasource=apk --max-release-age=4380h

  # Only images/charts, with a week's minimum-release-age applied at generation time.
  renovate-datasource snapshot --org=my.org.com -d /out \
      --datasource=repo --min-release-age=168h`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshot(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.org, "org", "", "Chainguard org/group name. Required unless the repo datasource is disabled and --apk-repository is used to bypass the Chainguard client entirely.")
	cmd.Flags().StringVarP(&opts.outputDir, "output-dir", "d", "", "output directory; must not already exist (required)")
	cmd.Flags().DurationVar(&opts.minimumReleaseAge, "min-release-age", 0, "minimum-release-age window (Go duration, e.g. 168h). Frozen at generation time. 0 (default) disables it.")
	cmd.Flags().DurationVar(&opts.maxReleaseAge, "max-release-age", 0, "drop releases older than this (Go duration, e.g. 4380h for ~6 months) and skip packages/repos with nothing in the window. 0 (default) disables it.")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 16, "cap on concurrent platform-API calls; also drives the per-package fan-out inside each source and the per-repo history fan-out")
	cmd.Flags().StringSliceVar(&opts.apkRepositories, "apk-repository", nil, "apk repository root URL (repeatable, comma-separated). When set, overrides the default apk.cgr.dev/virtualapk.cgr.dev chain. Auth is picked up from HTTP_AUTH (format: basic:<host>:<user>:<password>).")
	cmd.Flags().StringSliceVar(&opts.datasources, "datasource", []string{"repo", "apk"}, "comma-separated list of datasources to snapshot (repo, apk).")
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

	enableRepo, enableAPK, err := parseDatasources(opts.datasources)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var cg *chainguard.Client
	if enableRepo || (enableAPK && len(opts.apkRepositories) == 0) {
		cg, err = newChainguardClient(ctx, log, opts.org, opts.identity, opts.identityToken, opts.concurrency)
		if err != nil {
			return err
		}
		defer func() {
			if err := cg.Close(); err != nil {
				log.Warn("closing Chainguard client", "err", err)
			}
		}()
	}

	snapshotOpts := []snapshot.Option{
		snapshot.WithLogger(log),
		snapshot.WithMinimumReleaseAge(opts.minimumReleaseAge),
		snapshot.WithMaxReleaseAge(opts.maxReleaseAge),
		snapshot.WithConcurrency(opts.concurrency),
	}

	if enableAPK {
		const apkArch = "x86_64"
		apkRepos, err := resolveAPKRepositories(ctx, cg, opts.apkRepositories)
		if err != nil {
			return err
		}
		// One-shot: interval=0 skips the background refresh; only
		// the initial synchronous load runs.
		store, err := apk.NewIndexStoreWithRefresh(ctx, apkArch, apkRepos, 0, log)
		if err != nil {
			return fmt.Errorf("loading apk indexes: %w", err)
		}
		snapshotOpts = append(snapshotOpts, snapshot.WithDatasource("apk", datasource.NewAPKDatasource(store)))
	}
	if enableRepo {
		snapshotOpts = append(snapshotOpts, snapshot.WithDatasource("repo", datasource.NewRepoDatasource(cg, opts.concurrency)))
	}

	return snapshot.Generate(ctx, opts.outputDir, snapshotOpts...)
}
