package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// The setup slot is a WHOLE-PANEL SINGLETON. comfySetupCTA renders the app's only
// id="comfy-setup", and the failure panel has two sections that can want it: the
// missing-models batch action and the incompatible-options form. failureSetupOwner
// decides between them; these tests are what make that decision checkable.
//
// 🔴 NONE of them may assert `strings.Contains(out, "disabled")`. Measured on
// 52cb872, that substring is TRUE on a fully-working panel — every section form
// carries htmx's `hx-disabled-elt="find button[type='submit']"` — so it is satisfied
// by a live control and cannot distinguish a blocked state from an unblocked one.
// It is the exact vacuous-guard shape CLAUDE.md catalogues (`Contains(body,
// "disabled")` satisfied by `hx-disabled-elt`). These assert ids, attributes on the
// same element, and exact counts instead.

// Fixtures. The three bad-option shapes differ in the ONE property the setup slot
// keys on — whether a models folder would turn the group's Install button live:
//
//   - routableFileBadOption: a model file whose type routes to a ComfyUI subdir, so
//     supplying the folder unblocks it. The only shape that can claim the slot.
//   - unroutableFileBadOption: a model file civitai-manager cannot place. Blocked for
//     a reason no folder choice changes.
//   - inertBadOption: not a model file at all; it has no Install control to unblock.
var (
	routableFileBadOption = comfy.BadOption{
		NodeIDs: []string{"42"}, ClassType: "UltralyticsDetectorProvider", InputName: "model_name",
		Current: "bbox/face_yolov9c.pt", Choices: []string{"bbox/face_yolov8m.pt"},
	}
	unroutableFileBadOption = comfy.BadOption{
		NodeIDs: []string{"9"}, ClassType: "UpscaleModelLoader", InputName: "model_name",
		Current: "RealESRGAN_x4plus.pth", Choices: []string{"other.pth"},
	}
	inertBadOption = comfy.BadOption{
		NodeIDs: []string{"3"}, ClassType: "ImpactWildcardProcessor", InputName: "Select to add Wildcard",
		Current: "Select Wildcard", Choices: []string{"Select the Wildcard to add to the text"},
	}
)

// installableMissing is a missing reference installDedupeKey accepts.
func installableMissing() []comfy.MissingModel {
	return []comfy.MissingModel{{Filename: "upscaler.pth", CivitaiType: "Upscaler"}}
}

// degenerateMissing is a missing reference installDedupeKey REJECTS (empty basename),
// so MissingModels is non-empty while batchInstallPlan.Installable is empty and
// installAllMissingAction returns nil.
//
// 🔴 This row is the whole reason failureSetupOwner tests "did the batch section
// render" instead of "are there missing models": the two disagree here, and the
// simpler predicate would hand the slot to a section that rendered nothing, leaving
// the panel with no affordance while believing it had one.
func degenerateMissing() []comfy.MissingModel {
	return []comfy.MissingModel{{Filename: "", CivitaiType: "Upscaler"}}
}

func failurePanelSnapshot(missing []comfy.MissingModel, bad []comfy.BadOption, comfyRemote bool) runSnapshot {
	pf := &comfy.PreflightReport{Nodes: 4, BadOptions: bad}
	for _, mm := range missing {
		pf.MissingModels = append(pf.MissingModels, mm.Filename)
	}
	return runSnapshot{
		Started: true, Message: "preflight failed",
		Preflight: pf, MissingModels: missing, ComfyRemote: comfyRemote,
	}
}

const setupContainerAttr = `id="comfy-setup"`

// setupContainers counts the rendered id="comfy-setup" elements.
//
// It is built from the literal attribute rather than from comfySetupContainerID so
// the expectation and the subject do not share a source — mutating that const would
// otherwise move both sides together and no mutation could separate them (CLAUDE.md's
// "calibrated to its own SOURCE"). The two are pinned equal by
// TestSetupContainerAttrMatchesTheConstant.
func setupContainers(out string) int { return strings.Count(out, setupContainerAttr) }

func TestSetupContainerAttrMatchesTheConstant(t *testing.T) {
	if want := `id="` + comfySetupContainerID + `"`; setupContainerAttr != want {
		t.Fatalf("the literal these tests count (%q) has drifted from the container id (%q)",
			setupContainerAttr, want)
	}
}

// TestFailurePanelHasAtMostOneComfySetupContainer is GUARD 1: the exact count of
// id="comfy-setup" over the full (MissingModels × BadOptions × dlEligible ×
// ComfyRemote) matrix.
//
// 🔴 It pins an EXACT count per row, never "<= 1". An at-most assertion is satisfied
// by a change that renders the control nowhere — which is the pre-existing bug this
// work fixes — so the rows expecting 1 are the harness's positive control and the
// rows expecting 0 its negative one. A run that reported all zeroes would fail here.
func TestFailurePanelHasAtMostOneComfySetupContainer(t *testing.T) {
	for _, tc := range []struct {
		name        string
		missing     []comfy.MissingModel
		bad         []comfy.BadOption
		dlEligible  bool
		comfyRemote bool
		want        int
		why         string
	}{
		{
			name: "missing models, configured", missing: installableMissing(),
			dlEligible: true, want: 1,
			why: "the working batch action carries the change-the-folder disclosure",
		}, {
			name: "missing models, local comfy, no folder", missing: installableMissing(),
			want: 1, why: "the blocked batch action's primary CTA is the setup step",
		}, {
			name: "missing models, remote comfy", missing: installableMissing(),
			comfyRemote: true, want: 0,
			why: "no folder choice can unblock a remote comfy_url, so there is nothing to offer",
		}, {
			name: "bad options only, local comfy, no folder", bad: []comfy.BadOption{routableFileBadOption},
			want: 1, why: "THE GAP: this surface rendered no setup affordance at all on 52cb872",
		}, {
			name: "bad options only, remote comfy", bad: []comfy.BadOption{routableFileBadOption},
			comfyRemote: true, want: 0,
			why: "same fail direction as the batch section: a remote comfy_url gets the explanation, not a CTA",
		}, {
			name: "bad options only, configured", bad: []comfy.BadOption{routableFileBadOption},
			dlEligible: true, want: 0,
			why: "every Install button already works; there is nothing to set up",
		}, {
			name: "unroutable bad option, no folder", bad: []comfy.BadOption{unroutableFileBadOption},
			want: 0, why: "a folder choice cannot place a file civitai-manager cannot route",
		}, {
			name: "inert bad option, no folder", bad: []comfy.BadOption{inertBadOption},
			want: 0, why: "an inert enum drift has no Install control to unblock",
		}, {
			name: "degenerate missing + bad option, no folder", missing: degenerateMissing(),
			bad: []comfy.BadOption{routableFileBadOption}, want: 1,
			why: "the batch section rendered NOTHING, so the options section must take the slot",
		}, {
			name: "degenerate missing only, no folder", missing: degenerateMissing(),
			want: 0, why: "neither section renders anything that needs the slot",
		}, {
			name: "missing models AND bad options, no folder", missing: installableMissing(),
			bad: []comfy.BadOption{routableFileBadOption}, want: 1,
			why: "BOTH sections want it; the batch section takes it and the options section must not add a second",
		}, {
			name: "missing models AND bad options, configured", missing: installableMissing(),
			bad: []comfy.BadOption{routableFileBadOption}, dlEligible: true, want: 1,
			why: "the disclosure is the single instance; the options section adds nothing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := failurePanelSnapshot(tc.missing, tc.bad, tc.comfyRemote)
			out := renderString(t, runFailure(snap, 7, "tok", tc.dlEligible, fullMaturityRange()))

			// PRECONDITIONS. A fixture that renders an empty or half-built panel would
			// otherwise satisfy every want==0 row for the wrong reason.
			if !strings.Contains(out, "Run failed") {
				t.Fatalf("precondition: this is not a failure panel:\n%s", out)
			}
			if got := strings.Contains(out, "Incompatible options"); got != (len(tc.bad) > 0) {
				t.Fatalf("precondition: incompatible-options section rendered=%v, want %v:\n%s",
					got, len(tc.bad) > 0, out)
			}

			if n := setupContainers(out); n != tc.want {
				t.Errorf("want %d #comfy-setup container(s), got %d — %s:\n%s", tc.want, n, tc.why, out)
			}
		})
	}
}

// TestBadOptionOnlyFailureReachesTheSetupControl is GUARD 2: the reachability half.
//
// A container that is present but not wired to anything is the same dead end as no
// container, so this asserts the TRIGGER on the container's own subtree — the button
// carrying hx-get to the setup endpoint — and its PLACEMENT above the groups it
// unblocks. Measured on 52cb872 this panel carried 0 of both.
func TestBadOptionOnlyFailureReachesTheSetupControl(t *testing.T) {
	snap := failurePanelSnapshot(nil, []comfy.BadOption{routableFileBadOption}, false)
	out := renderString(t, runFailure(snap, 7, "tok", false, fullMaturityRange()))

	// PRECONDITION: the surface really is bad-options-only and really is blocked.
	if !strings.Contains(out, "Incompatible options") {
		t.Fatalf("precondition: want the incompatible-options section:\n%s", out)
	}
	if strings.Contains(out, "install-option-and-run") {
		t.Fatalf("precondition: the Install control must be BLOCKED in this state:\n%s", out)
	}

	if n := setupContainers(out); n != 1 {
		t.Fatalf("want exactly one #comfy-setup on the bad-option-only panel, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, `hx-get="/workflows/7/comfy-setup"`) {
		t.Errorf("the container must carry a trigger that loads the setup form:\n%s", out)
	}
	// The button names the sentence explaining it, on the same control.
	if !strings.Contains(out, `aria-describedby="`+comfySetupWhyID+`"`) {
		t.Errorf("the setup CTA must point at its own explanation:\n%s", out)
	}
	// The label counts the blocked model-file options — one, here.
	if !strings.Contains(out, "Set up automatic install for 1 model file<") {
		t.Errorf("the CTA label must count the files it would unblock:\n%s", out)
	}
	// PLACEMENT: above the groups. opt_input is the first field of the first group.
	ci, gi := strings.Index(out, setupContainerAttr), strings.Index(out, `name="opt_input"`)
	if ci < 0 || gi < 0 || ci > gi {
		t.Errorf("the setup control must render ABOVE the groups whose Install buttons it "+
			"unblocks (container at %d, first group at %d):\n%s", ci, gi, out)
	}
}

// TestRemoteComfyBadOptionPanelOffersNoSetup is GUARD 3: the preserved fail
// direction. A remote comfy_url gets NO form, on this surface exactly as on the
// missing-models one — a prominent CTA opening a panel that says "this server is not
// pointed at a ComfyUI on this machine" is the same dead end, one click further in.
func TestRemoteComfyBadOptionPanelOffersNoSetup(t *testing.T) {
	snap := failurePanelSnapshot(nil, []comfy.BadOption{routableFileBadOption}, true)
	out := renderString(t, runFailure(snap, 7, "tok", false, fullMaturityRange()))

	if !strings.Contains(out, "Incompatible options") {
		t.Fatalf("precondition: want the incompatible-options section:\n%s", out)
	}
	if n := setupContainers(out); n != 0 {
		t.Errorf("a remote comfy_url must get NO setup container, got %d:\n%s", n, out)
	}
	if strings.Contains(out, "/comfy-setup") {
		t.Errorf("a remote comfy_url must reach no setup endpoint at all:\n%s", out)
	}
	// It says WHY, once — not once per group.
	if n := strings.Count(out, installRemoteComfyReason); n != 1 {
		t.Errorf("want the remote reason stated exactly once, got %d:\n%s", n, out)
	}
	// And the per-option line names the real blocker rather than the folder.
	if !strings.Contains(out, badOptionRemoteComfyText) {
		t.Errorf("the blocked Install must name comfy_url, not a missing folder:\n%s", out)
	}
	if strings.Contains(out, badOptionNeedsSetupText) {
		t.Errorf("a remote comfy_url must not tell the reader to supply a folder:\n%s", out)
	}
}

// TestBadOptionSetupPointerImpliesASetupControlOnThePage pins the INVARIANT that
// makes badOptionNeedsSetupText honest.
//
// That sentence says "use the setup step above". The predecessor string deliberately
// pointed at nothing, because on the bad-option-only surface no setup control
// existed. The pointer is only safe while failureSetupOwner guarantees one is on the
// page — so this asserts the implication directly across the whole matrix instead of
// trusting the reasoning, and it fails in BOTH directions: the pointer without a
// control, and a state that should carry the pointer carrying none.
func TestBadOptionSetupPointerImpliesASetupControlOnThePage(t *testing.T) {
	pointerSeen := 0
	for _, missing := range [][]comfy.MissingModel{nil, installableMissing(), degenerateMissing()} {
		for _, bad := range [][]comfy.BadOption{
			{routableFileBadOption},
			{unroutableFileBadOption},
			{inertBadOption},
			{routableFileBadOption, inertBadOption},
		} {
			for _, dlEligible := range []bool{true, false} {
				for _, comfyRemote := range []bool{true, false} {
					snap := failurePanelSnapshot(missing, bad, comfyRemote)
					out := renderString(t, runFailure(snap, 7, "tok", dlEligible, fullMaturityRange()))
					if !strings.Contains(out, badOptionNeedsSetupText) {
						continue
					}
					pointerSeen++
					if n := setupContainers(out); n != 1 {
						t.Errorf("the reason says 'use the setup step above' with %d setup "+
							"control(s) on the page (missing=%d bad=%d dlEligible=%v remote=%v):\n%s",
							n, len(missing), len(bad), dlEligible, comfyRemote, out)
					}
				}
			}
		}
	}
	// POSITIVE CONTROL. A zero here is indistinguishable from a scan wired to
	// nothing — the loop would then have "verified" the implication without ever
	// evaluating it.
	if pointerSeen == 0 {
		t.Fatalf("the sweep never rendered the setup pointer, so it proved nothing about it")
	}
	t.Logf("setup pointer rendered in %d of the matrix's states", pointerSeen)
}
