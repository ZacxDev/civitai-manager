package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// A SUCCESSFUL node-pack install used to present as a failure.
//
// Measured end-to-end: ComfyUI-Manager reports the pack installed and pending a
// restart, the user clicks the "Run again" the panel itself invites, and the
// missing-nodes panel comes back offering "Install ComfyUI_UltimateSDUpscale" all
// over again with no acknowledgement. Meanwhile the honest terminal message ("…
// installed — ComfyUI must restart before this workflow can run") still existed
// server-side and GET /workflows/nodepacks/status still returned it verbatim — but
// the new run's fragment emits an EMPTY #nodepack-status, so the swap destroyed the
// one place the restart instruction was shown.
//
// The cause was that attributeMissingNodes consulted Manager's INDEX (getmappings /
// getlist) and never its INSTALLED DIFF, which the install job already used.
//
// Every assertion in this file is on a STATE — a data-nodepack-action /
// data-nodepack-state attribute — never on a label or on the word "disabled". This
// repo has shipped both of those mistakes: a Contains(body,"disabled") satisfied by
// htmx's own hx-disabled-elt on a live button, and a dead-control guard that knew
// only the previous button label.

// installedPendingMarker / installActionMarker / restartActionMarker are the three
// states these tests count. They are built from the same strings the renderer emits
// via dataAttr, so a renamed marker fails loudly here instead of silently matching
// nothing (a substring that matches nothing is indistinguishable from a pass).
const (
	installedPendingMarker = `data-nodepack-state="installed-pending"`
	installActionMarker    = `data-nodepack-action="install"`
	restartActionMarker    = `data-nodepack-action="restart"`
)

// countMarkers is the counting primitive. Every zero it reports is paired with a
// positive control somewhere in this file: a count of 0 from a probe wired to
// nothing looks exactly like a count of 0 from a correct render.
func countMarkers(body, marker string) int {
	return strings.Count(body, marker)
}

// installedUpscalePack is the measured pack from the real report: ssitu's
// ComfyUI_UltimateSDUpscale, which provides UltimateSDUpscale.
func installedUpscalePack() comfy.Pack {
	return comfy.Pack{
		ID: "comfyui_ultimatesdupscale", Title: "ComfyUI_UltimateSDUpscale",
		Repository:  "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
		Version:     "1.7.2",
		Installable: true,
		Classes:     []string{"UltimateSDUpscale"}, ClaimedClasses: 4,
		Source: comfy.SourceMap,
	}
}

// upscaleDirName is what ComfyUI-Manager's installed set actually contains: the
// custom_nodes DIRECTORY name, which is the repository's last path segment and not
// this app's pack id. Matching the two is matchPackInDiff's job — the same predicate
// the install job confirms a landing with — so a fixture that used the pack id would
// be testing an easier problem than production has.
const upscaleDirName = "ComfyUI_UltimateSDUpscale"

// TestPackCardDistinguishesInstalledFromNotInstalled asserts the three panel states
// the fix has to keep apart. It is the REGRESSION test: on pre-change code the
// second case renders the first case's markup.
func TestPackCardDistinguishesInstalledFromNotInstalled(t *testing.T) {
	pack := installedUpscalePack()

	cases := []struct {
		name    string
		pending []string
		// wantInstallActions is a literal, never derived from what the render
		// produced — an expectation sharing a source with its subject cannot fail.
		wantInstallActions int
		wantPendingState   int
	}{
		{
			name:               "not installed — the Install affordance is offered",
			pending:            nil,
			wantInstallActions: 1,
			wantPendingState:   0,
		},
		{
			name:               "installed, pending a restart — acknowledged, never re-offered",
			pending:            []string{upscaleDirName},
			wantInstallActions: 0,
			wantPendingState:   1,
		},
		{
			name: "installed under some OTHER pack's name — no false acknowledgement",
			// The fail-closed direction of the same predicate: an unrelated entry in
			// Manager's diff must not mark this pack installed. Without this the
			// "installed" case could pass with a predicate that returns true for any
			// non-empty diff.
			pending:            []string{"ComfyUI-SomethingElse"},
			wantInstallActions: 1,
			wantPendingState:   0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			attr := nodeAttribution{
				ManagerPresent:   true,
				Packs:            []comfy.Pack{pack},
				InstalledPending: c.pending,
			}
			body := renderMissingNodesPanel(t, attr)

			// PRECONDITION: the card for THIS pack really rendered. Otherwise "0 install
			// actions" is satisfied by a panel that rendered no pack at all — one of this
			// repo's recorded vacuous-guard modes.
			if !strings.Contains(body, pack.Title) {
				t.Fatalf("precondition: the pack card did not render at all:\n%s", body)
			}

			if got := countMarkers(body, installActionMarker); got != c.wantInstallActions {
				t.Errorf("install affordances = %d, want %d\n%s", got, c.wantInstallActions, body)
			}
			if got := countMarkers(body, installedPendingMarker); got != c.wantPendingState {
				t.Errorf("installed-pending states = %d, want %d\n%s", got, c.wantPendingState, body)
			}

			// 🔴 The restart affordance is the whole point of the acknowledgement: a card
			// that says "restart ComfyUI to use it" beside no restart control is the bug
			// wearing different words. Exactly one, in every state — two identical
			// controls a few lines apart is the failure mode this panel already has a
			// documented history of.
			if got := countMarkers(body, restartActionMarker); got != 1 {
				t.Errorf("restart controls = %d, want exactly 1\n%s", got, body)
			}
		})
	}
}

// TestInstalledPendingCardStatesTheNextAction pins the words the user acts on, on
// the element that carries the state. Asserting the copy alone would be a spelling
// guard; asserting the state alone would let the state ship with no instruction.
func TestInstalledPendingCardStatesTheNextAction(t *testing.T) {
	attr := nodeAttribution{
		ManagerPresent:   true,
		Packs:            []comfy.Pack{installedUpscalePack()},
		InstalledPending: []string{upscaleDirName},
	}
	body := renderMissingNodesPanel(t, attr)

	i := strings.Index(body, installedPendingMarker)
	if i < 0 {
		t.Fatalf("precondition: no installed-pending state rendered:\n%s", body)
	}
	// Read the element the state is ON, not the whole document: a sentence elsewhere
	// in the panel (the shared "restart ComfyUI" after-install note, for one) would
	// otherwise satisfy this assertion while the state element said nothing. The
	// slice starts at the element's own opening tag, so an attribute emitted BEFORE
	// the marker is still inside it.
	start := strings.LastIndex(body[:i], "<p ")
	end := strings.Index(body[i:], "</p>")
	if start < 0 || end < 0 {
		t.Fatalf("the installed-pending state is not on a <p>:\n%s", body)
	}
	el := body[start : i+end]

	if !strings.Contains(el, "Installed") {
		t.Errorf("the state element does not say the pack is installed: %q", el)
	}
	if !strings.Contains(el, "restart") {
		t.Errorf("the state element does not name the next action: %q", el)
	}
	// It is an announced status, not silent decoration.
	if !strings.Contains(el, `role="status"`) {
		t.Errorf("the state element is not announced: %q", el)
	}
}

// TestInstalledPendingOverridesABlockedPack: a policy-blocked pack renders its
// refusal reason to explain an ABSENT Install button. Once the pack is on disk that
// explanation is simply wrong, so the installed state must win.
func TestInstalledPendingOverridesABlockedPack(t *testing.T) {
	blocked := blockedPack()
	attr := nodeAttribution{
		ManagerPresent:   true,
		Packs:            []comfy.Pack{blocked},
		InstalledPending: []string{repoLastSegment(blocked.Repository)},
	}

	// PRECONDITION: this fixture really is the blocked shape, and it really carries a
	// reason — otherwise the assertion below passes against a pack that never had one.
	if blocked.Installable || strings.TrimSpace(blocked.Reason) == "" {
		t.Fatalf("precondition: want a blocked pack carrying a reason, got %+v", blocked)
	}
	body := renderMissingNodesPanel(t, attr)

	if countMarkers(body, installedPendingMarker) != 1 {
		t.Errorf("a blocked pack that is already on disk is not acknowledged:\n%s", body)
	}
	if strings.Contains(body, blocked.Reason) {
		t.Errorf("the panel still explains why it cannot install a pack it already has:\n%s", body)
	}
}

// managerForInstalledDiff is a ComfyUI-Manager that attributes UltimateSDUpscale to
// ssitu's pack and answers the installed-diff as programmed. It is the harness for
// the fail-open test and for the end-to-end seam test.
func managerForInstalledDiff(diffs [][]string, diffErr error) *fakeManager {
	return &fakeManager{
		info: managerPresent(),
		mappings: json.RawMessage(`{
			"https://github.com/ssitu/ComfyUI_UltimateSDUpscale": [["UltimateSDUpscale"], {"title_aux": "ComfyUI_UltimateSDUpscale"}]
		}`),
		getlist: json.RawMessage(`{"node_packs": {
			"comfyui_ultimatesdupscale": {
				"cnr_latest": "1.7.2",
				"repository": "https://github.com/ssitu/ComfyUI_UltimateSDUpscale",
				"title": "ComfyUI_UltimateSDUpscale"
			}
		}}`),
		diffs:   diffs,
		diffErr: diffErr,
	}
}

// attributeWithManager runs the PRODUCTION attribution seam against mgr, with the
// two public rungs switched off so nothing can leave the machine. classes defaults
// to the one class the fixture Manager can place.
func attributeWithManager(t *testing.T, mgr managerClient, classes ...string) nodeAttribution {
	t.Helper()
	if len(classes) == 0 {
		classes = []string{"UltimateSDUpscale"}
	}
	srv := newLibraryTestServer(t, t.TempDir())
	srv.cfg.ResolveNodePacks = false
	srv.managerClientFn = func() managerClient { return mgr }
	return srv.attributeMissingNodes(context.Background(), classes)
}

// diffCountingManager counts ManagerInstalledDiff calls. It is a local wrapper
// rather than a new field on the shared fakeManager so this file stays
// self-contained — a package-level test fixture edited by two branches at once is
// how a clean `git merge` produces a tree that does not compile.
type diffCountingManager struct {
	*fakeManager
	mu    sync.Mutex
	calls int
}

func (d *diffCountingManager) ManagerInstalledDiff(ctx context.Context) ([]string, error) {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	return d.fakeManager.ManagerInstalledDiff(ctx)
}

func (d *diffCountingManager) diffCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// TestAttributionWithNoPacksMakesNoInstalledDiffCall pins the `len(out.Packs) > 0`
// half of the guard in realAttributeMissingNodes.
//
// The claim it defends is "a pass that attributed nothing makes no extra Manager
// call at all". That is a cost claim about a path a user waits on, and without this
// test nothing asserted it — the whole suite stayed green with the condition removed.
//
// The second row is the POSITIVE CONTROL: a pass that DOES attribute a pack must
// make at least one call through the same counter, so the zero above cannot be a
// counter wired to nothing.
func TestAttributionWithNoPacksMakesNoInstalledDiffCall(t *testing.T) {
	cases := []struct {
		name string
		// class decides whether the fixture Manager can place anything.
		class        string
		wantPacks    int
		wantAnyCalls bool
	}{
		{
			name:         "nothing attributed — no installed-diff call is made",
			class:        "SomeClassManagerCannotPlace",
			wantPacks:    0,
			wantAnyCalls: false,
		},
		{
			name:         "positive control: a pack was attributed — the diff IS read",
			class:        "UltimateSDUpscale",
			wantPacks:    1,
			wantAnyCalls: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mgr := &diffCountingManager{fakeManager: managerForInstalledDiff([][]string{{}}, nil)}
			attr := attributeWithManager(t, mgr, c.class)

			// PRECONDITIONS: Manager really answered (so the call was POSSIBLE and its
			// absence means the guard declined it, not that the rung never ran), and the
			// pass really did/didn't attribute a pack.
			if !attr.ManagerPresent {
				t.Fatal("precondition: the Manager rung did not run, so a zero call count means nothing")
			}
			if len(attr.Packs) != c.wantPacks {
				t.Fatalf("precondition: attributed %d packs, want %d", len(attr.Packs), c.wantPacks)
			}

			if got := mgr.diffCalls(); (got > 0) != c.wantAnyCalls {
				t.Errorf("ManagerInstalledDiff calls = %d, want %s", got,
					map[bool]string{true: "at least one", false: "exactly zero"}[c.wantAnyCalls])
			}
		})
	}
}

// TestRestartControlSurvivesAnInconsistentSnapshot covers the `|| anyPackInstalledPending`
// disjunct in missingNodesPanel.
//
// 🔴 It is a RENDER-LAYER invariant, deliberately not dependent on its caller: a card
// that tells the user to restart ComfyUI must never be the one state with no restart
// control. Production cannot currently build this snapshot — InstalledPending is
// populated only under `info.Present`, the same condition that sets ManagerPresent —
// so `deadcode` structurally cannot see the branch (the function IS reachable) and the
// rest of the suite left it green when deleted.
//
// It is NOT hypothetical: `Server.attributeFn` is a live test seam and
// seedNodeAttrRun builds nodeAttribution values by hand, so this pair is constructible
// today by anything that does not go through realAttributeMissingNodes.
func TestRestartControlSurvivesAnInconsistentSnapshot(t *testing.T) {
	attr := nodeAttribution{
		// The inconsistency under test: no Manager reported present, yet the installed
		// set says a pack is on disk waiting for a restart.
		ManagerPresent:   false,
		Packs:            []comfy.Pack{installedUpscalePack()},
		InstalledPending: []string{upscaleDirName},
	}
	body := renderMissingNodesPanel(t, attr)

	// PRECONDITION: the card really did take the installed branch. Without it, "one
	// restart control" could be satisfied by a panel that acknowledged nothing.
	if n := countMarkers(body, installedPendingMarker); n != 1 {
		t.Fatalf("precondition: installed-pending states = %d, want 1\n%s", n, body)
	}
	if n := countMarkers(body, restartActionMarker); n != 1 {
		t.Errorf("restart controls = %d, want exactly 1 — a card that says 'restart ComfyUI "+
			"to use it' must never render without one\n%s", n, body)
	}
}

// TestInstalledDiffFailureRendersExactlyTodaysPanel is the FAIL-OPEN guard, and the
// most important test in this file.
//
// 🔴 The installed-diff is a DISPLAY improvement over a third-party service this app
// does not control. It must never be able to WITHHOLD an install the user needs: a
// Manager that is absent, slow, wedged or answering garbage has to leave the panel
// exactly as it was before this signal existed.
//
// "Exactly" is asserted as byte equality against the same attribution with the field
// cleared — the pre-change rendering by construction — rather than as a list of
// substrings, which could not see a subtler drift.
//
// The second sub-case is the POSITIVE CONTROL: the identical harness with a WORKING
// diff must produce a DIFFERENT render. Without it, byte-equality would also be
// satisfied by a field nothing ever reads.
func TestInstalledDiffFailureRendersExactlyTodaysPanel(t *testing.T) {
	cases := []struct {
		name      string
		diffs     [][]string
		diffErr   error
		wantEqual bool
	}{
		{
			name:      "the diff errors — fail open to today's rendering",
			diffErr:   errors.New("manager installed: connection refused"),
			wantEqual: true,
		},
		{
			name:      "the diff is empty — nothing is known to be installed",
			diffs:     [][]string{{}},
			wantEqual: true,
		},
		{
			name:      "positive control: the diff answers — the rendering MUST change",
			diffs:     [][]string{{upscaleDirName}},
			wantEqual: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			attr := attributeWithManager(t, managerForInstalledDiff(c.diffs, c.diffErr))

			// PRECONDITION: attribution actually placed the class through Manager.
			// Without this the comparison below is between two identical EMPTY panels
			// and proves nothing at all.
			if len(attr.Packs) != 1 || attr.Packs[0].Title != "ComfyUI_UltimateSDUpscale" {
				t.Fatalf("precondition: Manager did not attribute the class, got %+v", attr.Packs)
			}
			if !attr.ManagerPresent {
				t.Fatal("precondition: the Manager rung did not run")
			}

			asToday := attr
			asToday.InstalledPending = nil
			today := renderMissingNodesPanel(t, asToday)
			got := renderMissingNodesPanel(t, attr)

			if (got == today) != c.wantEqual {
				t.Errorf("rendering equal to the pre-signal panel = %v, want %v\n--- got:\n%s\n--- pre-signal:\n%s",
					got == today, c.wantEqual, got, today)
			}
			// The fail-open cases must ALSO still offer the install — byte equality to a
			// panel that itself lost its button would be a green for the wrong reason.
			if c.wantEqual {
				if n := countMarkers(got, installActionMarker); n != 1 {
					t.Errorf("install affordances = %d, want 1 — the fail-open panel lost its button\n%s", n, got)
				}
				if len(attr.InstalledPending) != 0 {
					t.Errorf("InstalledPending = %v, want empty when the diff could not answer", attr.InstalledPending)
				}
			}
		})
	}
}

// TestInstalledStateSurvivesMultiClaimantRanking: sortPacks deliberately never drops
// a competing claimant, and an installed-aware render must not start hiding
// candidates. With the BEST match already installed, the alternative stays visible,
// stays labelled and stays installable — the user may still disagree with the
// ranking and install the other one.
func TestInstalledStateSurvivesMultiClaimantRanking(t *testing.T) {
	attr := contestedAttribution()
	attr.InstalledPending = []string{repoLastSegment(attr.Packs[0].Repository)}

	// PRECONDITIONS. The fixture has to actually express a contest, in the measured
	// order, or "the loser is still rendered" is a fact about a one-pack panel.
	ranked := rankPacks(attr.Packs)
	if len(ranked) != 2 {
		t.Fatalf("precondition: want 2 claimants, got %d", len(ranked))
	}
	if !ranked[0].Best || ranked[1].Best {
		t.Fatalf("precondition: want the first pack ranked best, got best=%v,%v", ranked[0].Best, ranked[1].Best)
	}
	if ranked[1].needed() {
		t.Fatal("precondition: the second claimant must be a pure alternative for this test")
	}

	body := renderMissingNodesPanel(t, attr)

	// The installed winner is acknowledged...
	if n := countMarkers(body, installedPendingMarker); n != 1 {
		t.Errorf("installed-pending states = %d, want 1 (the best match is on disk)\n%s", n, body)
	}
	// ...and exactly ONE install affordance remains: the alternative's. Not zero (the
	// alternative was hidden or disabled) and not two (the installed pack was
	// re-offered).
	if n := countMarkers(body, installActionMarker); n != 1 {
		t.Errorf("install affordances = %d, want exactly 1 (the alternative's)\n%s", n, body)
	}
	for _, want := range []string{
		"ComfyUI-PromptChain",
		"https://github.com/mobcat40/ComfyUI-PromptChain",
		"Also claims it",       // the alternative keeps its demotion label
		"Install ComfyUI-Prom", // and its own working button
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the losing claimant lost %q:\n%s", want, body)
		}
	}
	if n := countMarkers(body, restartActionMarker); n != 1 {
		t.Errorf("restart controls = %d, want exactly 1\n%s", n, body)
	}
}

// TestAResolvedClassLeavesNoNodepackPanel is the third state: the pack was installed
// AND ComfyUI restarted, so the class resolves and preflight no longer reports it.
// The panel must not be offering that pack at all — nothing to acknowledge, nothing
// to install, no stale restart prompt.
//
// ⚠ Labelled honestly: this is an INVARIANT GUARD, not regression coverage. The
// pre-change tree behaved this way too (the panel is gated on
// len(Preflight.MissingNodes) > 0). It exists so a future edit cannot make the
// installed-aware render leak a panel onto a run that has nothing missing.
func TestAResolvedClassLeavesNoNodepackPanel(t *testing.T) {
	resolved := runSnapshot{
		Started: true, WorkflowID: 9, UIFormat: true,
		Phase: runPhaseFailed, Message: "Preflight failed.",
		Preflight: &comfy.PreflightReport{
			MissingModels: []string{"4xNomosWebPhoto_RealPLKSR.pth"},
		},
		NodeAttr: nodeAttribution{ManagerPresent: true, InstalledPending: []string{upscaleDirName}},
	}
	body := renderString(t, runStatusFragment(resolved, 9, "csrf-tok", false, fullMaturityRange()))

	// PRECONDITION + POSITIVE CONTROL for the same probe: a run that DOES miss the
	// class renders the panel and exactly one install affordance, so the zeros below
	// cannot come from a probe wired to nothing.
	missing := resolved
	missing.Preflight = &comfy.PreflightReport{MissingNodes: []string{"UltimateSDUpscale"}}
	missing.NodeAttr = nodeAttribution{ManagerPresent: true, Packs: []comfy.Pack{installedUpscalePack()}}
	control := renderString(t, runStatusFragment(missing, 9, "csrf-tok", false, fullMaturityRange()))
	if n := countMarkers(control, installActionMarker); n != 1 {
		t.Fatalf("positive control: install affordances = %d, want 1 — the probe sees nothing\n%s", n, control)
	}
	if !strings.Contains(control, "Missing custom nodes") {
		t.Fatalf("positive control: the panel did not render at all\n%s", control)
	}

	if strings.Contains(body, "Missing custom nodes") {
		t.Errorf("a run with no missing node types still rendered the missing-nodes panel:\n%s", body)
	}
	for _, marker := range []string{installActionMarker, installedPendingMarker} {
		if n := countMarkers(body, marker); n != 0 {
			t.Errorf("%s count = %d, want 0 when nothing is missing:\n%s", marker, n, body)
		}
	}
}

// TestARunAcknowledgesAPackAlreadyInstalled is the SEAM test: attribution and render
// are each covered above in isolation, and this is the only case that builds the
// state where they meet — a real run, the production attributeMissingNodes, a real
// ComfyUI-Manager fake answering both the index and the installed diff, and the
// terminal fragment the user actually sees.
//
// 🔴 This is the test that is RED on the pre-change tree for the RIGHT reason (it
// compiles there — it names no new symbol): the acknowledgement is simply absent and
// the Install button is offered again.
func TestARunAcknowledgesAPackAlreadyInstalled(t *testing.T) {
	cases := []struct {
		name               string
		diffs              [][]string
		wantPendingState   int
		wantInstallActions int
	}{
		{
			// POSITIVE CONTROL, and the pre-fix behaviour: nothing on disk, so the run
			// offers the install. It proves the counter can observe a non-zero, which
			// is what makes the other row's zero mean anything.
			name:               "nothing installed — the run offers the install",
			diffs:              [][]string{{}},
			wantPendingState:   0,
			wantInstallActions: 1,
		},
		{
			name:               "already on disk — the run acknowledges it and asks for a restart",
			diffs:              [][]string{{upscaleDirName}},
			wantPendingState:   1,
			wantInstallActions: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newLibraryTestServer(t, t.TempDir())
			srv.cfg.ResolveNodePacks = false // nothing may leave the machine
			fake := nodepackFakeComfy(t)
			srv.comfyClientFn = func() comfyClient { return fake }
			mgr := managerForInstalledDiff(c.diffs, nil)
			srv.managerClientFn = func() managerClient { return mgr }

			id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
				`{"663":{"class_type":"UltimateSDUpscale","inputs":{}}}`)
			if r := post(t, srv, "/workflows/"+id+"/run", nil, true); r.Code != http.StatusOK {
				t.Fatalf("run start = %d", r.Code)
			}
			body := pollRunUntilDone(t, srv, id)

			// INTERMEDIATE STATE: the run really reached the missing-nodes panel with a
			// real attributed pack. Without this, both counts are zero for the boring
			// reason that no panel rendered.
			if !strings.Contains(body, "Missing custom nodes") {
				t.Fatalf("the run did not reach the missing-nodes panel:\n%s", body)
			}
			if !strings.Contains(body, "ComfyUI_UltimateSDUpscale") {
				t.Fatalf("attribution did not place the class through Manager:\n%s", body)
			}
			if fake.submitCalled {
				t.Fatal("a graph with a missing node type must never be submitted")
			}

			if n := countMarkers(body, installedPendingMarker); n != c.wantPendingState {
				t.Errorf("installed-pending states = %d, want %d\n%s", n, c.wantPendingState, body)
			}
			if n := countMarkers(body, installActionMarker); n != c.wantInstallActions {
				t.Errorf("install affordances = %d, want %d\n%s", n, c.wantInstallActions, body)
			}
			if n := countMarkers(body, restartActionMarker); n != 1 {
				t.Errorf("restart controls = %d, want exactly 1\n%s", n, body)
			}
		})
	}
}

// TestInstallEndpointStillAcceptsAnInstalledPack is the OTHER half of fail-open, and
// it guards a tempting "hardening" that would be a regression: the render change must
// not become a REFUSAL.
//
// The user may legitimately want to reinstall — Manager's own diff is a heuristic
// over a directory listing, and the install job already handles the case honestly
// (it settles on "already installed and waiting for a restart"). A server that
// refused would take away a recovery action on the strength of a third-party signal
// this panel elsewhere treats as advisory.
func TestInstallEndpointStillAcceptsAnInstalledPack(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	pack := installedUpscalePack()
	attr := nodeAttribution{
		ManagerPresent:   true,
		Packs:            []comfy.Pack{pack},
		InstalledPending: []string{upscaleDirName},
	}
	id := seedNodeAttrRun(t, srv, attr, []string{"UltimateSDUpscale"})

	rec := post(t, srv, "/workflows/"+id+"/nodepacks/install", url.Values{
		"pack_id":   {pack.ID},
		"pack_repo": {pack.Repository},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("install = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The first click must still return the confirmation, not a refusal note.
	if countMarkers(body, `data-nodepack-action="install-confirm"`) != 1 {
		t.Errorf("the endpoint stopped offering the confirm step for an installed pack:\n%s", body)
	}
}
