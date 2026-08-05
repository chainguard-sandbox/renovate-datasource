// Package diffutil holds shared helpers for rendering unified text
// diffs.
package diffutil

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Context matches GNU diff -u: three lines of unchanged context
// before and after each hunk.
const Context = 3

// Unified returns a unified diff of from/to. Identical or both-empty
// inputs return "". The label is embedded in the FromFile/ToFile
// headers alongside the supplied side labels.
func Unified(label, fromLabel, toLabel string, from, to []byte) (string, error) {
	if bytes.Equal(from, to) {
		return "", nil
	}
	out, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        SplitLines(string(from)),
		B:        SplitLines(string(to)),
		FromFile: label + " " + fromLabel,
		ToFile:   label + " " + toLabel,
		Context:  Context,
	})
	if err != nil {
		return "", fmt.Errorf("rendering unified diff for %s: %w", label, err)
	}
	return out, nil
}

// SplitLines preserves trailing newlines per line so the unified-diff
// output stays consistent with `diff -u`. strings.Split on "\n" would
// drop them and produce a degenerate "\ No newline at end of file"
// marker on every hunk.
func SplitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.SplitAfter(s, "\n")
	if last := len(parts) - 1; last >= 0 && parts[last] == "" {
		parts = parts[:last]
	}
	return parts
}
