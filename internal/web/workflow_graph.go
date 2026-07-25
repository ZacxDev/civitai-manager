package web

import (
	"encoding/json"
	"fmt"
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
	Nodes []lgNode        `json:"nodes"`
	Links json.RawMessage `json:"links"`
}

type lgNode struct {
	ID            json.RawMessage `json:"id"`
	Type          string          `json:"type"`
	Title         string          `json:"title"`
	Mode          int             `json:"mode"`
	Pos           json.RawMessage `json:"pos"`
	Size          json.RawMessage `json:"size"`
	Inputs        []lgSlot        `json:"inputs"`
	Outputs       []lgSlot        `json:"outputs"`
	WidgetsValues json.RawMessage `json:"widgets_values"`
}

type lgSlot struct {
	Name string `json:"name"`
}

// placedNode is a node with resolved geometry, used to compute link endpoints.
type placedNode struct {
	x, y, w, hgt      float64
	inCount, outCount int
	bypassed          bool
}

// workflowGraphSVG renders a UI-format (litegraph) graph as an SVG node. ok=false
// when the graph is not UI-format, has no placeable nodes, or does not parse — the
// caller then falls back to the structured view.
func workflowGraphSVG(graph []byte) (g.Node, bool) {
	var lg lgGraph
	if err := json.Unmarshal(graph, &lg); err != nil {
		return nil, false
	}
	if len(lg.Nodes) == 0 {
		return nil, false
	}

	placed := make(map[string]placedNode, len(lg.Nodes))
	var nodeEls []g.Node
	var haveBox bool
	var minX, minY, maxX, maxY float64

	for _, n := range lg.Nodes {
		px, py, ok := parseXY(n.Pos)
		if !ok {
			continue // can't place — skip this node (defensive)
		}
		w, hh, sok := parseXY(n.Size)
		if !sok || w <= 0 || hh <= 0 {
			w, hh = gDefaultW, gDefaultH
		}
		id := rawIDToString(n.ID)
		p := placedNode{
			x: px, y: py, w: w, hgt: hh,
			inCount:  len(n.Inputs),
			outCount: len(n.Outputs),
			bypassed: n.Mode == 2 || n.Mode == 4,
		}
		if id != "" {
			placed[id] = p
		}
		nodeEls = append(nodeEls, svgNode(n, p))

		top := py - gTitleH
		if !haveBox {
			minX, minY, maxX, maxY = px, top, px+w, py+hh
			haveBox = true
		} else {
			minX = minf(minX, px)
			minY = minf(minY, top)
			maxX = maxf(maxX, px+w)
			maxY = maxf(maxY, py+hh)
		}
	}
	if !haveBox {
		return nil, false // no placeable node
	}

	// Links UNDER nodes so wires do not obscure node chrome.
	linkEls := svgLinks(lg.Links, placed)

	minX -= gPad
	minY -= gPad
	vbW := (maxX - minX) + gPad
	vbH := (maxY - minY) + gPad

	children := []g.Node{
		g.Attr("xmlns", "http://www.w3.org/2000/svg"),
		g.Attr("viewBox", fmt.Sprintf("%s %s %s %s", f(minX), f(minY), f(vbW), f(vbH))),
		// Responsive: fill the scroll container's width, cap intrinsic height.
		g.Attr("width", f(vbW)),
		g.Attr("height", f(vbH)),
		h.Style("width:100%;height:auto;max-width:100%;display:block"),
		g.Attr("role", "img"),
		g.Attr("preserveAspectRatio", "xMidYMid meet"),
	}
	children = append(children, linkEls...)
	children = append(children, nodeEls...)
	svg := g.El("svg", children...)

	// Scrollable both ways, bounded height, so a huge graph never blows the layout.
	return h.Div(
		h.Class("overflow-auto rounded border border-slate-800 bg-slate-900 p-2"),
		h.Style("max-height:32rem"),
		svg,
	), true
}

// svgNode renders one placed node group: body + title bar + title + a few widget
// values + input/output slot circles. Bypassed/muted nodes render dimmed + dashed.
func svgNode(n lgNode, p placedNode) g.Node {
	title := strings.TrimSpace(n.Title)
	if title == "" {
		title = n.Type
	}

	bodyFill := "#334155" // slate-700 — reads on light + dark card surfaces
	titleFill := "#1e293b"
	stroke := "#475569"
	textColor := "#e2e8f0"

	body := g.El("rect",
		g.Attr("x", f(p.x)), g.Attr("y", f(p.y)),
		g.Attr("width", f(p.w)), g.Attr("height", f(p.hgt)),
		g.Attr("rx", "6"),
		g.Attr("fill", bodyFill), g.Attr("stroke", stroke), g.Attr("stroke-width", "1"),
	)
	titleBar := g.El("rect",
		g.Attr("x", f(p.x)), g.Attr("y", f(p.y-gTitleH)),
		g.Attr("width", f(p.w)), g.Attr("height", f(gTitleH)),
		g.Attr("rx", "6"),
		g.Attr("fill", titleFill), g.Attr("stroke", stroke), g.Attr("stroke-width", "1"),
	)
	titleText := g.El("text",
		g.Attr("x", f(p.x+7)), g.Attr("y", f(p.y-7)),
		g.Attr("fill", textColor), g.Attr("font-size", "12"), g.Attr("font-family", "sans-serif"),
		g.Text(truncate(title, 30)),
	)

	els := []g.Node{titleBar, body, titleText}

	// Key widget values as text lines inside the body.
	for i, wv := range widgetScalars(n.WidgetsValues, gMaxWidgets) {
		els = append(els, g.El("text",
			g.Attr("x", f(p.x+7)), g.Attr("y", f(p.y+16+float64(i)*14)),
			g.Attr("fill", "#cbd5e1"), g.Attr("font-size", "10"), g.Attr("font-family", "monospace"),
			g.Text(truncate(wv, 30)),
		))
	}

	// Slot circles: inputs LEFT, outputs RIGHT.
	for i := 0; i < p.inCount; i++ {
		els = append(els, slotCircle(p.x, p.y+gSlotStart+float64(i)*gSlotSpacing, "#64748b"))
	}
	for i := 0; i < p.outCount; i++ {
		els = append(els, slotCircle(p.x+p.w, p.y+gSlotStart+float64(i)*gSlotSpacing, "#64748b"))
	}

	attrs := []g.Node{}
	if p.bypassed {
		attrs = append(attrs, g.Attr("opacity", "0.4"))
		// Re-stamp the body with a dashed stroke to mark the bypass/mute visually.
		els[1] = g.El("rect",
			g.Attr("x", f(p.x)), g.Attr("y", f(p.y)),
			g.Attr("width", f(p.w)), g.Attr("height", f(p.hgt)),
			g.Attr("rx", "6"),
			g.Attr("fill", bodyFill), g.Attr("stroke", stroke),
			g.Attr("stroke-width", "1.5"), g.Attr("stroke-dasharray", "5 3"),
		)
	}
	attrs = append(attrs, els...)
	return g.El("g", attrs...)
}

func slotCircle(cx, cy float64, fill string) g.Node {
	return g.El("circle",
		g.Attr("cx", f(cx)), g.Attr("cy", f(cy)), g.Attr("r", f(gSlotR)),
		g.Attr("fill", fill), g.Attr("stroke", "#0f172a"), g.Attr("stroke-width", "0.5"),
	)
}

// svgLinks parses the links array and emits a bezier wire per resolvable link,
// colored by data type. A link referencing a skipped/absent node, or with an
// out-of-range slot, is clamped or skipped — never panics.
func svgLinks(raw json.RawMessage, placed map[string]placedNode) []g.Node {
	if len(raw) == 0 {
		return nil
	}
	var links [][]json.RawMessage
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil
	}
	var out []g.Node
	for _, l := range links {
		// [link_id, origin_node, origin_slot, target_node, target_slot, type]
		if len(l) < 6 {
			continue
		}
		origin := rawIDToString(l[1])
		target := rawIDToString(l[3])
		op, ook := placed[origin]
		tp, tok := placed[target]
		if !ook || !tok {
			continue
		}
		oSlot, _ := rawInt(l[2])
		tSlot, _ := rawInt(l[4])
		var typ string
		_ = json.Unmarshal(l[5], &typ) // non-string type → "" → default color

		x1 := op.x + op.w
		y1 := op.y + gSlotStart + float64(clampSlot(oSlot, op.outCount))*gSlotSpacing
		x2 := tp.x
		y2 := tp.y + gSlotStart + float64(clampSlot(tSlot, tp.inCount))*gSlotSpacing

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
				h.Span(h.Class("text-sm font-semibold text-slate-100"), g.Text(title)),
			),
			g.If(len(rows) > 0, h.Ul(h.Class("mt-1 pl-4 space-y-0.5"), g.Group(rows))),
		))
	}
	return h.Div(cards...)
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
			return h.Li(h.Class("text-xs text-slate-300 font-mono"),
				g.Text(label),
				h.Span(h.Class("text-indigo-400"),
					g.Text(fmt.Sprintf("← #%s[%d]", src, slot))))
		}
	}
	val, _ := scalarText(raw)
	if val == "" {
		val = strings.TrimSpace(string(raw))
	}
	return h.Li(h.Class("text-xs text-slate-300 font-mono"),
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
	for _, n := range lg.Nodes {
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
			rows = append(rows, h.Li(h.Class("text-xs text-slate-300 font-mono"), g.Text(in.Name)))
		}
		cards = append(cards, h.Div(h.Class("py-2 border-b border-slate-800 last:border-0"),
			h.Div(h.Class("flex items-baseline gap-2"),
				h.Span(h.Class("text-xs font-mono text-slate-500"), g.Text("#"+id)),
				h.Span(h.Class("text-sm font-semibold text-slate-100"), g.Text(title)),
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
