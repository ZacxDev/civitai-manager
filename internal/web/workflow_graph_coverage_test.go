package web

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file is the DETERMINISTIC comparison for the "the Graph card shows a different
// workflow than ComfyUI" report: parse the stored graph, compute its true node-id set
// and link edge set, and assert the emitted SVG covers them — or visibly reports what
// it dropped. It is driven by the real workflow-587 graph shape (75 nodes, 140 links,
// 19 groups, 15 collapsed nodes, per-node litegraph colors).

func loadWebTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// truthGraph is the independent (non-renderer) parse of a litegraph document used as
// the expected answer.
type truthGraph struct {
	Nodes []struct {
		ID    json.Number `json:"id"`
		Pos   []float64   `json:"pos"`
		Size  []float64   `json:"size"`
		Flags struct {
			Collapsed bool `json:"collapsed"`
		} `json:"flags"`
	} `json:"nodes"`
	Links  [][]json.RawMessage `json:"links"`
	Groups []json.RawMessage   `json:"groups"`
}

func parseTruth(t *testing.T, raw []byte) truthGraph {
	t.Helper()
	var tg truthGraph
	if err := json.Unmarshal(raw, &tg); err != nil {
		t.Fatalf("truth parse: %v", err)
	}
	return tg
}

// TestGraphSVGCoversEveryNodeAndLinkOfRealGraph is the regression assertion for the
// bug-2 report: for workflow 587's real graph, the renderer must place EVERY node and
// draw EVERY link — no silent drops, no truncation notice.
func TestGraphSVGCoversEveryNodeAndLinkOfRealGraph(t *testing.T) {
	raw := loadWebTestdata(t, "wf587_graph_shape.json")
	truth := parseTruth(t, raw)

	node, st, ok := buildWorkflowGraphSVG(raw)
	if !ok {
		t.Fatal("real graph should render as an SVG")
	}
	if st.TotalNodes != len(truth.Nodes) || st.DrawnNodes != len(truth.Nodes) {
		t.Errorf("nodes: drew %d of %d (skipped %d), want all %d",
			st.DrawnNodes, st.TotalNodes, st.SkippedNodes, len(truth.Nodes))
	}
	if st.TotalLinks != len(truth.Links) || st.DrawnLinks != len(truth.Links) {
		t.Errorf("links: drew %d of %d (skipped %d), want all %d",
			st.DrawnLinks, st.TotalLinks, st.SkippedLinks, len(truth.Links))
	}
	if st.NodesCapped || st.LinksCapped {
		t.Errorf("a 75-node/140-link graph must not hit the render caps: %+v", st)
	}
	if st.TotalGroups != len(truth.Groups) || st.DrawnGroups != len(truth.Groups) {
		t.Errorf("groups: drew %d of %d, want all %d",
			st.DrawnGroups, st.TotalGroups, len(truth.Groups))
	}

	out := renderGraphNode(t, node)
	if strings.Contains(out, "Incomplete preview") {
		t.Error("nothing was dropped, so no incomplete-preview notice should render")
	}
	if n := strings.Count(out, "<path"); n != len(truth.Links) {
		t.Errorf("emitted %d wire paths, want %d", n, len(truth.Links))
	}
	// Every group title must appear (they are the canvas landmarks the user compares
	// against). Titles come from the fixture, so they are the real ones.
	for _, want := range []string{"Control Center", "Hand ADetailer", "Face ADetailer", "Load Image"} {
		if !strings.Contains(out, want) {
			t.Errorf("group %q missing from the render", want)
		}
	}
}

// TestGraphSVGCollapsedNodesDrawnAsPills pins the concrete geometry defect: a node
// with flags.collapsed must be drawn at the title-pill size, NOT at its stored
// (expanded) size — otherwise it buries its neighbours and its wires attach hundreds
// of pixels from where ComfyUI puts them.
func TestGraphSVGCollapsedNodesDrawnAsPills(t *testing.T) {
	raw := loadWebTestdata(t, "wf587_graph_shape.json")
	truth := parseTruth(t, raw)

	var collapsed [][]float64 // {x, y, storedW, storedH}
	for _, n := range truth.Nodes {
		if n.Flags.Collapsed && len(n.Pos) >= 2 && len(n.Size) >= 2 {
			collapsed = append(collapsed, []float64{n.Pos[0], n.Pos[1], n.Size[0], n.Size[1]})
		}
	}
	if len(collapsed) < 10 {
		t.Fatalf("fixture should carry the real graph's collapsed nodes, found %d", len(collapsed))
	}

	node, _, ok := buildWorkflowGraphSVG(raw)
	if !ok {
		t.Fatal("render failed")
	}
	out := renderGraphNode(t, node)

	for _, c := range collapsed {
		x, y, w, hh := c[0], c[1], c[2], c[3]
		// The stored (expanded) body rect must NOT be emitted for a collapsed node.
		bad := fmt.Sprintf(`<rect x="%s" y="%s" width="%s" height="%s"`, f(x), f(y), f(w), f(hh))
		if strings.Contains(out, bad) {
			t.Errorf("collapsed node at (%s,%s) was drawn at its expanded size %sx%s", f(x), f(y), f(w), f(hh))
		}
		// A pill of the collapsed height must be present at the node's title position.
		pill := fmt.Sprintf(`<rect x="%s" y="%s" width="`, f(x), f(y-gTitleH))
		if !strings.Contains(out, pill) {
			t.Errorf("no collapsed pill at (%s,%s)", f(x), f(y-gTitleH))
		}
	}
}

// TestGraphSVGCollapsedWireEndpointsConverge proves every wire on a collapsed node
// attaches at the pill edge (one point per side), not at the slot rows it would have
// when expanded — the concrete "wires going somewhere else" symptom.
func TestGraphSVGCollapsedWireEndpointsConverge(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"Src","pos":[0,0],"size":[100,40],"outputs":[{"name":"a"},{"name":"b"}]},
	  {"id":2,"type":"Wide","pos":[400,0],"size":[300,400],"flags":{"collapsed":true},
	   "inputs":[{"name":"i"},{"name":"j"},{"name":"k"}],"outputs":[{"name":"o"}]}
	],"links":[[1,1,0,2,0,"MODEL"],[2,1,1,2,2,"CLIP"]]}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.DrawnLinks != 2 {
		t.Fatalf("drew %d links, want 2", st.DrawnLinks)
	}
	out := renderGraphNode(t, node)
	// Both wires must END at the pill's left edge mid-title, regardless of slot index.
	want := fmt.Sprintf(`, 400 %s"`, f(-gTitleH/2))
	if n := strings.Count(out, want); n != 2 {
		t.Errorf("expected both wires to converge at the collapsed pill edge (%q x%d):\n%s", want, n, out)
	}
	// And the expanded-size body rect must be absent.
	if strings.Contains(out, `width="300" height="400"`) {
		t.Error("collapsed node drawn at its expanded size")
	}
}

// TestGraphSVGAcceptsObjectFormLinks proves a links[] entry serialized as an OBJECT
// (newer frontends) still draws, and that one unparseable entry costs only itself
// rather than blanking every wire.
func TestGraphSVGAcceptsObjectFormLinks(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"A","pos":[0,0],"size":[100,40],"outputs":[{"name":"o"}]},
	  {"id":2,"type":"B","pos":[400,0],"size":[100,40],"inputs":[{"name":"i"}]}
	],"links":[
	  {"id":1,"origin_id":1,"origin_slot":0,"target_id":2,"target_slot":0,"type":"MODEL"},
	  "garbage",
	  [3,1,0,2,0,"CLIP"]
	]}`
	_, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.DrawnLinks != 2 || st.SkippedLinks != 1 {
		t.Errorf("stats = %+v, want 2 drawn / 1 skipped", st)
	}
}

// TestGraphSVGLinkEdgeSetMatchesGraph pins the positional link-tuple decoding
// ([id, origin_node, origin_slot, target_node, target_slot, type]): the drawn wire set
// must be exactly the graph's edge set. A wrong index mapping would draw the right
// NUMBER of wires between the WRONG nodes, which is invisible to a count-only check —
// so this asserts each wire's endpoint coordinates against independently computed ones.
func TestGraphSVGLinkEdgeSetMatchesGraph(t *testing.T) {
	// Two producers and two consumers, deliberately positioned so every endpoint
	// coordinate is distinct: a swapped origin/target index would move every path.
	const graph = `{"nodes":[
	  {"id":1,"type":"A","pos":[0,100],"size":[100,60],"outputs":[{"name":"o"}]},
	  {"id":2,"type":"B","pos":[0,400],"size":[100,60],"outputs":[{"name":"o"}]},
	  {"id":3,"type":"C","pos":[500,100],"size":[100,60],"inputs":[{"name":"i"},{"name":"j"}]},
	  {"id":4,"type":"D","pos":[900,400],"size":[100,60],"inputs":[{"name":"i"}]}
	],"links":[
	  [1,1,0,3,0,"MODEL"],
	  [2,2,0,3,1,"CLIP"],
	  [3,1,0,4,0,"VAE"]
	]}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.DrawnLinks != 3 || st.SkippedLinks != 0 {
		t.Fatalf("stats = %+v, want 3 drawn / 0 skipped", st)
	}
	out := renderGraphNode(t, node)

	// Independently computed endpoints: origin = (pos.x + width, slotY(origin_slot)),
	// target = (pos.x, slotY(target_slot)).
	slotY := func(y float64, slot int) float64 { return y + gSlotStart + float64(slot)*gSlotSpacing }
	for _, want := range []struct{ x1, y1, x2, y2 float64 }{
		{100, slotY(100, 0), 500, slotY(100, 0)}, // 1[0] → 3[0]
		{100, slotY(400, 0), 500, slotY(100, 1)}, // 2[0] → 3[1]
		{100, slotY(100, 0), 900, slotY(400, 0)}, // 1[0] → 4[0]
	} {
		prefix := fmt.Sprintf(`d="M %s %s C `, f(want.x1), f(want.y1))
		suffix := fmt.Sprintf(`, %s %s"`, f(want.x2), f(want.y2))
		found := false
		for _, seg := range strings.Split(out, "<path ") {
			if strings.Contains(seg, prefix) && strings.Contains(seg, suffix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no wire from (%s,%s) to (%s,%s):\n%s",
				f(want.x1), f(want.y1), f(want.x2), f(want.y2), out)
		}
	}
}

// TestGraphSVGReportsWhatItDropped proves the renderer no longer drops elements
// SILENTLY: an unplaceable node, a malformed link tuple and a link to a missing node
// are counted and surfaced in the card.
func TestGraphSVGReportsWhatItDropped(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"A","pos":[0,0],"size":[100,60],"outputs":[{"name":"o"}]},
	  {"id":2,"type":"B","pos":"nonsense","size":[100,60]},
	  {"id":3,"type":"C","pos":{"x":1},"size":[100,60]}
	],"links":[
	  [1,1,0,99,0,"MODEL"],
	  [2,1,0],
	  "not-a-link"
	]}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.SkippedNodes != 2 {
		t.Errorf("skipped nodes = %d, want 2", st.SkippedNodes)
	}
	if st.SkippedLinks != 3 {
		t.Errorf("skipped links = %d, want 3", st.SkippedLinks)
	}
	out := renderGraphNode(t, node)
	for _, want := range []string{"Incomplete preview", "2 nodes could not be placed", "3 links could not be drawn"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// TestGraphSVGCapsAreReported builds graphs past both render caps and proves the
// caps hold AND are stated, rather than silently truncating.
func TestGraphSVGCapsAreReported(t *testing.T) {
	var nodes, links []string
	total := gMaxNodes + 5
	for i := 1; i <= total; i++ {
		nodes = append(nodes, fmt.Sprintf(
			`{"id":%d,"type":"N","pos":[%d,0],"size":[80,40],"inputs":[{"name":"i"}],"outputs":[{"name":"o"}]}`,
			i, i*10))
	}
	for i := 1; i <= gMaxLinks+5; i++ {
		links = append(links, fmt.Sprintf(`[%d,1,0,2,0,"MODEL"]`, i))
	}
	graph := `{"nodes":[` + strings.Join(nodes, ",") + `],"links":[` + strings.Join(links, ",") + `]}`

	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.DrawnNodes != gMaxNodes || !st.NodesCapped {
		t.Errorf("node cap: drew %d capped=%v, want %d/true", st.DrawnNodes, st.NodesCapped, gMaxNodes)
	}
	if st.DrawnLinks != gMaxLinks || !st.LinksCapped {
		t.Errorf("link cap: drew %d capped=%v, want %d/true", st.DrawnLinks, st.LinksCapped, gMaxLinks)
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, fmt.Sprintf("showing the first %d of %d nodes", gMaxNodes, total)) {
		t.Error("node truncation not reported")
	}
	if !strings.Contains(out, fmt.Sprintf("showing the first %d of %d links", gMaxLinks, gMaxLinks+5)) {
		t.Error("link truncation not reported")
	}
}

// TestGraphSVGHonorsNodeColorsSafely proves a node's litegraph color/bgcolor is used
// when it is a hex literal, and that a hostile value is NEVER echoed into the SVG.
func TestGraphSVGHonorsNodeColorsSafely(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"A","pos":[0,0],"size":[100,60],"color":"#323","bgcolor":"#535"},
	  {"id":2,"type":"B","pos":[200,0],"size":[100,60],
	   "color":"url(#x) onload=alert(1)","bgcolor":"javascript:alert(1)"}
	]}`
	node, _, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, `fill="#323"`) || !strings.Contains(out, `fill="#535"`) {
		t.Errorf("hex node colors not honored:\n%s", out)
	}
	for _, bad := range []string{"onload", "javascript:", "url(#x)"} {
		if strings.Contains(out, bad) {
			t.Errorf("unsafe color value %q leaked into the SVG:\n%s", bad, out)
		}
	}
}

// TestGraphSVGGroupsRenderedAndBounded proves group boxes are drawn behind the nodes,
// extend the viewBox, and survive a malformed bounding.
func TestGraphSVGGroupsRenderedAndBounded(t *testing.T) {
	const graph = `{"nodes":[{"id":1,"type":"A","pos":[100,100],"size":[80,40]}],
	  "groups":[
	    {"title":"Big Region","bounding":[0,0,2000,900],"color":"#3f789e"},
	    {"title":"Bad","bounding":"nope","color":"#444"},
	    {"title":"Zero","bounding":[0,0,0,0]}
	  ]}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.TotalGroups != 3 || st.DrawnGroups != 1 {
		t.Errorf("groups: %d of %d drawn, want 1 of 3", st.DrawnGroups, st.TotalGroups)
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, "Big Region") || strings.Contains(out, ">Bad<") {
		t.Errorf("group rendering wrong:\n%s", out)
	}
	// The group reaches far past the single node, so the viewBox must cover it.
	if !strings.Contains(out, `viewBox="-26 -26 2052 952"`) {
		t.Errorf("viewBox should cover the group box:\n%s", out)
	}
	// Groups come BEFORE the node elements (drawn behind them).
	if strings.Index(out, "Big Region") > strings.Index(out, ">A<") {
		t.Error("group box should be emitted behind the nodes")
	}
}

// TestGraphSVGToleratesOddlyTypedNodeFields is the F4 regression: the whole document
// is decoded in ONE Unmarshal, so a strictly-typed field meeting an unexpected JSON
// type would abort the parse and drop the ENTIRE graph to the structured fallback.
// Every wild-varying field must decode defensively instead.
func TestGraphSVGToleratesOddlyTypedNodeFields(t *testing.T) {
	for _, tc := range []struct{ name, graph string }{
		{"numeric node color", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40],"color":0}]}`},
		{"numeric node bgcolor", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40],"bgcolor":12}]}`},
		{"array flags", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40],"flags":[]}]}`},
		{"string flags", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40],"flags":"x"}]}`},
		{"non-bool collapsed", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40],"flags":{"collapsed":1}}]}`},
		{"numeric group color", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40]}],
		  "groups":[{"title":"G","bounding":[0,0,50,50],"color":7}]}`},
		{"numeric group title", `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40]}],
		  "groups":[{"title":5,"bounding":[0,0,50,50]}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, st, ok := buildWorkflowGraphSVG([]byte(tc.graph))
			if !ok {
				t.Fatalf("one oddly-typed field must not blank the whole SVG (stats %+v)", st)
			}
			if st.DrawnNodes != 1 {
				t.Errorf("drew %d nodes, want 1", st.DrawnNodes)
			}
			out := renderGraphNode(t, node)
			// The bad value must never reach the SVG; the theme default is used.
			if !strings.Contains(out, `fill="#334155"`) && !strings.Contains(out, `fill="#1e293b"`) {
				t.Errorf("expected the fallback node colors:\n%s", out)
			}
		})
	}
}

// TestGraphSVGReportsUnreadableLinkList is the F5 regression: a links value that is
// not an array at all drew zero wires with NO notice — nodes rendered, so the graph
// silently looked disconnected.
func TestGraphSVGReportsUnreadableLinkList(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"A","pos":[0,0],"size":[100,40],"outputs":[{"name":"o"}]},
	  {"id":2,"type":"B","pos":[400,0],"size":[100,40],"inputs":[{"name":"i"}]}
	],"links":{"1":[1,1,0,2,0,"MODEL"]}}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if !st.LinksUnreadable || st.DrawnLinks != 0 {
		t.Errorf("stats = %+v, want LinksUnreadable with 0 drawn", st)
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, "Incomplete preview") || !strings.Contains(out, "NO wires are shown") {
		t.Errorf("an unreadable link list must be reported:\n%s", out)
	}
}

// TestGraphSVGReportsDroppedGroups proves malformed and capped group boxes are
// counted and surfaced rather than vanishing.
func TestGraphSVGReportsDroppedGroups(t *testing.T) {
	const graph = `{"nodes":[{"id":1,"type":"A","pos":[0,0],"size":[100,40]}],
	  "groups":[{"title":"ok","bounding":[0,0,50,50]},{"title":"bad","bounding":"x"},
	            {"title":"zero","bounding":[0,0,0,0]}]}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.SkippedGroups != 2 {
		t.Errorf("skipped groups = %d, want 2", st.SkippedGroups)
	}
	if !strings.Contains(renderGraphNode(t, node), "2 group boxes could not be drawn") {
		t.Error("dropped group boxes must be reported")
	}
}

// TestGraphSVGAcceptsFiveElementLinkTuple pins the arity agreement with
// comfy.parseLink: the trailing data type is optional, so a 5-element tuple the RUN
// path honors must not be a wire the PREVIEW drops.
func TestGraphSVGAcceptsFiveElementLinkTuple(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":1,"type":"A","pos":[0,0],"size":[100,40],"outputs":[{"name":"o"}]},
	  {"id":2,"type":"B","pos":[400,0],"size":[100,40],"inputs":[{"name":"i"}]}
	],"links":[[1,1,0,2,0],[2,1,0,2]]}`
	node, st, ok := buildWorkflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("render failed")
	}
	if st.DrawnLinks != 1 || st.SkippedLinks != 1 {
		t.Errorf("stats = %+v, want 1 drawn (5-element) / 1 skipped (4-element)", st)
	}
	// No type element → the neutral default wire color.
	if !strings.Contains(renderGraphNode(t, node), `stroke="#94a3b8"`) {
		t.Error("a typeless link should use the default wire color")
	}
}

// TestGraphSectionCarriesHonestCaption proves the card states what the static preview
// does and does not show.
func TestGraphSectionCarriesHonestCaption(t *testing.T) {
	out := renderGraphNode(t, workflowGraphSection(loadWebTestdata(t, "wf587_graph_shape.json"), "ui"))
	for _, want := range []string{"Static preview", "Open in ComfyUI", "muted/bypassed nodes are dimmed"} {
		if !strings.Contains(out, want) {
			t.Errorf("graph card caption missing %q", want)
		}
	}
}
