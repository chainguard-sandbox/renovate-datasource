package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "renovate-datasource",
		Short: "Renovate custom datasource for Chainguard images, apks, and helm charts",
		Long: `Serve or snapshot the /releases feeds Renovate consumes as a custom
datasource. Two subcommands:

  serve     run an HTTP server that answers /releases requests live
  snapshot  write the same JSON payloads to a static folder tree

Both talk to a single Chainguard org and support the same --datasource
selection (repo, apk) and minimumReleaseAge semantics.

Authentication:
  By default the service loads the chainctl token from disk
  (run "chainctl auth login" beforehand).

  For deployed environments, pass --identity (an assumable identity UIDP)
  together with --identity-token (either a path to an OIDC token file or
  a literal token string). When the value points at a file, the file is
  re-read on demand so Kubernetes service-account token rotation works.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newServeCmd())
	cmd.AddCommand(newSnapshotCmd())
	return cmd
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

// newLogger builds the JSON slog handler both subcommands use. level
// is one of "debug", "info", "warn", "error"; anything else is
// rejected with a client-facing error so a typo doesn't silently
// swallow the operator's intent.
func newLogger(level string) (*slog.Logger, error) {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "info", "":
		lvl = slog.LevelInfo
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		return nil, fmt.Errorf("unknown --log-level %q; supported: debug, info, warn, error", level)
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})), nil
}

// chainguardOptions translates identity flags into chainguard.Option
// values, plus a label for the startup log line. Shared by serve and
// snapshot since their auth flags are identical.
func chainguardOptions(identity, identityToken string) ([]chainguard.Option, string, error) {
	switch {
	case identity != "" && identityToken != "":
		return []chainguard.Option{chainguard.WithIdentity(identity, identityToken)}, "identity", nil
	case identity != "" || identityToken != "":
		return nil, "", errors.New("--identity and --identity-token must be set together")
	default:
		return nil, "chainctl", nil
	}
}
