package chainguard

import (
	"context"
	"fmt"
)

// maxGroupDepth caps recursion depth in walkRepos. Chainguard org
// hierarchies are shallow (subgroups nest a handful of levels in
// practice) so 16 is well beyond real use and low enough that a
// malformed IAM response can't consume the process stack or authenticated
// API budget.
const maxGroupDepth = 16

// groupChild is the minimal shape walkRepos needs from an IAM
// subgroup: a stable ID to recurse into and a name to fold into the
// output path prefix.
type groupChild struct {
	ID   string
	Name string
}

type (
	listReposFn  func(ctx context.Context, parentUIDP string) ([]string, error)
	listGroupsFn func(ctx context.Context, parentUIDP string) ([]groupChild, error)
)

// walkRepos returns every repo path reachable from rootUIDP,
// walking IAM subgroups depth-first. A visited-set guards against
// cyclic subgroup responses and maxGroupDepth guards against
// unexpectedly deep trees; both fail loudly rather than degrading
// silently.
func walkRepos(ctx context.Context, rootUIDP string, listRepos listReposFn, listGroups listGroupsFn) ([]string, error) {
	var out []string
	visited := map[string]struct{}{}
	if err := walkReposStep(ctx, rootUIDP, "", 0, visited, listRepos, listGroups, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func walkReposStep(ctx context.Context, parentUIDP, prefix string, depth int, visited map[string]struct{}, listRepos listReposFn, listGroups listGroupsFn, out *[]string) error {
	if depth > maxGroupDepth {
		return fmt.Errorf("group traversal exceeded depth %d at %s", maxGroupDepth, parentUIDP)
	}
	if _, seen := visited[parentUIDP]; seen {
		return fmt.Errorf("group traversal cycle detected at %s", parentUIDP)
	}
	visited[parentUIDP] = struct{}{}

	repos, err := listRepos(ctx, parentUIDP)
	if err != nil {
		return fmt.Errorf("listing repos under %s: %w", parentUIDP, err)
	}
	for _, r := range repos {
		name := r
		if prefix != "" {
			name = prefix + "/" + r
		}
		*out = append(*out, name)
	}

	groups, err := listGroups(ctx, parentUIDP)
	if err != nil {
		return fmt.Errorf("listing subgroups under %s: %w", parentUIDP, err)
	}
	for _, g := range groups {
		childPrefix := g.Name
		if prefix != "" {
			childPrefix = prefix + "/" + g.Name
		}
		if err := walkReposStep(ctx, g.ID, childPrefix, depth+1, visited, listRepos, listGroups, out); err != nil {
			return err
		}
	}
	return nil
}
