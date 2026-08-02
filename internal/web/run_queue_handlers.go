package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// canQueueWorkflow is THE predicate behind every "this can be queued as a batch of
// N" decision. It is deliberately ONE function rather than the comparison written
// out at each site, because the sites are not interchangeable and cannot see each
// other:
//
//   - handleWorkflowRunQueue (below) — the server-side AUTHORITY. It 404s.
//   - generateSection (run_pages.go) — whether the ×1/×2/… count picker is rendered
//     at all, and which of the two runZoneHint sentences appears.
//   - handleWorkflowRunComfyStatus (run_handlers.go) — which endpoint the ONE
//     primary Generate button posts to. This one is resolved in a SEPARATE request
//     from the page, so it re-reads the workflow and used to re-derive the rule.
//
// 🔴 The render sites drifting APART is a silent wrong result, not a refusal. The
// picker and the button are rendered by two different handlers: if the picker says
// "queueable" and the button does not, the user picks ×8, the button posts to the
// single-run endpoint, and exactly ONE run happens with no error and no warning —
// the count is discarded while the picker stays fully interactive. The authority
// below still fails closed, so nothing unsafe happens; what is lost is the user's
// instruction. TestCanQueueAgreesAcrossPickerHintButtonAndHandler pins all four
// decisions to this one function.
//
// nil is not queueable: the comfy-status handler tolerates a workflow that has gone
// missing since the page rendered and degrades to the non-batch endpoint.
func canQueueWorkflow(wf *store.Workflow) bool {
	return wf != nil && wf.Format == store.WorkflowFormatUI
}

// handleWorkflowRunQueue starts a batch of N sequential runs of the posted
// parameters, each with a fresh random seed.
//
// It is `handleWorkflowRunWithParams` plus a count and a seed re-roll: the SAME
// prologue (ParseForm → verifyCSRF → gate), the SAME unchanged
// parseModeChoices/parseWidgetOverridesForModes allow-lists, the SAME preset
// attribution and the SAME one-run-at-a-time invariant. Nothing about how a single
// item runs is different, which is deliberate — realRun does not know batches exist.
//
// Two things it does that a single run does not:
//
//   - THE NO-SEED OFFER. If the mode-applied graph exposes no RunInputSeed, every
//     item would be byte-identical. That is offered, never performed: the first
//     click returns the offer, and only a second click carrying confirm_no_seed=1
//     proceeds — the same "offer, do not perform" shape as the substitute
//     confirmation. The offer is reached ONLY after the singleton check, and is
//     rendered as a SIBLING above the status fragment — see the `respond` closure.
//   - THE CLAMP. count is clamped server-side to [1, maxBatchCount] and the user is
//     TOLD when the request was reduced, rather than the request being rejected.
func (s *Server) handleWorkflowRunQueue(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	// Queueing is UI-format only, exactly like the preset surface it is rendered
	// inside: DetectRunInputs returns nothing for an api graph, so there is no seed
	// to re-roll and no parameters to batch.
	if !canQueueWorkflow(wf) {
		http.NotFound(w, r)
		return
	}

	// The mode picks ride along via hx-include and are applied FIRST: the panel was
	// rendered against the mode-applied graph, so its widget keys — and its seed —
	// only mean anything in that same graph.
	modes := parseModeChoices(r.Form, wf)
	opts := runOptions{
		ModeSelection:     modes,
		UIWidgetOverrides: parseWidgetOverridesForModes(r.Form, wf, modes),
	}
	if pid := formPresetID(r); pid > 0 {
		p := s.presetOfWorkflow(w, r, wf, pid)
		if p == nil {
			return
		}
		opts.PresetID, opts.PresetName = p.ID, p.Name
	}

	count, clamped := clampBatchCount(batchCountFromForm(r.Form))

	// EVERY response is a server-authored lead node ABOVE the status fragment, never
	// instead of it: #run-status is swapped innerHTML, so a response that omits the
	// fragment deletes the 1 s poller and the Stop control along with it.
	respond := func(lead g.Node) {
		s.render(w, http.StatusOK, g.Group([]g.Node{
			lead,
			runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.maturity()),
		}))
	}

	// 🔴 THE SINGLETON IS CHECKED FIRST — before the no-seed offer. A batch is
	// running, the user clicks Queue on a seedless workflow: the offer is not just
	// the wrong answer to a request that would be REFUSED anyway, it would replace
	// the live batch fragment with a static panel and the batch would look gone.
	if ref := s.batchInFlight(); ref.Running {
		respond(runNoticeLine(ref.notice(), false))
		return
	}

	// The seed keys come from the graph the run will ACTUALLY convert — the same
	// mode-applied copy the allow-list above was derived from. Reading wf.Graph
	// instead would, on a multi-mode template, randomise a BYPASSED pipeline's seed
	// and miss the selected one — a silently identical N-item batch, i.e. exactly
	// what the seed code exists to prevent. The no-seed offer does NOT cover this:
	// the raw graph still exposes "a" seed, so nothing fires.
	// TestQueueSeedKeysComeFromTheModeAppliedGraph is the mutation guard.
	seedKeys := comfy.SeedWidgetKeys(modeAppliedGraph(wf, modes))
	// 🔴 A COUNT OF ONE MUST NOT RE-ROLL THE SEED. This endpoint is now the ONE
	// primary run control's endpoint (see run_zone.go), so count==1 is the ordinary
	// "Generate once" click — and "Run once" has to run the seed the user can SEE in
	// the Parameters field. Passing seedKeys unconditionally (which is what this did
	// while it was only reachable from a "Queue ×N" block) would silently overwrite
	// that value on every single run, making the field a lie and making a deliberate
	// re-run of one specific seed impossible.
	//
	// batchSpec.SeedKeys documents exactly this: "only the Queue endpoint randomises,
	// because Run once must run the seed the user can see in the field."
	// TestSingleRunKeepsTheVisibleSeed is the mutation guard.
	batchSeedKeys := seedKeys
	if count <= 1 {
		batchSeedKeys = nil
	}
	if len(seedKeys) == 0 && count > 1 && r.FormValue(batchConfirmNoSeedField) != "1" {
		// A SIBLING above the status fragment, the way runNoticeLine already is. The
		// check above has already ruled out a running batch; this closes the window
		// between that check and here, and keeps a finished run's terminal panel
		// (its images, its "Run again") from being wiped by a question.
		respond(noSeedBatchOffer(wf.ID, s.csrf, count))
		return
	}

	started, refusal := s.startBatch(wf, opts, batchSpec{
		Count:    count,
		Message:  "Starting run…",
		SeedKeys: batchSeedKeys,
	})
	notice := refusal.notice()
	if started {
		notice = queueStartNotice(count, clamped, len(seedKeys) == 0)
	}
	respond(runNoticeLine(notice, started))
}

// queueStartNotice is the server-authored line above a freshly started batch. It
// exists to make two consequences VISIBLE rather than surprising:
//
//   - the clamp, when the request asked for more than the cap;
//   - the eviction pressure, because every successful item captures its images and
//     then runs enforceOutputsCap, which deletes the OLDEST generations to stay
//     under the disk cap. A batch multiplies that pressure N-fold, and finding your
//     older outputs gone is exactly the kind of thing that must be said up front.
//
// A count of ONE says NOTHING. It used to say "Started a single run — nothing was
// queued", which was the right answer while this endpoint was only reachable from a
// block labelled "Queue ×N": a click there that did one run needed explaining. It is
// now the ONE run control's endpoint and `1` is a selectable, visible option on it
// (run_zone.go), so that line would scold the user for doing exactly what the
// control they used says. An empty notice also keeps the single-run response
// byte-identical to the plain /run-with-params response it replaced.
//
// Nothing from the request is reflected into it.
func queueStartNotice(count int, clamped, noSeed bool) string {
	if count <= 1 {
		return ""
	}
	msg := "Queued " + strconv.Itoa(count) + " runs. They run one at a time; Stop cancels the rest."
	if clamped {
		msg = "Queued " + strconv.Itoa(maxBatchCount) + " runs — the maximum per batch. " +
			"They run one at a time; Stop cancels the rest."
	}
	if noSeed {
		msg += " This workflow exposes no editable seed, so every run uses identical parameters."
	}
	return msg + " Each completed run is captured to your outputs, which can evict your " +
		"OLDEST captured generations if that pushes the outputs folder over its disk cap."
}

// runNoticeLine renders a server-authored line above the run status fragment: the
// refusal when a start was discarded, or the batch confirmation when it was not.
//
// It renders NOTHING for an empty notice, so every single-run response is
// byte-identical to before. It is a sibling of the status fragment, never a wrapper:
// #run-status stays the stable container the poller swaps.
func runNoticeLine(notice string, ok bool) g.Node {
	if notice == "" {
		return g.Group(nil)
	}
	cls := "text-sm text-amber-400 mb-2"
	if ok {
		cls = "text-sm text-slate-400 mb-2"
	}
	return h.P(h.Class(cls), g.Text(notice))
}

// noSeedBatchOffer is the OFFER shown when a batch would produce N identical runs.
//
// A hard block would be wrong — a workflow can carry randomness we cannot see (a
// custom sampler outside the curated layouts; or a seed inside a subgraph whose
// interior DetectRunInputs refuses to expose, i.e. a definition instantiated more
// than once, or a bypassed/muted instance — see comfy.subgraphRunTargets. Ordinary
// single-instance subgraph interiors ARE scanned) — but a silent 8× identical batch
// is worse. So: offer, do not perform. Only the second click, carrying
// confirm_no_seed=1, starts anything.
//
// ⚠ Deliberately NOT claimed here: that ComfyUI would return cached outputs for an
// identical re-submitted prompt. That was never verified. What IS certain from our
// own code is that the submitted graph would be byte-identical.
func noSeedBatchOffer(wfID int64, csrf string, count int) g.Node {
	id := strconv.FormatInt(wfID, 10)
	n := strconv.Itoa(count)
	confirm := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+id+"/run/queue"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("include", runPresetInclude),
		hx("disabled-elt", "this"),
		hx("vals", `{"csrf_token":"`+csrf+`","`+batchCountField+`":"`+n+`","`+
			batchConfirmNoSeedField+`":"1"}`),
	}, g.Text("Queue "+n+" identical runs anyway"))
	once := civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+id+"/run/queue"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("include", runPresetInclude),
		hx("disabled-elt", "this"),
		hx("vals", `{"csrf_token":"`+csrf+`","`+batchCountField+`":"1"}`),
	}, g.Text("Run once instead"))

	return alert("warning", "This workflow exposes no editable seed",
		h.P(h.Class("text-sm"),
			g.Text("All "+n+" runs would use identical parameters, so they would most "+
				"likely produce the same image "+n+" times.")),
		h.Div(h.Class("pt-1 flex flex-wrap items-center gap-2"), confirm, once),
	)
}

// (runQueueControl lived here. It was the "Queue ×N" block — its own heading, its
// own paragraph, four quick-pick buttons, a number input and a "Queue" button,
// rendered INSIDE the preset form. It is gone: the count is now a segment on the ONE
// primary run control, and every reason the block existed is documented at the top
// of run_zone.go. Two of its properties were kept rather than lost — the count still
// rides along on the run request, and its Custom field still defaults to a real
// batch so it cannot silently mean "one".)
