package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	"github.com/chainguard-sandbox/renovate-datasource/internal/server"
)

type options struct {
	port               int
	cooldown           time.Duration
	org                string
	historyConcurrency int
	apkIndexRefresh    time.Duration
	datasources        []string

	identity      string
	identityToken string
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "renovate-datasource",
		Short: "Renovate custom datasource for Chainguard images, apks, and helm charts",
		Long: `An HTTP service that acts as a Renovate custom datasource for a single
Chainguard org. It exposes the /releases endpoints Renovate consumes:

  * /v1/repo/{repo}/releases   images and charts (repo may be multi-segment,
                               e.g. "python", "charts/nginx",
                               "iamguarded-charts/postgresql")
  * /v1/apk/{name}/releases    apk packages, including prefixed capabilities
                               like "cmd:gcloud" or unversioned "nodejs"

Pass --cooldown=<dur> to set a server-wide default, or ?cooldown=<dur>
per request, to only surface digests that have been stable that long.
Tags newer than the cooldown are rewound to the most recent historical
digest that satisfies it.

Authentication:
  By default the service loads the chainctl token from disk
  (run "chainctl auth login" beforehand).

  For deployed environments, pass --identity (an assumable identity UIDP)
  together with --identity-token (either a path to an OIDC token file or
  a literal token string). When the value points at a file, the file is
  re-read on demand so Kubernetes service-account token rotation works.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), opts)
		},
	}

	cmd.Flags().IntVar(&opts.port, "port", 8080, "HTTP listen port")
	cmd.Flags().DurationVar(&opts.cooldown, "cooldown", 0, "default cooldown window (Go duration, e.g. 168h); can be overridden per request via ?cooldown=<dur>. 0 (default) disables it.")
	cmd.Flags().StringVar(&opts.org, "org", "", "Chainguard org/group name (required)")
	cmd.Flags().IntVar(&opts.historyConcurrency, "history-concurrency", 16, "max concurrent ListTagHistory calls per request")
	cmd.Flags().DurationVar(&opts.apkIndexRefresh, "apk-index-refresh", time.Hour, "how often to re-fetch the apk indexes served under /v1/apk/{name}/releases. 0 disables the background refresh (indexes are still loaded once at startup).")
	cmd.Flags().StringSliceVar(&opts.datasources, "datasource", []string{"repo", "apk"}, "select which datasources to expose (repo, apk). Repo-only skips the APK index fetch and apk.cgr.dev token exchange at startup.")
	cmd.Flags().StringVar(&opts.identity, "identity", "", "UIDP of an assumable Chainguard identity (enables identity auth)")
	cmd.Flags().StringVar(&opts.identityToken, "identity-token", "", "OIDC token to assume the identity; either a file path or a literal JWT")
	_ = cmd.MarkFlagRequired("org")

	return cmd
}

func run(parent context.Context, opts *options) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	enableRepo, enableAPK, err := parseDatasources(opts.datasources)
	if err != nil {
		return err
	}

	cgOpts, authLbl, err := chainguardOptions(opts)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cg, err := chainguard.New(ctx, opts.org, cgOpts...)
	if err != nil {
		log.Error("initializing Chainguard client", "err", err)
		return fmt.Errorf("initializing Chainguard client: %w", err)
	}
	defer cg.Close()

	log.Info("resolved org", "org", opts.org, "uidp", cg.OrgUIDP, "cooldown", opts.cooldown, "auth", authLbl, "datasources", opts.datasources)

	serverOpts := []server.Option{
		server.WithCooldown(opts.cooldown),
		server.WithHistoryConcurrency(opts.historyConcurrency),
		server.WithLogger(log),
		server.WithRepoEnabled(enableRepo),
		server.WithAPKEnabled(enableAPK),
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
		serverOpts = append(serverOpts, server.WithAPKIndex(apkStore))
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

// parseDatasources validates the --datasource list and returns the
// enable-repo / enable-apk booleans it implies. Empty and unknown values
// are rejected so a typo doesn't silently disable both endpoints.
func parseDatasources(values []string) (bool, bool, error) {
	if len(values) == 0 {
		return false, false, errors.New("--datasource must list at least one of: repo, apk")
	}
	var enableRepo, enableAPK bool
	for _, v := range values {
		switch v {
		case "repo":
			enableRepo = true
		case "apk":
			enableAPK = true
		default:
			return false, false, fmt.Errorf("unknown --datasource %q; supported: repo, apk", v)
		}
	}
	return enableRepo, enableAPK, nil
}

// chainguardOptions translates CLI flags into chainguard.Option values, plus
// a label for the startup log line.
func chainguardOptions(opts *options) ([]chainguard.Option, string, error) {
	switch {
	case opts.identity != "" && opts.identityToken != "":
		return []chainguard.Option{chainguard.WithIdentity(opts.identity, opts.identityToken)}, "identity", nil
	case opts.identity != "" || opts.identityToken != "":
		return nil, "", errors.New("--identity and --identity-token must be set together")
	default:
		return nil, "chainctl", nil
	}
}
