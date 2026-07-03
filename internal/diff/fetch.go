package diff

// FetchChanges buckets fetch-pipeline differences between two
// melange.yaml files. fetch steps pull non-git artifacts (tarballs and
// the like) by URI; we surface URI and hash changes without trying to
// link out, since fetch URIs are opaque downloads.
type FetchChanges struct {
	Added   []FetchEntry `json:"added"`
	Removed []FetchEntry `json:"removed"`
	Updated []FetchDelta `json:"updated"`
}

// FetchEntry describes one fetch pipeline step. Hash is prefixed with
// its algorithm — e.g. "sha256:abcd…" — so the UI can render the type
// inline without an extra discriminator field.
type FetchEntry struct {
	URI  string `json:"uri"`
	Hash string `json:"hash,omitempty"`
}

// FetchDelta describes a fetch step that's present on both sides but
// whose URI or hash changed.
type FetchDelta struct {
	FromURI  string `json:"fromUri,omitempty"`
	ToURI    string `json:"toUri,omitempty"`
	FromHash string `json:"fromHash,omitempty"`
	ToHash   string `json:"toHash,omitempty"`
}

// diffFetches pairs fetch steps from both sides positionally — most
// packages carry exactly one fetch, and the URI itself encodes the
// version, so pairing by URL is unhelpful. Returns nil when neither
// side has any change.
func diffFetches(fromMelange, toMelange parsedMelange) *FetchChanges {
	from := extractFetches(fromMelange)
	to := extractFetches(toMelange)

	out := &FetchChanges{
		Added:   []FetchEntry{},
		Removed: []FetchEntry{},
		Updated: []FetchDelta{},
	}

	n := len(from)
	if len(to) < n {
		n = len(to)
	}
	for i := 0; i < n; i++ {
		if from[i].URI == to[i].URI && from[i].Hash == to[i].Hash {
			continue
		}
		out.Updated = append(out.Updated, FetchDelta{
			FromURI:  from[i].URI,
			ToURI:    to[i].URI,
			FromHash: from[i].Hash,
			ToHash:   to[i].Hash,
		})
	}
	for i := n; i < len(from); i++ {
		out.Removed = append(out.Removed, from[i])
	}
	for i := n; i < len(to); i++ {
		out.Added = append(out.Added, to[i])
	}

	if len(out.Added) == 0 && len(out.Removed) == 0 && len(out.Updated) == 0 {
		return nil
	}
	return out
}

// extractFetches walks an already-parsed melange.yaml looking for
// `uses: fetch` pipeline steps (top-level, subpackage pipelines, and
// nested composite pipelines). Parse errors are absorbed by
// parseMelange so this never errors.
func extractFetches(m parsedMelange) []FetchEntry {
	var out []FetchEntry
	for _, p := range m.pipelines() {
		out = appendFetchesFromPipeline(out, p)
	}
	return out
}

func appendFetchesFromPipeline(out []FetchEntry, v any) []FetchEntry {
	for _, step := range asSlice(v) {
		m, ok := step.(map[string]any)
		if !ok {
			continue
		}
		if asString(m["uses"]) == "fetch" {
			if e, ok := buildFetchEntry(m["with"]); ok {
				out = append(out, e)
			}
		}
		out = appendFetchesFromPipeline(out, m["pipeline"])
	}
	return out
}

func buildFetchEntry(with any) (FetchEntry, bool) {
	m, ok := with.(map[string]any)
	if !ok {
		return FetchEntry{}, false
	}
	uri := asString(m["uri"])
	if uri == "" {
		return FetchEntry{}, false
	}
	e := FetchEntry{URI: uri}
	// Prefer the strongest hash present. Real melange yamls usually
	// pick one or the other; this preference is purely defensive.
	if h := asString(m["expected-sha512"]); h != "" {
		e.Hash = "sha512:" + h
	} else if h := asString(m["expected-sha256"]); h != "" {
		e.Hash = "sha256:" + h
	}
	return e, true
}
