package chart

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

func TestFlatten(t *testing.T) {
	// Predicate abbreviated from a real chart-lock attestation.
	const predicate = `{
	  "chart": {"package": "chart-foo", "ref": "cgr.dev/x/charts/foo@sha256:aaa"},
	  "images": {
	    "refs": {
	      "alertmanager": {"digest": "sha256:aaa", "repoName": "prometheus-alertmanager", "tag": "latest"},
	      "prometheus":   {"digest": "sha256:bbb", "repoName": "prometheus", "tag": "latest"}
	    },
	    "template": {
	      "images": {
	        "alertmanager": {"requirement": "required"},
	        "prometheus":   {"requirement": "required"}
	      }
	    },
	    "subcharts": {
	      "grafana": {
	        "refs": {
	          "grafana": {"digest": "sha256:ccc", "repoName": "grafana", "tag": "latest"}
	        },
	        "template": {
	          "images": {"grafana": {"requirement": "optional"}}
	        }
	      }
	    }
	  }
	}`
	var lock ChartLock
	if err := json.Unmarshal([]byte(predicate), &lock); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	flat := lock.Flatten()
	sort.Slice(flat, func(i, j int) bool {
		if p := strings.Join(flat[i].Path, "/"); p != strings.Join(flat[j].Path, "/") {
			return p < strings.Join(flat[j].Path, "/")
		}
		return flat[i].LogicalName < flat[j].LogicalName
	})

	want := []LockedImage{
		{LogicalName: "alertmanager", RepoName: "prometheus-alertmanager", Digest: "sha256:aaa", Tag: "latest", Requirement: "required"},
		{LogicalName: "prometheus", RepoName: "prometheus", Digest: "sha256:bbb", Tag: "latest", Requirement: "required"},
		{Path: []string{"grafana"}, LogicalName: "grafana", RepoName: "grafana", Digest: "sha256:ccc", Tag: "latest", Requirement: "optional"},
	}
	if len(flat) != len(want) {
		t.Fatalf("got %d images, want %d: %+v", len(flat), len(want), flat)
	}
	for i, w := range want {
		g := flat[i]
		if !equalPath(g.Path, w.Path) || g.LogicalName != w.LogicalName || g.RepoName != w.RepoName ||
			g.Digest != w.Digest || g.Tag != w.Tag || g.Requirement != w.Requirement {
			t.Errorf("image[%d] = %+v, want %+v", i, g, w)
		}
	}
}

func TestFlatten_Nil(t *testing.T) {
	var l *ChartLock
	if got := l.Flatten(); got != nil {
		t.Errorf("nil ChartLock should Flatten to nil, got %+v", got)
	}
}

func equalPath(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
