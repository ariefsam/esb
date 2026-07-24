package ui

import "embed"

// templatesFS holds the html/template files. Each body template is
// registered with a top-level {{define}} so the layout template can
// pick the right body via the per-request helper in server.go.
//
//go:embed templates/*.html
var templatesFS embed.FS

// staticFS holds the CSS and helper JS shipped with the binary. The
// binary must run offline, so we embed everything instead of pointing
// at a CDN.
//
//go:embed static/app.css static/app.js
var staticFS embed.FS