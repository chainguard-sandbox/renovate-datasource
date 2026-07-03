package server

import (
	_ "embed"
	"net/http"
)

//go:embed templates/common.js
var commonJS string

// diffPageCSS is the shared stylesheet used by every HTML page the server
// renders — the repo diff view and both apk pages — so all three keep a
// consistent look. Lives here rather than in repo_diff.go because it is
// no longer specific to that one page.
//
//go:embed templates/diff.css
var diffPageCSS string

// writeHTMLHeaders sets the Content-Type and a defensive CSP that
// matches how the templates actually load resources: inline script and
// style only, no fetches off-origin (the AJAX hits /v1/...), no
// embedding by other sites. `unsafe-inline` is still required because
// each page embeds its template-substituted variables in a <script>
// block and the styles inline; switch to per-request nonces if that
// changes.
func writeHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; "+
			"script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; "+
			"connect-src 'self'; "+
			"frame-ancestors 'none'; "+
			"base-uri 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// Belt-and-braces HSTS. Browsers ignore it on plain-HTTP responses
	// anyway, so this is harmless when the operator runs the service
	// behind a non-TLS ingress in dev; in production it hardens against
	// a downgrade on the (rare) direct-to-pod fetch path.
	w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
}
