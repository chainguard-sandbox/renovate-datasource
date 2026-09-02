package chainguard

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// walkFakes wires up in-memory list functions for walkRepos tests.
// Each entry keys off the group UIDP being enumerated.
type walkFakes struct {
	repos     map[string][]string
	subgroups map[string][]groupChild
	reposErr  map[string]error
	groupsErr map[string]error
}

func (f *walkFakes) listRepos() listReposFn {
	return func(_ context.Context, uidp string) ([]string, error) {
		if err, ok := f.reposErr[uidp]; ok {
			return nil, err
		}
		return f.repos[uidp], nil
	}
}

func (f *walkFakes) listGroups() listGroupsFn {
	return func(_ context.Context, uidp string) ([]groupChild, error) {
		if err, ok := f.groupsErr[uidp]; ok {
			return nil, err
		}
		return f.subgroups[uidp], nil
	}
}

func TestWalkRepos_FlatOrg(t *testing.T) {
	f := &walkFakes{
		repos: map[string][]string{
			"root": {"python", "curl"},
		},
	}
	got, err := walkRepos(context.Background(), "root", f.listRepos(), f.listGroups())
	if err != nil {
		t.Fatalf("walkRepos: %v", err)
	}
	want := []string{"python", "curl"}
	if !equalSets(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestWalkRepos_NestedSubgroupsProduceSlashPaths(t *testing.T) {
	f := &walkFakes{
		repos: map[string][]string{
			"root":    {"python"},
			"g-chart": {"nginx", "postgresql"},
			"g-deep":  {"leaf"},
		},
		subgroups: map[string][]groupChild{
			"root":    {{ID: "g-chart", Name: "charts"}},
			"g-chart": {{ID: "g-deep", Name: "extras"}},
		},
	}
	got, err := walkRepos(context.Background(), "root", f.listRepos(), f.listGroups())
	if err != nil {
		t.Fatalf("walkRepos: %v", err)
	}
	want := []string{"python", "charts/nginx", "charts/postgresql", "charts/extras/leaf"}
	if !equalSets(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestWalkRepos_CycleIsDetected(t *testing.T) {
	// gA -> gB -> gA. Without cycle detection this would recurse
	// forever and exhaust CPU / API budget.
	f := &walkFakes{
		subgroups: map[string][]groupChild{
			"root": {{ID: "gA", Name: "a"}},
			"gA":   {{ID: "gB", Name: "b"}},
			"gB":   {{ID: "gA", Name: "a-again"}},
		},
	}
	_, err := walkRepos(context.Background(), "root", f.listRepos(), f.listGroups())
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("err = %v, want cycle-related", err)
	}
}

func TestWalkRepos_DepthCapEnforced(t *testing.T) {
	// Build a strictly linear chain deeper than maxGroupDepth.
	subgroups := map[string][]groupChild{}
	prev := "root"
	for i := 0; i <= maxGroupDepth+1; i++ {
		next := "g" + string(rune('A'+i))
		subgroups[prev] = []groupChild{{ID: next, Name: next}}
		prev = next
	}
	f := &walkFakes{subgroups: subgroups}

	_, err := walkRepos(context.Background(), "root", f.listRepos(), f.listGroups())
	if err == nil {
		t.Fatal("expected depth error, got nil")
	}
	if !strings.Contains(err.Error(), "depth") {
		t.Errorf("err = %v, want depth-related", err)
	}
}

func TestWalkRepos_ListReposErrorIsWrapped(t *testing.T) {
	sentinel := errors.New("upstream boom")
	f := &walkFakes{
		reposErr: map[string]error{"root": sentinel},
	}
	_, err := walkRepos(context.Background(), "root", f.listRepos(), f.listGroups())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wraps %v", err, sentinel)
	}
}

func TestWalkRepos_ListGroupsErrorIsWrapped(t *testing.T) {
	sentinel := errors.New("iam boom")
	f := &walkFakes{
		groupsErr: map[string]error{"root": sentinel},
	}
	_, err := walkRepos(context.Background(), "root", f.listRepos(), f.listGroups())
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wraps %v", err, sentinel)
	}
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}
