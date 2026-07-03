package diff

import (
	"cmp"
	"net/url"
	"slices"
	"strings"
)

// GitCheckoutChanges buckets git-checkout pipeline differences between
// two melange.yaml files. Each apk typically pins one or two source
// repos; subpackages can pin additional ones.
type GitCheckoutChanges struct {
	Added   []GitCheckoutEntry `json:"added"`
	Removed []GitCheckoutEntry `json:"removed"`
	Updated []GitCheckoutDelta `json:"updated"`
}

// GitCheckoutEntry describes one git-checkout pipeline step as it
// appears in a melange.yaml.
type GitCheckoutEntry struct {
	Repository string `json:"repository"`     // canonical https URL, no .git suffix
	Host       string `json:"host,omitempty"` // e.g. "github.com"
	Path       string `json:"path,omitempty"` // e.g. "nodejs/node"
	Tag        string `json:"tag,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
	TagURL     string `json:"tagUrl,omitempty"`    // GitHub release page for Tag
	CommitURL  string `json:"commitUrl,omitempty"` // GitHub commit page for Commit
}

// GitCheckoutDelta describes a git-checkout entry that's present on
// both sides but whose pinned tag or commit changed. Both sides carry
// their own resolved URLs so the UI can hyperlink each tag/commit
// independently; CompareURL spans the two commits.
type GitCheckoutDelta struct {
	Repository string           `json:"repository"`
	Host       string           `json:"host,omitempty"`
	Path       string           `json:"path,omitempty"`
	From       GitCheckoutSide  `json:"from"`
	To         GitCheckoutSide  `json:"to"`
	CompareURL string           `json:"compareUrl,omitempty"`
}

// GitCheckoutSide carries the pinned tag, branch, commit and the
// derived release / commit URLs for one side of a git-checkout diff.
type GitCheckoutSide struct {
	Tag       string `json:"tag,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Commit    string `json:"commit,omitempty"`
	TagURL    string `json:"tagUrl,omitempty"`
	CommitURL string `json:"commitUrl,omitempty"`
}

// diffGitCheckouts pairs from/to git-checkout entries by canonical
// repository URL and surfaces add/remove/update buckets. Returns nil
// when there are no changes so the JSON field can be omitted.
func diffGitCheckouts(from, to parsedMelange) *GitCheckoutChanges {
	fromIdx := indexByRepo(extractGitCheckouts(from))
	toIdx := indexByRepo(extractGitCheckouts(to))

	out := &GitCheckoutChanges{
		Added:   []GitCheckoutEntry{},
		Removed: []GitCheckoutEntry{},
		Updated: []GitCheckoutDelta{},
	}

	for repo, f := range fromIdx {
		t, ok := toIdx[repo]
		if !ok {
			out.Removed = append(out.Removed, decorate(f))
			continue
		}
		if f.Tag == t.Tag && f.Commit == t.Commit {
			continue
		}
		out.Updated = append(out.Updated, buildDelta(f, t))
	}
	for repo, t := range toIdx {
		if _, ok := fromIdx[repo]; !ok {
			out.Added = append(out.Added, decorate(t))
		}
	}

	slices.SortFunc(out.Added, func(a, b GitCheckoutEntry) int { return cmp.Compare(a.Repository, b.Repository) })
	slices.SortFunc(out.Removed, func(a, b GitCheckoutEntry) int { return cmp.Compare(a.Repository, b.Repository) })
	slices.SortFunc(out.Updated, func(a, b GitCheckoutDelta) int { return cmp.Compare(a.Repository, b.Repository) })

	if len(out.Added) == 0 && len(out.Removed) == 0 && len(out.Updated) == 0 {
		return nil
	}
	return out
}

// buildDelta computes the diff entry for a single repo, populating each
// side's tag/commit and its derived URLs. Per-side URLs are always
// generated when the value is present so the UI can render every tag
// and commit as a navigable link. CompareURL is only set when both
// commits are known and differ.
func buildDelta(f, t GitCheckoutEntry) GitCheckoutDelta {
	d := GitCheckoutDelta{
		Repository: t.Repository,
		Host:       t.Host,
		Path:       t.Path,
		From:       newSide(f),
		To:         newSide(t),
	}
	if f.Commit != "" && t.Commit != "" && f.Commit != t.Commit {
		d.CompareURL = githubCompareURL(t.Host, t.Path, f.Commit, t.Commit)
	}
	return d
}

// newSide turns one parsed entry into a GitCheckoutSide with its
// release / commit URLs populated where derivable.
func newSide(e GitCheckoutEntry) GitCheckoutSide {
	return GitCheckoutSide{
		Tag:       e.Tag,
		Branch:    e.Branch,
		Commit:    e.Commit,
		TagURL:    githubTagURL(e.Host, e.Path, e.Tag),
		CommitURL: githubCommitURL(e.Host, e.Path, e.Commit),
	}
}

// decorate fills the TagURL / CommitURL fields on a standalone entry
// used in the Added / Removed buckets, so the UI can link out without
// re-deriving them.
func decorate(e GitCheckoutEntry) GitCheckoutEntry {
	if e.Tag != "" {
		e.TagURL = githubTagURL(e.Host, e.Path, e.Tag)
	}
	if e.Commit != "" {
		e.CommitURL = githubCommitURL(e.Host, e.Path, e.Commit)
	}
	return e
}

func indexByRepo(entries []GitCheckoutEntry) map[string]GitCheckoutEntry {
	m := make(map[string]GitCheckoutEntry, len(entries))
	for _, e := range entries {
		// Last-write-wins is fine; melange yamls rarely (never?) carry
		// two git-checkout steps targeting the same repository.
		m[e.Repository] = e
	}
	return m
}

// extractGitCheckouts walks an already-parsed melange.yaml looking for
// any `uses: git-checkout` step under any `pipeline:` (top-level or
// inside any subpackage's pipeline) and returns the extracted entries.
// Parse errors are absorbed by parseMelange so this never errors.
func extractGitCheckouts(m parsedMelange) []GitCheckoutEntry {
	var out []GitCheckoutEntry
	for _, p := range m.pipelines() {
		out = appendFromPipeline(out, p)
	}
	return out
}

// appendFromPipeline walks a pipeline (slice of step maps) recursively;
// each step may itself carry a nested `pipeline` of further steps.
func appendFromPipeline(out []GitCheckoutEntry, v any) []GitCheckoutEntry {
	for _, step := range asSlice(v) {
		m, ok := step.(map[string]any)
		if !ok {
			continue
		}
		if asString(m["uses"]) == "git-checkout" {
			if e, ok := buildEntry(m["with"]); ok {
				out = append(out, e)
			}
		}
		// Recurse into any nested pipeline (melange supports composite
		// steps that group sub-steps under a `pipeline:` key).
		out = appendFromPipeline(out, m["pipeline"])
	}
	return out
}

func buildEntry(with any) (GitCheckoutEntry, bool) {
	m, ok := with.(map[string]any)
	if !ok {
		return GitCheckoutEntry{}, false
	}
	repo := canonicalRepoURL(asString(m["repository"]))
	if repo == "" {
		return GitCheckoutEntry{}, false
	}
	host, path := repoHostAndPath(repo)
	return GitCheckoutEntry{
		Repository: repo,
		Host:       host,
		Path:       path,
		Tag:        asString(m["tag"]),
		Branch:     asString(m["branch"]),
		Commit:     asString(m["expected-commit"]),
	}, true
}

// canonicalRepoURL strips the .git suffix and any trailing slash so two
// references to the same repository compare equal across versions.
func canonicalRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// repoHostAndPath parses a repository URL into (host, path). Only
// http/https schemes produce a (host, path) — anything else (ssh://,
// file://, or — critically — javascript: / data:) returns empty
// strings so the UI never renders a clickable link to it. Hosts are
// lowercased so case-mismatched references to the same repo collapse
// to one entry in the diff index.
func repoHostAndPath(repo string) (string, string) {
	u, err := url.Parse(repo)
	if err != nil || u.Host == "" {
		return "", ""
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		// safe to surface as a link
	default:
		return "", ""
	}
	return strings.ToLower(u.Host), strings.TrimPrefix(u.Path, "/")
}

// githubTagURL builds the release page URL for a given (host, path,
// tag). Only GitHub and GitLab are supported; other hosts return "".
func githubTagURL(host, path, tag string) string {
	if path == "" || tag == "" {
		return ""
	}
	switch host {
	case "github.com":
		return "https://github.com/" + path + "/releases/tag/" + tag
	case "gitlab.com":
		return "https://gitlab.com/" + path + "/-/tags/" + tag
	}
	return ""
}

func githubCommitURL(host, path, commit string) string {
	if path == "" || commit == "" {
		return ""
	}
	switch host {
	case "github.com":
		return "https://github.com/" + path + "/commit/" + commit
	case "gitlab.com":
		return "https://gitlab.com/" + path + "/-/commit/" + commit
	}
	return ""
}

func githubCompareURL(host, path, from, to string) string {
	if path == "" || from == "" || to == "" {
		return ""
	}
	switch host {
	case "github.com":
		return "https://github.com/" + path + "/compare/" + from + "..." + to
	case "gitlab.com":
		return "https://gitlab.com/" + path + "/-/compare/" + from + "..." + to
	}
	return ""
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
