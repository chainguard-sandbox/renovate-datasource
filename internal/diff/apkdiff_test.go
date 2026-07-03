package diff

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
)

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

func TestComputeAPKDiff(t *testing.T) {
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
		got, err := ComputeAPKDiff(context.Background(), f, "foo", "1.0", "2.0")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if !strings.Contains(got.Melange, "-  version: 1.0") {
			t.Errorf("melange diff missing expected lines:\n%s", got.Melange)
		}
		if !strings.Contains(got.PKGINFO, "-pkgver = 1.0") {
			t.Errorf("pkginfo diff missing expected lines:\n%s", got.PKGINFO)
		}
	})

	t.Run("identical contents yield empty diffs", func(t *testing.T) {
		got, err := ComputeAPKDiff(context.Background(), f, "bar", "1.0", "1.1")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got.Melange != "" || got.PKGINFO != "" {
			t.Errorf("non-empty diffs for identical sides: %+v", got)
		}
	})

	t.Run("fetch failure surfaces an error", func(t *testing.T) {
		_, err := ComputeAPKDiff(context.Background(), f, "missing", "1.0", "2.0")
		if err == nil {
			t.Fatal("expected error for missing entries")
		}
	})
}

func TestSplitLinesPreservesNewlines(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a\n", []string{"a\n"}},
		{"a\nb\n", []string{"a\n", "b\n"}},
		{"a\nb", []string{"a\n", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := splitLines(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d (got=%q)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
