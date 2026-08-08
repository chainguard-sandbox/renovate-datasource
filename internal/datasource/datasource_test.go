package datasource

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
)

func TestAPKDatasource_ReleasesFiltersAndSorts(t *testing.T) {
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return now.AddDate(0, 0, -d) }

	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {
			{Version: "8.20.0-r0", Timestamp: day(30)},
			{Version: "8.21.0-r0", Timestamp: day(15)},
			{Version: "8.22.0-r0", Timestamp: day(1)},     // newer than 7d cutoff — filtered
			{Version: "0.9.0-r0", Timestamp: time.Time{}}, // zero-ts — filtered (can't prove it's outside the window)
		},
	})
	ds := NewAPKDatasource(store)

	before := now.Add(-7 * 24 * time.Hour)
	got, err := ds.Releases(context.Background(), "curl", before)
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
			{Version: "1.0.0-r0", Timestamp: time.Unix(1, 0).UTC()},
			{Version: "2.0.0-r0", Timestamp: time.Now().Add(time.Hour)}, // "future" — passes with zero before
		},
	})
	ds := NewAPKDatasource(store)

	got, err := ds.Releases(context.Background(), "curl", time.Time{})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("zero before should not filter; got %+v", got)
	}
}

func TestAPKDatasource_ReleasesUnknownName(t *testing.T) {
	ds := NewAPKDatasource(apk.NewIndexStore())
	_, err := ds.Releases(context.Background(), "nonesuch", time.Time{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestAPKDatasource_ReleasesRejectsMalformedName(t *testing.T) {
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{"curl": {{Version: "1"}}})
	ds := NewAPKDatasource(store)

	for _, name := range []string{"curl", "cmd:node", "so:libssl.so.3", "py3.14:setuptools", "nodejs-24", "Catch2", "ImageMagick", "cmd:["} {
		if _, err := ds.Releases(context.Background(), name, time.Time{}); err != nil {
			var invalid *InvalidPackageNameError
			if errors.As(err, &invalid) {
				t.Errorf("%q rejected as malformed: %v", name, err)
			}
			// Otherwise err is ErrNotFound (which is fine — the store only has "curl").
		}
	}
	for _, name := range []string{"", "../escape", "has space", ".hidden", "foo/bar", "\x00null"} {
		_, err := ds.Releases(context.Background(), name, time.Time{})
		var invalid *InvalidPackageNameError
		if !errors.As(err, &invalid) {
			t.Errorf("%q: got err=%v, want *InvalidPackageNameError", name, err)
		}
	}
}

func TestRepoDatasource_ReleasesRejectsMalformedName(t *testing.T) {
	ds := NewRepoDatasource(&repoStub{tags: map[string][]chainguard.Tag{
		"python":       {{ID: "1", Name: "3.14", LastUpdated: time.Now(), Digest: "sha256:x"}},
		"charts/nginx": {{ID: "2", Name: "22", LastUpdated: time.Now(), Digest: "sha256:y"}},
	}}, 0)

	for _, name := range []string{"python", "charts/nginx"} {
		if _, err := ds.Releases(context.Background(), name, time.Time{}); err != nil {
			t.Errorf("%q rejected: %v", name, err)
		}
	}
	for _, name := range []string{"", "../escape", "has space", "TRAILING/", "/leading", ".dotfile"} {
		_, err := ds.Releases(context.Background(), name, time.Time{})
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
	store.Replace(map[string][]apk.PackageVersion{"foo": {{Version: "1"}}})
	if err := ds.Ready(context.Background()); err != nil {
		t.Errorf("populated store: unexpected error %v", err)
	}
}

type repoStub struct {
	tags       map[string][]chainguard.Tag
	tagsErr    error
	allRepos   []string
	allErr     error
	histCalls  int
	histResult []chainguard.TagHistory
	readyErr   error
}

func (b *repoStub) ListAllRepos(_ context.Context) ([]string, error) {
	return b.allRepos, b.allErr
}
func (b *repoStub) ListTags(_ context.Context, repo string) ([]chainguard.Tag, error) {
	if b.tagsErr != nil {
		return nil, b.tagsErr
	}
	tags, ok := b.tags[repo]
	if !ok {
		return nil, chainguard.ErrRepoNotFound
	}
	return tags, nil
}
func (b *repoStub) ListTagHistory(_ context.Context, _ string) ([]chainguard.TagHistory, error) {
	b.histCalls++
	return b.histResult, nil
}
func (b *repoStub) Ready(_ context.Context) error { return b.readyErr }

func TestRepoDatasource_ReleasesZeroBeforePassesThrough(t *testing.T) {
	backend := &repoStub{tags: map[string][]chainguard.Tag{
		"python": {{ID: "1", Name: "3.14", LastUpdated: time.Now(), Digest: "sha256:x"}},
	}}
	ds := NewRepoDatasource(backend, 0)

	got, err := ds.Releases(context.Background(), "python", time.Time{})
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}
	if len(got) != 1 || got[0].Version != "3.14" {
		t.Errorf("got %+v", got)
	}
	if backend.histCalls != 0 {
		t.Errorf("history calls = %d, want 0 (zero before should short-circuit)", backend.histCalls)
	}
}

func TestRepoDatasource_ReleasesUnknownRepoMapsToErrNotFound(t *testing.T) {
	ds := NewRepoDatasource(&repoStub{}, 0)
	_, err := ds.Releases(context.Background(), "nonesuch", time.Time{})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestRepoDatasource_Ready(t *testing.T) {
	ds := NewRepoDatasource(&repoStub{}, 0)
	if err := ds.Ready(context.Background()); err != nil {
		t.Errorf("Ready: %v", err)
	}
}
