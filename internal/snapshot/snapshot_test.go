package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chainguard-sandbox/renovate-datasource/internal/apk"
	"github.com/chainguard-sandbox/renovate-datasource/internal/chainguard"
	"github.com/chainguard-sandbox/renovate-datasource/internal/datasource"
)

var silentLog = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeDatasource lets tests emit whatever (path, releases) pairs they
// need without wiring up a real APKDatasource or RepoDatasource.
type fakeDatasource struct {
	packages     map[string][]datasource.Release
	pkgErr       error
	relErr       error
	invalidNames map[string]string
}

func (f *fakeDatasource) PackageNames(_ context.Context) ([]string, error) {
	if f.pkgErr != nil {
		return nil, f.pkgErr
	}
	out := make([]string, 0, len(f.packages))
	for name := range f.packages {
		out = append(out, name)
	}
	return out, nil
}

func (f *fakeDatasource) Releases(_ context.Context, name string, _ datasource.ReleasesOptions) ([]datasource.Release, error) {
	if f.relErr != nil {
		return nil, f.relErr
	}
	if msg, ok := f.invalidNames[name]; ok {
		return nil, &datasource.InvalidPackageNameError{Message: msg}
	}
	r, ok := f.packages[name]
	if !ok {
		return nil, datasource.ErrNotFound
	}
	return r, nil
}

func (f *fakeDatasource) Ready(_ context.Context) error { return nil }

func TestGenerate_NoDatasourcesErrors(t *testing.T) {
	if err := Generate(context.Background(), t.TempDir()+"/new"); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGenerate_RefusesExistingOutputDir(t *testing.T) {
	err := Generate(context.Background(), t.TempDir(),
		WithDatasource("apk", &fakeDatasource{}),
	)
	if err == nil {
		t.Fatal("expected error refusing to reuse existing dir")
	}
}

func TestGenerate_WritesJSONForEachEntry(t *testing.T) {
	out := filepath.Join(t.TempDir(), "snap")
	packages := map[string][]datasource.Release{
		"curl":     {{Version: "8.21.0-r1", ReleaseTimestamp: time.Unix(1, 0).UTC()}},
		"cmd:node": {{Version: "24.14.0-r0", ReleaseTimestamp: time.Unix(2, 0).UTC()}},
	}
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("apk", &fakeDatasource{packages: packages}),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for name, want := range packages {
		got := readResponse(t, filepath.Join(out, "apk", name, "releases.json"))
		if len(got.Releases) != len(want) || got.Releases[0].Version != want[0].Version {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
}

func TestGenerate_MultipleDatasources(t *testing.T) {
	out := filepath.Join(t.TempDir(), "snap")
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("apk", &fakeDatasource{packages: map[string][]datasource.Release{
			"curl": {{Version: "8.21.0-r1"}},
		}}),
		WithDatasource("repo", &fakeDatasource{packages: map[string][]datasource.Release{
			"python":       {{Version: "3.14"}},
			"charts/nginx": {{Version: "22.1.0"}},
		}}),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, path := range []string{
		filepath.Join(out, "apk", "curl", "releases.json"),
		filepath.Join(out, "repo", "python", "releases.json"),
		filepath.Join(out, "repo", "charts", "nginx", "releases.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file at %s: %v", path, err)
		}
	}
}

func TestGenerate_MaxReleaseAgeAppliesToAllDatasources(t *testing.T) {
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	oldTs := now.AddDate(0, 0, -400)
	freshTs := now.AddDate(0, 0, -30)
	out := filepath.Join(t.TempDir(), "snap")

	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithNow(func() time.Time { return now }),
		WithMaxReleaseAge(180*24*time.Hour),
		WithDatasource("apk", &fakeDatasource{packages: map[string][]datasource.Release{
			"dormant":  {{Version: "1.0.0-r0", ReleaseTimestamp: oldTs}},
			"active":   {{Version: "2.0.0-r0", ReleaseTimestamp: freshTs}},
		}}),
		WithDatasource("repo", &fakeDatasource{packages: map[string][]datasource.Release{
			"stale": {{Version: "1", ReleaseTimestamp: oldTs}},
			"fresh": {{Version: "2", ReleaseTimestamp: freshTs}},
		}}),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	mustExist(t, filepath.Join(out, "apk", "active", "releases.json"))
	mustExist(t, filepath.Join(out, "repo", "fresh", "releases.json"))
	mustNotExist(t, filepath.Join(out, "apk", "dormant"))
	mustNotExist(t, filepath.Join(out, "repo", "stale"))
}

func TestGenerate_UnsafePathRejected(t *testing.T) {
	out := filepath.Join(t.TempDir(), "snap")
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("apk", &fakeDatasource{packages: map[string][]datasource.Release{
			"../escape": {{Version: "1"}},
		}}),
	)
	if err == nil {
		t.Fatal("expected error for unsafe path")
	}
}

// TestGenerate_InvalidPackageNameIsSkipped confirms that a datasource
// which surfaces a name in PackageNames but rejects it in Releases with
// InvalidPackageNameError (e.g. a poisoned APK index record) doesn't
// abort the whole snapshot run.
func TestGenerate_InvalidPackageNameIsSkipped(t *testing.T) {
	out := filepath.Join(t.TempDir(), "snap")
	ds := &fakeDatasource{
		packages: map[string][]datasource.Release{
			"good":  {{Version: "1.0.0-r0"}},
			"../bad": nil,
		},
		invalidNames: map[string]string{
			"../bad": "The apk package name isn't well-formed.",
		},
	}
	if err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("apk", ds),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "apk", "good", "releases.json")); err != nil {
		t.Errorf("expected good/releases.json: %v", err)
	}
}

func TestGenerate_EnumerationErrorPropagates(t *testing.T) {
	out := filepath.Join(t.TempDir(), "snap")
	sentinel := errors.New("enumeration bang")
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("repo", &fakeDatasource{pkgErr: sentinel}),
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want to wrap %v", err, sentinel)
	}
}

// TestGenerate_FailureLeavesNoOutput confirms the atomic-commit
// story: a mid-run error leaves outputDir absent (rather than
// half-populated), and the sibling tmp dir is cleaned up too.
func TestGenerate_FailureLeavesNoOutput(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "snap")

	sentinel := errors.New("enumeration bang")
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("repo", &fakeDatasource{pkgErr: sentinel}),
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want to wrap %v", err, sentinel)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("expected outputDir to be absent after failure, got err=%v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("ReadDir(parent): %v", err)
	}
	for _, e := range entries {
		t.Errorf("leftover entry in parent after failure: %s", e.Name())
	}
}

// TestGenerate_TargetNeverHalfPopulated covers the successful case:
// during generation the outputDir doesn't exist yet — writes land in
// the tmp dir — and only after all datasources complete does the atomic
// rename swap it into place.
func TestGenerate_TargetNeverHalfPopulated(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "snap")

	saw := struct {
		outExistsMidRun bool
		tmpExistsMidRun bool
	}{}

	probe := &fakeDatasource{packages: map[string][]datasource.Release{
		"foo": {{Version: "1"}},
	}}
	// Wrap probe.Releases so we can observe the filesystem mid-run.
	probeDatasource := &observerDatasource{inner: probe, onRelease: func() {
		if _, err := os.Stat(out); err == nil {
			saw.outExistsMidRun = true
		}
		entries, _ := os.ReadDir(parent)
		for _, e := range entries {
			if e.Name() != "snap" {
				saw.tmpExistsMidRun = true
			}
		}
	}}

	if err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("apk", probeDatasource),
	); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if saw.outExistsMidRun {
		t.Error("outputDir appeared mid-run (should stay absent until final rename)")
	}
	if !saw.tmpExistsMidRun {
		t.Error("expected sibling tmp dir to exist mid-run")
	}
	// Post-condition: outputDir is populated, no tmp siblings remain.
	if _, err := os.Stat(filepath.Join(out, "apk", "foo", "releases.json")); err != nil {
		t.Errorf("expected populated outputDir: %v", err)
	}
	entries, _ := os.ReadDir(parent)
	for _, e := range entries {
		if e.Name() != "snap" {
			t.Errorf("leftover tmp entry after commit: %s", e.Name())
		}
	}
}

// observerDatasource wraps a fakeDatasource and fires onRelease every time
// Releases is called, letting the test peek at the filesystem
// during generation.
type observerDatasource struct {
	inner     *fakeDatasource
	onRelease func()
}

func (o *observerDatasource) PackageNames(ctx context.Context) ([]string, error) {
	return o.inner.PackageNames(ctx)
}
func (o *observerDatasource) Releases(ctx context.Context, name string, opts datasource.ReleasesOptions) ([]datasource.Release, error) {
	o.onRelease()
	return o.inner.Releases(ctx, name, opts)
}
func (o *observerDatasource) Ready(ctx context.Context) error { return o.inner.Ready(ctx) }

// TestAPKDatasource_SnapshotShape drives an APKDatasource through Generate
// and confirms the on-disk shape matches the live handler's JSON.
func TestAPKDatasource_SnapshotShape(t *testing.T) {
	now := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	store := apk.NewIndexStore()
	store.Replace(map[string][]apk.PackageVersion{
		"curl": {{Version: "8.21.0-r1", Timestamp: now.AddDate(0, 0, -30), Arch: "x86_64"}},
	}, []string{"x86_64"})
	ds := datasource.NewAPKDatasource(store)

	out := filepath.Join(t.TempDir(), "snap")
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithNow(func() time.Time { return now }),
		WithDatasource("apk", ds),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got := readResponse(t, filepath.Join(out, "apk", "curl", "releases.json"))
	if len(got.Releases) != 1 || got.Releases[0].Version != "8.21.0-r1" {
		t.Errorf("got %+v, want single 8.21.0-r1 entry", got)
	}
}

// TestRepoDatasource_SnapshotShape drives a RepoDatasource through Generate
// with a fake backend and confirms writes land under repo/<path>/.
func TestRepoDatasource_SnapshotShape(t *testing.T) {
	backend := &fakeRepoBackend{
		repos: []string{"python", "charts/nginx"},
		tags: map[string][]chainguard.Tag{
			"python":       {{ID: "1", Name: "3.14", LastUpdated: time.Unix(1, 0).UTC(), Digest: "sha256:x"}},
			"charts/nginx": {{ID: "2", Name: "22.1.0", LastUpdated: time.Unix(2, 0).UTC(), Digest: "sha256:y"}},
		},
	}
	ds := datasource.NewRepoDatasource(backend, 0)

	out := filepath.Join(t.TempDir(), "snap")
	err := Generate(context.Background(), out,
		WithLogger(silentLog),
		WithDatasource("repo", ds),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	mustExist(t, filepath.Join(out, "repo", "python", "releases.json"))
	mustExist(t, filepath.Join(out, "repo", "charts", "nginx", "releases.json"))
}

type fakeRepoBackend struct {
	repos []string
	tags  map[string][]chainguard.Tag
}

func (b *fakeRepoBackend) ListAllRepos(_ context.Context) ([]string, error) {
	return b.repos, nil
}
func (b *fakeRepoBackend) ListTags(_ context.Context, repo string) ([]chainguard.Tag, error) {
	return b.tags[repo], nil
}
func (b *fakeRepoBackend) ListTagHistory(_ context.Context, _ string) ([]chainguard.TagHistory, error) {
	return nil, nil
}
func (b *fakeRepoBackend) Ready(_ context.Context) error { return nil }

func readResponse(t *testing.T, path string) datasource.Response {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var r datasource.Response
	if err := json.NewDecoder(f).Decode(&r); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return r
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist, got err=%v", path, err)
	}
}
