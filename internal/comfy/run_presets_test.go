package comfy

import (
	"encoding/json"
	"os"
	"testing"
)

// presetGraph is a UI graph with one titled prompt, a KSampler (its seed carries
// the control_after_generate slot, so later widget indices are shifted) and an
// EmptyLatentImage — the same shape the run Parameters panel is built against.
const presetGraph = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "POSITIVE",
     "widgets_values": ["a scenic mountain"], "inputs": []},
    {"id": 3, "type": "KSampler",
     "widgets_values": [1234, "randomize", 20, 8.0, "euler", "normal", 1.0], "inputs": []},
    {"id": 5, "type": "EmptyLatentImage", "widgets_values": [1024, 768, 2], "inputs": []}
  ],
  "links": []
}`

// presetGraphRetargeted is presetGraph with node 3's class swapped to
// CLIPTextEncode: the key {3,0} still exists but now drives a PROMPT rather than a
// SEED. This is the headline hazard the tuple check exists to catch.
const presetGraphRetargeted = `{
  "nodes": [
    {"id": 6, "type": "CLIPTextEncode", "title": "POSITIVE",
     "widgets_values": ["a scenic mountain"], "inputs": []},
    {"id": 3, "type": "CLIPTextEncode", "title": "NEGATIVE",
     "widgets_values": ["blurry"], "inputs": []}
  ],
  "links": []
}`

// presetModeGraph packs two mutually-exclusive pipelines into one file, declared
// by an ACTIVE rgthree bypasser with toggleRestriction "max one". Both groups ship
// bypassed, so the graph exposes NOTHING until a mode is applied.
const presetModeGraph = `{
  "nodes": [
    {"id": 1, "type": "Fast Groups Bypasser (rgthree)", "mode": 0, "pos": [0,0], "size": [100,50],
     "properties": {"matchColors": "purple", "matchTitle": "", "toggleRestriction": "max one"}},
    {"id": 2, "type": "KSampler", "mode": 4, "pos": [210,210], "size": [100,100],
     "widgets_values": [777, "randomize", 30, 7.5, "euler", "normal", 1.0], "inputs": []},
    {"id": 3, "type": "CLIPTextEncode", "title": "V2V", "mode": 4, "pos": [610,210], "size": [100,100],
     "widgets_values": ["video prompt"], "inputs": []}
  ],
  "links": [],
  "groups": [
    {"title": "TEXT2IMAGE", "bounding": [200,200,200,200], "color": "#a1309b"},
    {"title": "IMAGE2VIDEO", "bounding": [600,200,200,200], "color": "#a1309b"}
  ]
}`

// entriesFor snapshots every live input of graph (with the given mode selection)
// into stored preset entries carrying value.
func entriesFor(t *testing.T, graph string, modes map[string]string, value func(RunInput) string) []PresetEntry {
	t.Helper()
	g := json.RawMessage(graph)
	if len(modes) > 0 {
		g = ApplyModeSelection(g, modes)
	}
	var out []PresetEntry
	for _, ri := range DetectRunInputs(g, nil) {
		out = append(out, PresetEntryFor(ri, value(ri)))
	}
	if len(out) == 0 {
		t.Fatalf("fixture exposes no run inputs")
	}
	return out
}

func fieldByLabel(rec PresetReconciliation, label string) (PresetField, bool) {
	for _, f := range rec.Fields {
		if f.Input.Label == label {
			return f, true
		}
	}
	return PresetField{}, false
}

func dropNames(drops []PresetDrop) []string {
	out := make([]string, 0, len(drops))
	for _, d := range drops {
		out = append(out, d.Name)
	}
	return out
}

// TestSeedInputsSelectedByKind pins the ONE correct seed hook: RunInput.Kind ==
// RunInputSeed. The obvious-looking alternative, isSeedControlSlot, matches the
// control_after_generate STRING ("fixed"/"randomize"), not the seed value — a
// preset keyed on it would store/replace the wrong slot entirely and
// typedOverrideValue would silently refuse the write (green tests, dead in
// production).
func TestSeedInputsSelectedByKind(t *testing.T) {
	raw, err := os.ReadFile("testdata/wf587_converted_widgets.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	var seeds []RunInput
	for _, ri := range DetectRunInputs(raw, nil) {
		if ri.Kind == RunInputSeed {
			seeds = append(seeds, ri)
		}
	}
	if len(seeds) != 1 {
		t.Fatalf("seed inputs = %d, want exactly 1", len(seeds))
	}
	if seeds[0].NodeID != "40" || seeds[0].WidgetIndex != 0 {
		t.Errorf("seed input = node %s widget %d, want node 40 widget 0",
			seeds[0].NodeID, seeds[0].WidgetIndex)
	}
	// And the entry snapshotted for it carries the seed KIND, not a control string.
	e := PresetEntryFor(seeds[0], "42")
	if e.Kind != RunInputSeed {
		t.Errorf("snapshotted kind = %q, want %q", e.Kind, RunInputSeed)
	}
	if isSeedControlSlot(json.RawMessage(`"` + e.Value + `"`)) {
		t.Error("a seed VALUE must never satisfy isSeedControlSlot")
	}
}

// TestSeedInputsIgnoreControlSlot pins the control-slot cursor skip: the
// KSampler's sampler_name/scheduler sit at widgets_values 4 and 5, not 3 and 4.
// An implementation that forgets it would key a preset's "Steps" onto the seed
// control string.
func TestSeedInputsIgnoreControlSlot(t *testing.T) {
	inputs := DetectRunInputs(json.RawMessage(presetGraph), nil)
	want := map[string]int{"Seed": 0, "Steps": 2, "CFG": 3, "Sampler": 4, "Scheduler": 5, "Denoise": 6}
	got := map[string]int{}
	for _, ri := range inputs {
		if ri.ClassType == "KSampler" {
			got[ri.Label] = ri.WidgetIndex
		}
		if ri.Kind == RunInputText && ri.Current == "randomize" {
			t.Errorf("a control_after_generate slot was surfaced as an editable input: %+v", ri)
		}
	}
	for label, idx := range want {
		if got[label] != idx {
			t.Errorf("%s widget index = %d, want %d", label, got[label], idx)
		}
	}
}

// TestReconcileExactHashAppliesEverything: equal non-blank hashes ⇒ the hash IS
// the proof. Every stored value is applied with NO per-entry tuple check and no
// banner. Over-strict reconciliation here makes valid presets look broken.
func TestReconcileExactHashAppliesEverything(t *testing.T) {
	entries := entriesFor(t, presetGraph, nil, func(ri RunInput) string { return "STORED:" + ri.InputName })
	// Deliberately blank every tuple: on the EXACT path they must not be consulted.
	for i := range entries {
		entries[i].Kind, entries[i].ClassType, entries[i].InputName = "", "", ""
	}
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, true)

	if !rec.Exact {
		t.Error("Exact = false, want true")
	}
	if rec.NeedsBanner() {
		t.Errorf("exact hash must render no banner; dropped=%v new=%v modes=%v",
			dropNames(rec.Dropped), rec.NewInputs, dropNames(rec.DroppedModes))
	}
	if rec.Applied() != len(entries) {
		t.Errorf("applied = %d, want %d", rec.Applied(), len(entries))
	}
	for _, f := range rec.Fields {
		if !f.FromPreset {
			t.Errorf("field %q not from preset", f.Input.Label)
		}
	}
}

// TestReconcileBlankHashIsDrifted: a blank hash on EITHER side cannot be proven
// equal, so it takes the drifted path — the pre-0011 NULL graph_hash case must
// never silently apply stale positions.
func TestReconcileBlankHashIsDrifted(t *testing.T) {
	entries := entriesFor(t, presetGraph, nil, func(ri RunInput) string { return "kept" })
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, false)
	if rec.Exact {
		t.Fatal("Exact = true for an unproven hash")
	}
	// The tuples still match (same graph), so the values survive the per-entry check
	// and the banner stays clean — drifted is not the same as "throw everything away".
	if rec.Applied() != len(entries) {
		t.Errorf("applied = %d, want %d (tuples match)", rec.Applied(), len(entries))
	}
	if rec.NeedsBanner() {
		t.Errorf("unexpected banner: dropped=%v new=%v", dropNames(rec.Dropped), rec.NewInputs)
	}
}

// TestReconcileDropsRetargetedSlot is the headline bug: the stored key {3,0} was a
// SEED and is now a PROMPT. The stored value must be discarded, the field
// defaulted to the graph's current value, and the entry NAMED with both sides.
func TestReconcileDropsRetargetedSlot(t *testing.T) {
	entries := entriesFor(t, presetGraph, nil, func(ri RunInput) string { return "9999" })
	rec := ReconcileRunPreset(json.RawMessage(presetGraphRetargeted), nil, nil, entries, false)

	var retarget *PresetDrop
	for i := range rec.Dropped {
		if rec.Dropped[i].Reason == PresetDropRetargeted {
			retarget = &rec.Dropped[i]
		}
	}
	if retarget == nil {
		t.Fatalf("no retargeted drop; drops = %+v", rec.Dropped)
	}
	if retarget.Name != "Seed" {
		t.Errorf("retargeted drop named %q, want the STORED label %q", retarget.Name, "Seed")
	}
	if retarget.Detail != "Prompt (NEGATIVE)" {
		t.Errorf("retargeted detail = %q, want the LIVE label", retarget.Detail)
	}
	f, ok := fieldByLabel(rec, "Prompt (NEGATIVE)")
	if !ok {
		t.Fatal("live field missing")
	}
	if f.FromPreset {
		t.Error("a retargeted field must NOT carry the preset's value")
	}
	if f.Value != "blurry" {
		t.Errorf("retargeted field value = %q, want the graph's current value %q", f.Value, "blurry")
	}
}

// TestReconcileDropsVanishedKey: a stored key with no live field is discarded and
// named by its stored label — a silently-ignored value the user believes is
// applied is exactly the failure this feature must not ship.
func TestReconcileDropsVanishedKey(t *testing.T) {
	entries := []PresetEntry{{
		NodeID: "999", Widget: 0, Value: "ghost",
		Kind: RunInputText, ClassType: "CLIPTextEncode", InputName: "text", Label: "Prompt (GHOST)",
	}}
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, false)
	if len(rec.Dropped) != 1 || rec.Dropped[0].Reason != PresetDropGone {
		t.Fatalf("drops = %+v, want one 'gone'", rec.Dropped)
	}
	if rec.Dropped[0].Name != "Prompt (GHOST)" {
		t.Errorf("gone drop named %q", rec.Dropped[0].Name)
	}
	// It is reported on the EXACT path too: a value with nowhere to go is a value
	// the user believes is applied and is not.
	rec = ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, true)
	if len(rec.Dropped) != 1 || rec.Dropped[0].Reason != PresetDropGone {
		t.Errorf("exact-path drops = %+v, want one 'gone'", rec.Dropped)
	}
}

// TestReconcileDropsUnverifiableLegacyEntry: an entry with no drift tuple at all
// cannot be checked against a changed graph, so it is dropped with its OWN reason
// rather than trusted.
func TestReconcileDropsUnverifiableLegacyEntry(t *testing.T) {
	entries := []PresetEntry{{NodeID: "3", Widget: 0, Value: "5555"}} // no kind/class/input
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, false)

	if len(rec.Dropped) != 1 || rec.Dropped[0].Reason != PresetDropUnverifiable {
		t.Fatalf("drops = %+v, want one 'unverifiable'", rec.Dropped)
	}
	f, ok := fieldByLabel(rec, "Seed")
	if !ok {
		t.Fatal("Seed field missing")
	}
	if f.FromPreset || f.Value != "1234" {
		t.Errorf("unverifiable entry applied: %+v", f)
	}
	// With an EXACT hash the same entry IS applied — the hash is the proof.
	rec = ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, true)
	if f, _ := fieldByLabel(rec, "Seed"); !f.FromPreset || f.Value != "5555" {
		t.Errorf("exact hash must apply a tuple-less entry, got %+v", f)
	}
}

// TestReconcileReportsNewInputs: a live input with no stored entry is defaulted to
// the graph's value AND named, so new parameters never appear silently.
func TestReconcileReportsNewInputs(t *testing.T) {
	entries := []PresetEntry{{
		NodeID: "6", Widget: 0, Value: "my prompt",
		Kind: RunInputText, ClassType: "CLIPTextEncode", InputName: "text", Label: "Prompt (POSITIVE)",
	}}
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, false)
	if len(rec.NewInputs) == 0 {
		t.Fatal("no new inputs reported")
	}
	for _, want := range []string{"Seed", "Steps", "Denoise", "Width"} {
		found := false
		for _, n := range rec.NewInputs {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("new inputs %v missing %q", rec.NewInputs, want)
		}
	}
	if f, _ := fieldByLabel(rec, "Prompt (POSITIVE)"); !f.FromPreset || f.Value != "my prompt" {
		t.Errorf("the one matching entry should still apply, got %+v", f)
	}
	// A preset with NO entries at all is a brand-new tab, not "11 new parameters".
	rec = ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, nil, false)
	if len(rec.NewInputs) != 0 || rec.NeedsBanner() {
		t.Errorf("an empty preset must not report new inputs: %v", rec.NewInputs)
	}
}

// TestReconcileKeepsNameKeyedFamiliesOutOfTheDropList: substitute / option_fixes
// are NAME-keyed and degrade safely downstream, so the reconciler never touches
// them — it reports only positional (widget/mode) entries.
func TestReconcileKeepsNameKeyedFamiliesOutOfTheDropList(t *testing.T) {
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, nil, false)
	if len(rec.Dropped) != 0 {
		t.Errorf("a preset with only name-keyed families must produce no drops, got %+v", rec.Dropped)
	}
}

// TestReconcileRunsAgainstModeAppliedGraph: reconciliation must use the
// mode-applied graph — the one the run would actually convert. Against the stored
// (all-bypassed) graph DetectRunInputs surfaces ZERO inputs and every entry would
// be dropped.
func TestReconcileRunsAgainstModeAppliedGraph(t *testing.T) {
	sels := DetectModeSelectors(json.RawMessage(presetModeGraph))
	if len(sels) != 1 || len(sels[0].Modes) != 2 {
		t.Fatalf("fixture: selectors = %+v", sels)
	}
	modeA := map[string]string{sels[0].Key: sels[0].Modes[0].Key}

	if got := DetectRunInputs(json.RawMessage(presetModeGraph), nil); len(got) != 0 {
		t.Fatalf("fixture: stored graph should expose nothing, got %d", len(got))
	}
	entries := entriesFor(t, presetModeGraph, modeA, func(ri RunInput) string { return "888" })

	withModes := ReconcileRunPreset(json.RawMessage(presetModeGraph), modeA, nil, entries, true)
	if len(withModes.Fields) == 0 {
		t.Fatal("mode-applied reconciliation surfaced no fields")
	}
	if withModes.Applied() != len(entries) {
		t.Errorf("applied = %d, want %d", withModes.Applied(), len(entries))
	}

	withoutModes := ReconcileRunPreset(json.RawMessage(presetModeGraph), nil, nil, entries, true)
	if len(withoutModes.Fields) != 0 {
		t.Errorf("without modes the all-bypassed graph must expose nothing, got %d", len(withoutModes.Fields))
	}
	if len(withoutModes.Dropped) != len(entries) {
		t.Errorf("without modes every entry should be reported gone, got %d", len(withoutModes.Dropped))
	}
}

// TestResolvePresetModes covers the two independent mode gates: STRUCTURE (the key
// must still be surfaced, by the same selector) and HASH (a mode key is positional,
// so it is withheld when the graph cannot be proven unchanged). Both drops are
// NAMED — a silent drop degrades into a confusing "nothing to run".
func TestResolvePresetModes(t *testing.T) {
	sels := DetectModeSelectors(json.RawMessage(presetModeGraph))
	selKey := sels[0].Key
	modeB := sels[0].Modes[1].Key

	t.Run("exact hash applies", func(t *testing.T) {
		got, dropped := ResolvePresetModes(json.RawMessage(presetModeGraph),
			map[string]string{selKey: modeB}, true)
		if len(dropped) != 0 {
			t.Errorf("dropped = %+v, want none", dropped)
		}
		if got[selKey] != modeB {
			t.Errorf("resolved = %v, want %s=%s", got, selKey, modeB)
		}
	})

	t.Run("drift withholds and names", func(t *testing.T) {
		got, dropped := ResolvePresetModes(json.RawMessage(presetModeGraph),
			map[string]string{selKey: modeB}, false)
		if len(got) != 0 {
			t.Errorf("a positional mode key must not cross a graph change, got %v", got)
		}
		if len(dropped) != 1 || dropped[0].Reason != PresetDropModeDrifted {
			t.Fatalf("dropped = %+v, want one mode-drifted", dropped)
		}
		if dropped[0].Name != "IMAGE2VIDEO" {
			t.Errorf("drop named %q, want the group title", dropped[0].Name)
		}
	})

	t.Run("unknown key dropped and named even on an exact hash", func(t *testing.T) {
		got, dropped := ResolvePresetModes(json.RawMessage(presetModeGraph),
			map[string]string{selKey: "1:99"}, true)
		if len(got) != 0 {
			t.Errorf("unknown mode resolved: %v", got)
		}
		if len(dropped) != 1 || dropped[0].Reason != PresetDropModeUnknown {
			t.Fatalf("dropped = %+v, want one mode-unknown", dropped)
		}
		if dropped[0].Name != "1:99" {
			t.Errorf("drop named %q, want the raw key when it cannot be resolved", dropped[0].Name)
		}
	})

	t.Run("key stored under the wrong selector is dropped", func(t *testing.T) {
		got, dropped := ResolvePresetModes(json.RawMessage(presetModeGraph),
			map[string]string{"someotherselector": modeB}, true)
		if len(got) != 0 {
			t.Errorf("cross-selector mode resolved: %v", got)
		}
		if len(dropped) != 1 || dropped[0].Reason != PresetDropModeUnknown {
			t.Errorf("dropped = %+v, want one mode-unknown", dropped)
		}
	})

	t.Run("ordinary workflow with no stored modes", func(t *testing.T) {
		got, dropped := ResolvePresetModes(json.RawMessage(presetGraph), nil, true)
		if got != nil || dropped != nil {
			t.Errorf("got %v / %v, want nil/nil", got, dropped)
		}
	})
}

// TestReconcileIsDeterministic: two reconciliations of the same inputs must report
// the same drops in the same order, or the banner text flickers between renders.
func TestReconcileIsDeterministic(t *testing.T) {
	entries := []PresetEntry{
		{NodeID: "91", Widget: 1, Value: "a", Label: "Ghost B", Kind: RunInputInt, ClassType: "X", InputName: "b"},
		{NodeID: "90", Widget: 0, Value: "b", Label: "Ghost A", Kind: RunInputInt, ClassType: "X", InputName: "a"},
		{NodeID: "91", Widget: 0, Value: "c", Label: "Ghost C", Kind: RunInputInt, ClassType: "X", InputName: "c"},
	}
	var first []string
	for i := 0; i < 20; i++ {
		rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, false)
		names := dropNames(rec.Dropped)
		if first == nil {
			first = names
			continue
		}
		if len(names) != len(first) {
			t.Fatalf("drop count varies: %v vs %v", names, first)
		}
		for j := range names {
			if names[j] != first[j] {
				t.Fatalf("drop order varies: %v vs %v", names, first)
			}
		}
	}
	want := []string{"Ghost A", "Ghost C", "Ghost B"} // (node id, widget) order
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("drop order = %v, want %v", first, want)
		}
	}
}

// TestReconcileDropsAndNamesMalformedEntry: an entry with no usable positional key
// must never be defaulted onto some node's slot 0 — AND must never vanish without a
// word. "Dropped, defaulted, and named" is the rule for every other drop; a value
// the user believes is saved is exactly the one they must be told about.
func TestReconcileDropsAndNamesMalformedEntry(t *testing.T) {
	entries := []PresetEntry{
		{NodeID: "", Widget: 0, Value: "evil", Kind: RunInputSeed},
		{NodeID: "3", Malformed: true, Value: "unparsable widget", Label: "Steps"},
	}
	rec := ReconcileRunPreset(json.RawMessage(presetGraph), nil, nil, entries, true)
	for _, f := range rec.Fields {
		if f.FromPreset {
			t.Errorf("malformed entry applied to %q", f.Input.Label)
		}
		if f.Value == "evil" || f.Value == "unparsable widget" {
			t.Errorf("malformed value leaked into field %q", f.Input.Label)
		}
	}
	if len(rec.Dropped) != 2 {
		t.Fatalf("both malformed entries must be NAMED, got drops %+v", rec.Dropped)
	}
	for _, d := range rec.Dropped {
		if d.Reason != PresetDropMalformed {
			t.Errorf("drop reason = %q, want %q", d.Reason, PresetDropMalformed)
		}
		if d.Name == "" {
			t.Error("a drop with an empty name is not a named drop")
		}
	}
	if rec.Dropped[0].Name != "a saved value" || rec.Dropped[1].Name != "Steps" {
		t.Errorf("drop names = %q, %q", rec.Dropped[0].Name, rec.Dropped[1].Name)
	}
	if !rec.NeedsBanner() {
		t.Error("a dropped value must raise the banner even on an exact hash")
	}
}
