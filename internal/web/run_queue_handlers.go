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
	if wf.Format != store.WorkflowFormatUI {
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

	count, clamped := clampBatchCount(r.FormValue(batchCountField))

	// EVERY response is a server-authored lead node ABOVE the status fragment, never
	// instead of it: #run-status is swapped innerHTML, so a response that omits the
	// fragment deletes the 1 s poller and the Stop control along with it.
	respond := func(lead g.Node) {
		s.render(w, http.StatusOK, g.Group([]g.Node{
			lead,
			runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()),
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
		SeedKeys: seedKeys,
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
// custom sampler outside the curated layouts, or a seed inside a subgraph, which
// DetectRunInputs deliberately does not scan) — but a silent 8× identical batch is
// worse. So: offer, do not perform. Only the second click, carrying
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

// runQueueControl is the "Queue ×N" row inside the preset form: quick picks plus a
// number input, all posting to the queue endpoint.
//
// It targets #run-status and swaps innerHTML — identical to the existing Run button
// — so the tab strip in #run-params is never touched by it, and the 1 s poller
// still owns #run-status alone. hx-disabled-elt="this" is what stops a double-click
// from racing the singleton check.
func runQueueControl(wfID string, csrf string) g.Node {
	picks := make([]g.Node, 0, len(batchQuickPicks))
	for _, n := range batchQuickPicks {
		s := strconv.Itoa(n)
		picks = append(picks, civButton("outline", "sm", []g.Node{
			h.Type("button"),
			g.Attr("title", "Queue "+s+" runs, each with a new random seed"),
			hx("post", "/workflows/"+wfID+"/run/queue"),
			hx("target", "#"+runStatusContainerID),
			hx("swap", "innerHTML"),
			hx("include", runPresetInclude),
			hx("disabled-elt", "this"),
			hx("vals", `{"csrf_token":"`+csrf+`","`+batchCountField+`":"`+s+`"}`),
		}, g.Text("×"+s)))
	}

	// The number input is NOT inside the preset form's field set by accident: it is
	// named `count`, which no other run control submits, so it rides along on every
	// preset request harmlessly and is read only here.
	custom := h.Div(dataAttr("civitai-ui", "text-input"), h.Class("cm-param-int"),
		h.Label(dataFlag("civitai-ui-label"), h.For("cm-batch-count"), g.Text("Custom count")),
		h.Input(dataFlag("civitai-ui-control"), h.ID("cm-batch-count"),
			h.Type("number"), h.Name(batchCountField),
			g.Attr("min", "1"), g.Attr("max", strconv.Itoa(maxBatchCount)),
			g.Attr("step", "1"), h.Value("")),
	)
	queue := civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+wfID+"/run/queue"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("include", runPresetInclude),
		hx("disabled-elt", "this"),
		csrfInline(csrf),
	}, g.Text("Queue"))

	return h.Div(h.Class("pt-2 space-y-1"),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Queue ×N")),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Runs these parameters N times, one at a time, each with a new random "+
				"seed. Maximum "+strconv.Itoa(maxBatchCount)+" per batch. Stop cancels the "+
				"remaining runs; anything already captured is kept.")),
		h.Div(h.Class("flex flex-wrap items-end gap-2"),
			g.Group(picks),
			h.Div(h.Class("w-28"), custom),
			queue,
		),
	)
}
