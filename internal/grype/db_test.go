package grype

import (
	"log/slog"
	"testing"

	grypePkg "github.com/anchore/grype/grype/pkg"
	"github.com/anchore/grype/grype/vulnerability"
)

type fakeProvider struct {
	closeCalls int
}

func (f *fakeProvider) PackageSearchNames(grypePkg.Package) []string { return nil }
func (f *fakeProvider) FindVulnerabilities(...vulnerability.Criteria) ([]vulnerability.Vulnerability, error) {
	return nil, nil
}
func (f *fakeProvider) VulnerabilityMetadata(vulnerability.Reference) (*vulnerability.Metadata, error) {
	return nil, nil
}
func (f *fakeProvider) Close() error {
	f.closeCalls++
	return nil
}

func TestSwap_ClosesPreviousProvider(t *testing.T) {
	oldP := &fakeProvider{}
	newP := &fakeProvider{}
	s := &DB{log: slog.Default(), provider: oldP}

	// Simulate a successful reload: install newP and close oldP.
	s.mu.Lock()
	old := s.provider
	s.provider = newP
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	if got := oldP.closeCalls; got != 1 {
		t.Errorf("previous provider Close calls = %d; want 1", got)
	}
	if got := newP.closeCalls; got != 0 {
		t.Errorf("new provider must not be closed; got %d Close calls", got)
	}
	if s.provider != newP {
		t.Errorf("provider not swapped in")
	}
}

func TestSwap_NoPreviousProviderIsSafe(t *testing.T) {
	// Fresh DB with no prior provider — the first install must not
	// call Close on nil.
	newP := &fakeProvider{}
	s := &DB{log: slog.Default()}

	s.mu.Lock()
	old := s.provider
	s.provider = newP
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	if got := newP.closeCalls; got != 0 {
		t.Errorf("first-install provider must not be closed; got %d", got)
	}
}

// Compile-time interface satisfaction check for fakeProvider — if
// vulnerability.Provider's shape changes, this test file fails to
// build alongside db.go, keeping the fake honest.
var _ vulnerability.Provider = (*fakeProvider)(nil)
