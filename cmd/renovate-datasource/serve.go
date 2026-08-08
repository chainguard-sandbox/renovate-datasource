package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
	"github.com/chainguard-sandbox/renovate-datasource/internal/server"
)

type serveOptions struct {
	port              int
	minimumReleaseAge time.Duration
	org               string
	concurrency       int
	apkIndexRefresh   time.Duration
	datasources       []string
	logLevel          string

	identity      string
	identityToken string
}

func newServeCmd() *cobra.Command {
	opts := &serveOptions{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve /releases over HTTP as a Renovate custom datasource",
		Long: `Run an HTTP server that exposes the /releases endpoints Renovate
consumes:

  /v1/repo/{repo}/releases   images and charts (repo may be multi-segment,
                             e.g. "python", "charts/nginx",
                             "iamguarded-charts/postgresql")
  /v1/apk/{name}/releases    apk packages, including prefixed capabilities
                             like "cmd:gcloud" or unversioned "nodejs"

Pass --min-release-age=<dur> to set a server-wide default, or
?minimumReleaseAge=<dur> per request, to only surface digests that
have been stable that long. Tags newer than the window are rewound
to the most recent historical digest that satisfies it.

Examples:

  # Serve both datasources on the default port.
  renovate-datasource serve --org=my.org.com

  # Serve only /v1/repo, with a week's default minimum-release-age.
  renovate-datasource serve --org=my.org.com --datasource=repo --min-release-age=168h`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), opts)
		},
	}

	cmd.Flags().IntVar(&opts.port, "port", 8080, "HTTP listen port")
	cmd.Flags().DurationVar(&opts.minimumReleaseAge, "min-release-age", 0, "default minimum-release-age window (Go duration, e.g. 168h); can be overridden per request via ?minimumReleaseAge=<dur>. 0 (default) disables it.")
	cmd.Flags().StringVar(&opts.org, "org", "", "Chainguard org/group name (required)")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 16, "cap on concurrent platform-API calls (ListTags, ListTagHistory, ListAllRepos); also bounds per-request history fan-out")
	cmd.Flags().DurationVar(&opts.apkIndexRefresh, "apk-index-refresh", time.Hour, "how often to re-fetch the apk indexes served under /v1/apk/{name}/releases. 0 disables the background refresh (indexes are still loaded once at startup).")
	cmd.Flags().StringSliceVar(&opts.datasources, "datasource", []string{"repo", "apk"}, "select which datasources to expose (repo, apk). Repo-only skips the APK index fetch and apk.cgr.dev token exchange at startup.")
	cmd.Flags().StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().StringVar(&opts.identity, "identity", "", "UIDP of an assumable Chainguard identity (enables identity auth)")
	cmd.Flags().StringVar(&opts.identityToken, "identity-token", "", "OIDC token to assume the identity; either a file path or a literal JWT")
	_ = cmd.MarkFlagRequired("org")

	return cmd
}

func runServe(parent context.Context, opts *serveOptions) error {
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

	log.Info("resolved org", "org", opts.org, "uidp", cg.OrgUIDP, "minimumReleaseAge", opts.minimumReleaseAge, "auth", authLbl, "datasources", opts.datasources)

	serverOpts := []server.Option{
		server.WithMinimumReleaseAge(opts.minimumReleaseAge),
		server.WithLogger(log),
	}

	if enableRepo {
		serverOpts = append(serverOpts, server.WithRepoDatasource(datasource.NewRepoDatasource(cg, opts.concurrency)))
	}
	if enableAPK {
		// APK repository chain feeding the /v1/apk/{name}/releases index.
		const apkArch = "x86_64"
		apkRepos := apk.DefaultRepositories(cg.OrgName, cg.OrgUIDP, cg.APKTokenSource(ctx))

		// Initial load blocks so /v1/apk/{name}/releases is warm as soon
		// as the listener comes up; refresh continues in the background
		// tied to ctx.
		apkStore, err := apk.NewIndexStoreWithRefresh(ctx, apkArch, apkRepos, opts.apkIndexRefresh, log)
		if err != nil {
			log.Warn("initial apk index load failed; /v1/apk/{name}/releases will 404 until the next successful refresh", "err", err)
		}
		serverOpts = append(serverOpts, server.WithAPKDatasource(datasource.NewAPKDatasource(apkStore)))
	}

	srv := &http.Server{
		Addr:    net.JoinHostPort("", strconv.Itoa(opts.port)),
		Handler: server.New(cg, serverOpts...).Handler(),
		// Bound every part of a connection so a slow or stuck client can't
		// pin a goroutine.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		log.Error("server stopped unexpectedly", "err", err)
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
		return err
	}
	return nil
}
