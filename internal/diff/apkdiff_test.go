package diff

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
)

func pv(name, version string) apk.PackageVersion {
	return apk.PackageVersion{Name: name, Version: version}
}

// fakeAPKFetcher is a small in-memory APKFetcher driven by a map keyed
// by "name/version" and an optional per-key error override.
type fakeAPKFetcher struct {
	mu       sync.Mutex
	contents map[string]*apk.Contents
	errs     map[string]error
	calls    []string
}

func (f *fakeAPKFetcher) Fetch(_ context.Context, name, version string) (*apk.Contents, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := name + "/" + version
	f.calls = append(f.calls, key)
	if err, ok := f.errs[key]; ok {
		return nil, err
	}
	if c, ok := f.contents[key]; ok {
		return c, nil
	}
	return nil, errors.New("not configured")
}

func TestAPKs(t *testing.T) {
	f := &fakeAPKFetcher{
		contents: map[string]*apk.Contents{
			"foo/1.0": {
				Melange: []byte("package:\n  name: foo\n  version: 1.0\n"),
				PKGINFO: []byte("pkgname = foo\npkgver = 1.0\n"),
			},
			"foo/2.0": {
				Melange: []byte("package:\n  name: foo\n  version: 2.0\n"),
				PKGINFO: []byte("pkgname = foo\npkgver = 2.0\n"),
			},
			"bar/1.0": {
				Melange: []byte("package: bar\n"),
				PKGINFO: []byte("pkgname = bar\n"),
			},
			"bar/1.1": {
				Melange: []byte("package: bar\n"),
				PKGINFO: []byte("pkgname = bar\n"),
			},
		},
	}

	t.Run("metadata diffs populate when sides differ", func(t *testing.T) {
		got, err := APKs(context.Background(), f, pv("foo", "1.0"), pv("foo", "2.0"))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Name != "foo" || got.FromName != "foo" || got.ToName != "foo" {
			t.Errorf("names not populated for same-name diff: %+v", got)
		}
		if !strings.Contains(got.Melange, "-  version: 1.0") {
			t.Errorf("melange diff missing expected lines:\n%s", got.Melange)
		}
		if !strings.Contains(got.PKGINFO, "-pkgver = 1.0") {
			t.Errorf("pkginfo diff missing expected lines:\n%s", got.PKGINFO)
		}
	})

	t.Run("identical contents yield empty diffs", func(t *testing.T) {
		got, err := APKs(context.Background(), f, pv("bar", "1.0"), pv("bar", "1.1"))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Melange != "" || got.PKGINFO != "" {
			t.Errorf("non-empty diffs for identical sides: %+v", got)
		}
	})

	t.Run("fetch failure surfaces an error", func(t *testing.T) {
		_, err := APKs(context.Background(), f, pv("missing", "1.0"), pv("missing", "2.0"))
		if err == nil {
			t.Fatal("expected error for missing entries")
		}
	})

	t.Run("cross-package diff labels reflect both names", func(t *testing.T) {
		crossFetcher := &fakeAPKFetcher{
			contents: map[string]*apk.Contents{
				"nodejs-22/22.14.0-r0": {
					Melange: []byte("package:\n  name: nodejs-22\n  version: 22.14.0-r0\n"),
					PKGINFO: []byte("pkgname = nodejs-22\npkgver = 22.14.0-r0\n"),
				},
				"nodejs-26/26.4.0-r1": {
					Melange: []byte("package:\n  name: nodejs-26\n  version: 26.4.0-r1\n"),
					PKGINFO: []byte("pkgname = nodejs-26\npkgver = 26.4.0-r1\n"),
				},
			},
		}
		got, err := APKs(context.Background(), crossFetcher, pv("nodejs-22", "22.14.0-r0"), pv("nodejs-26", "26.4.0-r1"))
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Name != "" {
			t.Errorf("Name should be omitted for cross-package diff, got %q", got.Name)
		}
		if got.FromName != "nodejs-22" || got.ToName != "nodejs-26" {
			t.Errorf("FromName/ToName = %q/%q, want nodejs-22/nodejs-26", got.FromName, got.ToName)
		}
		// Diff labels should carry both name+version so the hunks are
		// unambiguous when the two sides come from different packages.
		if !strings.Contains(got.Melange, "nodejs-22 22.14.0-r0") || !strings.Contains(got.Melange, "nodejs-26 26.4.0-r1") {
			t.Errorf("melange diff missing cross-package labels:\n%s", got.Melange)
		}
	})
}

