package comfy

import (
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// A ComfyUI workflow routinely documents its own model downloads in `Note` /
// `MarkdownNote` nodes — a "## Model links" block listing exactly the files the
// graph needs and where the author got them. Nothing else in this codebase reads
// that text, and the ordinary resolution paths structurally cannot find those
// files: CivitAI has no filename search (we do a fuzzy title search plus an
// exact-basename filter), and HuggingFace's `?search=` indexes repo NAMES, so a
// file sitting in a repo whose name shares nothing with the filename is
// unreachable from a query derived from that filename.
//
// 🔴 EXTRACTION MUST RUN ON THE UI GRAPH. `Note` and `MarkdownNote` are in
// convert.go's virtualNodeTypes set and are dropped by UI→API conversion, so by
// the time Preflight has an api graph the notes are GONE. ExtractNoteLinks
// therefore refuses any format other than FormatUI rather than silently
// returning nothing useful for an api graph.
//
// 🔴 NOTE TEXT IS UNTRUSTED AUTHOR CONTENT. A URL out of here is a CANDIDATE,
// never a permission: it carries no more authority than a string typed by a
// stranger. Every consumer must still put it through the same gates as any other
// download URL (scheme assertion + the host allowlist inside a hardened client's
// dialer), and must never hand it to a fetcher that lacks one.

// noteNodeTypes are the UI-graph node types whose widget text is on-canvas prose
// the author wrote for a human reader. It is deliberately a SUBSET of
// convert.go's virtualNodeTypes: a Reroute or a Fast Groups Bypasser is also
// virtual, but its widget values are wiring, not documentation.
var noteNodeTypes = map[string]bool{
	"Note":            true,
	"MarkdownNote":    true,
	"Note Plus (mtb)": true,
}

// A BYPASSED OR MUTED NOTE STILL COUNTS, and that is a deliberate departure from
// how convert.go treats mode on every other node.
//
// The mode rule elsewhere is about EXECUTION: a bypassed node was never going to
// run, so its missing class needs no node pack. A note never executes in any
// mode, so that reasoning does not transfer — mode on a note only greys it out on
// the canvas, and the text stays readable to the author and to anyone who opens
// the graph. Excluding them would silently drop documentation for a reason that
// does not apply, and the direction of the error matters: including a bypassed
// note can only ever offer one extra LINK (still gated identically downstream),
// while excluding one loses the only pointer to a file nothing else can find.
//
// Pinned by TestExtractNoteLinksIncludesBypassedNotes.

// noteMaxTextBytes bounds how much of ONE note's text is scanned. Note text is
// arbitrary attacker-influenced input (a workflow is imported from CivitAI), so
// a graph carrying a megabyte-long note must cost a bounded amount of work, not
// a proportional one. The largest note on the operator's real workflow 590 is
// under 3 KiB; 32 KiB is generous headroom.
const noteMaxTextBytes = 32 * 1024

// noteMaxTotalBytes bounds the text scanned across the WHOLE graph, so a graph
// with thousands of at-cap notes is bounded too. Per-note and total caps are both
// needed: either one alone is trivially evaded by shaping the input the other way.
const noteMaxTotalBytes = 256 * 1024

// noteMaxLinks caps how many links are kept. The UI shows these one per missing
// model file, so a graph offering hundreds is noise at best and a rendering DoS at
// worst.
const noteMaxLinks = 64

// noteURLRe matches an https URL inside prose.
//
// Only `https` is matched, so an `http://` URL is rejected by construction rather
// than by a later check that could be forgotten — the scheme gate is the regex.
//
// The terminator class is tuned for MARKDOWN, which is what these notes actually
// contain: `)` and `]` end a `[text](url)` link, `|` ends a table cell, and the
// quote/angle characters end an HTML attribute. That costs us a URL containing a
// literal `)` — vanishingly rare for a model file, and the failure is a link we
// do not offer rather than a wrong link we do.
var noteURLRe = regexp.MustCompile(`https://[^\s<>"'` + "`" + `()\[\]{}|\\]+`)

// noteURLTrailing is the trailing punctuation trimmed off a match — prose ends
// sentences with these and they are never part of the URL.
const noteURLTrailing = ".,;:!?*_~"

// NoteLink is one https URL found in the text of a Note / MarkdownNote node.
//
// Basename is the last path segment of the URL, percent-DECODED and with the
// query string and fragment already excluded (they are not part of url.URL.Path).
// It is "" when the URL addresses no file (a bare host, a trailing slash, a
// directory-shaped path), which is how a page link — `civitai.com/models/620406`
// — is told apart from a file link. Callers match on Basename; an empty one can
// never equal a model filename, so those links are surfaced as links only.
type NoteLink struct {
	// NodeID is the id of the note node the URL was found in (display/debug).
	NodeID string
	// URL is the full https URL, exactly as written in the note.
	URL string
	// Basename is the URL's final path segment, decoded ("" when there is none).
	Basename string
}

// noteGraph is a MINIMAL, lenient decode of a UI graph: only what note
// extraction reads. It is deliberately NOT uiConvGraph — a graph whose links or
// subgraph definitions are malformed in a way the converter rejects should still
// yield its notes, and this decode has strictly fewer ways to fail.
type noteGraph struct {
	Nodes []noteNode `json:"nodes"`
}

type noteNode struct {
	ID            json.RawMessage `json:"id"`
	Type          string          `json:"type"`
	WidgetsValues json.RawMessage `json:"widgets_values"`
}

// ExtractNoteLinks returns every https URL written in the graph's Note /
// MarkdownNote nodes, de-duplicated by URL with first-seen (document) order
// preserved.
//
// It returns nil for anything that is not a UI-format graph, for a malformed
// graph, and for a graph with no notes — a malformed or hostile graph yields
// NOTHING rather than an error, because this is an advisory aid and no caller
// should fail a run over it. Every path through it is bounded (see the
// noteMax* constants), so it cannot be made to hang or to allocate without limit.
//
// KNOWN LIMIT: notes living inside a SUBGRAPH definition
// (`definitions.subgraphs[].nodes`) are not scanned. The extractor reads the
// top-level node list only; flattening subgraphs here would pull in the whole
// converter for prose that, in every real workflow examined, sits on the root
// canvas.
func ExtractNoteLinks(format string, graph json.RawMessage) []NoteLink {
	if format != FormatUI {
		return nil
	}
	var g noteGraph
	if err := json.Unmarshal(graph, &g); err != nil {
		return nil
	}

	var out []NoteLink
	seen := map[string]bool{}
	budget := noteMaxTotalBytes
	for i := range g.Nodes {
		if len(out) >= noteMaxLinks || budget <= 0 {
			break
		}
		n := &g.Nodes[i]
		if !noteNodeTypes[n.Type] {
			continue
		}
		id := idToString(n.ID)
		for _, text := range noteTexts(n.WidgetsValues) {
			if len(out) >= noteMaxLinks || budget <= 0 {
				break
			}
			scanned, used := boundedText(text, budget)
			budget -= used
			for _, raw := range noteURLRe.FindAllString(scanned, -1) {
				if len(out) >= noteMaxLinks {
					break
				}
				u, base, ok := parseNoteURL(raw)
				if !ok || seen[u] {
					continue
				}
				seen[u] = true
				out = append(out, NoteLink{NodeID: id, URL: u, Basename: base})
			}
		}
	}
	return out
}

// boundedText clips text to the per-note cap AND to the remaining whole-graph
// budget, returning the slice to scan and how much budget it consumed.
//
// When it clips, it drops back to the last whitespace so a URL cannot be cut in
// half and matched as a shorter, DIFFERENT, still-well-formed URL — which is the
// one way truncation could invent a link the author never wrote. A clipped chunk
// with no whitespace at all is dropped entirely for the same reason.
func boundedText(text string, budget int) (string, int) {
	limit := noteMaxTextBytes
	if budget < limit {
		limit = budget
	}
	if len(text) <= limit {
		return text, len(text)
	}
	clipped := text[:limit]
	i := strings.LastIndexAny(clipped, " \t\r\n")
	if i < 0 {
		return "", limit
	}
	return clipped[:i], limit
}

// noteTexts pulls the string values out of a note node's widgets_values, which
// ComfyUI serializes as an ARRAY in every note node observed, but which the
// converter already has to treat as polymorphic elsewhere (object form, v0.1.46).
// Non-string entries are skipped; a bare string value is accepted too.
func noteTexts(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			var s string
			if err := json.Unmarshal(e, &s); err == nil {
				out = append(out, s)
			}
		}
		return out
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	// Object form: emit values in a DETERMINISTIC order (sorted keys), because a
	// Go map iterates randomly and this list feeds a rendered, order-bearing UI.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		return sortedStringValues(obj)
	}
	return nil
}

// sortedStringValues returns obj's string values ordered by key.
func sortedStringValues(obj map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		if err := json.Unmarshal(obj[k], &s); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// parseNoteURL trims trailing prose punctuation off a raw match, validates it as
// an absolute https URL with a host, and returns the cleaned URL plus its decoded
// final path segment ("" when the URL addresses no file).
func parseNoteURL(raw string) (string, string, bool) {
	raw = strings.TrimRight(raw, noteURLTrailing)
	if raw == "" {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	// Belt to the regex's braces: the scheme gate is asserted, not assumed, so a
	// future loosening of the pattern cannot silently admit another scheme.
	if !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return "", "", false
	}
	return raw, noteBasename(u), true
}

// noteBasename is the URL's final path segment, percent-decoded. It is "" for a
// path that names no file: empty, "/", a trailing slash, or a "."/".." segment.
// Query and fragment are excluded because url.URL.Path holds neither — which is
// exactly why a ?download=true suffix cannot smuggle itself into a filename.
func noteBasename(u *url.URL) string {
	// 🔴 EscapedPath, NOT Path. url.Parse DECODES `%2F` into a real `/` in Path, so
	// `/dir%2Fa.safetensors` — one segment whose name contains a literal slash —
	// reads there as two segments and path.Base hands back `a.safetensors`, a
	// filename this URL does not address. Splitting the still-escaped form and
	// decoding ONE segment afterwards keeps the distinction, which is what makes
	// the "no path structure" check below reachable at all.
	p := u.EscapedPath()
	if p == "" || strings.HasSuffix(p, "/") {
		return ""
	}
	b := path.Base(p)
	if b == "" || b == "." || b == ".." || b == "/" {
		return ""
	}
	if dec, err := url.PathUnescape(b); err == nil {
		b = dec
	}
	// A decoded segment must not reintroduce path structure.
	if strings.ContainsAny(b, "/\\") {
		return ""
	}
	return b
}

// NoteLinksMatching returns the links whose Basename equals filename's basename,
// case-insensitively — the EXACT-match rule, and the only rule that makes a note
// URL usable without asking the user anything.
//
// Case-insensitivity matches modelWithVersions.pickFile in internal/web, which is
// how every other filename→remote-file match in this app is decided; using a
// different rule here would make the note path accept or refuse files the CivitAI
// path does not.
//
// A link with an empty Basename can never match, so a page link (a CivitAI model
// page, an openmodeldb entry) is never mistaken for the file itself.
func NoteLinksMatching(links []NoteLink, filename string) []NoteLink {
	want := strings.ToLower(PathBase(strings.TrimSpace(filename)))
	if want == "" {
		return nil
	}
	var out []NoteLink
	for _, l := range links {
		if l.Basename == "" {
			continue
		}
		if strings.ToLower(l.Basename) == want {
			out = append(out, l)
		}
	}
	return out
}
