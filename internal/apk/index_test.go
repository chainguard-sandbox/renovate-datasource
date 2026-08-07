package apk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// makeIndex wraps a raw APKINDEX text body into the tarball format the
// loader expects (a tar containing an entry called "APKINDEX", gzipped).
func makeIndex(t *testing.T, body string) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "APKINDEX", Size: int64(len(body)), Mode: 0o644}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return &buf
}

func TestParseIndex(t *testing.T) {
	body := strings.Join([]string{
		"C:Q1abc=",
		"P:foo",
		"V:1.2.3-r0",
		"A:x86_64",
		"T:First release",
		"t:1700000000",
		"D:so:libc.so.6",
		"",
		"C:Q1def=",
		"P:foo",
		"V:1.2.4-r0",
		"t:1710000000",
		"",
		"C:Q1ghi=",
		"P:bar",
		"V:0.1.0-r0",
		"t:1720000000",
		"",
	}, "\n")

	out := newIndexData()
	n, err := parseIndex(makeIndex(t, body), out)
	if err != nil {
		t.Fatalf("parseIndex: %v", err)
	}
	if n != 3 {
		t.Errorf("added = %d, want 3", n)
	}

	if got := len(out.releases["foo"]); got != 2 {
		t.Fatalf("foo releases = %d, want 2", got)
	}
	if out.releases["foo"][0].Version != "1.2.3-r0" {
		t.Errorf("foo[0].Version = %q, want 1.2.3-r0", out.releases["foo"][0].Version)
	}
	wantTS := time.Unix(1700000000, 0).UTC()
	if !out.releases["foo"][0].Timestamp.Equal(wantTS) {
		t.Errorf("foo[0].Timestamp = %v, want %v", out.releases["foo"][0].Timestamp, wantTS)
	}
	if got := len(out.releases["bar"]); got != 1 {
		t.Errorf("bar releases = %d, want 1", got)
	}
}

func TestParseIndex_TrailingRecordWithoutBlankLine(t *testing.T) {
	// A record whose final line isn't followed by a blank line should
	// still be flushed — otherwise the very last package in a real
	// APKINDEX would silently drop.
	body := "P:foo\nV:1.0.0-r0\nt:1700000000"
	out := newIndexData()
	if _, err := parseIndex(makeIndex(t, body), out); err != nil {
		t.Fatalf("parseIndex: %v", err)
	}
	if len(out.releases["foo"]) != 1 {
		t.Errorf("expected trailing record to be captured, got %+v", out.releases)
	}
}

func TestParseIndex_SkipsMalformedRecords(t *testing.T) {
	// A record with no version shouldn't be emitted, and a record with
	// no name shouldn't either. Malformed timestamps are ignored (the
	// record is kept but Timestamp is zero).
	body := strings.Join([]string{
		"P:only-name",
		"", // no V
		"V:orphan-version",
		"", // no P
		"P:good",
		"V:1.0.0-r0",
		"t:not-a-number",
		"",
	}, "\n")

	out := newIndexData()
	if _, err := parseIndex(makeIndex(t, body), out); err != nil {
		t.Fatalf("parseIndex: %v", err)
	}
	if _, ok := out.releases["only-name"]; ok {
		t.Errorf("record with no version should not be emitted")
	}
	if len(out.releases) != 1 || out.releases["good"][0].Version != "1.0.0-r0" {
		t.Errorf("expected only 'good' package, got %+v", out.releases)
	}
	if !out.releases["good"][0].Timestamp.IsZero() {
		t.Errorf("malformed timestamp should leave Timestamp zero, got %v", out.releases["good"][0].Timestamp)
	}
}

func TestParseIndex_CapturesProvides(t *testing.T) {
	// A `p:` field carrying a mix of unprefixed (nodejs, nodejs-lts) and
	// prefixed (cmd:node, so:libnode.so.137, pc:node) provides plus a
	// nameonly entry. All versioned entries should reach the store; the
	// nameonly one is dropped since there's nothing to update against.
	body := strings.Join([]string{
		"P:nodejs-24",
		"V:24.14.0-r0",
		"t:1700000000",
		"p:cmd:node=24.14.0-r0 so:libnode.so.137=24.14.0-r0 pc:node=24.14.0-r0 nodejs=24.14.0-r0 nodejs-lts=24.14.0-r0 alsoprovided",
		"",
	}, "\n")

	out := newIndexData()
	if _, err := parseIndex(makeIndex(t, body), out); err != nil {
		t.Fatalf("parseIndex: %v", err)
	}

	// Real package name is still emitted.
	if len(out.releases["nodejs-24"]) != 1 {
		t.Errorf("nodejs-24 missing: %+v", out.releases["nodejs-24"])
	}
	// Every versioned provide — prefixed or not — is captured under its
	// provided name with the provided version.
	for _, name := range []string{"nodejs", "nodejs-lts", "cmd:node", "so:libnode.so.137", "pc:node"} {
		if len(out.releases[name]) != 1 || out.releases[name][0].Version != "24.14.0-r0" {
			t.Errorf("%s provide missing or wrong: %+v", name, out.releases[name])
		}
	}
	// Nameonly provides carry no version to update against.
	if _, ok := out.releases["alsoprovided"]; ok {
		t.Errorf("nameonly provide should not be in the store")
	}
}

func TestParseIndex_SkipsUnversionedProvides(t *testing.T) {
	// apk uses `=0` in `p:` fields to declare an unversioned provide
	// (e.g. python-3.14 provides `python-3=0` to advertise itself as a
	// python-3 flavor without pinning a specific version). Those
	// shouldn't reach the store — they'd surface as bogus "version 0"
	// entries in the /releases response.
	body := strings.Join([]string{
		"P:python-3.14",
		"V:3.14.6-r0",
		"t:1700000000",
		"p:python-3=0 python-3.14=3.14.6-r0",
		"",
	}, "\n")

	out := newIndexData()
	if _, err := parseIndex(makeIndex(t, body), out); err != nil {
		t.Fatalf("parseIndex: %v", err)
	}
	if _, ok := out.releases["python-3"]; ok {
		t.Errorf("python-3=0 (unversioned provide) should be skipped, got %+v", out.releases["python-3"])
	}
	if len(out.releases["python-3.14"]) != 2 {
		t.Errorf("python-3.14 expected 2 entries (real package + versioned provide), got %+v", out.releases["python-3.14"])
	}
}

func TestDedupe(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0).UTC() // older
	t1 := time.Unix(1_700_000_100, 0).UTC() // newer

	merged := &indexData{
		releases: map[string][]Release{
			// Same version across two sources with different timestamps →
			// the later timestamp wins.
			"git": {
				{Version: "2.55.0-r3", Timestamp: t0},
				{Version: "2.55.0-r3", Timestamp: t1},
				{Version: "2.55.0-r2", Timestamp: t0},
			},
			// Distinct versions are all kept.
			"openssl": {
				{Version: "3.6.3-r0", Timestamp: t0},
				{Version: "3.6.3-r1", Timestamp: t1},
			},
			// Single-entry packages skip the map allocation and pass
			// through unchanged.
			"solo": {{Version: "1.0.0-r0", Timestamp: t0}},
		},
	}

	dedupe(merged)

	if got := len(merged.releases["git"]); got != 2 {
		t.Errorf("git deduped len = %d, want 2 (%+v)", got, merged.releases["git"])
	}
	var gotGitR3 Release
	for _, r := range merged.releases["git"] {
		if r.Version == "2.55.0-r3" {
			gotGitR3 = r
		}
	}
	if !gotGitR3.Timestamp.Equal(t1) {
		t.Errorf("git 2.55.0-r3 kept older timestamp %v, want %v", gotGitR3.Timestamp, t1)
	}
	if got := len(merged.releases["openssl"]); got != 2 {
		t.Errorf("openssl len = %d, want 2 (nothing to dedupe)", got)
	}
	if got := len(merged.releases["solo"]); got != 1 {
		t.Errorf("solo len = %d, want 1", got)
	}
}

func TestNewIndexStoreWithRefresh_NoReposReturnsEmptyStore(t *testing.T) {
	// Empty repo slice means Load finds zero installed sources and
	// zero errors, so the loader treats that as a successful no-op
	// rather than a failure. The returned store is empty but usable.
	// interval=0 skips the background loop entirely (no goroutine
	// leak).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := NewIndexStoreWithRefresh(ctx, "x86_64", nil, 0, log)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if store == nil {
		t.Fatal("store should be non-nil even on empty repo list")
	}
	if store.Len() != 0 {
		t.Errorf("empty store Len = %d, want 0", store.Len())
	}
	if got := store.Get("anything"); got != nil {
		t.Errorf("Get on empty store = %+v, want nil", got)
	}
}

func TestIndexStore_GetAndReplace(t *testing.T) {
	s := NewIndexStore()
	if got := s.Get("missing"); got != nil {
		t.Errorf("Get on empty store = %v, want nil", got)
	}
	s.Replace(map[string][]Release{
		"foo": {{Version: "1", Timestamp: time.Unix(1, 0)}},
	})
	got := s.Get("foo")
	if len(got) != 1 || got[0].Version != "1" {
		t.Fatalf("Get after Replace = %+v, want foo@1", got)
	}
	// Mutating the returned slice must not affect the store.
	got[0].Version = "mutated"
	if s.Get("foo")[0].Version != "1" {
		t.Errorf("Get should return a copy; store was mutated")
	}
}
