package web

import (
	"mime"
	"path/filepath"
	"strings"
)

// --- Captured-output media types ---
//
// 🔴 THE RULE: a Content-Type we serve is chosen from the whitelist below and
// NEVER copied from anything ComfyUI said.
//
// Three untrusted strings describe a captured output, and not one of them can be
// trusted into a header:
//
//   - the history entry's `format`, whose REAL observed value from
//     VideoHelperSuite is "video/h264-mp4" — which is not a registered MIME type
//     and which no browser will play. Echoing it produces a video that silently
//     does not work, and more generally puts attacker-chosen text in a response
//     header.
//   - the /view response's own Content-Type, which is whatever the ComfyUI host's
//     mimetypes database guessed. A compromised or merely buggy ComfyUI can return
//     "text/html" for bytes we then serve from OUR origin.
//   - the filename, which is why it is sanitized to a basename first
//     (safePathSegment) before its extension is read.
//
// So resolution is: OUR sanitized extension → a small explicit map of known-bogus
// upstream format strings → the upstream type only if it is already a whitelist
// VALUE → otherwise refuse with application/octet-stream. Refusing is a real
// outcome, not a failure path: the bytes are still stored and downloadable, they
// simply do not get an inline-renderable type they have not earned.

// outputMediaTypeByExt maps a lowercased file extension to the real IANA type we
// are willing to serve inline. Derived from OUR sanitized basename, so it is the
// most trustworthy signal available and is consulted first.
//
// Deliberately conservative. ".mov" is absent: VHS's ProRes output lands there and
// no browser plays ProRes, so labelling it "video/quicktime" would render a black
// <video> box that can never work. Refusing it instead makes the UI show an honest
// "not previewable" placeholder with a download link. Add a type here only when a
// browser can actually render it.
var outputMediaTypeByExt = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
	".avif": "image/avif",
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".webm": "video/webm",
}

// vhsFormatAliases maps the KNOWN-BOGUS `format` strings VideoHelperSuite reports
// onto real MIME types. It is a fallback for the case where the filename carries no
// usable extension — never an override of one.
//
// These keys are exactly the values VHS's own format presets emit. Anything not
// listed here is refused, so a hostile `format` cannot invent a type: it is a
// lookup, not a transformation.
var vhsFormatAliases = map[string]string{
	"video/h264-mp4": "video/mp4",
	"video/h265-mp4": "video/mp4",
	"video/av1-webm": "video/webm",
	"video/webm":     "video/webm",
	"video/mp4":      "video/mp4",
	"image/gif":      "image/gif",
	"image/webp":     "image/webp",
}

// outputMediaTypeAllowed is the set of types we will ever put in a Content-Type
// header for a captured output. Built from outputMediaTypeByExt so the "what may we
// serve" set can never drift from the "what may we label" set.
var outputMediaTypeAllowed = func() map[string]bool {
	m := make(map[string]bool, len(outputMediaTypeByExt))
	for _, ct := range outputMediaTypeByExt {
		m[ct] = true
	}
	return m
}()

// outputMediaTypeRefused is what a type we will not vouch for is stored/served as.
// The bytes remain available; only the inline-renderable label is withheld.
const outputMediaTypeRefused = "application/octet-stream"

// outputMediaType resolves the Content-Type to STORE (and later serve) for one
// captured output. See the file header for why each input is untrusted.
//
// filename is the comfy-supplied name (sanitized to a basename here, so a hostile
// "../../x.png" cannot smuggle a different extension in through a directory part).
// format is the history entry's self-reported format. upstreamCT is the /view
// response's Content-Type. Any of them may be empty.
//
// It never returns "" — an unresolvable output gets outputMediaTypeRefused.
func outputMediaType(filename, format, upstreamCT string) string {
	// 1. Our own sanitized extension. Most trustworthy: it is the name we already
	//    validated and the one the bytes are stored under.
	if base, err := safePathSegment(filename); err == nil {
		if ct, ok := outputMediaTypeByExt[strings.ToLower(filepath.Ext(base))]; ok {
			return ct
		}
	}
	// 2. A KNOWN upstream format string, mapped (not echoed) onto a real type.
	if ct, ok := vhsFormatAliases[strings.ToLower(strings.TrimSpace(format))]; ok {
		return ct
	}
	// 3. The upstream Content-Type — accepted ONLY when it is already something we
	//    would have produced ourselves. Parameters (charset, boundary) are stripped
	//    first so "image/png; charset=utf-8" is compared as "image/png"; a value that
	//    will not parse is discarded rather than passed through.
	if mt, _, err := mime.ParseMediaType(strings.TrimSpace(upstreamCT)); err == nil {
		if outputMediaTypeAllowed[strings.ToLower(mt)] {
			return strings.ToLower(mt)
		}
	}
	// 4. Refuse. Do not guess, and above all do not echo.
	return outputMediaTypeRefused
}

// servableOutputContentType is the serving-side gate: it returns the Content-Type
// to emit for a STORED row's content_type, refusing anything outside the whitelist.
//
// The DB is not a trust boundary we get to skip. A row written by an older build
// (or a corrupted one) can hold any string, and this route serves bytes from the
// app's own origin, so the header is re-derived from the whitelist on every request
// rather than taken from the row. Paired with X-Content-Type-Options: nosniff at the
// call site, a refused type can neither render nor be sniffed into something that
// does.
func servableOutputContentType(stored string) string {
	if mt, _, err := mime.ParseMediaType(strings.TrimSpace(stored)); err == nil {
		if outputMediaTypeAllowed[strings.ToLower(mt)] {
			return strings.ToLower(mt)
		}
	}
	return outputMediaTypeRefused
}

// isVideoOutput reports whether a stored content_type is a video we are willing to
// render inline. It checks whitelist membership, NOT a bare "video/" prefix, so a
// row holding "video/h264-mp4" is correctly treated as non-renderable rather than
// handed to a <video> element that can never play it.
func isVideoOutput(contentType string) bool {
	ct := servableOutputContentType(contentType)
	return outputMediaTypeAllowed[ct] && strings.HasPrefix(ct, "video/")
}

// isImageOutput reports whether a stored content_type is a whitelisted image.
// A row with no content_type at all is treated as an image: every pre-video row
// was an image, and InsertGeneration defaults a blank one to image/png.
func isImageOutput(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	ct := servableOutputContentType(contentType)
	return outputMediaTypeAllowed[ct] && strings.HasPrefix(ct, "image/")
}
