package web

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

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
}

// linked reports whether the resource resolved to a civitai model AND version, the
// only case in which a source link can be built. It mirrors the guard
// comfy.resolveModelRef applies before deriving an AIR URN.
func (r resourceInfo) linked() bool { return r.ModelID > 0 && r.VersionID > 0 }

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
//   - not in the library, or resolved only by HuggingFace at download time → a
//     plain <span> with NO source link. `internal/hf` is a download-time fallback
//     resolver that persists NO provenance, so there is nothing to link to and we
//     must not imply otherwise.
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

	attrs := []g.Node{
		h.Class("cm-res-chip"),
		dataAttr("have", state),
		h.Title(hover),
		g.Attr("aria-label", res+" — "+state1),
	}

	body := []g.Node{
		h.Span(h.Class("cm-res-mark"), g.Attr("aria-hidden", "true"), g.Text(mark)),
		h.Span(h.Class("cm-res-name"), g.Text(base)),
	}

	// --- PR C2 SEAM ------------------------------------------------------------
	// The native "reveal in file manager" button goes HERE, inside the chip, next
	// to the name and only when info.Path != "". It is deliberately NOT built in
	// C1: it would make the server exec an opener on an HTTP request, which needs
	// its own gating (loopback + CSRF + containment to a configured library root +
	// a fixed opener allowlist) and a POST endpoint. resourceInfo.Path is the value
	// that button will submit.
	// ---------------------------------------------------------------------------

	if info.linked() {
		body = append(body, h.Span(h.Class("cm-res-go"), g.Attr("aria-hidden", "true"), g.Text("↗")))
		href := "/models/" + strconv.Itoa(info.ModelID) + "?modelVersionId=" + strconv.Itoa(info.VersionID)
		return h.A(append(append(attrs, h.Href(href)), body...)...)
	}
	return h.Span(append(attrs, body...)...)
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
