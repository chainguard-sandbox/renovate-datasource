package diff

import "gopkg.in/yaml.v3"

// parsedMelange holds the unmarshalled root of a melange.yaml so the
// git-checkout and fetch extractors can walk the same parse-tree
// instead of each calling yaml.Unmarshal on the raw bytes. Malformed
// input degrades to a zero value (empty map) — the extractors return
// empty slices, matching the previous tolerate-and-ignore behaviour.
type parsedMelange struct {
	root map[string]any
}

// parseMelange runs yaml.Unmarshal once. Returns an empty parsedMelange
// for invalid input rather than propagating the error.
func parseMelange(yml []byte) parsedMelange {
	if len(yml) == 0 {
		return parsedMelange{}
	}
	var root map[string]any
	if err := yaml.Unmarshal(yml, &root); err != nil {
		return parsedMelange{}
	}
	return parsedMelange{root: root}
}

// pipelines yields the top-level pipeline plus every subpackage
// pipeline as raw `any` values for the extractors to walk.
func (p parsedMelange) pipelines() []any {
	if p.root == nil {
		return nil
	}
	out := []any{p.root["pipeline"]}
	for _, sub := range asSlice(p.root["subpackages"]) {
		if m, ok := sub.(map[string]any); ok {
			out = append(out, m["pipeline"])
		}
	}
	return out
}
