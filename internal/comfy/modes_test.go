package comfy

import (
	"encoding/json"
	"os"
	"testing"
)

// The two fixtures are REDUCED-BUT-VERBATIM extracts of the user's real library:
//
//	wf581_modes_multimode.json — workflow 581, "WAN 2.2 Smooth Workflow v6.0"
//	  (CivitAI model 1847730). One ACTIVE "Fast Groups Bypasser (rgthree)" titled
//	  "Worflows" with toggleRestriction "max one" matching the PURPLE groups, i.e.
//	  the 4 parallel pipelines; every pipeline node bypassed (mode 4); each pipeline
//	  contains its own "<X> AUDIO" sub-group driven by a SEPARATE, non-exclusive
//	  "Audio Enabler" bypasser.
//
//	wf588_modes_muter.json — workflow 588, "Lonecat's AIO Z-Image ver 18" (CivitAI
//	  model 2184844). An "always one" BYPASSER over 3 model-loader groups with one
//	  already active, plus a "max one" MUTER (off mode = 2) over 2 mask groups.
//
// Nodes/groups were only DROPPED; every retained object is byte-verbatim from the
// stored graph (real ids, types, positions, sizes, modes, colors, properties,
// boundings), so the geometry these tests exercise is the authors' real geometry.
func loadModeFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return json.RawMessage(b)
}

func TestDetectModeSelectorsOn581(t *testing.T) {
	sels := DetectModeSelectors(loadModeFixture(t, "wf581_modes_multimode.json"))
	if len(sels) != 1 {
		t.Fatalf("want exactly 1 mode selector, got %d (%+v)", len(sels), sels)
	}
	s := sels[0]
	if s.Key != "151" {
		t.Errorf("selector key = %q, want the rgthree toggler node id 151", s.Key)
	}
	if s.Label != "Worflows" {
		t.Errorf("selector label = %q, want the toggler's own title", s.Label)
	}
	if s.OffMode != 4 {
		t.Errorf("OffMode = %d, want 4 (a Fast Groups BYPASSER turns groups off by bypass)", s.OffMode)
	}
	// The three non-exclusive "Audio Enabler" bypassers must NOT become selectors.
	want := []string{"TEXT2VIDEO", "AUDIO2VIDEO", "IMAGE2VIDEO", "FIRST2LASTFRAME"}
	if len(s.Modes) != len(want) {
		t.Fatalf("want %d modes, got %d: %+v", len(want), len(s.Modes), s.Modes)
	}
	for i, w := range want {
		if s.Modes[i].Title != w {
			t.Errorf("mode[%d].Title = %q, want %q", i, s.Modes[i].Title, w)
		}
		if s.Modes[i].Active {
			t.Errorf("mode %q reported active, but every pipeline in 581 is bypassed", w)
		}
		if s.Modes[i].NodeCount == 0 {
			t.Errorf("mode %q has no selectable nodes", w)
		}
	}
	if got := s.Selected(); got != "" {
		t.Errorf("Selected() = %q, want \"\" — no pipeline is enabled in the stored graph", got)
	}
}

// TestDetectModeSelectorsExcludesNestedSubSwitchGroups pins the subtraction that
// keeps a mode switch from stomping an independent sub-switch: each 581 pipeline
// nests an "<X> AUDIO" group driven by its own non-exclusive "Audio Enabler", and
// those nodes must not ride along when the pipeline is enabled.
func TestDetectModeSelectorsExcludesNestedSubSwitchGroups(t *testing.T) {
	raw := loadModeFixture(t, "wf581_modes_multimode.json")
	sels := DetectModeSelectors(raw)
	byTitle := map[string]ModeGroup{}
	for _, m := range sels[0].Modes {
		byTitle[m.Title] = m
	}

	doc := decodeModeDoc(t, raw)
	audio := map[string][]string{} // mode title -> node ids inside its AUDIO sub-group
	for _, pair := range [][2]string{{"TEXT2VIDEO", "T2V AUDIO"}, {"IMAGE2VIDEO", "I2V AUDIO"}, {"FIRST2LASTFRAME", "F2LF AUDIO"}} {
		audio[pair[0]] = nodeIDsInGroup(doc, pair[1])
		if len(audio[pair[0]]) == 0 {
			t.Fatalf("fixture lost the %q sub-group — the test would be vacuous", pair[1])
		}
	}

	for mode, audioIDs := range audio {
		total := len(nodeIDsInGroup(doc, mode))
		if got := byTitle[mode].NodeCount; got != total-len(audioIDs) {
			t.Errorf("mode %q: NodeCount = %d, want %d (%d in the group minus %d in its nested AUDIO sub-switch)",
				mode, got, total-len(audioIDs), total, len(audioIDs))
		}
	}

	// And the applied graph must leave every AUDIO node at its stored mode.
	out := ApplyModeSelection(raw, map[string]string{sels[0].Key: byTitle["TEXT2VIDEO"].Key})
	before, after := nodeModes(t, raw), nodeModes(t, out)
	for _, id := range audio["TEXT2VIDEO"] {
		if after[id] != before[id] {
			t.Errorf("nested sub-switch node %s changed mode %d → %d", id, before[id], after[id])
		}
	}
}

// TestApplyModeSelectionEnablesExactlyOnePipeline is the core behavioural claim:
// selecting a mode un-bypasses that group's nodes AND NOTHING ELSE.
func TestApplyModeSelectionEnablesExactlyOnePipeline(t *testing.T) {
	raw := loadModeFixture(t, "wf581_modes_multimode.json")
	sels := DetectModeSelectors(raw)
	sel := sels[0]
	doc := decodeModeDoc(t, raw)

	for _, m := range sel.Modes {
		out := ApplyModeSelection(raw, map[string]string{sel.Key: m.Key})
		before, after := nodeModes(t, raw), nodeModes(t, out)

		expected := map[string]bool{}
		for _, id := range nodeIDsInGroup(doc, m.Title) {
			expected[id] = true
		}
		for _, sub := range []string{"T2V AUDIO", "I2V AUDIO", "F2LF AUDIO"} {
			for _, id := range nodeIDsInGroup(doc, sub) {
				delete(expected, id)
			}
		}

		for id, wasMode := range before {
			changed := after[id] != wasMode
			if expected[id] {
				if after[id] != 0 {
					t.Errorf("mode %q: node %s should be enabled, got mode %d", m.Title, id, after[id])
				}
			} else if changed {
				t.Errorf("mode %q: node %s outside the selected group changed %d → %d",
					m.Title, id, wasMode, after[id])
			}
		}
		if len(expected) != m.NodeCount {
			t.Errorf("mode %q: expected-set %d != reported NodeCount %d", m.Title, len(expected), m.NodeCount)
		}
	}
}

// TestApplyModeSelectionDisablesTheOutgoingMode covers the "max one" half: picking a
// mode while another is live must switch the other OFF, not run both pipelines.
func TestApplyModeSelectionDisablesTheOutgoingMode(t *testing.T) {
	raw := loadModeFixture(t, "wf588_modes_muter.json")
	sels := DetectModeSelectors(raw)
	if len(sels) != 2 {
		t.Fatalf("want 2 selectors (an 'always one' bypasser + a 'max one' muter), got %d", len(sels))
	}
	var bypasser ModeSelector
	for _, s := range sels {
		if s.Label == "Model selector" {
			bypasser = s
		}
	}
	if bypasser.Key == "" {
		t.Fatalf("did not find the 'Model selector' toggler in %+v", sels)
	}
	if bypasser.OffMode != 4 {
		t.Errorf("bypasser OffMode = %d, want 4", bypasser.OffMode)
	}
	// "Diffusion Model" ships active; switching to "GGUF Model" must flip both.
	if got := bypasser.Selected(); got == "" {
		t.Fatal("an 'always one' selector with a live group must report a selection")
	}
	doc := decodeModeDoc(t, raw)
	var gguf ModeGroup
	for _, m := range bypasser.Modes {
		if m.Title == "GGUF Model" {
			gguf = m
		}
	}
	out := ApplyModeSelection(raw, map[string]string{bypasser.Key: gguf.Key})
	after := nodeModes(t, out)
	for _, id := range nodeIDsInGroup(doc, "GGUF Model") {
		if after[id] != 0 {
			t.Errorf("GGUF Model node %s = mode %d, want 0", id, after[id])
		}
	}
	for _, id := range nodeIDsInGroup(doc, "Diffusion Model") {
		if after[id] != 4 {
			t.Errorf("outgoing Diffusion Model node %s = mode %d, want 4 (bypassed)", id, after[id])
		}
	}
}

// TestApplyModeSelectionMuterUsesMuteAsOff pins that a Fast Groups MUTER turns its
// other groups off with mode 2, not 4 — real data (588) has both kinds in one file.
func TestApplyModeSelectionMuterUsesMuteAsOff(t *testing.T) {
	raw := loadModeFixture(t, "wf588_modes_muter.json")
	var muter ModeSelector
	for _, s := range DetectModeSelectors(raw) {
		if s.Label == "Picture or Mask?" {
			muter = s
		}
	}
	if muter.Key == "" {
		t.Fatal("did not detect the 'Picture or Mask?' Fast Groups Muter")
	}
	if muter.OffMode != 2 {
		t.Fatalf("muter OffMode = %d, want 2 (mute)", muter.OffMode)
	}
	doc := decodeModeDoc(t, raw)
	var create ModeGroup
	for _, m := range muter.Modes {
		if m.Title == "Create Mask'" {
			create = m
		}
	}
	out := ApplyModeSelection(raw, map[string]string{muter.Key: create.Key})
	after := nodeModes(t, out)
	for _, id := range nodeIDsInGroup(doc, "Create Mask'") {
		if after[id] != 0 {
			t.Errorf("selected node %s = %d, want 0", id, after[id])
		}
	}
	for _, id := range nodeIDsInGroup(doc, "Load Mask'") {
		if after[id] != 2 {
			t.Errorf("outgoing muter node %s = %d, want 2 (muted, not bypassed)", id, after[id])
		}
	}
}

// TestOrdinaryWorkflowsHaveNoModeSelector is THE regression guard: the pack that
// looks superficially similar (many bypassed nodes, many groups, rgthree togglers)
// but declares no exclusivity must be left completely alone.
func TestOrdinaryWorkflowsHaveNoModeSelector(t *testing.T) {
	cases := []struct {
		name  string
		graph string
	}{
		{"api-format graph", `{"3":{"class_type":"KSampler","inputs":{"seed":1}}}`},
		{"ui graph with no groups", `{"nodes":[{"id":1,"type":"KSampler","mode":0,"pos":[0,0],"size":[10,10]}],"groups":[]}`},
		{"empty", ``},
		{"garbage", `not json`},
		{
			// The 1386234 pack shape: an ACTIVE Fast Groups Bypasser over many
			// uniformly-bypassed groups, but toggleRestriction "default" — optional
			// features, not modes. A "uniformly bypassed group" heuristic would have
			// mangled this; the toggleRestriction rule must not.
			"non-exclusive toggler over bypassed groups",
			`{"nodes":[
			  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[0,0],"size":[100,50],
			   "properties":{"matchColors":"pale_blue","matchTitle":"","toggleRestriction":"default"}},
			  {"id":2,"type":"KSampler","mode":4,"pos":[210,210],"size":[100,100]},
			  {"id":3,"type":"VAEDecode","mode":4,"pos":[610,210],"size":[100,100]}],
			 "groups":[
			  {"title":"Detailer","bounding":[200,200,200,200],"color":"#3f789e"},
			  {"title":"Upscale","bounding":[600,200,200,200],"color":"#3f789e"}]}`,
		},
		{
			// Exclusive restriction, but the toggler itself is switched off.
			"bypassed exclusive toggler",
			`{"nodes":[
			  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":4,"pos":[0,0],"size":[100,50],
			   "properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"max one"}},
			  {"id":2,"type":"KSampler","mode":4,"pos":[210,210],"size":[100,100]},
			  {"id":3,"type":"VAEDecode","mode":4,"pos":[610,210],"size":[100,100]}],
			 "groups":[
			  {"title":"A","bounding":[200,200,200,200],"color":"#a1309b"},
			  {"title":"B","bounding":[600,200,200,200],"color":"#a1309b"}]}`,
		},
		{
			// Exclusive, but only ONE group matches — a single option is not a choice.
			"exclusive toggler over one group",
			`{"nodes":[
			  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[0,0],"size":[100,50],
			   "properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"max one"}},
			  {"id":2,"type":"KSampler","mode":4,"pos":[210,210],"size":[100,100]}],
			 "groups":[{"title":"A","bounding":[200,200,200,200],"color":"#a1309b"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(tc.graph)
			if sels := DetectModeSelectors(raw); len(sels) != 0 {
				t.Fatalf("detected %d selectors, want 0: %+v", len(sels), sels)
			}
			// Byte-identity, not just semantic equality: an ordinary workflow must go
			// through the run path exactly as stored.
			for _, choices := range []map[string]string{
				nil, {}, {"1": "1:0"}, {"1": "2:0"}, {"../../etc/passwd": "1:0"},
			} {
				if got := ApplyModeSelection(raw, choices); string(got) != tc.graph {
					t.Fatalf("graph changed under choices %v:\n got %s\nwant %s", choices, got, tc.graph)
				}
			}
		})
	}
}

// TestApplyModeSelectionRejectsHostileKeys pins that only a real mode of a real
// selector can ever be applied — the accepted set is re-derived from the graph.
func TestApplyModeSelectionRejectsHostileKeys(t *testing.T) {
	raw := loadModeFixture(t, "wf581_modes_multimode.json")
	sel := DetectModeSelectors(raw)[0]
	other := DetectModeSelectors(loadModeFixture(t, "wf588_modes_muter.json"))[0]

	for _, tc := range []struct {
		name    string
		choices map[string]string
	}{
		{"unknown selector", map[string]string{"999999": sel.Modes[0].Key}},
		{"unknown mode index", map[string]string{sel.Key: sel.Key + ":9999"}},
		{"mode key from another selector", map[string]string{sel.Key: other.Modes[0].Key}},
		{"non-numeric mode index", map[string]string{sel.Key: sel.Key + ":../../x"}},
		{"empty mode", map[string]string{sel.Key: ""}},
		{"selector key used as mode key", map[string]string{sel.Key: sel.Key}},
		{"huge index", map[string]string{sel.Key: sel.Key + ":99999999999999999999"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ApplyModeSelection(raw, tc.choices); string(got) != string(raw) {
				t.Errorf("hostile choices %v mutated the graph", tc.choices)
			}
		})
	}
}

// TestApplyModeSelectionNeverMutatesTheInput is the "stored workflow is untouched"
// discipline ApplyUIWidgetOverrides established.
func TestApplyModeSelectionNeverMutatesTheInput(t *testing.T) {
	raw := loadModeFixture(t, "wf581_modes_multimode.json")
	original := append([]byte(nil), raw...)
	sel := DetectModeSelectors(raw)[0]
	for _, m := range sel.Modes {
		out := ApplyModeSelection(raw, map[string]string{sel.Key: m.Key})
		if string(raw) != string(original) {
			t.Fatal("ApplyModeSelection mutated its input graph")
		}
		if string(out) == string(raw) {
			t.Fatalf("mode %q produced no change at all", m.Title)
		}
	}
	// Re-selecting the mode that is ALREADY live must be byte-identical.
	raw588 := loadModeFixture(t, "wf588_modes_muter.json")
	for _, s := range DetectModeSelectors(raw588) {
		if cur := s.Selected(); cur != "" {
			if got := ApplyModeSelection(raw588, map[string]string{s.Key: cur}); string(got) != string(raw588) {
				t.Errorf("re-selecting the live mode of %q rewrote the graph", s.Label)
			}
		}
	}
}

// TestApplyModeSelectionPreservesEveryOtherField proves the rewrite touches ONLY
// `mode` — node ids, types, widget values, links, groups and extras survive.
func TestApplyModeSelectionPreservesEveryOtherField(t *testing.T) {
	raw := loadModeFixture(t, "wf581_modes_multimode.json")
	sel := DetectModeSelectors(raw)[0]
	out := ApplyModeSelection(raw, map[string]string{sel.Key: sel.Modes[0].Key})

	strip := func(b json.RawMessage) string {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(b, &top); err != nil {
			t.Fatal(err)
		}
		var nodes []map[string]json.RawMessage
		if err := json.Unmarshal(top["nodes"], &nodes); err != nil {
			t.Fatal(err)
		}
		for _, n := range nodes {
			delete(n, "mode")
		}
		nb, _ := json.Marshal(nodes)
		top["nodes"] = nb
		s, _ := json.Marshal(top)
		return string(s)
	}
	if strip(raw) != strip(out) {
		t.Error("a field other than node.mode changed")
	}
}

// TestModeMembershipWithOverlappingAndNestedGroups pins the geometry rule directly:
// membership is the node bounding-rect CENTRE inside the group bounding (LiteGraph's
// own containsCentre), so a node in a nested group belongs to BOTH, a node straddling
// an edge belongs to whichever side its centre falls on, and when two modes of one
// selector overlap the SELECTED one wins.
func TestModeMembershipWithOverlappingAndNestedGroups(t *testing.T) {
	// Group A [0,0,400,400] fully contains group B [100,100,200,200] — both purple,
	// both controlled by the same exclusive toggler, i.e. nested MODE groups.
	//   n10 centre (50,55)   → A only
	//   n11 centre (200,205) → A and B
	//   n12 centre (395,205) → A only (its pos is inside A, its centre near the edge)
	//   n13 centre (700,205) → neither
	graph := `{"nodes":[
	  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[-500,-500],"size":[100,50],
	   "properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"max one"}},
	  {"id":10,"type":"KSampler","mode":4,"pos":[40,70],"size":[20,20]},
	  {"id":11,"type":"KSampler","mode":4,"pos":[190,220],"size":[20,20]},
	  {"id":12,"type":"KSampler","mode":4,"pos":[385,220],"size":[20,20]},
	  {"id":13,"type":"KSampler","mode":4,"pos":[690,220],"size":[20,20]}],
	 "groups":[
	  {"title":"A","bounding":[0,0,400,400],"color":"#a1309b"},
	  {"title":"B","bounding":[100,100,200,200],"color":"#a1309b"}]}`
	raw := json.RawMessage(graph)
	sels := DetectModeSelectors(raw)
	if len(sels) != 1 || len(sels[0].Modes) != 2 {
		t.Fatalf("want 1 selector with 2 modes, got %+v", sels)
	}
	byTitle := map[string]ModeGroup{}
	for _, m := range sels[0].Modes {
		byTitle[m.Title] = m
	}
	if byTitle["A"].NodeCount != 3 {
		t.Errorf("group A NodeCount = %d, want 3 (10, 11, 12)", byTitle["A"].NodeCount)
	}
	if byTitle["B"].NodeCount != 1 {
		t.Errorf("group B NodeCount = %d, want 1 (11 only — 12's CENTRE is outside B)", byTitle["B"].NodeCount)
	}

	// Selecting B: 11 is in both, and the SELECTED mode wins → enabled. 10 and 12 are
	// only in A (the outgoing mode) → stay bypassed. 13 is in neither → untouched.
	out := ApplyModeSelection(raw, map[string]string{sels[0].Key: byTitle["B"].Key})
	after := nodeModes(t, out)
	for id, want := range map[string]int{"10": 4, "11": 0, "12": 4, "13": 4} {
		if after[id] != want {
			t.Errorf("selecting B: node %s = mode %d, want %d", id, after[id], want)
		}
	}

	// Selecting A: 10, 11, 12 enabled; 11 is also in the outgoing B but the selected
	// group wins, so it must NOT come back bypassed.
	out = ApplyModeSelection(raw, map[string]string{sels[0].Key: byTitle["A"].Key})
	after = nodeModes(t, out)
	for id, want := range map[string]int{"10": 0, "11": 0, "12": 0, "13": 4} {
		if after[id] != want {
			t.Errorf("selecting A: node %s = mode %d, want %d", id, after[id], want)
		}
	}
}

// TestModeSelectorMatchTitleFilter covers the OTHER real matcher shape: 588's
// selectors filter by matchTitle (a case-insensitive regex), not by color.
func TestModeSelectorMatchTitleFilter(t *testing.T) {
	graph := `{"nodes":[
	  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[-500,-500],"size":[100,50],
	   "properties":{"matchColors":"","matchTitle":"model","toggleRestriction":"always one"}},
	  {"id":10,"type":"KSampler","mode":4,"pos":[10,40],"size":[20,20]},
	  {"id":11,"type":"KSampler","mode":4,"pos":[310,40],"size":[20,20]},
	  {"id":12,"type":"KSampler","mode":0,"pos":[610,40],"size":[20,20]}],
	 "groups":[
	  {"title":"GGUF Model","bounding":[0,0,200,200]},
	  {"title":"Checkpoint MODEL","bounding":[300,0,200,200]},
	  {"title":"Sampling","bounding":[600,0,200,200]}]}`
	sels := DetectModeSelectors(json.RawMessage(graph))
	if len(sels) != 1 {
		t.Fatalf("want 1 selector, got %d", len(sels))
	}
	if len(sels[0].Modes) != 2 {
		t.Fatalf("want 2 modes (title match is case-insensitive and must not catch 'Sampling'), got %+v", sels[0].Modes)
	}
	// Selecting a mode must leave the unmatched "Sampling" group alone.
	out := ApplyModeSelection(json.RawMessage(graph), map[string]string{sels[0].Key: sels[0].Modes[0].Key})
	if got := nodeModes(t, out)["12"]; got != 0 {
		t.Errorf("node in the unmatched group changed to mode %d", got)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func decodeModeDoc(t *testing.T, raw json.RawMessage) *modeGraphDoc {
	t.Helper()
	doc, ok := parseModeGraph(raw)
	if !ok {
		t.Fatal("fixture did not parse as a UI graph with groups")
	}
	return doc
}

// nodeIDsInGroup lists the ids of every node whose bounding-rect centre is inside
// the named group — computed independently of the production membership helper so
// the assertions are not self-fulfilling.
func nodeIDsInGroup(doc *modeGraphDoc, title string) []string {
	var out []string
	for gi := range doc.Groups {
		if doc.Groups[gi].Title != title {
			continue
		}
		gr, ok := doc.Groups[gi].rect()
		if !ok {
			continue
		}
		for ni := range doc.Nodes {
			cx, cy := doc.Nodes[ni].centre()
			if cx >= gr.x && cx <= gr.x+gr.w && cy >= gr.y && cy <= gr.y+gr.h {
				out = append(out, idToString(doc.Nodes[ni].ID))
			}
		}
	}
	return out
}

func nodeModes(t *testing.T, raw json.RawMessage) map[string]int {
	t.Helper()
	var doc struct {
		Nodes []struct {
			ID   json.RawMessage `json:"id"`
			Mode int             `json:"mode"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	out := make(map[string]int, len(doc.Nodes))
	for _, n := range doc.Nodes {
		out[idToString(n.ID)] = n.Mode
	}
	return out
}
