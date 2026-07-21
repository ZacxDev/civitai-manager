package web

import "embed"

// assetsFS holds the self-contained, embedded static assets (the Tailwind build
// output and the vendored htmx bundle). Nothing is loaded from an external CDN,
// so the UI works fully offline.
//
//go:embed assets/output.css assets/htmx.min.js
var assetsFS embed.FS
