package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// parseObjectInfo unmarshals an ObjectInfo from a compact JSON literal, the same
// way a real /object_info response would be decoded.
func parseObjectInfo(t *testing.T, raw string) comfy.ObjectInfo {
	t.Helper()
	var oi comfy.ObjectInfo
	if err := json.Unmarshal([]byte(raw), &oi); err != nil {
		t.Fatalf("parse object_info: %v", err)
	}
	return oi
}

// TestWorkflowResourceChipComfyState is the table-driven pin on the three-state
// chip renderer: library (✓), comfy-only (◎), and not found (✗).
func TestWorkflowResourceChipComfyState(t *testing.T) {
	// Build a minimal ObjectInfo that contains "comfy_only.safetensors" in its
	// loader choices but NOT "absent.safetensors".
	oi := parseObjectInfo(t, `{
		"CheckpointLoaderSimple": {
			"input": {
				"required": {
					"ckpt_name": [["comfy_only.safetensors","both.safetensors"],{}]
				}
			},
			"input_order": {"required": ["ckpt_name"]}
		}
	}`)

	res := workflowResolver{
		haveFile: func(b string) bool {
			// Only "both.safetensors" and "library_only.safetensors" are in the library.
			return b == "both.safetensors" || b == "library_only.safetensors"
		},
		localResource: func(b string) (resourceInfo, bool) {
			switch b {
			case "both.safetensors":
				return resourceInfo{Path: "/lib/both.safetensors", ModelID: 10, VersionID: 20}, true
			case "library_only.safetensors":
				return resourceInfo{Path: "/lib/library_only.safetensors"}, true
			}
			return resourceInfo{}, false
		},
		comfyResource: func(b string) bool {
			for _, sch := range oi {
				if comfy.ChoicesContain(sch, b) {
					return true
				}
			}
			return false
		},
	}

	for _, tc := range []struct {
		name       string
		resource   string
		wantHave   string // data-have value
		wantMark   string // the visible mark character
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name:     "in library only — library wins",
			resource: "library_only.safetensors",
			wantHave: "yes",
			wantMark: "✓",
			wantSubstr: []string{
				`data-have="yes"`,
				"in your library",
				`title="/lib/library_only.safetensors"`,
			},
			notSubstr: []string{"◎", "comfy"},
		},
		{
			name:     "in comfy only — amber state",
			resource: "comfy_only.safetensors",
			wantHave: "comfy",
			wantMark: "◎",
			wantSubstr: []string{
				`data-have="comfy"`,
				"in ComfyUI",
				"◎",
			},
			notSubstr: []string{"✓", "✗", `data-have="yes"`, `data-have="no"`},
		},
		{
			name:     "in both — library wins",
			resource: "both.safetensors",
			wantHave: "yes",
			wantMark: "✓",
			wantSubstr: []string{
				`data-have="yes"`,
				`href="/models/10?modelVersionId=20"`,
			},
			notSubstr: []string{"◎"},
		},
		{
			name:     "not found anywhere — red",
			resource: "absent.safetensors",
			wantHave: "no",
			wantMark: "✗",
			wantSubstr: []string{
				`data-have="no"`,
				"not found",
				"✗",
			},
			notSubstr: []string{"✓", "◎", "href="},
		},
		{
			name:       "nil comfyResource — comfy state unavailable",
			resource:   "unknown.safetensors",
			wantHave:   "no",
			wantMark:   "✗",
			wantSubstr: []string{`data-have="no"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := res
			if tc.name == "nil comfyResource — comfy state unavailable" {
				r.comfyResource = nil
				r.haveFile = func(string) bool { return false }
				r.localResource = func(string) (resourceInfo, bool) { return resourceInfo{}, false }
			}
			got := renderString(t, workflowResourceChip(tc.resource, r))

			if !strings.Contains(got, tc.wantMark) {
				t.Errorf("expected mark %q in:\n%s", tc.wantMark, got)
			}
			if !strings.Contains(got, `data-have="`+tc.wantHave+`"`) {
				t.Errorf("expected data-have=%q in:\n%s", tc.wantHave, got)
			}
			for _, w := range tc.wantSubstr {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, n := range tc.notSubstr {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q in:\n%s", n, got)
				}
			}
		})
	}
}

// TestWorkflowResourceChipComfyCSSClass checks that the "comfy" state chip gets
// the right CSS class from app.css.
func TestWorkflowResourceChipComfyCSSClass(t *testing.T) {
	css := readAppCSS(t)
	// The amber comfy styling must exist in the shipped CSS.
	for _, want := range []string{
		`.cm-res-chip[data-have="comfy"]`,
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q — the comfy chip would be unstyled", want)
		}
	}
}

// TestWorkflowResourceChipComfyPopoverHasSourceDetail verifies the popover
// rendering for a workflow that has comfy-only resources — the popover trigger
// must show the resource count and the chip must show the comfy source.
func TestWorkflowResourceChipComfyPopoverHasSourceDetail(t *testing.T) {
	oi := parseObjectInfo(t, `{
		"Loader": {
			"input": {
				"required": {
					"model": [["model.safetensors"],{}]
				}
			},
			"input_order": {"required": ["model"]}
		}
	}`)
	res := workflowResolver{
		haveFile:      func(string) bool { return false },
		localResource: func(string) (resourceInfo, bool) { return resourceInfo{}, false },
		comfyResource: func(b string) bool {
			for _, sch := range oi {
				if comfy.ChoicesContain(sch, b) {
					return true
				}
			}
			return false
		},
	}
	chips := renderString(t, workflowResourceChips([]string{"model.safetensors"}, res))
	if !strings.Contains(chips, `data-have="comfy"`) {
		t.Errorf("the comfy chip must appear in the chips row:\n%s", chips)
	}
	if !strings.Contains(chips, "◎") {
		t.Errorf("the comfy chip must show the amber mark:\n%s", chips)
	}
}

// TestWorkflowResolverComfyResourceFunctionEndToEnd exercises the comfyResource
// function built by workflowResolver() against a real store with cached
// object_info data. This closes the loop from PutComfyObjectInfo → resolver →
// chip render.
func TestWorkflowResolverComfyResourceFunctionEndToEnd(t *testing.T) {
	srv := newWorkflowServer(t)
	// Store the raw object_info JSON directly — InputSpec has a custom
	// UnmarshalJSON (expects array format) but no MarshalJSON, so json.Marshal
	// of a parsed ObjectInfo would produce a struct representation that cannot
	// be unmarshaled back. The store holds bytes as-is, so seed with the raw
	// format that /object_info would return.
	oiJSON := []byte(`{
		"CheckpointLoaderSimple": {
			"input": {
				"required": {
					"ckpt_name": [["model_a.safetensors","model_b.safetensors"],{}]
				}
			},
			"input_order": {"required": ["ckpt_name"]}
		}
	}`)
	if err := srv.store.PutComfyObjectInfo(oiJSON); err != nil {
		t.Fatalf("seed comfy cache: %v", err)
	}

	resolver := srv.workflowResolver()

	// A basename present in the cached choices.
	if !resolver.comfyResource("model_a.safetensors") {
		t.Error("comfyResource should return true for model_a.safetensors")
	}
	// A basename NOT in the cached choices.
	if resolver.comfyResource("missing.safetensors") {
		t.Error("comfyResource should return false for missing.safetensors")
	}
}

// TestWorkflowResolverComfyResourceNoCache exercises the comfyResource function
// when the cache is empty — it must return false, never error.
func TestWorkflowResolverComfyResourceNoCache(t *testing.T) {
	srv := newWorkflowServer(t)
	resolver := srv.workflowResolver()

	if resolver.comfyResource("anything.safetensors") {
		t.Error("comfyResource should return false with an empty cache")
	}
}

// TestWorkflowResolverComfyResourceMalformedCache exercises the comfyResource
// function when the cache contains malformed JSON — it must return false, never
// panic.
func TestWorkflowResolverComfyResourceMalformedCache(t *testing.T) {
	srv := newWorkflowServer(t)
	if err := srv.store.PutComfyObjectInfo([]byte(`not valid json`)); err != nil {
		t.Fatal(err)
	}
	resolver := srv.workflowResolver()

	if resolver.comfyResource("anything.safetensors") {
		t.Error("comfyResource should return false with malformed cache")
	}
}
