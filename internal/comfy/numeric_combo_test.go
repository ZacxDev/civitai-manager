package comfy

import (
	"encoding/json"
	"testing"
)

// The shapes below are the REAL ones, taken from a live ComfyUI 0.27.1 /object_info
// (2462 node types). Exactly three node types there carry a numeric COMBO input:
//
//	RIFE VFI                    scale_factor  [0.25, 0.5, 1.0, 2.0, 4.0]   (floats)
//	IFRNet VFI                  scale_factor  [0.25, 0.5, 1.0, 2.0, 4.0]   (floats)
//	WanVideoSetRadialAttention  block_size    [128, 64]                    (ints)
//
// RIFE VFI is the one that mattered: it appears in 16 of the user's 70 workflows.

// specOf decodes one raw /object_info input spec array.
func specOf(t *testing.T, raw string) InputSpec {
	t.Helper()
	var s InputSpec
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		// UnmarshalJSON is deliberately permissive and must never return an error —
		// one odd input can never be allowed to fail the whole /object_info parse.
		t.Fatalf("InputSpec.UnmarshalJSON(%s) returned an error: %v", raw, err)
	}
	return s
}

// TestInputSpecNumericComboNoPhantomChoices is the decoder-level guard. Before the
// fix, decoding a numeric option list into a []string allocated the slice and THEN
// failed per element, leaving N EMPTY STRINGS behind — Choices=["","","","",""] for
// scale_factor. len(Choices) must be 0, not 5.
func TestInputSpecNumericComboNoPhantomChoices(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want int // the phantom length this must NOT have
	}{
		{"RIFE VFI scale_factor (floats)", `[[0.25, 0.5, 1.0, 2.0, 4.0], {"default": 1.0}]`, 5},
		{"WanVideoSetRadialAttention block_size (ints)", `[[128, 64], {"default": 128}]`, 2},
		{"floats, no config element", `[[0.25, 0.5, 1.0, 2.0, 4.0]]`, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := specOf(t, tc.raw)
			if len(s.Choices) != 0 {
				t.Fatalf("Choices = %q (len %d) — a numeric combo must leave NO phantom "+
					"strings; the pre-fix bug produced %d empty ones",
					s.Choices, len(s.Choices), tc.want)
			}
			for i, c := range s.Choices {
				if c == "" {
					t.Errorf("Choices[%d] is an empty string", i)
				}
			}
			// The invariant that makes this safe: a numeric combo is STILL a combo,
			// so it is still a widget. Clearing IsCombo would reclassify it as a LINK
			// input and break the converter.
			if !s.IsCombo {
				t.Error("IsCombo = false; a numeric option list is still a combo")
			}
			if !s.IsWidget() {
				t.Error("IsWidget() = false; a numeric combo must stay a widget")
			}
		})
	}
}

// TestInputSpecNumericComboKeepsConfig proves the fix did not cost the spec's second
// element: default/min/max must still decode for a numeric combo.
func TestInputSpecNumericComboKeepsConfig(t *testing.T) {
	s := specOf(t, `[[0.25, 0.5, 1.0, 2.0, 4.0], {"default": 1.0}]`)
	raw, ok := s.ConfigValue("default")
	if !ok {
		t.Fatal("config default missing")
	}
	if string(raw) != "1.0" {
		t.Errorf("default = %s, want 1.0", raw)
	}
}

// TestInputSpecStringComboUnchanged is the regression guard: the ordinary,
// overwhelmingly common case must decode exactly as before.
func TestInputSpecStringComboUnchanged(t *testing.T) {
	s := specOf(t, `[["sdpa", "flash_attn_2", "flash_attn_3", "sageattn"], {"default": "sdpa"}]`)
	if !s.IsCombo || !s.IsWidget() {
		t.Fatalf("IsCombo=%v IsWidget=%v, want both true", s.IsCombo, s.IsWidget())
	}
	want := []string{"sdpa", "flash_attn_2", "flash_attn_3", "sageattn"}
	if len(s.Choices) != len(want) {
		t.Fatalf("Choices = %q, want %q", s.Choices, want)
	}
	for i := range want {
		if s.Choices[i] != want[i] {
			t.Fatalf("Choices = %q, want %q", s.Choices, want)
		}
	}
	if s.TypeName != "" {
		t.Errorf("TypeName = %q, want empty for a combo", s.TypeName)
	}
}

// TestInputSpecPrimitiveTypesUnchanged guards the non-combo branch.
func TestInputSpecPrimitiveTypesUnchanged(t *testing.T) {
	s := specOf(t, `["INT", {"default": 10, "min": 1}]`)
	if s.IsCombo {
		t.Error("IsCombo = true for a string type name")
	}
	if s.TypeName != "INT" || !s.IsWidget() {
		t.Errorf("TypeName=%q IsWidget=%v, want INT/true", s.TypeName, s.IsWidget())
	}
	link := specOf(t, `["IMAGE"]`)
	if link.IsCombo || link.IsWidget() {
		t.Errorf("IMAGE: IsCombo=%v IsWidget=%v, want both false (a link input)", link.IsCombo, link.IsWidget())
	}
}

// TestInputSpecDegradesSafely covers mixed / null-bearing / empty / malformed specs.
// All must yield a zero-ish value WITHOUT an error — the permissiveness is deliberate
// and documented, so one odd input can never fail the whole /object_info parse.
func TestInputSpecDegradesSafely(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantCombo   bool
		wantChoices int
	}{
		{"mixed string+number", `[["a", 1, "b"], {}]`, true, 0},
		{"number first, strings after", `[[1, "a"], {}]`, true, 0},
		// A JSON null unmarshals into a string as a silent NO-OP (no error, value
		// untouched) — the exact mechanism by which a phantom "" could sneak back in.
		{"null element", `[["a", null], {}]`, true, 0},
		{"all nulls", `[[null, null]]`, true, 0},
		{"nested arrays", `[[["a"], ["b"]]]`, true, 0},
		{"objects", `[[{"x": 1}]]`, true, 0},
		{"booleans", `[[true, false]]`, true, 0},
		{"empty choice list", `[[], {}]`, true, 0},
		{"empty spec array", `[]`, false, 0},
		{"not an array at all", `{"nope": true}`, false, 0},
		{"a bare string", `"INT"`, false, 0},
		{"null spec", `null`, false, 0},
		{"config is not an object", `[["a"], "not-an-object"]`, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := specOf(t, tc.raw) // fails the test if UnmarshalJSON errors
			if s.IsCombo != tc.wantCombo {
				t.Errorf("IsCombo = %v, want %v", s.IsCombo, tc.wantCombo)
			}
			if len(s.Choices) != tc.wantChoices {
				t.Errorf("Choices = %q (len %d), want len %d", s.Choices, len(s.Choices), tc.wantChoices)
			}
			for i, c := range s.Choices {
				if c == "" {
					t.Errorf("Choices[%d] is an empty string — phantom choice", i)
				}
			}
		})
	}
}

// TestObjectInfoWithNumericComboStillParses proves a numeric combo cannot break the
// surrounding /object_info parse: its sibling node types must decode normally.
func TestObjectInfoWithNumericComboStillParses(t *testing.T) {
	info := buildInfo(t, rifeObjectInfo)
	if len(info) != 2 {
		t.Fatalf("decoded %d node types, want 2", len(info))
	}
	if got := info["RIFE VFI"].Input.Required["ckpt_name"].Choices; len(got) != 2 {
		t.Errorf("sibling string combo ckpt_name = %q, want 2 choices", got)
	}
	if order := info["RIFE VFI"].InputOrder.Required; len(order) == 0 {
		t.Error("input_order lost")
	}
}

// rifeObjectInfo is a RIFE VFI-shaped /object_info: a numeric float combo
// (scale_factor), a numeric int combo (block_size), a string model combo
// (ckpt_name) and a plain string combo (dense_attention_mode). The two string
// combos are what prove the fixture REACHES detectBadOptions' inner loop.
const rifeObjectInfo = `{
  "RIFE VFI": {
    "input": {"required": {
      "ckpt_name": [["rife47.pth", "rife49.pth"], {}],
      "clear_cache_after_n_frames": ["INT", {"default": 10}],
      "multiplier": ["INT", {"default": 2}],
      "fast_mode": ["BOOLEAN", {"default": true}],
      "ensemble": ["BOOLEAN", {"default": true}],
      "scale_factor": [[0.25, 0.5, 1.0, 2.0, 4.0], {"default": 1.0}],
      "frames": ["IMAGE"]
    }},
    "input_order": {"required": ["ckpt_name","frames","clear_cache_after_n_frames","multiplier","fast_mode","ensemble","scale_factor"]}
  },
  "WanVideoSetRadialAttention": {
    "input": {"required": {
      "dense_attention_mode": [["sdpa", "flash_attn_2"], {"default": "sdpa"}],
      "block_size": [[128, 64], {"default": 128}],
      "decay_factor": ["FLOAT", {"default": 0.2}]
    }},
    "input_order": {"required": ["dense_attention_mode","block_size","decay_factor"]}
  }
}`

// TestPreflightNumericComboProducesNoBadOption is THE user-visible guard: the whole
// bug was preflight halting a run on `RIFE VFI · scale_factor` with a picker of five
// blank options. A legitimate value must produce NO BadOption.
func TestPreflightNumericComboProducesNoBadOption(t *testing.T) {
	info := buildInfo(t, rifeObjectInfo)
	api := json.RawMessage(`{
	  "10": {"class_type": "RIFE VFI", "inputs": {
	     "ckpt_name": "rife47.pth", "clear_cache_after_n_frames": 10, "multiplier": 2,
	     "fast_mode": true, "ensemble": true, "scale_factor": 1, "frames": ["9", 0]}},
	  "11": {"class_type": "WanVideoSetRadialAttention", "inputs": {
	     "dense_attention_mode": "sdpa", "block_size": 128, "decay_factor": 0.2}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	for _, bo := range rep.BadOptions {
		t.Errorf("BadOption raised for a legitimate value: class=%q input=%q current=%q choices=%q",
			bo.ClassType, bo.InputName, bo.Current, bo.Choices)
	}
	if !rep.OK {
		t.Errorf("report not OK: missingNodes=%v missingModels=%v", rep.MissingNodes, rep.MissingModels)
	}
	// The graph writes scale_factor as the bare number 1 while the option list holds
	// 1.0 — the very mismatch that makes formatted-string matching unusable here.
	// Both 1 and 1.0 must behave the same.
	api2 := json.RawMessage(`{"10": {"class_type": "RIFE VFI", "inputs": {
	  "ckpt_name": "rife47.pth", "scale_factor": 1.0, "frames": ["9", 0]}}}`)
	if rep2 := Preflight(api2, info, func(string) bool { return true }); len(rep2.BadOptions) != 0 {
		t.Errorf("scale_factor 1.0 raised %d BadOptions: %+v", len(rep2.BadOptions), rep2.BadOptions)
	}
}

// TestPreflightStringComboStillDetected is the anti-vacuity half of the test above:
// it uses the SAME fixture and the SAME nodes, and proves detectBadOptions really
// does walk these inputs. Without it, "no BadOption" could mean "the fixture never
// reached the code path" rather than "the numeric combo is handled correctly".
func TestPreflightStringComboStillDetected(t *testing.T) {
	info := buildInfo(t, rifeObjectInfo)
	api := json.RawMessage(`{
	  "10": {"class_type": "RIFE VFI", "inputs": {
	     "ckpt_name": "rife47.pth", "scale_factor": 1, "frames": ["9", 0]}},
	  "11": {"class_type": "WanVideoSetRadialAttention", "inputs": {
	     "dense_attention_mode": "a_mode_that_is_gone", "block_size": 128, "decay_factor": 0.2}}
	}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 1 {
		t.Fatalf("want exactly 1 BadOption (the drifted STRING combo), got %d: %+v",
			len(rep.BadOptions), rep.BadOptions)
	}
	bo := rep.BadOptions[0]
	if bo.InputName != "dense_attention_mode" || bo.Current != "a_mode_that_is_gone" {
		t.Fatalf("wrong BadOption: %+v", bo)
	}
	if len(bo.Choices) != 2 {
		t.Errorf("Choices = %q, want the 2 live string choices", bo.Choices)
	}
	for _, c := range bo.Choices {
		if c == "" {
			t.Errorf("BadOption offers a BLANK choice: %q", bo.Choices)
		}
	}
}

// TestPreflightOffListNumericIsNotFlagged pins the DELIBERATE under-validation: even
// a numeric value that is genuinely absent from the option list raises no BadOption.
// That is the chosen trade-off, not an oversight — the repair path is string-typed
// (ApplyOptionFixes marshals the chosen choice as a JSON string), so a BadOption here
// could only offer a "fix" that writes "8" where ComfyUI requires the number 8.
// ComfyUI's own submit-time validation stays the authority for numeric combos.
func TestPreflightOffListNumericIsNotFlagged(t *testing.T) {
	info := buildInfo(t, rifeObjectInfo)
	api := json.RawMessage(`{"10": {"class_type": "RIFE VFI", "inputs": {
	  "ckpt_name": "rife47.pth", "scale_factor": 8, "frames": ["9", 0]}}}`)
	rep := Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 0 {
		t.Errorf("off-list numeric raised %d BadOptions (under-validation is intended): %+v",
			len(rep.BadOptions), rep.BadOptions)
	}
}

// TestNormalizeComboLeavesNumericComboAlone guards the other Choices consumer. A
// single-option numeric combo must NOT be rewritten: normalizeComboWidget's Tier 1
// emits Choices[0] as a JSON STRING, so normalizing 1.0 would write "1.0" and
// ComfyUI would reject the graph.
func TestNormalizeComboLeavesNumericComboAlone(t *testing.T) {
	c := &converter{}
	single := specOf(t, `[[2.0], {"default": 2.0}]`)
	raw := json.RawMessage(`4`)
	if got := c.normalizeComboWidget("RIFE VFI", "scale_factor", single, raw); string(got) != "4" {
		t.Errorf("normalizeComboWidget rewrote a numeric combo value to %s, want 4 unchanged", got)
	}
	// A single-choice STRING combo still normalizes, as before.
	str := specOf(t, `[["only_one"], {}]`)
	if got := c.normalizeComboWidget("SomeNode", "mode", str, json.RawMessage(`"gone"`)); string(got) != `"only_one"` {
		t.Errorf("single-choice string combo = %s, want \"only_one\"", got)
	}
}
