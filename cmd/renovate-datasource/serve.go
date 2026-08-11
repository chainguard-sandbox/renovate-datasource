package main

import (
	"context"
	"errors"
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
	apkRepositories   []string
	apkArchs          []string
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
  renovate-datasource serve --org=my.org.com --datasource=repo --min-release-age=168h

  # Serve only /v1/apk from your own mirror; no Chainguard credentials
  # needed. Provide HTTP_AUTH=basic:mirror.example:user:pass in the
  # environment if the mirror requires Basic auth.
  renovate-datasource serve --datasource=apk \
      --apk-repository=https://mirror.example/apk/chainguard \
      --apk-repository=https://mirror.example/apk/extra-packages`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServe(cmd.Context(), opts)
		},
	}

	cmd.Flags().IntVar(&opts.port, "port", 8080, "HTTP listen port")
	cmd.Flags().DurationVar(&opts.minimumReleaseAge, "min-release-age", 0, "default minimum-release-age window (Go duration, e.g. 168h); can be overridden per request via ?minimumReleaseAge=<dur>. 0 (default) disables it.")
	cmd.Flags().StringVar(&opts.org, "org", "", "Chainguard org/group name. Required unless the repo datasource is disabled and --apk-repository is used to bypass the Chainguard client entirely.")
	cmd.Flags().IntVar(&opts.concurrency, "concurrency", 16, "cap on concurrent platform-API calls (ListTags, ListTagHistory, ListAllRepos); also bounds per-request history fan-out")
	cmd.Flags().DurationVar(&opts.apkIndexRefresh, "apk-index-refresh", time.Hour, "how often to re-fetch the apk indexes served under /v1/apk/{name}/releases. 0 disables the background refresh (indexes are still loaded once at startup).")
	cmd.Flags().StringSliceVar(&opts.apkRepositories, "apk-repository", nil, "apk repository root URL (repeatable, comma-separated). When set, overrides the default apk.cgr.dev/virtualapk.cgr.dev chain. Auth is picked up from HTTP_AUTH (format: basic:<host>:<user>:<password>).")
	cmd.Flags().StringSliceVar(&opts.apkArchs, "apk-arch", []string{"x86_64", "aarch64"}, "architectures to load APKINDEX for (repeatable, comma-separated). Each configured repo must serve every listed arch or startup fails.")
	cmd.Flags().StringSliceVar(&opts.datasources, "datasource", []string{"repo", "apk"}, "select which datasources to expose (repo, apk). Repo-only skips the APK index fetch and apk.cgr.dev token exchange at startup.")
	cmd.Flags().StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	cmd.Flags().StringVar(&opts.identity, "identity", "", "UIDP of an assumable Chainguard identity (enables identity auth)")
	cmd.Flags().StringVar(&opts.identityToken, "identity-token", "", "OIDC token to assume the identity; either a file path or a literal JWT")

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

	serverOpts := []server.Option{
		server.WithMinimumReleaseAge(opts.minimumReleaseAge),
		server.WithLogger(log),
	}

	if enableRepo {
		serverOpts = append(serverOpts, server.WithDatasource("repo", datasource.NewRepoDatasource(cg, opts.concurrency)))
	}
	if enableAPK {
		// APK repository chain feeding the /v1/apk/{name}/releases index.
		apkRepos, err := resolveAPKRepositories(ctx, cg, opts.apkRepositories)
		if err != nil {
			return err
		}

		// Initial load blocks so /v1/apk/{name}/releases is warm as soon
		// as the listener comes up; refresh continues in the background
		// tied to ctx.
		apkStore, err := apk.NewIndexStoreWithRefresh(ctx, opts.apkArchs, apkRepos, opts.apkIndexRefresh, log)
		if err != nil {
			log.Warn("initial apk index load failed; /v1/apk/{name}/releases will 404 until the next successful refresh", "err", err)
		}
		serverOpts = append(serverOpts, server.WithDatasource("apk", datasource.NewAPKDatasource(apkStore)))
	}

	apiServer, err := server.New(serverOpts...)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:    net.JoinHostPort("", strconv.Itoa(opts.port)),
		Handler: apiServer.Handler(),
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
