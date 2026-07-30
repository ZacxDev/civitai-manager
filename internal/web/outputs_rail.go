package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// outputsRailLimit is how many ENTRIES the GLOBAL outputs rail shows — entries,
// not rows: a batch collapses to one entry (see railGroup). It is deliberately
// small: the rail is app chrome rendered on every page, so its store query must
// stay a fixed, bounded read (see store.ListRecentGenerations, which additionally
// clamps to its own hard cap).
const outputsRailLimit = 12

// railFetchLimit is how many generation ROWS rail() reads before grouping. It is
// deliberately larger than outputsRailLimit: collapsing batches over exactly
// outputsRailLimit rows would let a batch-heavy history UNDER-fill the rail (12
// rows that all belong to one batch collapse to a single entry, leaving 11 slots
// empty). So: fetch 2×, group, then clamp to outputsRailLimit GROUPS.
//
// It stays a fixed, bounded read — 24 is a constant, and well under
// store.recentGenerationsCap (50), so ListRecentGenerations' clamp never silently
// truncates it (asserted by TestRailStoreReadStaysBounded).
const railFetchLimit = outputsRailLimit * 2

// outputsRailSettingKey persists the rail's collapsed state, mirroring the
// theme/NSFW settings idiom (a CSRF-protected POST → settings store → HX-Refresh).
const outputsRailSettingKey = "outputs_rail_collapsed"

// railGroup is ONE rail entry. An ordinary run is a group of one; the N captured
// runs of a single "Queue ×N" batch collapse into one group, so an 8-item batch
// can no longer eat 8 of the rail's 12 slots.
type railGroup struct {
	// Rep is the row the entry renders from: its thumbnail, label and timestamp.
	// It is the group's NEWEST captured run — see groupRailGenerations for why.
	Rep store.Generation
	// Count is how many rows this entry COLLAPSED, which is what the ×N badge
	// reports. It is deliberately NOT Rep.BatchTotal: a batch stopped at item 2 of
	// 8 must read ×2 (what the rail actually stands for) rather than claim eight
	// outputs that were never made.
	Count int
}

// railData is the per-request state of the global "Recent outputs" rail. Its ZERO
// VALUE renders no rail at all, which is what unit tests that call a page builder
// without shell state get.
type railData struct {
	// Groups are the most recent ENTRIES across ALL workflows (never more than
	// outputsRailLimit). Empty means a fresh install / nothing captured yet — the
	// rail is then omitted entirely rather than rendered as a dead empty column.
	Groups []railGroup
	// Collapsed is the persisted collapse state (desktop rail width; the mobile
	// drawer's open/closed state is ephemeral and lives in the DOM).
	Collapsed bool
}

// groupRailGenerations collapses newest-first generation rows into at most limit
// rail entries: consecutive-or-not rows sharing a batch id become ONE group, and
// every other row is its own group.
//
// ORDERING is preserved exactly: a group takes the position of its FIRST-SEEN —
// i.e. NEWEST — member, so grouping can never quietly reorder the rail. That same
// row is the group's representative, which keeps the entry self-consistent: the
// caption's relative time then describes the row whose position the entry
// occupies. (Picking run 1 instead would caption a tile sitting at the newest
// member's slot with the OLDEST member's age.)
//
// A row whose batch id would not survive store.ValidBatchID is treated as
// UNBATCHED — the same defensive stance as generationBatchSegment. That is what
// guarantees a collapsed group can always emit a working batchHref, so a ×N badge
// never appears on a tile that links to a single generation.
//
// Honest limit: Count counts only the rows inside the caller's fetch window. A
// batch straddling that window's oldest edge therefore undercounts (its older
// members were never read). It can only ever affect the LAST entry, the batch page
// behind the link still shows the truth, and the alternative — a per-group COUNT
// query — is exactly the N+1 this surface must not have.
func groupRailGenerations(gens []store.Generation, limit int) []railGroup {
	groups := make([]railGroup, 0, len(gens))
	seen := make(map[string]int, len(gens))
	for _, gen := range gens {
		if gen.BatchID == "" || !store.ValidBatchID(gen.BatchID) {
			groups = append(groups, railGroup{Rep: gen, Count: 1})
			continue
		}
		if i, ok := seen[gen.BatchID]; ok {
			groups[i].Count++
			continue
		}
		seen[gen.BatchID] = len(groups)
		groups = append(groups, railGroup{Rep: gen, Count: 1})
	}
	if limit > 0 && len(groups) > limit {
		// Clamp to GROUPS, not rows — the whole point of over-fetching.
		groups = groups[:limit]
	}
	return groups
}

// visible reports whether the rail renders for this request. It is the SINGLE
// predicate behind the rail markup, the shell's reserved width, and the nav's
// drawer button, so those three can never disagree.
//
// NSFW: a captured generation carries no per-image rating signal, so the whole
// rail is treated as ONE surface — mode "hide" OMITS it server-side (no markup at
// all), "blur" renders it blurred (hover/focus reveals), "show" renders it plain.
//
// Like the omit branches in model_pages.go, the hide path is an INERT, PRESERVED
// capability rather than a live one: every production caller passes s.nsfwMode(),
// which runs normalizeNSFWMode and MIGRATES a stored "hide" to blur, so mode ==
// hide is unreachable in practice. The test below reads the RAW value (instead of
// normalizing first, which would make the branch unreachable from tests too) so
// the "hide omits server-side" invariant stays exercised and cannot rot.
func (rd railData) visible(nsfwMode string) bool {
	if len(rd.Groups) == 0 {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(nsfwMode), NSFWHide)
}

// railShellClass is the <body> class that reserves the rail's width on desktop.
// Empty when no rail renders, so the shell is byte-identical to the pre-rail one.
func railShellClass(rd railData, nsfwMode string) string {
	if !rd.visible(nsfwMode) {
		return ""
	}
	if rd.Collapsed {
		return "cm-shell-rail-collapsed"
	}
	return "cm-shell-rail"
}

// railOf picks the optional shell state a page builder was handed. Page builders
// take it as a trailing variadic so the (many) unit tests that render a page
// without any shell state keep compiling and keep rendering a rail-free shell.
func railOf(rd []railData) railData {
	if len(rd) > 0 {
		return rd[0]
	}
	return railData{}
}

// outputsRail renders the global right-hand "Recent outputs" sidebar: the most
// recent generations across ALL workflows, each linking to its detail page exactly
// like a gallery tile. It returns nil when the rail is not visible.
//
// Layout is CSS-only (.cm-rail in app.css): a fixed right column on desktop that
// collapses to a thin labelled edge, and an off-canvas drawer below 1024px opened
// from the nav. It is a sibling of <main>, never inside it, so it cannot interfere
// with any htmx poll target in the page body.
func outputsRail(rd railData, csrf, nsfwMode string) g.Node {
	// visible() reads the RAW mode (see its doc) — normalize only afterwards, for
	// the blur/show distinction.
	if !rd.visible(nsfwMode) {
		return nil
	}
	mode := normalizeNSFWMode(nsfwMode)

	tiles := make([]g.Node, 0, len(rd.Groups))
	for _, gr := range rd.Groups {
		tiles = append(tiles, railTile(gr))
	}

	// Collapse control: POSTs the NEXT state with the CSRF token and replies
	// HX-Refresh, exactly like the theme and NSFW toggles.
	next, glyph, label := "true", "›", "Collapse recent outputs"
	if rd.Collapsed {
		next, glyph, label = "false", "‹", "Expand recent outputs"
	}
	collapse := civButton("subtle", "sm",
		[]g.Node{
			h.Type("button"),
			h.Class("cm-rail-collapse"),
			hx("post", "/settings/outputs-rail"),
			hx("vals", fmt.Sprintf(`{"collapsed":%q,"csrf_token":%q}`, next, csrf)),
			hx("swap", "none"),
			g.Attr("aria-label", label),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text(glyph)),
	)

	// Mobile-only close control for the drawer. It is also the element focus moves
	// to when the drawer opens (see railDrawerScript), so it carries a stable id.
	closeBtn := civButton("subtle", "sm",
		[]g.Node{
			h.Type("button"),
			h.ID("cm-rail-close"),
			h.Class("cm-rail-close"),
			g.Attr("onclick", "cmRailDrawer(false)"),
			g.Attr("aria-label", "Close recent outputs"),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text("✕")),
	)

	aside := h.Aside(
		h.ID("cm-rail"),
		h.Class("cm-rail"),
		dataAttr("collapsed", strconv.FormatBool(rd.Collapsed)),
		dataAttr("open", "false"),
		g.If(mode == NSFWBlur, dataAttr("nsfw", "blur")),
		g.Attr("aria-label", "Recent outputs"),
		h.Div(h.Class("cm-rail-head"),
			h.Span(h.Class("cm-rail-title"), g.Text("Recent outputs")),
			collapse,
			closeBtn,
		),
		// Vertical label shown only while the desktop rail is collapsed, so the thin
		// edge still says what it is.
		h.Span(h.Class("cm-rail-vlabel"), g.Text("Recent outputs")),
		h.Div(h.Class("cm-rail-body"), g.Group(tiles)),
		h.Div(h.Class("cm-rail-foot"),
			h.A(h.Href("/outputs"), h.Class("cm-rail-all"), g.Text("View all outputs →")),
		),
	)

	return g.Group([]g.Node{
		h.Div(h.ID("cm-rail-scrim"), h.Class("cm-rail-scrim"),
			dataAttr("open", "false"),
			g.Attr("onclick", "cmRailDrawer(false)"),
			g.Attr("aria-hidden", "true")),
		aside,
		railDrawerScript(),
	})
}

// railTile is one rail entry: the representative row's first image as a lazy
// thumbnail linking to /outputs/{id} (the same destination the gallery tiles use),
// captioned with the escaped workflow label and a relative time.
//
// A COLLAPSED BATCH (Count > 1) differs in exactly two ways: it links to the batch
// view instead of one run's detail page, and it carries a ×N corner badge. A group
// of one renders byte-identically to the pre-batch tile — a ×1 badge would be pure
// noise, and a one-run "batch" has nothing to browse side by side.
//
// The href and the badge are set TOGETHER from one batchHref result: batchHref
// returns "" for anything store.ValidBatchID would reject, and a badge on a tile
// linking to a single generation would be a lie. groupRailGenerations already
// refuses to collapse such a row, so this is belt-and-braces.
func railTile(gr railGroup) g.Node {
	gen := gr.Rep
	label := generationLabel(gen)

	var thumb g.Node
	if gen.FirstImageID > 0 {
		thumb = h.Img(
			h.Src(generationImgURL(gen.FirstImageID)),
			h.Alt(label),
			g.Attr("loading", "lazy"),
			h.Class("cm-rail-thumb"),
		)
	} else {
		thumb = h.Span(h.Class("cm-rail-nothumb"), g.Text("no image"))
	}

	href := "/outputs/" + strconv.FormatInt(gen.ID, 10)
	title := label + " · " + humanSince(gen.CreatedAt)
	var badge g.Node
	if gr.Count > 1 {
		if bh := batchHref(gen.BatchID); bh != "" {
			href = bh
			title = label + " · " + strconv.Itoa(gr.Count) + " runs · " + humanSince(gen.CreatedAt)
			badge = h.Span(h.Class("cm-rail-badge"), g.Text("×"+strconv.Itoa(gr.Count)))
		}
	}

	return h.A(
		h.Href(href),
		h.Class("cm-rail-item"),
		h.Title(title),
		thumb,
		// After the thumbnail in DOM order, which is what paints it on top —
		// .cm-rail-cap relies on the same thing, so the tile needs no z-index at all.
		badge,
		h.Span(h.Class("cm-rail-cap"), g.Text(label)),
	)
}

// railNavToggle is the nav control that opens the rail drawer on narrow screens
// (hidden by CSS at the desktop breakpoint, where the rail is always on screen).
func railNavToggle() g.Node {
	return civButton("outline", "sm",
		[]g.Node{
			h.Type("button"),
			h.ID("cm-rail-open"),
			h.Class("cm-rail-open-btn"),
			g.Attr("aria-controls", "cm-rail"),
			g.Attr("aria-expanded", "false"),
			g.Attr("onclick", "cmRailDrawer(true)"),
			g.Attr("aria-label", "Open recent outputs"),
		},
		h.Span(g.Attr("aria-hidden", "true"), g.Text("▤")),
	)
}

// railDrawerScript toggles the EPHEMERAL mobile drawer state. Vendored inline —
// no CDN, no framework. The persisted desktop collapse state is server state and
// does NOT go through here.
//
// While open the rail behaves as a modal drawer, so the script (not the markup)
// applies the dialog semantics — on desktop the same element is a static
// complementary column, where role="dialog" would be wrong:
//   - role="dialog" + aria-modal="true" while open, removed on close;
//   - focus moves to the close button on open and is RESTORED to whatever opened
//     the drawer on close;
//   - the rest of the page (nav + main) is marked `inert`, which both removes it
//     from the tab order — containing focus without a hand-rolled Tab trap — and
//     hides it from assistive tech.
//
// Escape closes. This is a proportionate pass, not a full a11y sweep.
func railDrawerScript() g.Node {
	return h.Script(g.Raw(`
var cmRailPrevFocus = null;
function cmRailDrawer(open){
  var v = open ? 'true' : 'false';
  var r = document.getElementById('cm-rail');
  if (!r) { return; }
  var s = document.getElementById('cm-rail-scrim');
  var b = document.getElementById('cm-rail-open');
  if (open) {
    cmRailPrevFocus = document.activeElement;
    r.setAttribute('role', 'dialog');
    r.setAttribute('aria-modal', 'true');
  } else {
    r.removeAttribute('role');
    r.removeAttribute('aria-modal');
  }
  r.setAttribute('data-open', v);
  if (s) { s.setAttribute('data-open', v); }
  if (b) { b.setAttribute('aria-expanded', v); }
  var rest = document.querySelectorAll('nav, main');
  for (var i = 0; i < rest.length; i++) {
    if (open) { rest[i].setAttribute('inert', ''); } else { rest[i].removeAttribute('inert'); }
  }
  if (open) {
    var c = document.getElementById('cm-rail-close');
    if (c && c.focus) { c.focus(); }
  } else if (cmRailPrevFocus && cmRailPrevFocus.focus) {
    cmRailPrevFocus.focus();
    cmRailPrevFocus = null;
  }
}
document.addEventListener('keydown', function(e){
  if (e.key === 'Escape') { cmRailDrawer(false); }
});
`))
}
