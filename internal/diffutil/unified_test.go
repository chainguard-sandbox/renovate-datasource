package diffutil

import "testing"

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
			got := SplitLines(tc.in)
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
