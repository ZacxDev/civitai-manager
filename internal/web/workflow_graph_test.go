package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// renderGraphNode renders a gomponents node to its HTML/SVG string.
func renderGraphNode(t *testing.T, n g.Node) string {
	t.Helper()
	if n == nil {
		return ""
	}
	var b strings.Builder
	if err := n.Render(&b); err != nil {
		t.Fatalf("render: %v", err)
	}
	return b.String()
}

const twoNodeUIGraph = `{
  "nodes":[
    {"id":1,"type":"CheckpointLoaderSimple","pos":[10,20],"size":[200,100],
     "outputs":[{"name":"MODEL"}],"widgets_values":["sdxl.safetensors"]},
    {"id":2,"type":"KSampler","pos":[400,20],"size":[220,160],
     "inputs":[{"name":"model"}]}
  ],
  "links":[[5,1,0,2,0,"MODEL"]]
}`

// TestGraphSVGTwoNodesOneLink asserts a UI graph renders an SVG with a node rect
// per node, a link <path>, and a viewBox covering the node coordinates.
func TestGraphSVGTwoNodesOneLink(t *testing.T) {
	node, ok := workflowGraphSVG([]byte(twoNodeUIGraph))
	if !ok {
		t.Fatal("expected an SVG for a UI-format graph with coordinates")
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, "<svg") {
		t.Fatalf("no <svg> element:\n%s", out)
	}
	// 2 nodes × (body + title bar) = at least 4 rects.
	if n := strings.Count(out, "<rect"); n < 4 {
		t.Errorf("expected >=4 node rects, got %d", n)
	}
	if !strings.Contains(out, "<path") {
		t.Errorf("expected a link <path>")
	}
	// The MODEL link should be colored per the type→color map.
	if !strings.Contains(out, "#8b5cf6") {
		t.Errorf("MODEL link should use the MODEL wire color")
	}
	// Slot circles present.
	if !strings.Contains(out, "<circle") {
		t.Errorf("expected slot circles")
	}
	// viewBox must cover node 2's right edge (x=400 + w=220 = 620).
	vb := extractAttr(out, "viewBox")
	if vb == "" {
		t.Fatalf("no viewBox")
	}
	parts := strings.Fields(vb)
	if len(parts) != 4 {
		t.Fatalf("viewBox = %q", vb)
	}
	w := mustFloat(t, parts[2])
	if w < 500 {
		t.Errorf("viewBox width %.1f does not span the graph (~620)", w)
	}
}

// TestGraphSVGEscapesNodeTitle feeds a node title containing a <script> tag and
// asserts it is HTML-escaped in the SVG (no raw tag).
func TestGraphSVGEscapesNodeTitle(t *testing.T) {
	graph := `{"nodes":[{"id":1,"type":"X","title":"<script>alert(1)</script>","pos":[0,0],"size":[200,80]}],"links":[]}`
	node, ok := workflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("expected SVG")
	}
	out := renderGraphNode(t, node)
	if strings.Contains(out, "<script>alert(1)") {
		t.Errorf("node title must be escaped, found raw <script>:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped node title")
	}
}

// TestGraphSVGBypassedNode asserts a muted/bypassed node (mode 2/4) renders with
// the dimmed + dashed marker.
func TestGraphSVGBypassedNode(t *testing.T) {
	graph := `{"nodes":[{"id":1,"type":"X","mode":4,"pos":[0,0],"size":[200,80]}],"links":[]}`
	node, ok := workflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("expected SVG")
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, `opacity="0.4"`) {
		t.Errorf("bypassed node should be dimmed")
	}
	if !strings.Contains(out, "stroke-dasharray") {
		t.Errorf("bypassed node should be dashed")
	}
}

// TestGraphSVGSkipsNodeMissingPos asserts a node without a position is skipped (not
// a panic) while the rest of the graph still renders.
func TestGraphSVGSkipsNodeMissingPos(t *testing.T) {
	graph := `{"nodes":[
	  {"id":1,"type":"Good","pos":[0,0],"size":[120,80]},
	  {"id":2,"type":"NoPos"}
	],"links":[]}`
	node, ok := workflowGraphSVG([]byte(graph))
	if !ok {
		t.Fatal("expected SVG (one node is placeable)")
	}
	out := renderGraphNode(t, node)
	if !strings.Contains(out, "Good") {
		t.Errorf("placeable node should render")
	}
	if strings.Contains(out, "NoPos") {
		t.Errorf("un-placeable node should be skipped, not rendered")
	}
}

// TestGraphSVGRejectsNonUI asserts an API-format map does not produce an SVG.
func TestGraphSVGRejectsNonUI(t *testing.T) {
	api := `{"3":{"class_type":"KSampler","inputs":{}}}`
	if _, ok := workflowGraphSVG([]byte(api)); ok {
		t.Errorf("API-format map should not yield an SVG")
	}
	if _, ok := workflowGraphSVG([]byte("not json")); ok {
		t.Errorf("garbage should not yield an SVG")
	}
}

// TestGraphStructuredAPI asserts an API graph falls back to a structured listing of
// class_types + incoming connections.
func TestGraphStructuredAPI(t *testing.T) {
	api := `{
	  "3":{"class_type":"KSampler","inputs":{"model":["4",0],"seed":123}},
	  "4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"x.safetensors"}}
	}`
	out := renderGraphNode(t, workflowGraphStructured([]byte(api), "api"))
	for _, want := range []string{"KSampler", "CheckpointLoaderSimple", "x.safetensors", "← #4[0]"} {
		if !strings.Contains(out, want) {
			t.Errorf("structured API view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "<svg") {
		t.Errorf("structured view must not contain an SVG")
	}
}

// TestGraphStructuredEscapes asserts untrusted class_type/value text is escaped in
// the structured view.
func TestGraphStructuredEscapes(t *testing.T) {
	api := `{"1":{"class_type":"<b>evil</b>","inputs":{"x":"<i>v</i>"}}}`
	out := renderGraphNode(t, workflowGraphStructured([]byte(api), "api"))
	if strings.Contains(out, "<b>evil</b>") || strings.Contains(out, "<i>v</i>") {
		t.Errorf("structured view must escape untrusted text:\n%s", out)
	}
}

// TestGraphSectionPicksSVGForUI asserts workflowGraphSection returns an SVG for a
// UI graph and a structured view for an API graph.
func TestGraphSectionPicksSVGForUI(t *testing.T) {
	svgOut := renderGraphNode(t, workflowGraphSection([]byte(twoNodeUIGraph), "ui"))
	if !strings.Contains(svgOut, "<svg") {
		t.Errorf("UI graph section should render an SVG")
	}
	apiOut := renderGraphNode(t, workflowGraphSection(
		[]byte(`{"3":{"class_type":"KSampler","inputs":{}}}`), "api"))
	if strings.Contains(apiOut, "<svg") {
		t.Errorf("API graph section should not render an SVG")
	}
	// A UI graph that cannot be laid out falls back to the structured view.
	fb := renderGraphNode(t, workflowGraphSection([]byte(`{"nodes":[{"id":1,"type":"NoCoords"}]}`), "ui"))
	if strings.Contains(fb, "<svg") {
		t.Errorf("un-layoutable UI graph should fall back to structured, got SVG")
	}
	if !strings.Contains(fb, "NoCoords") {
		t.Errorf("structured fallback should list the node")
	}
}

// TestWorkflowDetailRendersSVGNotRawJSON asserts the detail page renders the graph
// as an SVG (UI format) rather than a raw-JSON <pre>, keeping raw JSON behind a
// disclosure.
func TestWorkflowDetailRendersSVGNotRawJSON(t *testing.T) {
	srv := newWorkflowServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, twoNodeUIGraph)

	rec := get(t, srv, "/workflows/"+id)
	if rec.Code != 200 {
		t.Fatalf("detail = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<svg") {
		t.Errorf("UI workflow detail should render an SVG graph")
	}
	if !strings.Contains(body, "View raw JSON") {
		t.Errorf("raw JSON should remain available behind a disclosure")
	}
}

// TestWorkflowDetailAPIStructured asserts an API-format workflow detail renders the
// structured view (class types), not an SVG.
func TestWorkflowDetailAPIStructured(t *testing.T) {
	srv := newWorkflowServer(t)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"3":{"class_type":"KSampler","inputs":{"model":["4",0]}},"4":{"class_type":"CheckpointLoaderSimple","inputs":{}}}`)

	body := get(t, srv, "/workflows/"+id).Body.String()
	if strings.Contains(body, "<svg") {
		t.Errorf("API workflow detail should not render an SVG")
	}
	if !strings.Contains(body, "KSampler") || !strings.Contains(body, "CheckpointLoaderSimple") {
		t.Errorf("API workflow detail should list class types")
	}
}

// extractAttr returns the value of the first name="value" attribute in s.
func extractAttr(s, name string) string {
	key := name + `="`
	i := strings.Index(s, key)
	if i < 0 {
		return ""
	}
	i += len(key)
	j := strings.Index(s[i:], `"`)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse float %q: %v", s, err)
	}
	return v
}
