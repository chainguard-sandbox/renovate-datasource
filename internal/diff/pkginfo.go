package diff

import (
	"bufio"
	"bytes"
	"strconv"
	"strings"
)

// PKGINFOMetadata is the subset of .PKGINFO fields we surface as
// structured data on the apk version page. Every field is optional —
// older apks may omit some entries, and .PKGINFO is missing entirely
// on packages that predate its inclusion.
type PKGINFOMetadata struct {
	Description string `json:"description,omitempty"`
	Arch        string `json:"arch,omitempty"`
	License     string `json:"license,omitempty"`
	// Size is the installed size in bytes, as reported by .PKGINFO.
	// The UI is responsible for humanising it (KB/MB/GB).
	Size int64 `json:"size,omitempty"`
	// BuildDate is Unix seconds — the epoch at which melange built
	// the apk. Rendered as an ISO 8601 timestamp client-side.
	BuildDate int64 `json:"buildDate,omitempty"`
}

// parsePKGINFO extracts the metadata fields we care about. .PKGINFO
// is a plain `key = value` line format; comment lines (leading `#`),
// malformed lines, and repeated keys we don't recognise are silently
// ignored so a garbled entry can't take down the whole response.
func parsePKGINFO(data []byte) PKGINFOMetadata {
	var m PKGINFOMetadata
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "pkgdesc":
			m.Description = v
		case "arch":
			m.Arch = v
		case "license":
			m.License = v
		case "size":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				m.Size = n
			}
		case "builddate":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				m.BuildDate = n
			}
		}
	}
	return m
}
