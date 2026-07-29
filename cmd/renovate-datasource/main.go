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

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/spf13/cobra"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/chainguard"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/grype"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/oci"
	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/server"
)

type options struct {
	port               int
	cooldown           time.Duration
	org                string
	historyConcurrency int
	apkIndexRefresh    time.Duration
	grypeScan          bool
	grypeDBDir         string
	grypeDBRefresh     time.Duration

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
		Short: "Renovate custom datasource for Chainguard images",
		Long: `An HTTP service that acts as a Renovate custom datasource for a single
Chainguard org. It serves two things:

  * /v1/repo/{repo}/releases — the tag/digest list Renovate consumes. By
    default the upstream tag list is served as-is; pass --cooldown=<dur>
    to set a server-wide default, or ?cooldown=<dur> per request, to only
    surface digests that have been stable that long. Tags newer than the
    cooldown are rewound to the most recent historical digest that
    satisfies it.

  * /v1/repo/{repo}/diff/{from}/{to} and an HTML page at /repo/.../diff/...
    — structured diffs between two image refs (apk packages, upstream
    source repos, image config, grype-flagged vulnerabilities). Useful
    as a Renovate changelogUrl target.

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
	cmd.Flags().BoolVar(&opts.grypeScan, "grype-scan", true, "enable vulnerability diffs on the image diff page. False skips the grype DB download.")
	cmd.Flags().StringVar(&opts.grypeDBDir, "grype-db-dir", "", "on-disk root for the grype DB. Empty uses ~/.cache/renovate-datasource/db (Linux), scoped separately from the grype-CLI cache. Ignored when --grype-scan=false.")
	cmd.Flags().DurationVar(&opts.grypeDBRefresh, "grype-db-refresh", 24*time.Hour, "how often to re-download the grype DB. 0 disables background refresh. Ignored when --grype-scan=false.")
	cmd.Flags().StringVar(&opts.identity, "identity", "", "UIDP of an assumable Chainguard identity (enables identity auth)")
	cmd.Flags().StringVar(&opts.identityToken, "identity-token", "", "OIDC token to assume the identity; either a file path or a literal JWT")
	_ = cmd.MarkFlagRequired("org")

	return cmd
}

func run(parent context.Context, opts *options) error {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

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

	log.Info("resolved org", "org", opts.org, "uidp", cg.OrgUIDP, "cooldown", opts.cooldown, "auth", authLbl)

	// OCI keychain for cgr.dev:
	//   - identity mode: mint cgr.dev-audience tokens via STS using the
	//     configured assumable identity.
	//   - chainctl mode: rely on go-containerregistry's default keychain,
	//     which reads ~/.docker/config.json. Operator is expected to have
	//     wired up cgr.dev creds locally (e.g. via `chainctl auth
	//     configure-docker`).
	var kc authn.Keychain
	if cg.IsIdentity() {
		kc = oci.Keychain(cg.RegistryTokenSource(ctx))
	} else {
		kc = authn.DefaultKeychain
	}
	fetcher := oci.New(cg.OrgName, kc)

	// APK repository chain — shared between the artifact fetcher (used
	// by the diff / version endpoints) and the index loader (which
	// populates the releases endpoint's in-memory store). Building it
	// once means anything the index surfaces is also fetchable — no
	// "listed but not fetchable" gaps. OCI fetcher is pinned to amd64;
	// match that arch on the apk side (apks live under /<repo>/x86_64/).
	const apkArch = "x86_64"
	apkRepos := apk.DefaultRepositories(cg.OrgName, cg.OrgUIDP, cg.APKTokenSource(ctx))
	apkFetcher := apk.NewFetcher(apkArch, apkRepos)

	// Initial load blocks so /v1/apk/{name}/releases is warm as soon
	// as the listener comes up; refresh continues in the background
	// tied to ctx.
	apkStore, err := apk.NewIndexStoreWithRefresh(ctx, apkArch, apkRepos, opts.apkIndexRefresh, log)
	if err != nil {
		log.Warn("initial apk index load failed; /v1/apk/{name}/releases will 404 until the next successful refresh", "err", err)
	}

	// Initial load blocks (~30-60s cold, near-instant when the on-disk
	// copy is fresh); refresh runs in the background. Load failure is
	// non-fatal — the Vulnerabilities section is omitted until the DB
	// loads.
	var grypeStore *grype.DB
	if opts.grypeScan {
		grypeStore, err = grype.NewDB(ctx,
			grype.WithDBRootDir(opts.grypeDBDir),
			grype.WithRefresh(opts.grypeDBRefresh),
			grype.WithLogger(log),
		)
		if err != nil {
			log.Warn("initial grype db load failed; vulnerability diffs will be unavailable until the next successful refresh", "err", err)
		}
	} else {
		log.Info("grype scan disabled; vulnerability diffs will be omitted from image diff responses")
	}

	serverOpts := []server.Option{
		server.WithCooldown(opts.cooldown),
		server.WithHistoryConcurrency(opts.historyConcurrency),
		server.WithLogger(log),
		server.WithOrgName(cg.OrgName),
		server.WithAPKFetcher(apkFetcher),
		server.WithAPKIndex(apkStore),
	}
	if grypeStore != nil {
		serverOpts = append(serverOpts, server.WithGrypeScanner(grypeStore))
	}

	srv := &http.Server{
		Addr:    net.JoinHostPort("", strconv.Itoa(opts.port)),
		Handler: server.New(cg, fetcher, serverOpts...).Handler(),
		// Bound every part of a connection so a slow or stuck client can't
		// pin a goroutine. WriteTimeout caps the worst-case diff latency —
		// if upstream cgr.dev takes longer than this, the response is
		// terminated rather than dripped out indefinitely.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
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
