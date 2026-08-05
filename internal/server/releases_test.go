package server

import (
	"testing"
	"time"
)

func TestParseCooldownQuery(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantMsg string
		wantOk  bool
	}{
		{name: "valid short", raw: "168h", want: 168 * time.Hour, wantOk: true},
		{name: "valid at max", raw: "8760h", want: maxCooldown, wantOk: true},
		{name: "zero", raw: "0", want: 0, wantOk: true},
		{name: "negative", raw: "-1h", wantMsg: "non-negative"},
		{name: "malformed", raw: "not-a-duration", wantMsg: "non-negative"},
		{name: "over max", raw: "8761h", wantMsg: "exceeds the maximum"},
		{name: "far over max", raw: "1000000h", wantMsg: "exceeds the maximum"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, msg, ok := parseCooldownQuery(tc.raw)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v (msg=%q)", ok, tc.wantOk, msg)
			}
			if ok && got != tc.want {
				t.Errorf("d = %v, want %v", got, tc.want)
			}
			if !ok && !contains(msg, tc.wantMsg) {
				t.Errorf("msg = %q, want to contain %q", msg, tc.wantMsg)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
