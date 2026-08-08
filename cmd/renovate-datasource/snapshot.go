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
	org           string
	outputDir     string
	cooldown      time.Duration
	maxReleaseAge time.Duration
	concurrency   int
	datasources   []string
	logLevel      string

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

  # Only images/charts, with a week's cooldown applied at generation time.
  renovate-datasource snapshot --org=my.org.com -d /out \
      --datasource=repo --cooldown=168h`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSnapshot(cmd.Context(), opts)
		},
	}

	cmd.Flags().StringVar(&opts.org, "org", "", "Chainguard org/group name (required)")
	cmd.Flags().StringVarP(&opts.outputDir, "output-dir", "d", "", "output directory; must not already exist (required)")
	cmd.Flags().DurationVar(&opts.cooldown, "cooldown", 0, "cooldown window (Go duration, e.g. 168h). Frozen at generation time. 0 (default) disables it.")
	cmd.Flags().DurationVar(&opts.maxReleaseAge, "max-release-age", 0, "drop releases older than this (Go duration, e.g. 4380h for ~6 months) and skip packages/repos with nothing in the window. 0 (default) disables it.")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 16, "cap on concurrent platform-API calls; also drives the per-package fan-out inside each source and the per-repo history fan-out")
	cmd.Flags().StringSliceVar(&opts.datasources, "datasource", []string{"repo", "apk"}, "comma-separated list of datasources to snapshot (repo, apk).")
	cmd.Flags().StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().StringVar(&opts.identity, "identity", "", "UIDP of an assumable Chainguard identity (enables identity auth)")
	cmd.Flags().StringVar(&opts.identityToken, "identity-token", "", "OIDC token to assume the identity; either a file path or a literal JWT")
	_ = cmd.MarkFlagRequired("org")
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

	cgOpts, authLbl, err := chainguardOptions(opts.identity, opts.identityToken)
	if err != nil {
		return err
	}
	cgOpts = append(cgOpts, chainguard.WithConcurrency(opts.concurrency))

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cg, err := chainguard.New(ctx, opts.org, cgOpts...)
	if err != nil {
		log.Error("initializing Chainguard client", "err", err)
		return fmt.Errorf("initializing Chainguard client: %w", err)
	}
	defer func() {
		if err := cg.Close(); err != nil {
			log.Warn("closing Chainguard client", "err", err)
		}
	}()

	log.Info("resolved org", "org", opts.org, "uidp", cg.OrgUIDP, "auth", authLbl, "datasources", opts.datasources)

	snapshotOpts := []snapshot.Option{
		snapshot.WithLogger(log),
		snapshot.WithCooldown(opts.cooldown),
		snapshot.WithMaxReleaseAge(opts.maxReleaseAge),
		snapshot.WithConcurrency(opts.concurrency),
	}

	if enableAPK {
		const apkArch = "x86_64"
		apkRepos := apk.DefaultRepositories(cg.OrgName, cg.OrgUIDP, cg.APKTokenSource(ctx))
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
