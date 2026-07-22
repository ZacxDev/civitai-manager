package web

import "embed"

// assetsFS holds the self-contained, embedded static assets: the Tailwind build
// output, the vendored htmx bundle, the small app.css cascade bridge, and the
// vendored @civitai/theme + @civitai/components 0.1.1 stylesheets (design tokens
// and the attribute-driven component CSS). Nothing is loaded from an external
// CDN, so the UI works fully offline — the civitai design system is served from
// the binary just like the rest of the assets.
//
//go:embed assets/output.css assets/htmx.min.js assets/app.css assets/civitai-theme.css assets/civitai-components.css
var assetsFS embed.FS
