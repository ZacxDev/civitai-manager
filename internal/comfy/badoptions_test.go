package comfy

import (
	"encoding/json"
	"reflect"
	"testing"
)

// wildcardInfo models the two node types from workflow 587's real failure: a
// wildcard picker whose ONLY valid choice is a placeholder label, and a detector
// provider whose model_name combo lists subfolder-prefixed installed files.
const badOptionInfo = `{
  "ImpactWildcardProcessor": {"input":{"required":{
    "wildcard_text":["STRING",{}],
    "Select to add Wildcard":[["Select the Wildcard to add to the text"],{}]
  }},"input_order":{"required":["wildcard_text","Select to add Wildcard"]}},
  "ToDetailerPipe": {"input":{"required":{
    "Select to add Wildcard":[["Select the Wildcard to add to the text"],{}]
  }},"input_order":{"required":["Select to add Wildcard"]}},
  "UltralyticsDetectorProvider": {"input":{"required":{
    "model_name":[["bbox/face_yolov8m.pt","bbox/hand_yolov8s.pt","segm/PitEyeDetailer-v2-seg.pt"],{}]
  }},"input_order":{"required":["model_name"]}},
  "CheckpointLoaderSimple": {"input":{"required":{
    "ckpt_name":[["installed.safetensors"],{}]
  }},"input_order":{"required":["ckpt_name"]}}
}`

// TestPreflightBadOptionInListOK: a combo value that IS a valid choice is not flagged.
func TestPreflightBadOptionInListOK(t *testing.T) {
	info := buildInfo(t, badOptionInfo)
	api := json.RawMessage(`{
		"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov8m.pt"}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 0 {
		t.Fatalf("BadOptions = %+v, want none", rep.BadOptions)
	}
	if !rep.OK {
		t.Errorf("report should be OK: %+v", rep)
	}
}

// TestPreflightBadOptionNotInList: a combo value NOT among choices is flagged, with
// the input's real choices carried through.
func TestPreflightBadOptionNotInList(t *testing.T) {
	info := buildInfo(t, badOptionInfo)
	api := json.RawMessage(`{
		"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if rep.OK {
		t.Error("report should not be OK (a combo value is not in the list)")
	}
	if len(rep.BadOptions) != 1 {
		t.Fatalf("BadOptions = %+v, want 1", rep.BadOptions)
	}
	bo := rep.BadOptions[0]
	if bo.InputName != "model_name" || bo.Current != "bbox/face_yolov9c.pt" {
		t.Errorf("bad option = %+v", bo)
	}
	if bo.ClassType != "UltralyticsDetectorProvider" {
		t.Errorf("ClassType = %q", bo.ClassType)
	}
	if !reflect.DeepEqual(bo.NodeIDs, []string{"42"}) {
		t.Errorf("NodeIDs = %v", bo.NodeIDs)
	}
	want := []string{"bbox/face_yolov8m.pt", "bbox/hand_yolov8s.pt", "segm/PitEyeDetailer-v2-seg.pt"}
	if !reflect.DeepEqual(bo.Choices, want) {
		t.Errorf("Choices = %v, want %v", bo.Choices, want)
	}
}

// TestPreflightBadOptionLinkInputIgnored: a combo whose value is a LINK ([id,slot])
// is not a widget value and is never flagged.
func TestPreflightBadOptionLinkInputIgnored(t *testing.T) {
	info := buildInfo(t, badOptionInfo)
	api := json.RawMessage(`{
		"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":["6",0]}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 0 {
		t.Errorf("a link input must not be flagged: %+v", rep.BadOptions)
	}
}

// TestPreflightBadOptionFreeTextNotFlagged: a free-text STRING input with an
// arbitrary value is NOT a combo and is never flagged.
func TestPreflightBadOptionFreeTextNotFlagged(t *testing.T) {
	info := buildInfo(t, badOptionInfo)
	api := json.RawMessage(`{
		"3":{"class_type":"ImpactWildcardProcessor","inputs":{"wildcard_text":"a totally arbitrary prompt","Select to add Wildcard":"Select the Wildcard to add to the text"}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 0 {
		t.Errorf("free-text STRING must not be flagged (only combos): %+v", rep.BadOptions)
	}
	if !rep.OK {
		t.Errorf("report should be OK: %+v", rep)
	}
}

// TestPreflightBadOptionDedupMissingModel: a missing-model combo (ckpt_name on a
// loader) is reported in MissingModels and NOT double-reported as a BadOption.
func TestPreflightBadOptionDedupMissingModel(t *testing.T) {
	info := buildInfo(t, badOptionInfo)
	api := json.RawMessage(`{
		"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}
	}`)
	rep := Preflight(api, info, func(string) bool { return false })
	if len(rep.MissingModels) != 1 || rep.MissingModels[0] != "absent.safetensors" {
		t.Fatalf("MissingModels = %v, want [absent.safetensors]", rep.MissingModels)
	}
	if len(rep.BadOptions) != 0 {
		t.Errorf("a missing-model combo must not also be a BadOption: %+v", rep.BadOptions)
	}
}

// TestPreflightBadOptionLocalHaveNotFlagged: a loader model file present in the
// local library (but absent from combo choices) is satisfied by the model path and
// is NOT flagged as a bad option (guards the existing localHave semantics).
func TestPreflightBadOptionLocalHaveNotFlagged(t *testing.T) {
	info := buildInfo(t, `{
		"CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["other.safetensors"],{}]}},"input_order":{"required":["ckpt_name"]}}
	}`)
	api := json.RawMessage(`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"mine.safetensors"}}}`)
	rep := Preflight(api, info, func(name string) bool { return name == "mine.safetensors" })
	if len(rep.BadOptions) != 0 {
		t.Errorf("a locally-present loader model must not be flagged as a bad option: %+v", rep.BadOptions)
	}
	if !rep.OK {
		t.Errorf("report should be OK via localHave: %+v", rep)
	}
}

// TestPreflightBadOptionGroupedAcrossNodes: the SAME (input,value) drift on four
// nodes of DIFFERENT classes is ONE BadOption aggregating all NodeIDs; a second
// distinct (input,value) across two nodes is a SECOND group. (This is workflow
// 587's exact shape: 2 groups, not 6 rows.)
func TestPreflightBadOptionGroupedAcrossNodes(t *testing.T) {
	info := buildInfo(t, badOptionInfo)
	api := json.RawMessage(`{
		"3":{"class_type":"ImpactWildcardProcessor","inputs":{"Select to add Wildcard":"Select Wildcard 🟢 Full Cache"}},
		"4":{"class_type":"ImpactWildcardProcessor","inputs":{"Select to add Wildcard":"Select Wildcard 🟢 Full Cache"}},
		"5":{"class_type":"ToDetailerPipe","inputs":{"Select to add Wildcard":"Select Wildcard 🟢 Full Cache"}},
		"14":{"class_type":"ToDetailerPipe","inputs":{"Select to add Wildcard":"Select Wildcard 🟢 Full Cache"}},
		"42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}},
		"9":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 2 {
		t.Fatalf("want 2 grouped bad options, got %d: %+v", len(rep.BadOptions), rep.BadOptions)
	}
	byInput := map[string]BadOption{}
	for _, bo := range rep.BadOptions {
		byInput[bo.InputName] = bo
	}
	wc, ok := byInput["Select to add Wildcard"]
	if !ok {
		t.Fatalf("missing wildcard group: %+v", rep.BadOptions)
	}
	if !reflect.DeepEqual(wc.NodeIDs, []string{"14", "3", "4", "5"}) { // node ids sorted as strings
		t.Errorf("wildcard NodeIDs = %v, want [14 3 4 5] (string-sorted)", wc.NodeIDs)
	}
	mn, ok := byInput["model_name"]
	if !ok {
		t.Fatalf("missing model_name group: %+v", rep.BadOptions)
	}
	if !reflect.DeepEqual(mn.NodeIDs, []string{"42", "9"}) {
		t.Errorf("model_name NodeIDs = %v, want [42 9] (string-sorted)", mn.NodeIDs)
	}
}

// TestPreflightBadOptionIsComboFalseIgnored: a primitive widget (INT) with any value
// is not a combo and is ignored even if its value looks off.
func TestPreflightBadOptionIsComboFalseIgnored(t *testing.T) {
	info := buildInfo(t, `{
		"KSampler":{"input":{"required":{"steps":["INT",{"default":20}]}},"input_order":{"required":["steps"]}}
	}`)
	api := json.RawMessage(`{"7":{"class_type":"KSampler","inputs":{"steps":9999}}}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 0 {
		t.Errorf("IsCombo=false input must be ignored: %+v", rep.BadOptions)
	}
}

// --- ApplyOptionFixes ---

// TestApplyOptionFixesRewritesAcrossNodes: a fix keyed by (input,oldValue) rewrites
// EVERY matching combo input across nodes of any class, leaves non-matching inputs,
// links, and other classes untouched, and does NOT mutate the input graph.
func TestApplyOptionFixesRewritesAcrossNodes(t *testing.T) {
	in := `{
		"3":{"class_type":"ImpactWildcardProcessor","inputs":{"Select to add Wildcard":"OLD","wildcard_text":"keep"}},
		"5":{"class_type":"ToDetailerPipe","inputs":{"Select to add Wildcard":"OLD"}},
		"6":{"class_type":"KSampler","inputs":{"seed":["3",0],"steps":20}},
		"7":{"class_type":"Other","inputs":{"Select to add Wildcard":"DIFFERENT"}}
	}`
	orig := in
	fixes := map[OptionFixKey]string{{InputName: "Select to add Wildcard", OldValue: "OLD"}: "NEW"}
	out := ApplyOptionFixes(json.RawMessage(in), fixes)

	var nodes map[string]map[string]json.RawMessage
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	get := func(id, input string) string {
		var inputs map[string]json.RawMessage
		_ = json.Unmarshal(nodes[id]["inputs"], &inputs)
		var s string
		_ = json.Unmarshal(inputs[input], &s)
		return s
	}
	if get("3", "Select to add Wildcard") != "NEW" || get("5", "Select to add Wildcard") != "NEW" {
		t.Errorf("matching combo inputs were not rewritten across nodes: %s", out)
	}
	if get("3", "wildcard_text") != "keep" {
		t.Errorf("non-matching input was altered")
	}
	if get("7", "Select to add Wildcard") != "DIFFERENT" {
		t.Errorf("a different value on the same input name was altered")
	}
	// node 6's link + numeric inputs must be preserved verbatim.
	var n6 map[string]json.RawMessage
	_ = json.Unmarshal(nodes["6"]["inputs"], &n6)
	if string(n6["seed"]) != `["3",0]` || string(n6["steps"]) != "20" {
		t.Errorf("link/number inputs mutated: %s", nodes["6"]["inputs"])
	}
	if in != orig {
		t.Errorf("input graph string changed (mutation): %s", in)
	}
	// The stored input bytes must be unaffected (operate on a copy).
	if string(ApplyOptionFixes(json.RawMessage(orig), nil)) != orig {
		t.Errorf("nil fixes should be a no-op returning the input unchanged")
	}
}

// TestApplyOptionFixesEmptyAndInvalid: empty fixes are a no-op; an unparseable graph
// is returned unchanged.
func TestApplyOptionFixesEmptyAndInvalid(t *testing.T) {
	in := `{"3":{"class_type":"X","inputs":{"a":"b"}}}`
	if got := string(ApplyOptionFixes(json.RawMessage(in), map[OptionFixKey]string{})); got != in {
		t.Errorf("empty fixes changed the graph: %s", got)
	}
	bad := `{not json`
	if got := string(ApplyOptionFixes(json.RawMessage(bad), map[OptionFixKey]string{{InputName: "a", OldValue: "b"}: "c"})); got != bad {
		t.Errorf("invalid graph should be returned unchanged: %s", got)
	}
	// No node matches → returned unchanged.
	if got := string(ApplyOptionFixes(json.RawMessage(in), map[OptionFixKey]string{{InputName: "a", OldValue: "zzz"}: "c"})); got != in {
		t.Errorf("no-match fixes changed the graph: %s", got)
	}
}

// --- ValidateOptionFixes ---

// TestValidateOptionFixes keeps on-list fixes matching a bad-option group and drops
// off-list values and fixes for non-bad-option inputs.
func TestValidateOptionFixes(t *testing.T) {
	bad := []BadOption{
		{InputName: "model_name", Current: "bbox/face_yolov9c.pt", Choices: []string{"bbox/face_yolov8m.pt", "bbox/hand_yolov8s.pt"}},
		{InputName: "Select to add Wildcard", Current: "OLD", Choices: []string{"Select the Wildcard to add to the text"}},
	}
	req := map[OptionFixKey]string{
		{InputName: "model_name", OldValue: "bbox/face_yolov9c.pt"}: "bbox/face_yolov8m.pt",                    // valid
		{InputName: "Select to add Wildcard", OldValue: "OLD"}:      "Select the Wildcard to add to the text", // valid
		{InputName: "steps", OldValue: "20"}:                        "50",                                     // not a bad option → drop
	}
	// add an off-list request under a fresh key by re-building (map keys unique):
	req2 := map[OptionFixKey]string{
		{InputName: "model_name", OldValue: "bbox/face_yolov9c.pt"}: "not/installed.pt", // off-list → drop
	}

	got := ValidateOptionFixes(req, bad)
	if v := got[OptionFixKey{InputName: "model_name", OldValue: "bbox/face_yolov9c.pt"}]; v != "bbox/face_yolov8m.pt" {
		t.Errorf("valid model_name fix dropped: %q", v)
	}
	if v := got[OptionFixKey{InputName: "Select to add Wildcard", OldValue: "OLD"}]; v != "Select the Wildcard to add to the text" {
		t.Errorf("valid wildcard fix dropped: %q", v)
	}
	if _, ok := got[OptionFixKey{InputName: "steps", OldValue: "20"}]; ok {
		t.Errorf("a fix for a non-bad-option input must be dropped")
	}
	if len(got) != 2 {
		t.Errorf("want 2 valid fixes, got %d: %+v", len(got), got)
	}

	off := ValidateOptionFixes(req2, bad)
	if len(off) != 0 {
		t.Errorf("an off-list value must be dropped: %+v", off)
	}
	if len(ValidateOptionFixes(nil, bad)) != 0 {
		t.Errorf("nil request → empty result")
	}
}
