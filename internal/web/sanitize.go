package web

import "github.com/microcosm-cc/bluemonday"

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

// sanitizeDescription returns model-description HTML with all unsafe markup
// removed. The result is safe to render verbatim (via g.Raw).
func sanitizeDescription(raw string) string {
	return descPolicy.Sanitize(raw)
}
