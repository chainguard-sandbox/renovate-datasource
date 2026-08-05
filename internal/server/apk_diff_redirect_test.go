package server

import (
	"testing"

	"github.com/chainguard-demo/cookbook/renovate-datasource/internal/apk"
)

func TestRedirectTargetIfResolvedElsewhere(t *testing.T) {
	pv := func(name, version string) apk.PackageVersion {
		return apk.PackageVersion{Name: name, Version: version}
	}

	nodejs22 := pv("nodejs-22", "22.14.0-r0")
	nodejs26 := pv("nodejs-26", "26.4.0-r1")
	cmdNode := pv("cmd:node", "22.14.0-r0")
	cmdNodeTo := pv("cmd:node", "26.4.0-r1")

	tests := []struct {
		name       string
		from, to   apk.PackageVersion
		fromCands  []apk.PackageVersion
		toCands    []apk.PackageVersion
		wantRFrom  apk.PackageVersion
		wantRTo    apk.PackageVersion
		wantRedir  bool
	}{
		{
			name:      "both sides real self-resolve — no redirect",
			from:      nodejs22,
			to:        nodejs26,
			fromCands: []apk.PackageVersion{nodejs22},
			toCands:   []apk.PackageVersion{nodejs26},
			wantRFrom: nodejs22, wantRTo: nodejs26, wantRedir: false,
		},
		{
			name:      "both sides resolve to a different provider — redirect on both",
			from:      cmdNode,
			to:        cmdNodeTo,
			fromCands: []apk.PackageVersion{nodejs22},
			toCands:   []apk.PackageVersion{nodejs26},
			wantRFrom: nodejs22, wantRTo: nodejs26, wantRedir: true,
		},
		{
			name:      "only from resolves elsewhere",
			from:      cmdNode,
			to:        nodejs26,
			fromCands: []apk.PackageVersion{nodejs22},
			toCands:   []apk.PackageVersion{nodejs26},
			wantRFrom: nodejs22, wantRTo: nodejs26, wantRedir: true,
		},
		{
			name:      "only to resolves elsewhere",
			from:      nodejs22,
			to:        cmdNodeTo,
			fromCands: []apk.PackageVersion{nodejs22},
			toCands:   []apk.PackageVersion{nodejs26},
			wantRFrom: nodejs22, wantRTo: nodejs26, wantRedir: true,
		},
		{
			name:      "no candidates — pass through, no redirect",
			from:      nodejs22,
			to:        nodejs26,
			fromCands: nil,
			toCands:   nil,
			wantRFrom: nodejs22, wantRTo: nodejs26, wantRedir: false,
		},
		{
			name:      "multi-candidate side — pass through on that side",
			from:      cmdNode,
			to:        nodejs26,
			fromCands: []apk.PackageVersion{nodejs22, pv("nodejs-24", "24.0.0-r0")},
			toCands:   []apk.PackageVersion{nodejs26},
			wantRFrom: cmdNode, wantRTo: nodejs26, wantRedir: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotFrom, gotTo, gotRedir := redirectTargetIfResolvedElsewhere(tc.from, tc.to, tc.fromCands, tc.toCands)
			if gotFrom != tc.wantRFrom {
				t.Errorf("rFrom = %+v; want %+v", gotFrom, tc.wantRFrom)
			}
			if gotTo != tc.wantRTo {
				t.Errorf("rTo = %+v; want %+v", gotTo, tc.wantRTo)
			}
			if gotRedir != tc.wantRedir {
				t.Errorf("redirected = %v; want %v", gotRedir, tc.wantRedir)
			}
		})
	}
}
