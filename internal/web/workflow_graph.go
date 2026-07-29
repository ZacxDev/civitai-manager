package web

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// This file renders a stored ComfyUI workflow graph as a server-side SVG (for the
// litegraph "UI" format, which carries node coordinates) or a structured node
// listing (for the "API" format, or when an SVG cannot be built). Every input is
// UNTRUSTED: parsing is bounded + defensive (a malformed node/link/coord is
// skipped, never panics) and all text is escaped via g.Text.

// SVG layout constants (litegraph-ish proportions).
const (
	gTitleH      = 22.0 // title bar height, sitting ABOVE the node body's pos
	gSlotStart   = 16.0 // first slot's y offset below the body top
	gSlotSpacing = 18.0 // vertical spacing between slots
	gSlotR       = 3.5  // slot circle radius
	gPad         = 26.0 // viewBox padding around the bounding box
	gDefaultW    = 200.0
	gDefaultH    = 90.0
	gMaxWidgets  = 3 // key widget values shown inside a node
	// Render caps: a crafted/huge workflow (nodes/links arrays can be enormous
	// within the import/scan size limits) must not spike memory/CPU when its
	// detail page is opened. Cap the elements we emit; note truncation to the user.
	gMaxNodes  = 600
	gMaxLinks  = 2000
	gMaxGroups = 200
	// gCollapsedW bounds a collapsed node's title pill (litegraph's own collapsed
	// nodes are title-only, ~80px wide, growing with the title text).
	gCollapsedMinW = 80.0
	gCollapsedMaxW = 220.0
	// gMinRenderW is the SVG's rendered min-width floor, in CSS px: the widest the
	// preview may be forced to be so its overflow-auto container actually scrolls
	// horizontally instead of shrink-to-fitting a 4000px graph into a 326px phone
	// column. A graph narrower than this keeps its natural width (never stretched).
	gMinRenderW = 900.0
)

// hexColorRe bounds what may be echoed into an SVG fill/stroke attribute from the
// UNTRUSTED graph: a #rgb / #rrggbb / #rrggbbaa literal and nothing else. A
// non-matching value falls back to the theme default rather than being emitted.
var hexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)

// safeHexColor returns c when it is a plain hex color literal, else fallback.
func safeHexColor(raw json.RawMessage, fallback string) string {
	if c := authorHex(raw); c != "" {
		return c
	}
	return fallback
}

// authorHex returns the graph's OWN sanitized hex color, or "" when the graph
// specified none (or specified something that is not a hex literal).
//
// The distinction matters for theming: an author-specified color is the user's
// deliberate visual grouping and is emitted as a fill=/stroke= presentation
// attribute, which always wins. Only when this returns "" does the element also
// get a .cm-g-* class, whose rule in app.css re-paints it per data-theme (see the
// graph palette block there) — so the FALLBACK palette flips with the theme and
// the author's palette never does.
func authorHex(raw json.RawMessage) string {
	c := strings.TrimSpace(rawString(raw))
	if hexColorRe.MatchString(c) {
		return c
	}
	return ""
}

// The dark-theme fallback palette. These stay on the elements as presentation
// attributes (so the SVG is still legible with no stylesheet at all); the
// .cm-g-* classes below re-paint them for the light theme.
const (
	gDarkBody       = "#334155"
	gDarkTitle      = "#1e293b"
	gDarkStroke     = "#475569"
	gDarkText       = "#e2e8f0"
	gDarkWidget     = "#cbd5e1"
	gDarkSlot       = "#64748b"
	gDarkSlotStroke = "#0f172a"
	gDarkGroup      = "#475569"
)

// linkTypeColor maps a litegraph link data type to a wire color (litegraph
// conventions). Unknown/blank types render in neutral gray. The palette is chosen
// to read on BOTH a light and a dark card surface (saturated wires; dark node
// bodies with light text), so it needs no per-theme branch.
func linkTypeColor(t string) string {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "MODEL":
		return "#8b5cf6"
	case "CLIP":
		return "#eab308"
	case "VAE":
		return "#ef4444"
	case "CONDITIONING":
		return "#f97316"
	case "LATENT":
		return "#ec4899"
	case "IMAGE":
		return "#3b82f6"
	case "MASK":
		return "#14b8a6"
	case "CONTROL_NET", "CONTROLNET":
		return "#22c55e"
	case "CLIP_VISION":
		return "#06b6d4"
	default:
		return "#94a3b8"
	}
}

// litegraph JSON shapes (defensive: fields that vary in the wild are RawMessage).
type lgGraph struct {
	Nodes  []lgNode        `json:"nodes"`
	Links  json.RawMessage `json:"links"`
	Groups []lgGroup       `json:"groups"`
}

// EVERY field that varies in the wild is json.RawMessage and parsed defensively.
// This decodes the WHOLE document in one Unmarshal, so a single strictly-typed field
// meeting an unexpected JSON type (a numeric `color`, an array `flags`) would abort
// the entire parse and silently drop the graph to the structured fallback — the same
// "one bad value blanks everything" failure this file already fixes for links. The
// converter's uiConvInput.Type carries the same warning (see CLAUDE.md).
type lgNode struct {
	ID            json.RawMessage `json:"id"`
	Type          string          `json:"type"`
	Title         string          `json:"title"`
	Mode          int             `json:"mode"`
	Pos           json.RawMessage `json:"pos"`
	Size          json.RawMessage `json:"size"`
	Color         json.RawMessage `json:"color"`
	BgColor       json.RawMessage `json:"bgcolor"`
	Flags         json.RawMessage `json:"flags"`
	Inputs        []lgSlot        `json:"inputs"`
	Outputs       []lgSlot        `json:"outputs"`
	WidgetsValues json.RawMessage `json:"widgets_values"`
}

// collapsed reports the node's `flags.collapsed`. A collapsed node is drawn by ComfyUI
// as a title-only pill, NOT at its stored size — rendering it expanded moves its wire
// endpoints by up to its full height and buries whatever sits behind it. A non-object
// `flags` (or a non-bool `collapsed`) reads as false.
func (n lgNode) collapsed() bool {
	var f struct {
		Collapsed bool `json:"collapsed"`
	}
	if len(n.Flags) == 0 || json.Unmarshal(n.Flags, &f) != nil {
		return false
	}
	return f.Collapsed
}

// lgGroup is a canvas group box: a titled, colored region behind a set of nodes.
// Groups carry no execution semantics but are the main visual landmark in a large
// ComfyUI workflow, so a preview without them reads as a different graph.
type lgGroup struct {
	Title    json.RawMessage `json:"title"`
	Bounding json.RawMessage `json:"bounding"` // [x, y, w, h]
	Color    json.RawMessage `json:"color"`
}

// rawString decodes a JSON string field, yielding "" for any other shape (number,
// array, object, null) rather than failing the enclosing parse.
func rawString(raw json.RawMessage) string {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

type lgSlot struct {
	Name string `json:"name"`
}

// placedNode is a node with resolved geometry, used to compute link endpoints.
type placedNode struct {
	x, y, w, hgt      float64
	inCount, outCount int
	bypassed          bool
	collapsed         bool
}

// inPoint / outPoint are where a wire attaches for a given slot index. A COLLAPSED
// node has no slot rows — litegraph converges every wire on the pill's left/right
// edge, so the preview must too.
func (p placedNode) inPoint(slot int) (float64, float64) {
	if p.collapsed {
		return p.x, p.y - gTitleH/2
	}
	return p.x, p.y + gSlotStart + float64(clampSlot(slot, p.inCount))*gSlotSpacing
}

func (p placedNode) outPoint(slot int) (float64, float64) {
	if p.collapsed {
		return p.x + p.w, p.y - gTitleH/2
	}
	return p.x + p.w, p.y + gSlotStart + float64(clampSlot(slot, p.outCount))*gSlotSpacing
}

// graphRenderStats is what the SVG renderer actually covered, so the card can be
// HONEST about omissions instead of silently dropping elements — and so a test can
// assert coverage against the graph's true node/link sets.
type graphRenderStats struct {
	TotalNodes, DrawnNodes   int
	TotalLinks, DrawnLinks   int
	SkippedNodes             int // unparseable pos/size — could not be placed
	SkippedLinks             int // malformed tuple, or an endpoint that was not placed
	NodesCapped, LinksCapped bool
	TotalGroups, DrawnGroups int
	GroupsCapped             bool
	SkippedGroups            int  // malformed/degenerate bounding
	LinksUnreadable          bool // the whole links value was not an array
}

// workflowGraphSVG renders a UI-format (litegraph) graph as an SVG node. ok=false
// when the graph is not UI-format, has no placeable nodes, or does not parse — the
// caller then falls back to the structured view.
func workflowGraphSVG(graph []byte) (g.Node, bool) {
	node, _, ok := buildWorkflowGraphSVG(graph)
	return node, ok
}

// buildWorkflowGraphSVG is workflowGraphSVG plus the coverage stats (what it drew and
// what it dropped), split out so the card can report omissions and tests can assert
// that the emitted SVG covers the graph's true node/link sets.
func buildWorkflowGraphSVG(graph []byte) (g.Node, graphRenderStats, bool) {
	var lg lgGraph
	var st graphRenderStats
	if err := json.Unmarshal(graph, &lg); err != nil {
		return nil, st, false
	}
	if len(lg.Nodes) == 0 {
		return nil, st, false
	}
	st.TotalNodes = len(lg.Nodes)
	st.TotalGroups = len(lg.Groups)

	placed := make(map[string]placedNode, len(lg.Nodes))
	var nodeEls []g.Node
	var haveBox bool
	var minX, minY, maxX, maxY float64
	grow := func(x0, y0, x1, y1 float64) {
		if !haveBox {
			minX, minY, maxX, maxY = x0, y0, x1, y1
			haveBox = true
			return
		}
		minX, minY = minf(minX, x0), minf(minY, y0)
		maxX, maxY = maxf(maxX, x1), maxf(maxY, y1)
	}

	st.NodesCapped = len(lg.Nodes) > gMaxNodes
	for i, n := range lg.Nodes {
		if i >= gMaxNodes {
			break // render cap — a hostile graph must not blow up the response
		}
		px, py, ok := parseXY(n.Pos)
		if !ok {
			st.SkippedNodes++
			continue // can't place — skip this node (defensive)
		}
		w, hh, sok := parseXY(n.Size)
		if !sok || w <= 0 || hh <= 0 {
			w, hh = gDefaultW, gDefaultH
		}
		p := placedNode{
			x: px, y: py, w: w, hgt: hh,
			inCount:   len(n.Inputs),
			outCount:  len(n.Outputs),
			bypassed:  n.Mode == 2 || n.Mode == 4,
			collapsed: n.collapsed(),
		}
		if p.collapsed {
			// ComfyUI draws a collapsed node as a title-only pill at pos; its stored
			// size is the size it would have when expanded and must NOT be used.
			p.w, p.hgt = collapsedWidth(nodeDisplayTitle(n)), 0
		}
		if id := rawIDToString(n.ID); id != "" {
			placed[id] = p
		}
		nodeEls = append(nodeEls, svgNode(n, p))
		st.DrawnNodes++
		grow(p.x, p.y-gTitleH, p.x+p.w, p.y+p.hgt)
	}
	if !haveBox {
		return nil, st, false // no placeable node
	}

	// Groups BEHIND everything, links under nodes: same stacking order as the canvas.
	groupEls := svgGroups(lg.Groups, &st, grow)
	linkEls := svgLinks(lg.Links, placed, &st)

	minX, minY = minX-gPad, minY-gPad
	maxX, maxY = maxX+gPad, maxY+gPad
	vbW, vbH := maxX-minX, maxY-minY

	// PANNING FLOOR. The SVG used to be `width:100%;max-width:100%`, which made it
	// mathematically incapable of exceeding its overflow-auto container — so
	// overflow-x could never engage and the "scrollable both ways" claim below was
	// false. A ~4000px graph simply scaled to ~8% of its size on a phone, with
	// sub-pixel unreadable text, and there was nothing to pan.
	//
	// min-width gives it a real floor (min-width beats max-width in CSS), so on a
	// narrow viewport the SVG stays legible and the container scrolls horizontally.
	// The floor is capped at gMinRenderW so a SMALL graph is never stretched past
	// its natural size — a 400px graph keeps rendering at 400px.
	minW := vbW
	if minW > gMinRenderW {
		minW = gMinRenderW
	}
	children := []g.Node{
		g.Attr("xmlns", "http://www.w3.org/2000/svg"),
		g.Attr("viewBox", fmt.Sprintf("%s %s %s %s", f(minX), f(minY), f(vbW), f(vbH))),
		// Responsive: fill the scroll container's width, cap intrinsic height.
		g.Attr("width", f(vbW)),
		g.Attr("height", f(vbH)),
		h.Style("width:100%;height:auto;max-width:100%;min-width:" + f(minW) + "px;display:block"),
		g.Attr("role", "img"),
		g.Attr("preserveAspectRatio", "xMidYMid meet"),
	}
	children = append(children, groupEls...)
	children = append(children, linkEls...)
	children = append(children, nodeEls...)
	svg := g.El("svg", children...)

	// Genuinely scrollable BOTH ways now (see the min-width floor above), with a
	// bounded height, so a huge graph never blows the layout.
	kids := []g.Node{
		// cm-graph scopes the theme-aware fallback palette (app.css) to this render.
		h.Class("cm-graph overflow-auto rounded border border-slate-800 bg-slate-900 p-2"),
		h.Style("max-height:32rem"),
	}
	if note := graphRenderNotice(st); note != nil {
		kids = append(kids, note)
	}
	kids = append(kids, svg)
	return h.Div(kids...), st, true
}

// graphRenderNotice states what the render dropped — a capped, unplaceable or
// unresolvable element is called out instead of silently vanishing (a preview that
// quietly omits nodes/wires reads as a DIFFERENT workflow). nil when nothing was lost.
func graphRenderNotice(st graphRenderStats) g.Node {
	var parts []string
	if st.NodesCapped {
		parts = append(parts, fmt.Sprintf("showing the first %d of %d nodes", gMaxNodes, st.TotalNodes))
	}
	if st.LinksCapped {
		parts = append(parts, fmt.Sprintf("showing the first %d of %d links", gMaxLinks, st.TotalLinks))
	}
	if st.SkippedNodes > 0 {
		parts = append(parts, fmt.Sprintf("%d node%s could not be placed", st.SkippedNodes, plural(st.SkippedNodes)))
	}
	if st.SkippedLinks > 0 {
		parts = append(parts, fmt.Sprintf("%d link%s could not be drawn", st.SkippedLinks, plural(st.SkippedLinks)))
	}
	if st.LinksUnreadable {
		parts = append(parts, "the link list could not be read, so NO wires are shown")
	}
	if st.GroupsCapped {
		parts = append(parts, fmt.Sprintf("showing the first %d of %d groups", gMaxGroups, st.TotalGroups))
	}
	if st.SkippedGroups > 0 {
		parts = append(parts, fmt.Sprintf("%d group box%s could not be drawn",
			st.SkippedGroups, map[bool]string{true: "es", false: ""}[st.SkippedGroups != 1]))
	}
	if len(parts) == 0 {
		return nil
	}
	return h.P(h.Class("mb-2 text-xs text-amber-400"),
		g.Text("Incomplete preview — "+strings.Join(parts, "; ")+"."))
}

// graphPreviewCaption is the standing honesty note under the graph card: the SVG is a
// static snapshot of the saved layout, not the ComfyUI canvas. It names the things
// ComfyUI draws that this preview does not, so a difference is expected rather than
// read as "a different workflow".
func graphPreviewCaption() g.Node {
	return h.P(h.Class("mt-2 text-xs text-slate-500"),
		g.Text("Static preview of the saved layout: node positions, sizes, colors, "+
			"groups and wires come from the graph file. It does not draw widget "+
			"controls, slot labels, images/previews, or reroute points, and "+
			"muted/bypassed nodes are dimmed — so it looks plainer than the ComfyUI "+
			"canvas. Use \"Open in ComfyUI\" for the real thing."))
}

// collapsedWidth sizes a collapsed node's title pill from its title length, bounded
// to litegraph-ish proportions.
func collapsedWidth(title string) float64 {
	w := 34 + 6.2*float64(len([]rune(truncate(title, 30))))
	if w < gCollapsedMinW {
		return gCollapsedMinW
	}
	if w > gCollapsedMaxW {
		return gCollapsedMaxW
	}
	return w
}

// nodeDisplayTitle is a node's title, falling back to its class type.
func nodeDisplayTitle(n lgNode) string {
	if t := strings.TrimSpace(n.Title); t != "" {
		return t
	}
	return n.Type
}

// svgGroups renders the canvas group boxes behind everything else and extends the
// bounding box to cover them (a group can reach past its member nodes).
func svgGroups(groups []lgGroup, st *graphRenderStats, grow func(x0, y0, x1, y1 float64)) []g.Node {
	var out []g.Node
	st.GroupsCapped = len(groups) > gMaxGroups
	for i, gr := range groups {
		if i >= gMaxGroups {
			break
		}
		var b []float64
		if json.Unmarshal(gr.Bounding, &b) != nil || len(b) < 4 || b[2] <= 0 || b[3] <= 0 {
			st.SkippedGroups++
			continue // malformed bounding — skip (groups are decoration, not topology)
		}
		x, y, w, hh := b[0], b[1], b[2], b[3]
		// The group BOX keeps the author's color when there is one; the group TITLE
		// is never author-colored, so it is always theme-aware (at #e2e8f0 it was
		// invisible on the light theme's near-white card).
		color := authorHex(gr.Color)
		themedBox := color == ""
		if themedBox {
			color = gDarkGroup
		}
		out = append(out, g.El("g",
			g.El("rect",
				g.Attr("x", f(x)), g.Attr("y", f(y)),
				g.Attr("width", f(w)), g.Attr("height", f(hh)),
				g.Attr("rx", "6"),
				g.If(themedBox, h.Class("cm-g-group")),
				g.Attr("fill", color), g.Attr("fill-opacity", "0.18"),
				g.Attr("stroke", color), g.Attr("stroke-width", "1.5"),
			),
			g.El("text",
				g.Attr("x", f(x+8)), g.Attr("y", f(y+20)),
				h.Class("cm-g-text"),
				g.Attr("fill", gDarkText), g.Attr("font-size", "16"),
				g.Attr("font-family", "sans-serif"),
				g.Text(truncate(strings.TrimSpace(rawString(gr.Title)), 40)),
			),
		))
		st.DrawnGroups++
		grow(x, y, x+w, y+hh)
	}
	return out
}

// svgNode renders one placed node group: body + title bar + title + a few widget
// values + input/output slot circles. A COLLAPSED node is drawn the way ComfyUI draws
// it — a title-only pill with no body and no slot rows. Bypassed/muted nodes render
// dimmed + dashed. Per-node litegraph colors are honored (sanitized to a hex literal).
func svgNode(n lgNode, p placedNode) g.Node {
	title := nodeDisplayTitle(n)

	// The graph's own color (title bar) and bgcolor (body) WIN when present — they
	// are the user's deliberate visual grouping. Only the fallbacks are theme-aware:
	// an element painted from the fallback also carries a .cm-g-* class whose
	// app.css rule re-paints it per data-theme.
	//
	// The text colors follow the surface they sit on: title text is themed only when
	// the title bar is, widget text only when the BODY is. Otherwise a light-theme
	// flip would put near-black text on an author's dark node and make it
	// unreadable — the exact regression the author-wins rule exists to prevent.
	bodyFill, themedBody := authorHex(n.BgColor), false
	if bodyFill == "" {
		bodyFill, themedBody = gDarkBody, true
	}
	titleFill, themedTitle := authorHex(n.Color), false
	if titleFill == "" {
		titleFill, themedTitle = gDarkTitle, true
	}

	if p.collapsed {
		return svgCollapsedNode(title, p, titleFill, themedTitle)
	}

	body := g.El("rect",
		g.Attr("x", f(p.x)), g.Attr("y", f(p.y)),
		g.Attr("width", f(p.w)), g.Attr("height", f(p.hgt)),
		g.Attr("rx", "6"),
		g.If(themedBody, h.Class("cm-g-body cm-g-stroke")),
		g.If(!themedBody, h.Class("cm-g-stroke")),
		g.Attr("fill", bodyFill), g.Attr("stroke", gDarkStroke), g.Attr("stroke-width", "1"),
	)
	titleBar := g.El("rect",
		g.Attr("x", f(p.x)), g.Attr("y", f(p.y-gTitleH)),
		g.Attr("width", f(p.w)), g.Attr("height", f(gTitleH)),
		g.Attr("rx", "6"),
		g.If(themedTitle, h.Class("cm-g-title cm-g-stroke")),
		g.If(!themedTitle, h.Class("cm-g-stroke")),
		g.Attr("fill", titleFill), g.Attr("stroke", gDarkStroke), g.Attr("stroke-width", "1"),
	)
	titleText := g.El("text",
		g.Attr("x", f(p.x+7)), g.Attr("y", f(p.y-7)),
		g.If(themedTitle, h.Class("cm-g-text")),
		g.Attr("fill", gDarkText), g.Attr("font-size", "12"), g.Attr("font-family", "sans-serif"),
		g.Text(truncate(title, 30)),
	)

	els := []g.Node{titleBar, body, titleText}

	// Key widget values as text lines inside the body.
	for i, wv := range widgetScalars(n.WidgetsValues, gMaxWidgets) {
		els = append(els, g.El("text",
			g.Attr("x", f(p.x+7)), g.Attr("y", f(p.y+16+float64(i)*14)),
			g.If(themedBody, h.Class("cm-g-widget")),
			g.Attr("fill", gDarkWidget), g.Attr("font-size", "10"), g.Attr("font-family", "monospace"),
			g.Text(truncate(wv, 30)),
		))
	}

	// Slot circles: inputs LEFT, outputs RIGHT. Never author-colored → always themed.
	for i := 0; i < p.inCount; i++ {
		els = append(els, slotCircle(p.x, p.y+gSlotStart+float64(i)*gSlotSpacing))
	}
	for i := 0; i < p.outCount; i++ {
		els = append(els, slotCircle(p.x+p.w, p.y+gSlotStart+float64(i)*gSlotSpacing))
	}

	attrs := []g.Node{}
	if p.bypassed {
		attrs = append(attrs, g.Attr("opacity", "0.4"))
		// Re-stamp the body with a dashed stroke to mark the bypass/mute visually.
		els[1] = g.El("rect",
			g.Attr("x", f(p.x)), g.Attr("y", f(p.y)),
			g.Attr("width", f(p.w)), g.Attr("height", f(p.hgt)),
			g.Attr("rx", "6"),
			g.If(themedBody, h.Class("cm-g-body cm-g-stroke")),
			g.If(!themedBody, h.Class("cm-g-stroke")),
			g.Attr("fill", bodyFill), g.Attr("stroke", gDarkStroke),
			g.Attr("stroke-width", "1.5"), g.Attr("stroke-dasharray", "5 3"),
		)
	}
	attrs = append(attrs, els...)
	return g.El("g", attrs...)
}

// svgCollapsedNode draws a collapsed node: the title pill (the only thing ComfyUI
// shows) plus the single dot each side where every wire converges.
// themedTitle reports that titleFill is the FALLBACK (not an author color), so the
// pill and its text may be re-painted per data-theme.
func svgCollapsedNode(title string, p placedNode, titleFill string, themedTitle bool) g.Node {
	els := []g.Node{
		g.El("rect",
			g.Attr("x", f(p.x)), g.Attr("y", f(p.y-gTitleH)),
			g.Attr("width", f(p.w)), g.Attr("height", f(gTitleH)),
			g.Attr("rx", f(gTitleH/2)),
			g.If(themedTitle, h.Class("cm-g-title cm-g-stroke")),
			g.If(!themedTitle, h.Class("cm-g-stroke")),
			g.Attr("fill", titleFill), g.Attr("stroke", gDarkStroke), g.Attr("stroke-width", "1"),
		),
		g.El("text",
			g.Attr("x", f(p.x+10)), g.Attr("y", f(p.y-7)),
			g.If(themedTitle, h.Class("cm-g-text")),
			g.Attr("fill", gDarkText), g.Attr("font-size", "11"), g.Attr("font-family", "sans-serif"),
			g.Text(truncate(title, 30)),
		),
	}
	if p.inCount > 0 {
		cx, cy := p.inPoint(0)
		els = append(els, slotCircle(cx, cy))
	}
	if p.outCount > 0 {
		cx, cy := p.outPoint(0)
		els = append(els, slotCircle(cx, cy))
	}
	attrs := []g.Node{}
	if p.bypassed {
		attrs = append(attrs, g.Attr("opacity", "0.4"))
	}
	return g.El("g", append(attrs, els...)...)
}

func slotCircle(cx, cy float64) g.Node {
	return g.El("circle",
		g.Attr("cx", f(cx)), g.Attr("cy", f(cy)), g.Attr("r", f(gSlotR)),
		h.Class("cm-g-slot"),
		g.Attr("fill", gDarkSlot), g.Attr("stroke", gDarkSlotStroke), g.Attr("stroke-width", "0.5"),
	)
}

// svgLinks parses the links array and emits a bezier wire per resolvable link,
// colored by data type. A link referencing a skipped/absent node, or with an
// out-of-range slot, is clamped or COUNTED AS SKIPPED (and reported to the user by
// graphRenderNotice) — never panics, never silently vanishes.
//
// A litegraph links[] entry is the positional tuple
// [link_id, origin_node, origin_slot, target_node, target_slot, type] — indices 1 and
// 3 are the endpoints. A wrong index here draws wires between the wrong nodes, so the
// mapping is pinned by TestGraphSVGLinkEdgeSetMatchesGraph.
func svgLinks(raw json.RawMessage, placed map[string]placedNode, st *graphRenderStats) []g.Node {
	if len(raw) == 0 {
		return nil
	}
	// Decode entry-by-entry: ONE malformed entry must cost only itself. Decoding the
	// whole array as [][]… would make a single bad element drop EVERY wire while all
	// nodes still render — a graph that looks disconnected rather than incomplete.
	var links []json.RawMessage
	if err := json.Unmarshal(raw, &links); err != nil {
		// A links value that is not an array at all (e.g. an id-keyed object) would
		// otherwise draw ZERO wires while every node still renders — a graph that
		// silently looks disconnected. Flag it so the card says so.
		st.LinksUnreadable = true
		return nil
	}
	st.TotalLinks = len(links)
	st.LinksCapped = len(links) > gMaxLinks
	var out []g.Node
	for i, entry := range links {
		if i >= gMaxLinks {
			break // render cap (see gMaxNodes)
		}
		e, ok := parseLinkEntry(entry)
		if !ok {
			st.SkippedLinks++
			continue
		}
		op, ook := placed[e.origin]
		tp, tok := placed[e.target]
		if !ook || !tok {
			st.SkippedLinks++
			continue
		}
		oSlot, tSlot, typ := e.originSlot, e.targetSlot, e.typ

		x1, y1 := op.outPoint(oSlot)
		x2, y2 := tp.inPoint(tSlot)
		st.DrawnLinks++

		dx := (x2 - x1) * 0.4
		if dx < 40 {
			dx = 40
		}
		d := fmt.Sprintf("M %s %s C %s %s, %s %s, %s %s",
			f(x1), f(y1), f(x1+dx), f(y1), f(x2-dx), f(y2), f(x2), f(y2))
		out = append(out, g.El("path",
			g.Attr("d", d), g.Attr("fill", "none"),
			g.Attr("stroke", linkTypeColor(typ)), g.Attr("stroke-width", "2"),
			g.Attr("opacity", "0.85"),
		))
	}
	return out
}

// lgLinkEntry is one decoded link: which node/slot it leaves and which it enters.
type lgLinkEntry struct {
	origin, target         string
	originSlot, targetSlot int
	typ                    string
}

// parseLinkEntry decodes ONE links[] entry. litegraph serializes a link as the
// positional tuple [id, origin_node, origin_slot, target_node, target_slot, type];
// newer frontends may emit the equivalent OBJECT form instead. Both are accepted so a
// format change cannot silently blank out every wire.
func parseLinkEntry(raw json.RawMessage) (lgLinkEntry, bool) {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		// >= 5 elements, matching comfy.parseLink (convert.go): the endpoints live at
		// indices 1..4 and the trailing data type is optional. Requiring 6 here while
		// the converter accepts 5 would mean a link the RUN path honors is a wire the
		// PREVIEW drops — two notions of a valid link in one repo.
		if len(arr) < 5 {
			return lgLinkEntry{}, false
		}
		e := lgLinkEntry{origin: rawIDToString(arr[1]), target: rawIDToString(arr[3])}
		e.originSlot, _ = rawInt(arr[2])
		e.targetSlot, _ = rawInt(arr[4])
		if len(arr) >= 6 {
			_ = json.Unmarshal(arr[5], &e.typ) // non-string type → "" → default color
		}
		if e.origin == "" || e.target == "" {
			return lgLinkEntry{}, false
		}
		return e, true
	}
	var obj struct {
		OriginID   json.RawMessage `json:"origin_id"`
		OriginSlot int             `json:"origin_slot"`
		TargetID   json.RawMessage `json:"target_id"`
		TargetSlot int             `json:"target_slot"`
		Type       string          `json:"type"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return lgLinkEntry{}, false
	}
	e := lgLinkEntry{
		origin: rawIDToString(obj.OriginID), target: rawIDToString(obj.TargetID),
		originSlot: obj.OriginSlot, targetSlot: obj.TargetSlot, typ: obj.Type,
	}
	if e.origin == "" || e.target == "" {
		return lgLinkEntry{}, false
	}
	return e, true
}

// clampSlot bounds a slot index into [0, count-1] (0 when the node reports no
// slots), so a dangling/oversized slot index still yields a sane endpoint.
func clampSlot(i, count int) int {
	if count <= 0 {
		return 0
	}
	if i < 0 {
		return 0
	}
	if i >= count {
		return count - 1
	}
	return i
}

// --- structured (API-format / SVG-fallback) view ---

// workflowGraphStructured renders a node-by-node listing: each node's id,
// class_type/type, and its inputs — a linked ["srcId", slot] shown as a
// connection, a scalar shown as a value. Handles the API map shape first, then a
// UI-nodes fallback. All text escaped.
func workflowGraphStructured(graph []byte, format string) g.Node {
	if nodes := structuredAPINodes(graph); nodes != nil {
		return nodes
	}
	if nodes := structuredUINodes(graph); nodes != nil {
		return nodes
	}
	return h.P(h.Class("text-sm text-slate-400"), g.Text("Could not parse this workflow graph."))
}

type apiNode struct {
	ClassType string                     `json:"class_type"`
	Inputs    map[string]json.RawMessage `json:"inputs"`
}

// structuredAPINodes renders an API-format graph ({id:{class_type,inputs}}), or
// nil if it does not parse as one.
func structuredAPINodes(graph []byte) g.Node {
	var m map[string]apiNode
	if err := json.Unmarshal(graph, &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	// Confirm it is actually api-shaped (at least one class_type present).
	anyClass := false
	for _, n := range m {
		if n.ClassType != "" {
			anyClass = true
			break
		}
	}
	if !anyClass {
		return nil
	}

	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return lessNumericID(ids[i], ids[j]) })

	cards := make([]g.Node, 0, len(ids))
	if len(ids) > gMaxNodes {
		cards = append(cards, structuredTruncNote(len(ids)))
		ids = ids[:gMaxNodes]
	}
	for _, id := range ids {
		n := m[id]
		var rows []g.Node
		inputKeys := make([]string, 0, len(n.Inputs))
		for k := range n.Inputs {
			inputKeys = append(inputKeys, k)
		}
		sort.Strings(inputKeys)
		for _, k := range inputKeys {
			rows = append(rows, structuredInputRow(k, n.Inputs[k]))
		}
		title := n.ClassType
		if title == "" {
			title = "(unknown)"
		}
		cards = append(cards, h.Div(h.Class("py-2 border-b border-slate-800 last:border-0"),
			h.Div(h.Class("flex items-baseline gap-2"),
				h.Span(h.Class("text-xs font-mono text-slate-500"), g.Text("#"+id)),
				h.Span(h.Class("text-sm font-semibold text-slate-100 break-all"), g.Text(title)),
			),
			g.If(len(rows) > 0, h.Ul(h.Class("mt-1 pl-4 space-y-0.5"), g.Group(rows))),
		))
	}
	return h.Div(cards...)
}

// structuredTruncNote is the "large workflow, list truncated" banner shared by the
// structured (non-SVG) fallbacks.
func structuredTruncNote(total int) g.Node {
	return h.P(h.Class("mb-2 text-xs text-amber-400"),
		g.Text(fmt.Sprintf("Large workflow — showing the first %d of %d nodes.", gMaxNodes, total)))
}

// structuredInputRow renders one input: a link ["srcId", slot] as a connection,
// otherwise the scalar value. Untrusted — escaped.
func structuredInputRow(name string, raw json.RawMessage) g.Node {
	label := name + ": "
	// A connection is a 2-element array [srcId, slot].
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) == 2 {
		src := rawIDToString(arr[0])
		slot, _ := rawInt(arr[1])
		if src != "" {
			return h.Li(h.Class("text-xs text-slate-300 font-mono break-all"),
				g.Text(label),
				h.Span(h.Class("text-indigo-400"),
					g.Text(fmt.Sprintf("← #%s[%d]", src, slot))))
		}
	}
	val, _ := scalarText(raw)
	if val == "" {
		val = strings.TrimSpace(string(raw))
	}
	return h.Li(h.Class("text-xs text-slate-300 font-mono break-all"),
		g.Text(label+truncate(val, 60)))
}

// structuredUINodes renders a UI-format graph as a simple id/type listing with any
// connected inputs (from the node's own inputs' link references), or nil if it does
// not parse.
func structuredUINodes(graph []byte) g.Node {
	var lg lgGraph
	if err := json.Unmarshal(graph, &lg); err != nil || len(lg.Nodes) == 0 {
		return nil
	}
	cards := make([]g.Node, 0, len(lg.Nodes))
	nodes := lg.Nodes
	if len(nodes) > gMaxNodes {
		cards = append(cards, structuredTruncNote(len(nodes)))
		nodes = nodes[:gMaxNodes]
	}
	for _, n := range nodes {
		id := rawIDToString(n.ID)
		title := strings.TrimSpace(n.Title)
		if title == "" {
			title = n.Type
		}
		var rows []g.Node
		for _, in := range n.Inputs {
			if strings.TrimSpace(in.Name) == "" {
				continue
			}
			rows = append(rows, h.Li(h.Class("text-xs text-slate-300 font-mono break-all"), g.Text(in.Name)))
		}
		cards = append(cards, h.Div(h.Class("py-2 border-b border-slate-800 last:border-0"),
			h.Div(h.Class("flex items-baseline gap-2"),
				h.Span(h.Class("text-xs font-mono text-slate-500"), g.Text("#"+id)),
				h.Span(h.Class("text-sm font-semibold text-slate-100 break-all"), g.Text(title)),
			),
			g.If(len(rows) > 0, h.Ul(h.Class("mt-1 pl-4 space-y-0.5"), g.Group(rows))),
		))
	}
	return h.Div(cards...)
}

// --- small parsing helpers ---

// parseXY parses a [x,y] array or a {"0":x,"1":y} object of numbers. ok=false for
// any other shape or a non-numeric coord (defensive — skip the bad element).
func parseXY(raw json.RawMessage) (float64, float64, bool) {
	if len(raw) == 0 {
		return 0, 0, false
	}
	var arr []float64
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) >= 2 {
		return arr[0], arr[1], true
	}
	var obj map[string]float64
	if err := json.Unmarshal(raw, &obj); err == nil {
		x, okx := obj["0"]
		y, oky := obj["1"]
		if okx && oky {
			return x, y, true
		}
	}
	return 0, 0, false
}

// rawIDToString normalizes a node id (number or string) to a bare string.
func rawIDToString(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	return strings.Trim(s, `"`)
}

// rawInt parses a JSON number into an int, ok=false otherwise.
func rawInt(raw json.RawMessage) (int, bool) {
	var fl float64
	if err := json.Unmarshal(raw, &fl); err != nil {
		return 0, false
	}
	return int(fl), true
}

// scalarText yields the display text of a scalar JSON value (string unquoted;
// number/bool as-is), ok=false for objects/arrays/null.
func scalarText(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	if s[0] == '{' || s[0] == '[' {
		return "", false
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str, true
	}
	return s, true
}

// widgetScalars returns up to max scalar widget values (strings/numbers) from a
// node's widgets_values (array form); non-scalars are skipped.
func widgetScalars(raw json.RawMessage, max int) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	var out []string
	for _, v := range arr {
		if len(out) >= max {
			break
		}
		if s, ok := scalarText(v); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// lessNumericID orders ids numerically when both are integers, else lexically.
func lessNumericID(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

// truncate shortens s to n runes with an ellipsis (rune-safe).
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// f formats a coordinate compactly (one decimal, trailing zero trimmed).
func f(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
