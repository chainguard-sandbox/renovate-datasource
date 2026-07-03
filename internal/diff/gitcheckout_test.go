package diff

import (
	"reflect"
	"testing"
)

func TestExtractGitCheckouts(t *testing.T) {
	yml := []byte(`
package:
  name: foo
pipeline:
  - uses: fetch
    with: {uri: https://example.com}
  - uses: git-checkout
    with:
      repository: https://github.com/nodejs/node.git
      expected-commit: abc123
      tag: v26.1.0
  - uses: composite
    pipeline:
      - uses: git-checkout
        with:
          repository: https://github.com/foo/bar
          expected-commit: deadbeef
          branch: main
subpackages:
  - name: foo-doc
    pipeline:
      - uses: git-checkout
        with:
          repository: https://gitlab.com/x/y/
          tag: v1.0
`)
	got := extractGitCheckouts(parseMelange(yml))
	want := []GitCheckoutEntry{
		{Repository: "https://github.com/nodejs/node", Host: "github.com", Path: "nodejs/node", Tag: "v26.1.0", Commit: "abc123"},
		{Repository: "https://github.com/foo/bar", Host: "github.com", Path: "foo/bar", Branch: "main", Commit: "deadbeef"},
		{Repository: "https://gitlab.com/x/y", Host: "gitlab.com", Path: "x/y", Tag: "v1.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestExtractGitCheckoutsMalformed(t *testing.T) {
	// Garbage yaml → empty slice, not an error. The diff code is happy
	// to render a blank section.
	got := extractGitCheckouts(parseMelange([]byte("not: [valid")))
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestDiffGitCheckoutsUpdated(t *testing.T) {
	from := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/nodejs/node.git
      expected-commit: 1eaf8a9bd9b665c104bc49f1890e962dddc87d8c
      tag: v26.0.0
`)
	to := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/nodejs/node.git
      expected-commit: e7da6f056ac4afeaaf012042188818ca7736f437
      tag: v26.1.0
`)
	got := diffGitCheckouts(parseMelange(from), parseMelange(to))
	if got == nil || len(got.Updated) != 1 {
		t.Fatalf("expected 1 updated entry, got %+v", got)
	}
	d := got.Updated[0]
	if d.From.Tag != "v26.0.0" || d.To.Tag != "v26.1.0" {
		t.Errorf("tag range mismatch: %s -> %s", d.From.Tag, d.To.Tag)
	}
	if d.From.TagURL != "https://github.com/nodejs/node/releases/tag/v26.0.0" {
		t.Errorf("From.TagURL = %s", d.From.TagURL)
	}
	if d.To.TagURL != "https://github.com/nodejs/node/releases/tag/v26.1.0" {
		t.Errorf("To.TagURL = %s", d.To.TagURL)
	}
	if d.From.CommitURL != "https://github.com/nodejs/node/commit/1eaf8a9bd9b665c104bc49f1890e962dddc87d8c" {
		t.Errorf("From.CommitURL = %s", d.From.CommitURL)
	}
	if d.To.CommitURL != "https://github.com/nodejs/node/commit/e7da6f056ac4afeaaf012042188818ca7736f437" {
		t.Errorf("To.CommitURL = %s", d.To.CommitURL)
	}
	wantCompare := "https://github.com/nodejs/node/compare/1eaf8a9bd9b665c104bc49f1890e962dddc87d8c...e7da6f056ac4afeaaf012042188818ca7736f437"
	if d.CompareURL != wantCompare {
		t.Errorf("CompareURL = %s, want %s", d.CompareURL, wantCompare)
	}
}

func TestDiffGitCheckoutsAddedRemoved(t *testing.T) {
	from := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/old/repo
      tag: v1
`)
	to := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/new/repo
      expected-commit: cafef00d
`)
	got := diffGitCheckouts(parseMelange(from), parseMelange(to))
	if got == nil {
		t.Fatal("expected changes, got nil")
	}
	if len(got.Added) != 1 || got.Added[0].Repository != "https://github.com/new/repo" {
		t.Errorf("Added: %+v", got.Added)
	}
	if got.Added[0].CommitURL != "https://github.com/new/repo/commit/cafef00d" {
		t.Errorf("Added CommitURL = %s", got.Added[0].CommitURL)
	}
	if len(got.Removed) != 1 || got.Removed[0].Repository != "https://github.com/old/repo" {
		t.Errorf("Removed: %+v", got.Removed)
	}
	if got.Removed[0].TagURL != "https://github.com/old/repo/releases/tag/v1" {
		t.Errorf("Removed TagURL = %s", got.Removed[0].TagURL)
	}
}

func TestDiffGitCheckoutsNoChanges(t *testing.T) {
	src := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/foo/bar.git
      tag: v1.0
      expected-commit: abc
`)
	parsed := parseMelange(src)
	if got := diffGitCheckouts(parsed, parsed); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestDiffGitCheckoutsCommitOnlyChange(t *testing.T) {
	// Same tag (or no tag), commit bumped → no TagURL, CommitURL +
	// CompareURL populated.
	from := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/foo/bar
      branch: main
      expected-commit: aaaa
`)
	to := []byte(`pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/foo/bar
      branch: main
      expected-commit: bbbb
`)
	got := diffGitCheckouts(parseMelange(from), parseMelange(to))
	if got == nil || len(got.Updated) != 1 {
		t.Fatalf("expected 1 updated entry, got %+v", got)
	}
	d := got.Updated[0]
	if d.From.TagURL != "" || d.To.TagURL != "" {
		t.Errorf("TagURL set despite no tag: from=%s to=%s", d.From.TagURL, d.To.TagURL)
	}
	if d.From.CommitURL == "" || d.To.CommitURL == "" {
		t.Errorf("expected both sides' CommitURL")
	}
	if d.CompareURL == "" {
		t.Errorf("expected non-empty CompareURL")
	}
}
