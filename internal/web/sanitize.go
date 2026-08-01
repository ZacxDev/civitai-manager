package web

import (
	"regexp"

	"github.com/microcosm-cc/bluemonday"
)

// descPolicy sanitizes author-authored model description HTML. Model
// descriptions on CivitAI are user-generated rich text (links, headings, lists,
// images, formatting), so they are rendered as HTML — but the raw markup is
// UNTRUSTED and MUST be sanitized before it reaches a page, or a malicious
// description could inject a <script>, an onerror= handler, or a javascript: URL
// into the local UI. bluemonday's UGCPolicy is the standard pure-Go allow-list
// sanitizer for exactly this "user-generated content" case: it keeps safe
// formatting tags and http(s)/mailto links while stripping scripts, event
// handlers, and dangerous URL schemes.
var descPolicy = bluemonday.UGCPolicy()

// descHeadingRE matches a remote description's <h1>/<h2> OPEN OR CLOSE tag, with
// or without attributes. It runs over bluemonday's OUTPUT, which is tokenizer-
// normalised (lowercase tag names, no stray whitespace inside the tag), so matching
// tag names with a regex here is safe in a way it would never be on raw input.
//
// The `[ >]` is load-bearing and NOT redundant: see demoteDescriptionHeadings.
var descHeadingRE = regexp.MustCompile(`<(/?)h[12]([ >])`)

// demoteDescriptionHeadings rewrites a sanitized description's <h1>/<h2> to <h3>.
//
// 🔴 WHY: bluemonday's UGCPolicy ALLOWS h1–h6, so a CivitAI description injects its
// own <h1> straight into our page. Measured on the live instance: /models/1386234
// and /models/4384 each rendered TWO <h1>s, one of them arbitrary remote text. That
// breaks the heading outline on the richest page in the app — a screen-reader user
// gets two competing level-1 headings — and descriptions render inside a card under
// an <h2>, so h3 is the level that actually nests.
//
// Demotion, not removal: SkipElementsContent("h1") would delete the author's heading
// TEXT as well, which is real content. Nothing about safety changes here — the input
// is already sanitized; only the outline is repaired.
//
// ⚠ IT CANNOT BE A PLAIN STRING REPLACE, AND THE UPSTREAM COMMENT SAYS OTHERWISE.
// policies.go claims h1–h6 "are permitted and take no attributes". Measured against
// bluemonday v1.0.27, that is FALSE:
//
//	<h1 class="x" id="y" onclick="alert(1)">Attrs</h1>  ->  <h1 id="y">Attrs</h1>
//
// `id` survives. So `strings.ReplaceAll(s, "<h1>", "<h3>")` silently misses every
// heading that carries one — which is the common case in rich descriptions. The
// regex matches the tag NAME and leaves the attributes alone, and `[ >]` is what
// stops it also rewriting a hypothetical <h1x>.
//
// (The surviving `id` is a separate, pre-existing concern: a remote description can
// mint an element id that collides with one of ours. Out of scope here — recorded so
// the next reader does not mistake this function for having addressed it.)
func demoteDescriptionHeadings(s string) string {
	return descHeadingRE.ReplaceAllString(s, "<${1}h3${2}")
}

// sanitizeDescription returns model-description HTML with all unsafe markup
// removed and its headings demoted below the page's own outline. The result is
// safe to render verbatim (via g.Raw).
func sanitizeDescription(raw string) string {
	return demoteDescriptionHeadings(descPolicy.Sanitize(raw))
}
