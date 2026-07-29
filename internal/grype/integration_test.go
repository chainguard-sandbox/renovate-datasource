package grype_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/grype"
)

// Minimal SPDX 2.3 SBOM with a deliberately-old openssl to guarantee
// a non-empty match list against any working DB.
const integrationSBOM = `{
  "spdxVersion": "SPDX-2.3",
  "dataLicense": "CC0-1.0",
  "SPDXID": "SPDXRef-DOCUMENT",
  "name": "spike-sbom",
  "documentNamespace": "https://example.com/renovate-datasource/spike",
  "creationInfo": {
    "created": "2024-01-01T00:00:00Z",
    "creators": ["Tool: renovate-datasource-spike"]
  },
  "packages": [
    {
      "SPDXID": "SPDXRef-Package-openssl",
      "name": "openssl",
      "versionInfo": "3.0.0-r0",
      "downloadLocation": "NOASSERTION",
      "filesAnalyzed": false,
      "externalRefs": [
        {
          "referenceCategory": "PACKAGE-MANAGER",
          "referenceType": "purl",
          "referenceLocator": "pkg:apk/wolfi/[email protected]?arch=x86_64&upstream=openssl&distro=wolfi-20220914"
        }
      ]
    }
  ]
}`

// TestScanner_EndToEnd downloads/opens the DB and scans a tiny SBOM.
// Skipped by default (~1GB DB); opt in with GRYPE_INTEGRATION=1.
func TestScanner_EndToEnd(t *testing.T) {
	if os.Getenv("GRYPE_INTEGRATION") != "1" {
		t.Skip("set GRYPE_INTEGRATION=1 to run; downloads ~1GB grype DB on first run")
	}

	ctx := context.Background()
	logDest := io.Discard
	if testing.Verbose() {
		logDest = os.Stderr
	}
	log := slog.New(slog.NewTextHandler(logDest, nil))

	// No dbRootDir → XDG default path scoped to this app, distinct
	// from the grype-CLI's cache.
	store, err := grype.NewDB(ctx, grype.WithLogger(log))
	if err != nil {
		t.Fatalf("db load: %v", err)
	}

	matches, err := store.Scan(ctx, []byte(integrationSBOM))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected at least one match against openssl 3.0.0-r0; got none")
	}

	// Sample a few for eyeball checking.
	t.Logf("%d matches; sample:", len(matches))
	for i, m := range matches {
		if i >= 5 {
			break
		}
		t.Logf("  %s %-8s %s=%s (fixed in: %v)", m.ID, m.Severity, m.Package.Name, m.Package.Version, m.FixVersions)
	}
}
