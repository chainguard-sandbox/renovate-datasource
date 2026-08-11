package datasource

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
)

func TestAPKDatasource_PackageNames(t *testing.T) {
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl":     {{Version: "1.0.0-r0", Arch: "x86_64"}},
		"wget":     {{Version: "1.21-r0", Arch: "x86_64"}},
		"cmd:node": {{Version: "24.0.0-r0", Arch: "x86_64"}},
	}, []string{"x86_64"})
	ds := NewAPKDatasource(store)

	got, err := ds.PackageNames(context.Background())
	if err != nil {
		t.Fatalf("PackageNames: %v", err)
	}
	want := []string{"cmd:node", "curl", "wget"}
	if len(got) != len(want) {
		t.Fatalf("PackageNames len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("PackageNames[%d] = %q, want %q", i, got[i], w)
		}
	}
}

func TestAPKDatasource_ReleasesFiltersAndSorts(t *testing.T) {
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }

	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {
			{Version: "8.20.0-r0", Timestamp: day(30), Arch: "x86_64"},
			{Version: "8.21.0-r0", Timestamp: day(15), Arch: "x86_64"},
			{Version: "8.22.0-r0", Timestamp: day(1), Arch: "x86_64"},     // newer than 7d cutoff — filtered
			{Version: "0.9.0-r0", Timestamp: time.Time{}, Arch: "x86_64"}, // zero-ts — filtered (can't prove it's outside the window)
		},
	}, []string{"x86_64"})
	ds := NewAPKDatasource(store)

	before := now.Add(-7 * 24 * time.Hour)
	got, err := ds.Releases(context.Background(), "curl", ReleasesOptions{Before: before})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	wantVersions := []string{"8.21.0-r0", "8.20.0-r0"}
	if len(got) != len(wantVersions) {
		t.Fatalf("len(got)=%d want %d (%+v)", len(got), len(wantVersions), got)
	}
	for i, want := range wantVersions {
		if got[i].Version != want {
			t.Errorf("release[%d].Version = %q, want %q", i, got[i].Version, want)
		}
	}
}

func TestAPKDatasource_ReleasesZeroBeforeReturnsEverything(t *testing.T) {
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {
			{Version: "1.0.0-r0", Timestamp: time.Unix(1, 0).UTC(), Arch: "x86_64"},
			{Version: "2.0.0-r0", Timestamp: time.Now().Add(time.Hour), Arch: "x86_64"}, // "future" — passes with zero before
		},
	}, []string{"x86_64"})
	ds := NewAPKDatasource(store)

	got, err := ds.Releases(context.Background(), "curl", ReleasesOptions{})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("zero before should not filter; got %+v", got)
	}
}

// TestAPKDatasource_ReleasesBoundaryTimestampAtCutoff pins the semantics of
// the Before filter: entries whose timestamp is exactly equal to the cutoff
// are kept (the code uses After, not !Before), one nanosecond newer is
// dropped.
func TestAPKDatasource_ReleasesBoundaryTimestampAtCutoff(t *testing.T) {
	cutoff := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {
			{Version: "1.0.0-r0", Timestamp: cutoff, Arch: "x86_64"},                     // exactly at cutoff — kept
			{Version: "2.0.0-r0", Timestamp: cutoff.Add(time.Nanosecond), Arch: "x86_64"}, // 1ns after cutoff — dropped
		},
	}, []string{"x86_64"})
	ds := NewAPKDatasource(store)

	got, err := ds.Releases(context.Background(), "curl", ReleasesOptions{Before: cutoff})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if len(got) != 1 || got[0].Version != "1.0.0-r0" {
		t.Errorf("got %+v, want single 1.0.0-r0", got)
	}
}

// TestAPKDatasource_ReleasesSortTieBreakByVersion verifies that when
// timestamps compare equal the sort falls back to version desc so the
// output is deterministic.
func TestAPKDatasource_ReleasesSortTieBreakByVersion(t *testing.T) {
	ts := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {
			{Version: "8.20.0-r0", Timestamp: ts, Arch: "x86_64"},
			{Version: "8.21.0-r0", Timestamp: ts, Arch: "x86_64"},
			{Version: "8.19.0-r0", Timestamp: ts, Arch: "x86_64"},
		},
	}, []string{"x86_64"})
	ds := NewAPKDatasource(store)

	got, err := ds.Releases(context.Background(), "curl", ReleasesOptions{})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	wantVersions := []string{"8.21.0-r0", "8.20.0-r0", "8.19.0-r0"}
	if len(got) != len(wantVersions) {
		t.Fatalf("len(got)=%d want %d (%+v)", len(got), len(wantVersions), got)
	}
	for i, want := range wantVersions {
		if got[i].Version != want {
			t.Errorf("release[%d].Version = %q, want %q", i, got[i].Version, want)
		}
	}
}

func TestAPKDatasource_ReleasesArchFilter(t *testing.T) {
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {
			{Version: "1.0.0-r0", Arch: "x86_64"},
			{Version: "1.0.0-r0", Arch: "aarch64"}, // same version on both archs
			{Version: "2.0.0-r0", Arch: "x86_64"},  // x86_64-only
			{Version: "3.0.0-r0", Arch: "aarch64"}, // aarch64-only
		},
	}, []string{"x86_64", "aarch64"})
	ds := NewAPKDatasource(store)

	// Merged view: three unique versions, dedupe collapses the shared 1.0.0-r0.
	got, err := ds.Releases(context.Background(), "curl", ReleasesOptions{})
	if err != nil {
		t.Fatalf("merged Releases: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("merged len = %d, want 3 (%+v)", len(got), got)
	}

	// x86_64 view: 1.0.0 and 2.0.0.
	got, err = ds.Releases(context.Background(), "curl", ReleasesOptions{Arch: "x86_64"})
	if err != nil {
		t.Fatalf("x86_64 Releases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("x86_64 len = %d, want 2 (%+v)", len(got), got)
	}

	// aarch64 view: 1.0.0 and 3.0.0.
	got, err = ds.Releases(context.Background(), "curl", ReleasesOptions{Arch: "aarch64"})
	if err != nil {
		t.Fatalf("aarch64 Releases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("aarch64 len = %d, want 2 (%+v)", len(got), got)
	}

	// Unknown arch: InvalidArgumentError.
	_, err = ds.Releases(context.Background(), "curl", ReleasesOptions{Arch: "riscv64"})
	var invalidArg *InvalidArgumentError
	if !errors.As(err, &invalidArg) {
		t.Errorf("unknown arch: got err=%v, want *InvalidArgumentError", err)
	}
}

func TestAPKDatasource_ReleasesUnknownName(t *testing.T) {
	ds := NewAPKDatasource(apk.NewIndexStore())
	_, err := ds.Releases(context.Background(), "nonesuch", ReleasesOptions{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAPKDatasource_ReleasesRejectsMalformedName(t *testing.T) {
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{"curl": {{Version: "1", Arch: "x86_64"}}}, []string{"x86_64"})
	ds := NewAPKDatasource(store)

	for _, name := range []string{"curl", "cmd:node", "so:libssl.so.3", "py3.14:setuptools", "nodejs-24", "Catch2", "ImageMagick", "cmd:["} {
		if _, err := ds.Releases(context.Background(), name, ReleasesOptions{}); err != nil {
			var invalid *InvalidPackageNameError
			if errors.As(err, &invalid) {
				t.Errorf("%q rejected as malformed: %v", name, err)
			}
			// Otherwise err is ErrNotFound (which is fine — the store only has "curl").
		}
	}
	for _, name := range []string{"", "../escape", "has space", ".hidden", "foo/bar", "\x00null"} {
		_, err := ds.Releases(context.Background(), name, ReleasesOptions{})
		var invalid *InvalidPackageNameError
		if !errors.As(err, &invalid) {
			t.Errorf("%q: got err=%v, want *InvalidPackageNameError", name, err)
		}
	}
}

func TestAPKDatasource_Ready(t *testing.T) {
	store := apk.NewIndexStore()
	ds := NewAPKDatasource(store)
	if err := ds.Ready(context.Background()); err == nil {
		t.Error("empty store: expected error, got nil")
	}
	store.Replace(map[string][]apk.PackageVersion{"foo": {{Version: "1", Arch: "x86_64"}}}, []string{"x86_64"})
	if err := ds.Ready(context.Background()); err != nil {
		t.Errorf("populated store: unexpected error %v", err)
	}
}
