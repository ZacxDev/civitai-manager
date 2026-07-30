package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// openComfyAction is the exact markup the shared "Open in ComfyUI" control emits.
// Asserting the ACTION (not just the label) is what pins "reuse the existing flow"
// — a re-implementation as an htmx button would not match.
const openComfyAction = `action="/workflows/9/open-in-comfyui"`

// TestRunFailureOffersOpenInComfyUI walks every failure path the run card can land
// on and asserts the escape hatch is there: the user is one click from the graph
// that needs fixing. It is deliberately NOT on the success path.
func TestRunFailureOffersOpenInComfyUI(t *testing.T) {
	base := runSnapshot{Started: true, WorkflowID: 9, UIFormat: true}

	failures := map[string]runSnapshot{
		"preflight — missing custom nodes": func() runSnapshot {
			s := base
			s.Phase, s.Message = runPhaseFailed, "Preflight failed."
			s.Preflight = &comfy.PreflightReport{MissingNodes: []string{"CR Float To Integer"}}
			return s
		}(),
		"preflight — missing models": func() runSnapshot {
			s := base
			s.Phase, s.Message = runPhaseFailed, "Preflight failed."
			s.Preflight = &comfy.PreflightReport{MissingModels: []string{"SmoothMix_T2V_High_v4.safetensors"}}
			return s
		}(),
		"preflight — incompatible options": func() runSnapshot {
			s := base
			s.Phase, s.Message = runPhaseFailed, "Preflight failed."
			s.Preflight = &comfy.PreflightReport{BadOptions: []comfy.BadOption{
				{ClassType: "UltralyticsDetectorProvider", InputName: "model_name",
					Current: "bbox/face_yolov9c.pt", Choices: []string{"a.pt", "b.pt"}}}}
			return s
		}(),
		"conversion warnings": func() runSnapshot {
			s := base
			s.Phase, s.Message = runPhaseFailed, "This workflow could not be converted into a runnable graph."
			s.Warnings = []string{`node 134 type "CR Float To Integer" not available`}
			return s
		}(),
		"aborted — nothing to run": func() runSnapshot {
			s := base
			s.Phase, s.Aborted, s.Message = runPhaseFailed, true,
				"all 98 executable nodes in this workflow are disabled (bypassed or muted)."
			return s
		}(),
		"transport / ComfyUI rejection": func() runSnapshot {
			s := base
			s.Phase, s.Message = runPhaseFailed, "ComfyUI rejected the workflow"
			return s
		}(),
	}

	for name, snap := range failures {
		t.Run(name, func(t *testing.T) {
			body := renderString(t, runStatusFragment(snap, 9, "csrf-tok", false, "blur"))
			if !strings.Contains(body, openComfyAction) {
				t.Errorf("failure path %q does not offer the existing Open-in-ComfyUI flow:\n%s", name, body)
			}
			if !strings.Contains(body, "Open in ComfyUI") {
				t.Errorf("failure path %q missing the visible label:\n%s", name, body)
			}
			// It is a real target=_blank form POST (see workflow_open_comfy.go) carrying
			// CSRF — never an htmx button, which cannot open the tab from the click.
			if !strings.Contains(body, `target="_blank"`) ||
				!strings.Contains(body, `method="post"`) ||
				!strings.Contains(body, `name="csrf_token"`) {
				t.Errorf("failure path %q: the control is not the real CSRF-carrying form POST:\n%s", name, body)
			}
			// The failure fragment is terminal — the poller must not be re-armed.
			if hasRunPoller(body) {
				t.Errorf("failure path %q re-armed the poller:\n%s", name, body)
			}
		})
	}
}

// TestRunSuccessAndStopOmitOpenInComfyUI keeps the success path clean, and leaves a
// user-initiated Stop alone (it is not a broken graph).
func TestRunSuccessAndStopOmitOpenInComfyUI(t *testing.T) {
	for name, snap := range map[string]runSnapshot{
		"success": {Started: true, WorkflowID: 9, UIFormat: true, Phase: runPhaseDone,
			Message: "Run complete.",
			Images:  []comfy.ImageRef{{Filename: "o.png", Type: "output"}}},
		"success, no images": {Started: true, WorkflowID: 9, UIFormat: true, Phase: runPhaseDone,
			Message: "Run complete (no images returned)."},
		"stopped by the user": {Started: true, WorkflowID: 9, UIFormat: true,
			Phase: runPhaseFailed, Stopped: true},
	} {
		t.Run(name, func(t *testing.T) {
			body := renderString(t, runStatusFragment(snap, 9, "csrf-tok", false, "blur"))
			if strings.Contains(body, "open-in-comfyui") {
				t.Errorf("%s should not carry the Open-in-ComfyUI escape hatch:\n%s", name, body)
			}
		})
	}
}

// TestRunFailureOmitsOpenInComfyUIForAPIGraphs: an api-format graph does not load
// into the editor (the handler refuses it), so offering the control would be a dead
// end. UIFormat rides on the run job, which is why it is available here at all.
func TestRunFailureOmitsOpenInComfyUIForAPIGraphs(t *testing.T) {
	snap := runSnapshot{Started: true, WorkflowID: 9, UIFormat: false,
		Phase: runPhaseFailed, Message: "Preflight failed.",
		Preflight: &comfy.PreflightReport{MissingModels: []string{"x.safetensors"}}}
	body := renderString(t, runStatusFragment(snap, 9, "csrf-tok", false, "blur"))
	if strings.Contains(body, "open-in-comfyui") {
		t.Errorf("api-format run failure must not offer the editor link:\n%s", body)
	}
	// The failure report itself is still rendered.
	if !strings.Contains(body, "Run failed") {
		t.Errorf("failure report missing:\n%s", body)
	}
}

// TestOpenInComfyFormIsSharedNotDuplicated pins that the Generate section and the
// failure report emit the SAME control from the SAME helper.
func TestOpenInComfyFormIsSharedNotDuplicated(t *testing.T) {
	wf := &store.Workflow{ID: 9, Format: store.WorkflowFormatUI, Graph: "{}"}
	card := renderString(t, generateSection(wf, runSnapshot{}, "csrf-tok", true, false, "blur",
		implicitPresetView(wf, nil), true, comfyHelperView{}))
	fail := renderString(t, runStatusFragment(runSnapshot{
		Started: true, WorkflowID: 9, UIFormat: true, Phase: runPhaseFailed,
		Message: "Preflight failed.",
		Preflight: &comfy.PreflightReport{
			MissingNodes: []string{"CR Float To Integer"}},
	}, 9, "csrf-tok", false, "blur"))

	for _, want := range []string{openComfyAction, `target="_blank"`, `rel="noopener"`} {
		if !strings.Contains(card, want) || !strings.Contains(fail, want) {
			t.Errorf("both surfaces must carry %q (card=%v fail=%v)", want,
				strings.Contains(card, want), strings.Contains(fail, want))
		}
	}
}
