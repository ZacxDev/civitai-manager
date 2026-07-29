package uxaudit

import _ "embed"

// axeSource is the vendored axe-core library (v4.12.1), injected into each page
// to run an accessibility scan. Copied verbatim from auditloop's crawler so the
// pushed a11y numbers are directly comparable to a native auditloop crawl.
//
//go:embed axe.min.js
var axeSource string

// AxeVersion is the vendored axe-core version (kept in sync with axe.min.js).
const AxeVersion = "4.12.1"

// axeRunScript is evaluated (await-promise) after axeSource is injected. It runs
// axe against the whole document and returns the violations as a JSON string.
const axeRunScript = `
(async () => {
  try {
    const r = await axe.run(document, { resultTypes: ['violations'] });
    return JSON.stringify({
      violations: r.violations.map(v => ({
        id: v.id, impact: v.impact, help: v.help, helpUrl: v.helpUrl,
        tags: v.tags, nodeCount: v.nodes.length,
        nodes: v.nodes.slice(0, 5).map(n => ({ target: n.target, html: (n.html||'').slice(0,300) }))
      }))
    });
  } catch (e) {
    return JSON.stringify({ error: String(e), violations: [] });
  }
})()
`
