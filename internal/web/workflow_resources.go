package web

import (
	"fmt"
	"path/filepath"
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
// for this resource. It requires a CONCRETE, known file — a chip that honestly
// reads "present" for an AMBIGUOUS basename carries no path and no id, and must
// therefore offer no folder button (there is no single folder to open).
func (r resourceInfo) revealable() bool { return r.FileID > 0 && r.Path != "" }

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
			// A folder glyph; the accessible name is the aria-label above.
			h.Span(g.Attr("aria-hidden", "true"), g.Text("🗀")),
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
func workflowResourceChip(res string, resolver workflowResolver) g.Node {
	base := filepath.Base(strings.ReplaceAll(res, "\\", "/"))
	have := resolver.have(base)
	info, _ := resolver.resource(base)

	mark, state, state1 := "✗", "no", "not in your library"
	if have {
		mark, state, state1 = "✓", "yes", "present in your library"
	}

	// Hover reveals the ABSOLUTE on-disk path when we know it; otherwise the chip
	// says plainly that it is not present rather than showing a bare filename with
	// no explanation.
	hover := res + " — " + state1
	if info.Path != "" {
		hover = info.Path
	}
	// A recorded provenance is stated IN FULL on hover, so the claim the link makes
	// is legible without clicking it — and it is appended to the path rather than
	// replacing it, because both are facts about the same file.
	hfHref, hasHF := info.hfHref()
	if hasHF {
		claim := "Downloaded from HuggingFace: " + info.HF.Repo + "/" + info.HF.Path +
			" @ " + shortRevision(info.HF.Revision)
		if info.Path != "" {
			hover = info.Path + " — " + claim
		} else {
			hover = claim
		}
	}

	attrs := []g.Node{
		h.Class("cm-res-chip"),
		dataAttr("have", state),
		h.Title(hover),
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

	// --- PR C2: the native "open containing folder" control --------------------
	// Offered only when a CONCRETE contained file is known (never for an ambiguous
	// basename, which resolves to no id and no path) and only on a loopback bind,
	// because the server execs a file manager on its OWN machine.
	// ---------------------------------------------------------------------------
	if resolver.openFolder && info.revealable() {
		return h.Span(h.Class("cm-res-item"), chip, resourceOpenControl(info.FileID, resolver.csrf, "", ""))
	}
	return chip
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
		g.Attr("aria-label", label+" referenced by this workflow"),
		h.Title(label+" referenced by this workflow"),
		g.Text(label),
		h.Span(
			h.Class("cm-updated-pop cm-res-pop"),
			g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text("Referenced resources")),
			workflowResourceChips(resources, resolver),
		),
	)
}
