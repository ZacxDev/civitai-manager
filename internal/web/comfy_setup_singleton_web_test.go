package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// The setup slot is a WHOLE-PANEL SINGLETON, and the failure panel has two sections
// that can want it: the missing-models batch action and the incompatible-options
// form. failureSetupOwner decides between them; these tests are what make that
// decision checkable.
//
// ⚠ This comment used to say comfySetupCTA renders "the app's only id=comfy-setup".
// TWO renderers carry that id — comfySetupCTA (run_install_all.go) and
// comfySetupDisclosure (run_comfy_setup.go) — so the singleton is a property the
// panel has to be GIVEN, not one it has for free. The two can never both be reached
// (the disclosure renders only when Available, and Available ⟹ dlEligible ⟹
// blockedModelFileOptions == 0), which is precisely the kind of load-bearing coupling
// the "only renderer" wording would let a future reader assume away.
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
	// A SECOND inert option, distinct from the first so a section can hold two of them
	// without the grouping key collapsing them.
	inertBadOption2 = comfy.BadOption{
		NodeIDs: []string{"4"}, ClassType: "ImpactWildcardEncode", InputName: "populated_text",
		Current: "a gone preset", Choices: []string{"another preset"},
	}
	// 🔴 THE DISCRIMINATOR FOR THE MODEL-FILE FILTER: routable=TRUE, modelFile=FALSE.
	// The same detector input as routableFileBadOption, drifted to a LABEL rather than
	// a filename — measured `IsModelFileValue=false, routable=true`.
	//
	// Every other fixture has the two filters agreeing, which is why deleting
	// comfy.IsModelFileValue from blockedModelFileOptions survived TWICE: the inert
	// fixtures are rejected by the routable filter anyway, so the row that looked like
	// it guarded the model-file filter proved nothing about it. This one is rejected
	// ONLY by the model-file filter.
	//
	// It is also the case with the worst consumer cost. badOptionGroup gates its
	// Install control on IsModelFileValue, so this option renders NO Install button at
	// all — counting it would promise to unblock a control that does not exist, which
	// is a step beyond the unroutable case (where the button at least renders,
	// disabled).
	routableNonFileBadOption = comfy.BadOption{
		NodeIDs: []string{"7"}, ClassType: "UltralyticsDetectorProvider", InputName: "model_name",
		Current: "Select a detector", Choices: []string{"bbox/face_yolov8m.pt"},
	}
	// A SECOND routable non-file. It exists so the two dropped-filter mutants produce
	// DIFFERENT counts: with one of each shape both yield 2 and a failure message
	// cannot say which filter went. See TestSetupCTALabelCountsOnlyUnblockableFiles.
	routableNonFileBadOption2 = comfy.BadOption{
		NodeIDs: []string{"8"}, ClassType: "UltralyticsDetectorProvider", InputName: "model_name",
		Current: "pick one", Choices: []string{"bbox/hand_yolov8s.pt"},
	}
)

// TestBadOptionFixturesAreDiscriminating is the PRECONDITION for every test below
// that reasons about these three shapes.
//
// 🔴 It exists because two of this file's rows were OVER-DETERMINED. The
// `inertBadOption` row expecting 0 containers passed with blockedModelFileOptions'
// comfy.IsModelFileValue filter DELETED — because the inert fixture also fails
// InferBadOptionInstall, so both signals said "no" and the row proved nothing about
// the filter it appeared to guard. Asserting the two signals SEPARATELY, and pinning
// that they disagree where they must, is what makes a fixture able to discriminate.
func TestBadOptionFixturesAreDiscriminating(t *testing.T) {
	for _, tc := range []struct {
		name           string
		bo             comfy.BadOption
		wantModelFile  bool
		wantRoutable   bool
		discriminating string
	}{
		{"routable", routableFileBadOption, true, true,
			"the only shape a models folder unblocks"},
		{"unroutable", unroutableFileBadOption, true, false,
			"a MODEL FILE that is NOT routable — separates the two filters"},
		{"inert", inertBadOption, false, false, "neither signal"},
		{"inert2", inertBadOption2, false, false, "neither signal"},
		{"routable non-file", routableNonFileBadOption, false, true,
			"ROUTABLE but not a model file — separates the filters the other way"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := comfy.IsModelFileValue(tc.bo.Current); got != tc.wantModelFile {
				t.Errorf("IsModelFileValue(%q) = %v, want %v (%s)",
					tc.bo.Current, got, tc.wantModelFile, tc.discriminating)
			}
			_, _, routable := comfy.InferBadOptionInstall(tc.bo.ClassType, tc.bo.InputName, tc.bo.Current)
			if routable != tc.wantRoutable {
				t.Errorf("InferBadOptionInstall(%q,%q,%q) routable = %v, want %v (%s)",
					tc.bo.ClassType, tc.bo.InputName, tc.bo.Current, routable, tc.wantRoutable,
					tc.discriminating)
			}
		})
	}
	// 🔴 The discrimination that matters, and it needs a fixture in EACH direction.
	// A set where the two filters only ever agree is over-determined: deleting either
	// one changes nothing, and a row that appears to guard it passes for the other
	// filter's reason. That is not hypothetical — it is why the model-file filter's
	// mutant survived two rounds.
	_, _, unroutableRoutable := comfy.InferBadOptionInstall(
		unroutableFileBadOption.ClassType, unroutableFileBadOption.InputName, unroutableFileBadOption.Current)
	if !comfy.IsModelFileValue(unroutableFileBadOption.Current) || unroutableRoutable {
		t.Fatalf("no fixture is model-file-but-NOT-routable, so deleting the routable filter " +
			"is undetectable and every row below is over-determined")
	}
	_, _, nonFileRoutable := comfy.InferBadOptionInstall(
		routableNonFileBadOption.ClassType, routableNonFileBadOption.InputName, routableNonFileBadOption.Current)
	if comfy.IsModelFileValue(routableNonFileBadOption.Current) || !nonFileRoutable {
		t.Fatalf("no fixture is routable-but-NOT-a-model-file, so deleting the model-file " +
			"filter is undetectable — the exact mutant that survived twice")
	}
}

// TestBlockedModelFileOptionsCountsOnlyUnblockableFiles pins the COUNT itself, over
// fixtures that are pairwise distinct on the dimension under test.
//
// 🔴 Two surviving mutants lived here. Deleting the comfy.IsModelFileValue filter
// survived the whole suite, and so did replacing the call with len(bad) at the CTA's
// label. Both survived for the same reason: every existing fixture had ONE bad option,
// where "count the unblockable ones", "count the model files" and "count everything"
// all produce 1. A mixed section is what separates them.
func TestBlockedModelFileOptionsCountsOnlyUnblockableFiles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bad        []comfy.BadOption
		dlEligible bool
		want       int
		why        string
	}{
		{"one routable", []comfy.BadOption{routableFileBadOption}, false, 1, "the ground case"},
		{
			name: "one routable among two inert",
			bad:  []comfy.BadOption{routableFileBadOption, inertBadOption, inertBadOption2},
			want: 1,
			why:  "len(bad) would say 3 and the label would over-promise by two files",
		}, {
			name: "one routable, one unroutable",
			bad:  []comfy.BadOption{routableFileBadOption, unroutableFileBadOption},
			want: 1,
			why:  "dropping the routable filter would say 2; the folder cannot place the second",
		}, {
			name: "unroutable and inert only",
			bad:  []comfy.BadOption{unroutableFileBadOption, inertBadOption},
			want: 0,
			why:  "nothing here a models folder unblocks",
		}, {
			name: "two inert only",
			bad:  []comfy.BadOption{inertBadOption, inertBadOption2},
			want: 0,
			why:  "nothing routable and nothing installable",
		}, {
			// 🔴 THE ROW THAT KILLS THE MODEL-FILE MUTANT. The "two inert" row above
			// looks like it does, and does not: the routable filter rejects those two
			// anyway, so it stays green with IsModelFileValue deleted. This one is
			// rejected ONLY by IsModelFileValue.
			name: "routable but not a model file",
			bad:  []comfy.BadOption{routableNonFileBadOption},
			want: 0,
			why:  "it renders no Install control at all, so a folder unblocks nothing",
		}, {
			name: "one routable among every other shape",
			bad: []comfy.BadOption{routableFileBadOption, routableNonFileBadOption,
				unroutableFileBadOption, inertBadOption},
			want: 1,
			why:  "exactly one of the four is a model file this server could place",
		}, {
			name: "routable but already configured", bad: []comfy.BadOption{routableFileBadOption},
			dlEligible: true, want: 0, why: "every Install button already works",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := blockedModelFileOptions(tc.bad, tc.dlEligible); got != tc.want {
				t.Errorf("blockedModelFileOptions = %d, want %d — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestSetupCTALabelCountsOnlyUnblockableFiles is the same discrimination at the
// RENDER layer: the number the user reads.
//
// The consuming cost of getting this wrong is a specific over-promise — "Set up
// automatic install for 3 model files" when exactly one gets unblocked — which is the
// harm blockedModelFileOptions' own comment exists to prevent.
func TestSetupCTALabelCountsOnlyUnblockableFiles(t *testing.T) {
	// 🔴 THE FIXTURE SET IS CHOSEN SO EACH MUTANT PRODUCES A DISTINCT COUNT. With one
	// option of each shape, dropping the model-file filter and dropping the routable
	// filter BOTH yield 2, so the failure message cannot say which one went — the
	// diagnostic would name a filter at random and send the reader to the wrong line.
	// A second routable non-file separates them:
	//
	//	shape                 modelFile  routable   counted by…
	//	routableFile              yes       yes     every variant   (the ONLY correct one)
	//	routableNonFile  ×2        no       yes     model-file filter dropped
	//	unroutableFile           yes        no      routable filter dropped
	//	inert                     no        no      len(bad) only
	//
	// → correct 1 · model-file dropped 3 · routable dropped 2 · len(bad) 5. All distinct.
	bad := []comfy.BadOption{routableFileBadOption, routableNonFileBadOption,
		routableNonFileBadOption2, unroutableFileBadOption, inertBadOption}
	section := renderString(t, incompatibleOptionsSection(bad, 7, "tok", false, false, true))

	// PRECONDITIONS. All five groups rendered, and exactly the two MODEL-FILE options
	// carry an Install control (that is what IsModelFileValue gates in badOptionGroup).
	// ⚠ That 2 is NOT the "routable filter dropped" answer — the two numbers coincide
	// only because both sets happen to have size 2 here. An earlier version of this
	// comment presented it as the mutant's signature, which is a coincidence, not a
	// mechanism.
	if n := strings.Count(section, `name="opt_input"`); n != len(bad) {
		t.Fatalf("precondition: want %d rendered groups, got %d:\n%s", len(bad), n, section)
	}
	if n := strings.Count(section, ">Install "); n != 2 {
		t.Fatalf("precondition: want 2 Install controls (the two model-file options), got %d:\n%s",
			n, section)
	}
	if !strings.Contains(section, "Set up automatic install for 1 model file<") {
		t.Errorf("the CTA must count only the files a models folder unblocks (1 of %d):\n%s",
			len(bad), section)
	}
	// Each wrong count now maps to exactly ONE dropped filter, so the message points at
	// the right line.
	for _, wrong := range []struct{ text, mutant string }{
		{"for 2 model file", "the ROUTABLE filter was dropped (it counted the two model files)"},
		{"for 3 model file", "the MODEL-FILE filter was dropped (it counted the three routable options)"},
		{"for 5 model file", "the count became len(bad)"},
	} {
		if strings.Contains(section, wrong.text) {
			t.Errorf("the CTA over-promises (%q) — %s:\n%s", wrong.text, wrong.mutant, section)
		}
	}
}

// TestBadOptionBlockedReasonOrderIsLoadBearing pins badOptionBlockedReason over ALL
// FOUR (routable, comfyRemote) combinations.
//
// 🔴 Swapping the function's first two branches survived the whole suite, while its
// comment says "ORDER IS LOAD-BEARING" and spells out the harm. It survived because
// no fixture was `!routable && comfyRemote` — the one input on which the two orders
// differ. A comment asserting an invariant that nothing tests is the shape this repo
// keeps finding; the missing row is here.
func TestBadOptionBlockedReasonOrderIsLoadBearing(t *testing.T) {
	for _, tc := range []struct {
		routable, comfyRemote bool
		want                  string
		why                   string
	}{
		{true, false, badOptionNeedsSetupText, "local ComfyUI, no folder: the page can fix it"},
		{true, true, badOptionRemoteComfyText, "remote comfy_url: the folder is not the blocker"},
		{false, false, badOptionUnroutableText, "no destination for this file"},
		{
			routable: false, comfyRemote: true, want: badOptionUnroutableText,
			why: "THE DISCRIMINATING ROW: unroutable survives fixing comfy_url, so it wins. " +
				"Reversed, the reader is sent to change comfy_url and comes back to the same " +
				"disabled button",
		},
	} {
		got := badOptionBlockedReason(tc.routable, tc.comfyRemote)
		if got != tc.want {
			t.Errorf("badOptionBlockedReason(routable=%v, comfyRemote=%v) = %q, want %q — %s",
				tc.routable, tc.comfyRemote, got, tc.want, tc.why)
		}
	}
	// The three strings must stay pairwise distinct, or the table above cannot tell
	// which branch answered and every row is satisfiable by one shared sentence.
	seen := map[string]bool{}
	for _, s := range []string{badOptionNeedsSetupText, badOptionRemoteComfyText, badOptionUnroutableText} {
		if seen[s] {
			t.Fatalf("two blocked reasons are byte-identical, so the table above cannot "+
				"discriminate between their branches: %q", s)
		}
		seen[s] = true
	}
}

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
