package main

import "testing"

func TestParseDatasources(t *testing.T) {
	tests := []struct {
		name       string
		in         []string
		wantRepo   bool
		wantAPK    bool
		wantErr    bool
	}{
		{"default both", []string{"repo", "apk"}, true, true, false},
		{"repo only", []string{"repo"}, true, false, false},
		{"apk only", []string{"apk"}, false, true, false},
		{"duplicates fold", []string{"repo", "repo", "apk"}, true, true, false},
		{"empty rejected", nil, false, false, true},
		{"unknown rejected", []string{"repo", "helm"}, false, false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, apk, err := parseDatasources(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if repo != tc.wantRepo || apk != tc.wantAPK {
				t.Errorf("got (repo=%v, apk=%v), want (repo=%v, apk=%v)", repo, apk, tc.wantRepo, tc.wantAPK)
			}
		})
	}
}
