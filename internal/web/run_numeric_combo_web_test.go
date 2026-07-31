package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// blankOptionRE matches an <option> with an EMPTY label, whatever attributes it
// carries. The attribute wildcard is load-bearing: a phantom choice renders as
// `<option value="" selected></option>` (the empty string equals the select's
// pre-selected value, so gomponents adds `selected`), so a literal search for
// `<option value=""></option>` finds nothing and the guard would pass while the
// picker is full of blanks. That miscalibration was observed under mutation.
var blankOptionRE = regexp.MustCompile(`<option[^>]*></option>`)

// rifeNumericComboInfo mirrors the REAL /object_info shape of ComfyUI 0.27.1's
// `RIFE VFI` (live-checked): a numeric FLOAT combo (scale_factor), alongside a string
// model combo. `WanVideoSetRadialAttention` contributes the numeric INT combo
// (block_size) and a plain string combo. Those two are the only shapes on a live
// 2462-node-type server that carry a non-string option list.
const rifeNumericComboInfo = `{
  "RIFE VFI": {
    "input": {"required": {
      "ckpt_name": [["rife47.pth"], {}],
      "scale_factor": [[0.25, 0.5, 1.0, 2.0, 4.0], {"default": 1.0}],
      "frames": ["IMAGE"]
    }},
    "input_order": {"required": ["ckpt_name","frames","scale_factor"]}
  },
  "WanVideoSetRadialAttention": {
    "input": {"required": {
      "dense_attention_mode": [["sdpa", "flash_attn_2"], {"default": "sdpa"}],
      "block_size": [[128, 64], {"default": 128}]
    }},
    "input_order": {"required": ["dense_attention_mode","block_size"]}
  }
}`

// TestNumericComboFixPickerNeverRendersBlankOptions is the user-visible end of the
// numeric-combo decode bug. Preflight used to raise a BadOption on
// `RIFE VFI · scale_factor` whose Choices were five EMPTY STRINGS, so the fix picker
// rendered five blank <option>s — an incompatible-options panel the user could not
// possibly resolve, blocking the run. This drives the REAL path (decode
// /object_info-shaped JSON -> Preflight -> render the panel), not a hand-built
// BadOption, so it fails if either half regresses.
func TestNumericComboFixPickerNeverRendersBlankOptions(t *testing.T) {
	var info comfy.ObjectInfo
	if err := json.Unmarshal([]byte(rifeNumericComboInfo), &info); err != nil {
		t.Fatalf("parse object_info: %v", err)
	}
	api := json.RawMessage(`{
	  "10": {"class_type": "RIFE VFI", "inputs": {
	     "ckpt_name": "rife47.pth", "scale_factor": 1, "frames": ["9", 0]}},
	  "11": {"class_type": "WanVideoSetRadialAttention", "inputs": {
	     "dense_attention_mode": "sdpa", "block_size": 128}}
	}`)
	rep := comfy.Preflight(api, info, func(string) bool { return true })

	if len(rep.BadOptions) != 0 {
		t.Errorf("preflight raised %d BadOption(s) on legitimate numeric-combo values — "+
			"the run would halt here: %+v", len(rep.BadOptions), rep.BadOptions)
	}
	section := renderString(t, incompatibleOptionsSection(rep.BadOptions, 7, "csrf-tok", true))
	if n := len(blankOptionRE.FindAllString(section, -1)); n != 0 {
		t.Errorf("fix picker rendered %d BLANK option(s):\n%s", n, section)
	}
	// Any <option> at all here would be a picker the user is being asked to resolve.
	if n := strings.Count(section, "<option"); n != 0 {
		t.Errorf("want no options rendered at all, got %d:\n%s", n, section)
	}
}

// TestDriftedStringComboStillRendersRealOptions is the anti-vacuity half: the same
// object_info, one genuinely drifted STRING combo value. The picker must appear and
// carry the real, non-blank choices — proving the test above says "no BadOption
// because numeric combos are handled", not "this fixture never reaches the panel".
func TestDriftedStringComboStillRendersRealOptions(t *testing.T) {
	var info comfy.ObjectInfo
	if err := json.Unmarshal([]byte(rifeNumericComboInfo), &info); err != nil {
		t.Fatalf("parse object_info: %v", err)
	}
	api := json.RawMessage(`{
	  "11": {"class_type": "WanVideoSetRadialAttention", "inputs": {
	     "dense_attention_mode": "a_mode_that_is_gone", "block_size": 128}}
	}`)
	rep := comfy.Preflight(api, info, func(string) bool { return true })
	if len(rep.BadOptions) != 1 {
		t.Fatalf("want 1 BadOption for the drifted string combo, got %d: %+v",
			len(rep.BadOptions), rep.BadOptions)
	}
	section := renderString(t, incompatibleOptionsSection(rep.BadOptions, 7, "csrf-tok", true))
	for _, want := range []string{`<option value="sdpa"`, `<option value="flash_attn_2"`} {
		if !strings.Contains(section, want) {
			t.Errorf("section missing %q:\n%s", want, section)
		}
	}
	if n := len(blankOptionRE.FindAllString(section, -1)); n != 0 {
		t.Errorf("blank option rendered %d time(s):\n%s", n, section)
	}
}
