package diff

import (
	"reflect"
	"testing"
)

func TestExtractFetches(t *testing.T) {
	yml := []byte(`
package:
  name: sqlite-libs
pipeline:
  - uses: fetch
    with:
      uri: https://www.sqlite.org/2026/sqlite-autoconf-3460000.tar.gz
      expected-sha256: abc123
  - uses: configure
subpackages:
  - name: sqlite-libs-doc
    pipeline:
      - uses: fetch
        with:
          uri: https://example.com/docs.tar.gz
          expected-sha512: deadbeef
`)
	got := extractFetches(parseMelange(yml))
	want := []FetchEntry{
		{URI: "https://www.sqlite.org/2026/sqlite-autoconf-3460000.tar.gz", Hash: "sha256:abc123"},
		{URI: "https://example.com/docs.tar.gz", Hash: "sha512:deadbeef"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestDiffFetchesSingleUpdate(t *testing.T) {
	from := []byte(`pipeline:
  - uses: fetch
    with:
      uri: https://www.sqlite.org/2026/sqlite-autoconf-3460000.tar.gz
      expected-sha256: aaa
`)
	to := []byte(`pipeline:
  - uses: fetch
    with:
      uri: https://www.sqlite.org/2026/sqlite-autoconf-3460100.tar.gz
      expected-sha256: bbb
`)
	got := diffFetches(parseMelange(from), parseMelange(to))
	if got == nil || len(got.Updated) != 1 {
		t.Fatalf("expected 1 updated entry, got %+v", got)
	}
	d := got.Updated[0]
	if !((d.FromURI == "https://www.sqlite.org/2026/sqlite-autoconf-3460000.tar.gz") &&
		(d.ToURI == "https://www.sqlite.org/2026/sqlite-autoconf-3460100.tar.gz") &&
		(d.FromHash == "sha256:aaa") && (d.ToHash == "sha256:bbb")) {
		t.Errorf("unexpected delta: %+v", d)
	}
}

func TestDiffFetchesAddedRemoved(t *testing.T) {
	from := []byte(`pipeline:
  - uses: fetch
    with: {uri: https://a.example/a.tar.gz, expected-sha256: a}
`)
	to := []byte(`pipeline:
  - uses: fetch
    with: {uri: https://b.example/b.tar.gz, expected-sha256: b}
  - uses: fetch
    with: {uri: https://c.example/c.tar.gz, expected-sha256: c}
`)
	got := diffFetches(parseMelange(from), parseMelange(to))
	if got == nil {
		t.Fatal("expected changes, got nil")
	}
	// Index 0 paired across sides → updated.
	if len(got.Updated) != 1 || got.Updated[0].ToURI != "https://b.example/b.tar.gz" {
		t.Errorf("Updated: %+v", got.Updated)
	}
	// Index 1 only in to → added.
	if len(got.Added) != 1 || got.Added[0].URI != "https://c.example/c.tar.gz" {
		t.Errorf("Added: %+v", got.Added)
	}
	if len(got.Removed) != 0 {
		t.Errorf("Removed: %+v", got.Removed)
	}
}

func TestDiffFetchesNoChanges(t *testing.T) {
	src := []byte(`pipeline:
  - uses: fetch
    with: {uri: https://x.example/x.tar.gz, expected-sha256: deadbeef}
`)
	parsed := parseMelange(src)
	if got := diffFetches(parsed, parsed); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}
