package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// resourceInfo is what a referenced resource filename resolved to in the LOCAL
// library. It is derived entirely from the store (never a civitai fetch), so it is
// safe to build on every render of an offline page.
//
// Path is the matched file's ABSOLUTE on-disk path (what the chip reveals on
// hover). ModelID/VersionID carry the civitai linkage the same file's row holds —
// exactly comfy.LocalMatch's two fields — and are 0 when the file is indexed but
// not linked to a civitai model/version.
//
// Note the deliberate asymmetry with workflowResolver.have(): presence is answered
// by store.HasLocalFileNamed, which says yes for a basename indexed at SEVERAL
// paths, while store.LocalFileByBasename refuses to resolve an AMBIGUOUS basename
// (differing linkage) and returns no match. A chip can therefore legitimately read
// "present" and still carry no path and no link — which is the honest rendering,
// not a bug to paper over.
type resourceInfo struct {
	Path      string
	ModelID   int
	VersionID int
	// FileID is the local_files rowid of the matched file. It is what the "open
	// containing folder" control submits: an INTEGER, so the request can never
	// carry a filesystem path and the server always re-derives the path itself.
	// Zero when no concrete file resolved.
	FileID int64
	// Contained reports that Path resolved (through symlinks) into one of the
	// CONFIGURED library roots at render time. The endpoint re-checks this itself
	// on every click — a rendered page is a stale snapshot and can never authorise
	// anything — so this exists purely so the button is not OFFERED for a file we
	// already know we would refuse to open.
	Contained bool
	// HF is the RECORDED HuggingFace provenance for this file's bytes — non-nil
	// only when THIS APP downloaded them through the HuggingFace fallback and
	// verified their sha256 before the atomic rename. It is never a hash match
	// against a remote index and never a basename guess: a chip link reads as
	// origin, and the only origin we can prove is a transfer we performed.
	HF *store.HFProvenance
}

// linked reports whether the resource resolved to a civitai model AND version, the
// only case in which an in-app source link can be built. It mirrors the guard
// comfy.resolveModelRef applies before deriving an AIR URN.
func (r resourceInfo) linked() bool { return r.ModelID > 0 && r.VersionID > 0 }

// hfHref returns the external link for a recorded HuggingFace provenance, or
// ok=false when there is none (or it cannot be expressed as a safe http(s) URL).
//
// It prefers the PINNED-revision file page — the only URL form that keeps
// resolving to the bytes the row makes a claim about after the repo's default
// branch moves — and degrades to the repo landing page only if that cannot be
// built. Verified live: /{repo}/blob/{commit-sha}/{path} returns 200 text/html
// with no redirect, for a root-level and a nested path alike.
//
// Every candidate must pass isSafeHTTPURL before it can become an href.
func (r resourceInfo) hfHref() (string, bool) {
	if r.HF == nil {
		return "", false
	}
	for _, cand := range []string{r.HF.FileURL(), r.HF.RepoURL()} {
		if cand != "" && isSafeHTTPURL(cand) {
			return cand, true
		}
	}
	return "", false
}

// revealable reports whether the "open containing folder" control may be shown
// for this resource. It requires a CONCRETE, CONTAINED file:
//
//   - a chip that honestly reads "present" for an AMBIGUOUS basename carries no
//     path and no id, so there is no single folder to open and no button;
//   - a file outside every configured library root would be refused on click, so
//     offering the button would be offering a control that cannot work.
func (r resourceInfo) revealable() bool {
	return r.FileID > 0 && r.Path != "" && r.Contained
}

// resource resolves a referenced resource's BASENAME through the resolver's local
// lookup. A nil lookup (the zero resolver used by several render paths) yields
// ok=false, so nothing about a file is ever claimed without store backing.
func (r workflowResolver) resource(basename string) (resourceInfo, bool) {
	if r.localResource == nil {
		return resourceInfo{}, false
	}
	return r.localResource(basename)
}

// workflowResourceChips renders a workflow's referenced resources as a wrapping row
// of chips. Shared by the detail page's "Referenced resources" card and the list
// item's popover, so the two can never drift.
func workflowResourceChips(resources []string, resolver workflowResolver) g.Node {
	chips := make([]g.Node, 0, len(resources))
	for _, res := range resources {
		chips = append(chips, workflowResourceChip(res, resolver))
	}
	return h.Div(h.Class("cm-res-chips"), g.Group(chips))
}

// openFolderTitle is the tooltip/aria text on the "open containing folder"
// control. It says WHERE the window appears, because the server opens it on the
// machine running `serve` — which is meaningless (or confusing) when the UI is
// being viewed from another device. The control is loopback-gated for the same
// reason, so this text describes the only situation in which it is offered.
const openFolderTitle = "Show this file in the file manager — the window opens on the computer running civitai-manager, not on this device"

// folderIconSVG is the "open containing folder" glyph.
//
// 🔴 IT IS AN INLINE SVG BECAUSE THE EMOJI WAS INVISIBLE. This button used to
// render U+1F5C0 (FOLDER) as text — spelled here as a codepoint, never as the
// character itself, because a literal one is indistinguishable from tofu in a
// diff and TestNoMissingFolderGlyphInTemplates refuses it. That codepoint has NO
// glyph in the common Linux font stacks, so the control shipped as a tofu box — a
// missing glyph, not a broken image, which is why it read as "the button is
// broken" rather than "the font lacks a character". (The Discover page's 📁
// U+1F4C1 is well supported and renders fine; the inconsistency between the two
// was the tell.) An inline SVG depends on no font at all, matches the icon
// approach the nav/dashboard already use (brand.go, the dashboard status chips),
// and inherits the button's colour via stroke="currentColor".
//
// It stays aria-hidden: the button's aria-label (openFolderTitle) is its
// accessible name, and an icon that announced itself would duplicate that.
const folderIconSVG = `<svg class="cm-res-open-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M3 7a2 2 0 0 1 2-2h4l2 2.5h8a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>`

// resourceOpenControl renders the "open containing folder" affordance for ONE
// resolved local file, plus (after a click) the outcome message in place.
//
// It submits ONLY the local_files rowid and the CSRF token — never a path. The
// server re-derives the path from that id and refuses anything outside a
// configured library root, so a forged POST cannot name a directory to open.
//
// It is a SIBLING of the chip, not a child: the chip is an <a> whenever the file
// carries a source link, and nesting a button (or a form) inside an anchor is
// invalid HTML and breaks keyboard/AT interaction. The .cm-res-item wrapper keeps
// the pair on one line and wrapping as a unit.
func resourceOpenControl(fileID int64, csrf, msg, state string) g.Node {
	kids := []g.Node{
		h.Button(
			h.Type("button"),
			h.Class("cm-res-open-btn"),
			h.Title(openFolderTitle),
			g.Attr("aria-label", openFolderTitle),
			hx("post", "/library/files/"+strconv.FormatInt(fileID, 10)+"/reveal"),
			hx("vals", fmt.Sprintf(`{"csrf_token":%q}`, csrf)),
			hx("target", "closest .cm-res-open"),
			hx("swap", "outerHTML"),
			// A folder icon; the accessible name is the aria-label above. Inline SVG,
			// never a text glyph — see folderIconSVG.
			g.Raw(folderIconSVG),
		),
	}
	if strings.TrimSpace(msg) != "" {
		kids = append(kids, h.Span(
			h.Class("cm-res-open-msg"),
			dataAttr("state", state),
			g.Attr("role", "status"),
			g.Text(msg),
		))
	}
	return h.Span(append([]g.Node{h.Class("cm-res-open")}, kids...)...)
}

// workflowResourceChip renders ONE referenced resource as a chip.
//
// The resource string comes from an arbitrary, untrusted graph — it is escaped via
// g.Text (label) and attribute escaping (title/aria-label), never interpolated raw.
//
// Three renderings, and the difference is load-bearing:
//
//   - MATCHED to a local file that carries a civitai model+version linkage → an
//     <a> to the in-app model page (/models/<id>?modelVersionId=<v>), built from
//     integers only so no request text reaches the URL.
//   - matched to a local file with no (or ambiguous) civitai linkage → a plain
//     <span>. It still reveals its absolute path on hover.
//   - matched to a local file whose BYTES this app downloaded from HuggingFace
//     (a recorded hf_provenance row, migration 0015) → an <a> to that repo's file
//     page at the PINNED commit sha, target=_blank rel=noopener. The claim is
//     "these bytes came from here", and it is backed by a sha256 that was verified
//     against the stream before the atomic rename.
//   - not in the library, or present with no recorded provenance → a plain <span>
//     with NO source link and no affordance implying one exists. We do not guess:
//     a basename match cannot distinguish two different files that share a name,
//     and a wrong source link routes someone to bytes that silently produce
//     different output.
//
// A resolved local file additionally gets an "open containing folder" control
// (loopback-gated, CSRF-protected, id-only) as a SIBLING of the chip.
//
// Orthogonally to the link, the MARK has three states: ✓ present in the local
// library, ◎ absent from the library but resolvable by ComfyUI (from the
// comfy_model_cache — see comfy_model_cache.go), ✗ neither. ◎ is checked only
// when ✓ is false, so a local file is never described by what ComfyUI happens to
// have.
func workflowResourceChip(res string, resolver workflowResolver) g.Node {
	// store.ResourceBasename is the SINGLE definition of "which part of a resource
	// entry is the filename" — shared with the model page's "workflows that use
	// this model" query, so a chip's ✓/◎/✗ and that list can never disagree about
	// what a resource is called.
	base := store.ResourceBasename(res)
	have := resolver.have(base)
	info, _ := resolver.resource(base)

	mark, state, state1 := "✗", "no", "not in your library"
	switch {
	case have:
		mark, state, state1 = "✓", "yes", "present in your library"
	case resolver.comfyHas(base):
		// The THIRD state. The wording names both halves on purpose: the file is
		// usable by a run, and it is still not something this app indexes, tracks
		// or can open a folder for. "In ComfyUI" alone reads as "you have it".
		mark, state, state1 = "◎", "comfy", "in ComfyUI, not in your library"
	}

	// The ABSOLUTE on-disk path when we know it; otherwise the plain statement that
	// it is not present, rather than a bare filename with no explanation.
	detail := res + " — " + state1
	if info.Path != "" {
		detail = info.Path
	}
	// A recorded provenance is stated IN FULL, so the claim the link makes is
	// legible without clicking it — appended to the path rather than replacing it,
	// because both are facts about the same file.
	hfHref, hasHF := info.hfHref()
	hfClaim := ""
	if hasHF {
		hfClaim = "Downloaded from HuggingFace: " + info.HF.Repo + "/" + info.HF.Path +
			" @ " + shortRevision(info.HF.Revision)
	}

	// 🔴 NO title= HERE. The chip is wrapped in a hover popover (see
	// resourceChipPopover), and a title= on the same hover unit makes the browser
	// paint its NATIVE tooltip over that popover after the OS delay — the exact
	// double-hover bug 297ccd2 fixed elsewhere and that workflowResourcesPopover's
	// own comment forbids. Everything the title used to say now lives in the
	// popover, which is why this is not a loss of information. The accessible name
	// stays on the chip: aria-label is what a screen reader announces, and it is
	// unaffected by the popover.
	attrs := []g.Node{
		h.Class("cm-res-chip"),
		dataAttr("have", state),
		g.Attr("aria-label", res+" — "+state1),
	}

	body := []g.Node{
		h.Span(h.Class("cm-res-mark"), g.Attr("aria-hidden", "true"), g.Text(mark)),
		// break-all: a resource basename is an arbitrary, often long unbroken string
		// with no guaranteed break opportunity (pinned by TestLongUntrustedStringsCanBreak).
		h.Span(h.Class("cm-res-name break-all"), g.Text(base)),
	}

	// The chip itself: an in-app CivitAI link wins over an off-site HuggingFace
	// link (it points at a page of ours), and a file with neither stays a <span>.
	var chip g.Node
	switch {
	case info.linked():
		body = append(body, h.Span(h.Class("cm-res-go"), g.Attr("aria-hidden", "true"), g.Text("↗")))
		href := "/models/" + strconv.Itoa(info.ModelID) + "?modelVersionId=" + strconv.Itoa(info.VersionID)
		chip = h.A(append(append(attrs, h.Href(href)), body...)...)
	case hasHF:
		attrs = append(attrs,
			dataAttr("src", "hf"),
			h.Href(hfHref),
			h.Target("_blank"),
			g.Attr("rel", "noopener noreferrer"),
		)
		body = append(body, h.Span(h.Class("cm-res-go"), g.Attr("aria-hidden", "true"), g.Text("↗")))
		chip = h.A(append(attrs, body...)...)
	default:
		chip = h.Span(append(attrs, body...)...)
	}

	// The chip and its detail popover are ONE hover unit, wrapped together.
	hoverUnit := h.Span(h.Class("cm-res-chip-wrap"),
		chip,
		resourceChipPopover(state1, detail, hfClaim),
	)

	// --- PR C2: the native "open containing folder" control --------------------
	// Offered only when a CONCRETE contained file is known (never for an ambiguous
	// basename, which resolves to no id and no path) and only on a loopback bind,
	// because the server execs a file manager on its OWN machine.
	//
	// It is a SIBLING of the hover unit, not a child: hovering the folder button
	// must not open the chip's popover over the thing you are about to click.
	// ---------------------------------------------------------------------------
	if resolver.openFolder && info.revealable() {
		return h.Span(h.Class("cm-res-item"), hoverUnit, resourceOpenControl(info.FileID, resolver.csrf, "", ""))
	}
	return hoverUnit
}

// resourceChipPopover renders the chip's detail panel: the state in words, the
// absolute path (or the "not present" sentence), and any recorded HuggingFace
// provenance claim.
//
// 🔴 IT EMITS NO LINKS, AND THAT IS THE POINT — twice over.
//
//	(1) The CHIP is the link. A "View model" row duplicating the chip's own href
//	    made every linked chip carry the SAME href twice, which is what destroyed
//	    the "exactly one href" guard in the provenance suite. One resource, one
//	    destination.
//	(2) The HuggingFace provenance stays TEXT. The chip deliberately prefers an
//	    in-app CivitAI link over an off-site one, and putting an off-site HF <a>
//	    in the popover would quietly reinstate exactly the off-site link that
//	    rule suppresses. The claim is still stated in full — repo, path and
//	    pinned short revision — so nothing is hidden; it simply is not clickable
//	    from a chip whose link belongs to CivitAI.
//
// 🔴 IT DOES NOT USE .cm-updated/.cm-updated-pop. Those rules are DESCENDANT
// selectors (`.cm-updated:hover .cm-updated-pop`), and these chips render INSIDE
// workflowResourcesPopover — itself a `.cm-updated`. Reusing the shared classes
// therefore opened EVERY chip's popover the moment the outer trigger opened,
// stacking panels over chips 2..N. `.cm-res-chip-wrap` + `.cm-res-chip-pop` is a
// separate, CHILD-combinator pair for that reason; see app.css.
func resourceChipPopover(state, detail, hfClaim string) g.Node {
	rows := []g.Node{
		h.Div(h.Class("cm-res-detail-row"),
			h.Span(h.Class("cm-res-detail-label"), g.Text("Status")),
			h.Span(h.Class("cm-res-detail-value"), g.Text(state)),
		),
		h.Div(h.Class("cm-res-detail-row"),
			h.Span(h.Class("cm-res-detail-label"), g.Text("File")),
			h.Span(h.Class("cm-res-detail-value break-all"), g.Text(detail)),
		),
	}
	if hfClaim != "" {
		rows = append(rows, h.Div(h.Class("cm-res-detail-row"),
			h.Span(h.Class("cm-res-detail-value break-all"), g.Text(hfClaim)),
		))
	}
	return h.Span(
		h.Class("cm-res-chip-pop"),
		// Every other popover in the app declares it; a panel that describes the
		// element it hangs off is a tooltip.
		g.Attr("role", "tooltip"),
		g.Group(rows),
	)
}

// shortRevision abbreviates a commit sha for display. A full 40-hex sha in a
// tooltip is noise; the first 7 are what a human compares.
func shortRevision(rev string) string {
	rev = strings.TrimSpace(rev)
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// workflowResourcesPopover is the list item's referenced-resources affordance: a
// compact "N resources" trigger whose hover/click reveals the chips.
//
// It REUSES the page's existing popover mechanism rather than adding a second one —
// the `.cm-updated` wrapper + `.cm-updated-pop` child is exactly what the shared
// hover controller in modelPageScript delegates on (its selector is
// `.cm-vstatus, .cm-updated`), and the CSS :hover/:focus-within rules are the no-JS
// fallback. tabindex=0 is what turns a CLICK (and a keyboard Tab) into an open,
// via :focus-within.
func workflowResourcesPopover(resources []string, resolver workflowResolver) g.Node {
	n := len(resources)
	label := fmt.Sprintf("%d resource%s", n, plural(n))
	return h.Span(
		// cm-updated → the shared popover controller + CSS; cm-res-trigger → the
		// local sizing/colour and the wider popover.
		h.Class("cm-updated cm-res-trigger"),
		g.Attr("tabindex", "0"),
		g.Attr("role", "button"),
		// No title= beside the popover — the native tooltip would render on top of
		// it (see updatedPopBody's "one hover affordance per element"). role=button
		// with no inner text of its own means the aria-label IS the accessible name.
		g.Attr("aria-label", label+" referenced by this workflow"),
		g.Text(label),
		h.Span(
			h.Class("cm-updated-pop cm-res-pop"),
			g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text("Referenced resources")),
			workflowResourceChips(resources, resolver),
		),
	)
}
