package web

import (
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// runPresetPanel renders the whole #run-params body: the tab strip, then the
// active tab's form.
//
// STRUCTURAL NOTE (the reason this shape was chosen): the strip lives INSIDE
// #run-params, which is already the mode picker's hx-target, and #run-params is a
// SIBLING of #run-status — never nested. The 1 s run poller swaps #run-status's
// innerHTML, so it structurally cannot clobber a half-typed prompt in a tab. The
// streaming invariant and the editable form are isolated for free.
//
// Returns nil when the graph exposes nothing editable (an api graph, or a
// multi-mode template before a mode is picked), so the panel is simply absent —
// exactly today's behaviour.
func runPresetPanel(wf *store.Workflow, csrf string, v presetTabView) g.Node {
	if !v.UIFormat || len(v.Rec.Fields) == 0 {
		return nil
	}
	id := strconv.FormatInt(wf.ID, 10)

	// 🔴 THE PANEL IS NO LONGER A DISCLOSURE. It used to be one <details> holding
	// the whole thing — summary "Parameters", tab strip, every field, AND the Run
	// button — closed unless the workflow had saved presets. So the default state of
	// the page you land on to run a workflow was: the prompt you want to type in is
	// hidden, and so is the button that runs it.
	//
	// Only the ADVANCED parameters collapse now (see runParamFieldSplit); the prompt
	// inputs, the tab strip and the actions are visible on load.
	return h.Div(
		h.Class("mt-4"),
		h.Div(h.Class("text-sm font-semibold text-slate-200"), g.Text("Parameters")),
		runPresetTabStrip(id, csrf, v),
		runPresetForm(id, csrf, v),
		runParamsScript(),
	)
}

// runParamFieldSplit partitions the rendered parameter controls into the ones that
// stay VISIBLE and the ones that fold into the "Advanced parameters" disclosure.
//
// The split keys on comfy.RunInputKind — a STRUCTURAL property the detector
// assigns — and never on the field's LABEL, which is the graph author's node title
// and says nothing reliable about what the input is. RunInputText is the multiline
// prompt kind (see internal/comfy: `RunInputText … // multiline prompt →
// <textarea>`); everything else is a seed, a step count, a cfg scale or a
// sampler/scheduler enum, i.e. the knobs you tune occasionally rather than the text
// you type every run.
//
// 🔴 RELATIVE ORDER IS PRESERVED WITHIN EACH HALF, AND EVERY FIELD LANDS IN EXACTLY
// ONE HALF. parseWidgetOverrides pairs the parallel wp_node / wp_widget / wp_value
// arrays BY POSITION, which survives this because each control emits exactly one
// entry in each of the three arrays — so partitioning permutes all three
// identically and index i still names the same widget. What would break it is a
// control that emits zero or several wp_value entries, or a field that appeared in
// both halves. Neither is possible here, and TestAdvancedSplitKeepsWidgetPairing
// pins it against the real parser rather than against this comment.
//
// A closed <details> does NOT disable its form controls — hidden fields still
// submit — so an advanced value the user never expanded is still sent, exactly as
// it was when the whole panel was collapsed.
func runParamFieldSplit(fields []comfy.PresetField) (prompt, advanced []int) {
	for i, f := range fields {
		if f.Input.Kind == comfy.RunInputText {
			prompt = append(prompt, i)
			continue
		}
		advanced = append(advanced, i)
	}
	return prompt, advanced
}

// runPresetTabStrip renders the tab row + Fork. Each tab is a POST (activate),
// because switching must PERSIST the outgoing tab's typed values in the same round
// trip — a GET switch would silently discard an unrun draft.
//
// It reuses the model page's .cm-version-tab* CSS: those rules are written against
// --civitai-* theme tokens, so both data-theme paths are covered by construction
// and no new Tailwind utility (which would be unstyled until output.css is
// regenerated) is introduced.
func runPresetTabStrip(wfID, csrf string, v presetTabView) g.Node {
	activeID := v.ActiveID()
	tabs := make([]g.Node, 0, len(v.Presets)+1)

	if len(v.Presets) == 0 {
		// The IMPLICIT tab: a workflow with nothing saved still reads as a tab, and a
		// page render never has to write to the database to make that true.
		tabs = append(tabs, runPresetTabButton("", "", "Preset 1", true, true))
	}
	for i, p := range v.Presets {
		pid := strconv.FormatInt(p.ID, 10)
		tabs = append(tabs, runPresetTabButton(
			"/workflows/"+wfID+"/run/presets/"+pid+"/activate", csrf,
			presetLabel(p, i), p.ID == activeID, false))
	}
	tabs = append(tabs, runPresetForkButton(wfID, csrf, v))

	return h.Div(
		g.Attr("role", "tablist"),
		g.Attr("aria-label", "Run presets"),
		h.Class("cm-version-tabs mt-3"),
		g.Group(tabs),
	)
}

// runPresetTabButton renders one tab. The implicit tab has no endpoint (there is
// nothing to activate), so it is a plain inert button rather than a link that
// would 404.
func runPresetTabButton(postURL, csrf, label string, active, inert bool) g.Node {
	attrs := []g.Node{
		h.Type("button"),
		g.Attr("role", "tab"),
	}
	if active {
		attrs = append(attrs, h.Class("cm-version-tab cm-version-tab-active"),
			g.Attr("aria-selected", "true"))
	} else {
		attrs = append(attrs, h.Class("cm-version-tab"), g.Attr("aria-selected", "false"))
	}
	if !inert {
		attrs = append(attrs,
			hx("post", postURL),
			hx("target", "#"+runParamsContainerID),
			hx("swap", "innerHTML"),
			hx("include", runPresetInclude),
			hx("disabled-elt", "this"),
			csrfInline(csrf),
		)
	}
	// The label is untrusted user text — g.Text escapes it.
	attrs = append(attrs, g.Text(label))
	return h.Button(attrs...)
}

// runPresetForkButton duplicates the active tab. At the cap it renders DISABLED
// with the reason rather than silently evicting the oldest preset: presets are
// user data.
func runPresetForkButton(wfID, csrf string, v presetTabView) g.Node {
	if v.AtCap {
		return h.Button(
			h.Type("button"), h.Disabled(),
			h.Class("cm-version-tab"),
			g.Attr("title", presetCapNotice()),
			g.Text("+ Fork"),
		)
	}
	attrs := []g.Node{
		h.Type("button"),
		h.Class("cm-version-tab"),
		hx("post", "/workflows/"+wfID+"/run/presets"),
		hx("target", "#"+runParamsContainerID),
		hx("swap", "innerHTML"),
		hx("include", runPresetInclude),
		hx("disabled-elt", "this"),
	}
	if id := v.ActiveID(); id > 0 {
		// Fork deep-copies THIS preset (params + graph_hash, so an inherited drift
		// stays visible). The outgoing values are persisted first, so the copy is of
		// what is on screen.
		attrs = append(attrs, hx("vals",
			`{"csrf_token":"`+csrf+`","`+presetFromField+`":"`+strconv.FormatInt(id, 10)+`"}`))
	} else {
		attrs = append(attrs, csrfInline(csrf))
	}
	attrs = append(attrs, g.Text("+ Fork"))
	return h.Button(attrs...)
}

// runPresetForm is the active tab's body: the drift report, the tab label, the
// reconciled parameter controls, and the actions.
func runPresetForm(wfID, csrf string, v presetTabView) g.Node {
	activeID := v.ActiveID()
	field := func(i int) g.Node {
		f := v.Rec.Fields[i]
		return runParamFieldValue(i, f.Input, f.Value)
	}

	body := []g.Node{
		h.ID(runPresetFormID),
		// The form itself still submits the RUN, exactly as before: same endpoint,
		// same target, same hx-include. Presets changed what fills the fields, not how
		// a run is started.
		hx("post", "/workflows/"+wfID+"/run-with-params"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "find button[type='submit']"),
		hx("include", runModesInclude),
		h.Class("mt-3 space-y-3"),
		h.Input(h.Type("hidden"), h.Name("csrf_token"), h.Value(csrf)),
		h.Input(h.ID(presetIDInputID), h.Type("hidden"), h.Name(presetIDField),
			h.Value(strconv.FormatInt(activeID, 10))),
	}
	if b := runPresetDriftBanner(v); b != nil {
		body = append(body, b)
	}
	if v.Notice != "" {
		body = append(body, alert("info", "", g.Text(v.Notice)))
	}
	if n := runPresetModeNote(v); n != nil {
		body = append(body, n)
	}
	body = append(body, runPresetNameField(activeID, v))
	// The parameter controls live in a responsive grid so each field can be sized to
	// the length of its value (see runParamKindClass / .cm-param-* in app.css).
	// ⚠ Grid auto-placement preserves DOM order, and DOM order is what pairs the
	// parallel wp_node/wp_widget/wp_value arrays in parseWidgetOverrides — so a
	// wrapper must never SPLIT a single field's three hidden/visible inputs apart.
	// Partitioning WHOLE fields between the two grids below is safe for exactly that
	// reason; see runParamFieldSplit.
	prompt, advanced := runParamFieldSplit(v.Rec.Fields)
	promptNodes := make([]g.Node, 0, len(prompt))
	for _, i := range prompt {
		promptNodes = append(promptNodes, field(i))
	}
	advancedNodes := make([]g.Node, 0, len(advanced))
	for _, i := range advanced {
		advancedNodes = append(advancedNodes, field(i))
	}
	// A graph exposing NO prompt input has no "basic vs advanced" distinction to
	// draw — folding 100% of its parameters into a disclosure would hide everything
	// and be strictly worse than what shipped. So the split only applies when there
	// is actually something to leave outside it.
	if len(promptNodes) == 0 {
		promptNodes, advancedNodes = advancedNodes, nil
	}
	body = append(body, h.Div(h.Class("cm-param-grid"), g.Group(promptNodes)))
	if len(advancedNodes) > 0 {
		attrs := []g.Node{h.Class("cm-meta-reveal")}
		// Same condition the whole panel used to open on: a user who has actually
		// saved presets is looking at values they set, so do not hide them.
		if len(v.Presets) > 0 {
			attrs = append(attrs, g.Attr("open"))
		}
		body = append(body, h.Details(append(attrs,
			h.Summary(h.Class("cm-meta-summary"),
				h.Span(h.Class("cm-meta-chevron"), g.Attr("aria-hidden", "true"), g.Text("›")),
				g.Text("Advanced parameters ("+strconv.Itoa(len(advancedNodes))+")")),
			h.Div(h.Class("cm-meta-body"),
				h.Div(h.Class("cm-param-grid"), g.Group(advancedNodes))),
		)...))
	}
	body = append(body,
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("These edits apply to THIS run only — the saved workflow is unchanged. "+
				"Save keeps them in this preset.")),
		runPresetActions(wfID, csrf, v),
	)
	return h.Form(body...)
}

// runPresetNameField is the tab label input.
func runPresetNameField(activeID int64, v presetTabView) g.Node {
	name := ""
	if v.Active != nil {
		name = v.Active.Name
	}
	placeholder := "Preset name"
	if activeID == 0 {
		placeholder = "Name this preset to save it"
	}
	return h.Div(
		dataAttr("civitai-ui", "text-input"),
		h.Label(dataFlag("civitai-ui-label"), h.For("cm-preset-name"), g.Text("Preset name")),
		h.Input(dataFlag("civitai-ui-control"), h.ID("cm-preset-name"),
			h.Type("text"), h.Name(presetNameField), h.Value(name),
			g.Attr("maxlength", strconv.Itoa(maxPresetNameLen)),
			g.Attr("placeholder", placeholder)),
	)
}

// runPresetActions renders the PRESET actions — Reset / Save / Adopt / Delete.
//
// The "Run with these parameters" submit that used to lead this row is gone. It was
// one of two buttons that started a run, and the other one (the big "Generate"
// above) both looked more primary AND ignored these fields, so the page had two
// primary actions with different behaviour. Running now happens in exactly one
// place — the run zone below — and this row is only about the preset.
//
// The form KEEPS its hx-post to /run-with-params: pressing Enter in a text field
// still runs, which is the behaviour a form-shaped panel promises, and that endpoint
// is exactly what a count of 1 does.
func runPresetActions(wfID, csrf string, v presetTabView) g.Node {
	activeID := v.ActiveID()
	actions := []g.Node{
		// A native form reset restores every control to its pre-filled value.
		civButton("subtle", "sm", []g.Node{h.Type("reset")}, g.Text("Reset")),
	}

	presetPost := func(path string, vals string, variant, label string) g.Node {
		attrs := []g.Node{
			h.Type("button"),
			hx("post", path),
			hx("target", "#"+runParamsContainerID),
			hx("swap", "innerHTML"),
			hx("include", runPresetInclude),
			hx("disabled-elt", "this"),
		}
		if vals != "" {
			attrs = append(attrs, hx("vals", vals))
		} else {
			attrs = append(attrs, csrfInline(csrf))
		}
		return civButton(variant, "sm", attrs, g.Text(label))
	}

	base := "/workflows/" + wfID + "/run/presets"
	if activeID == 0 {
		actions = append(actions, presetPost(base, "", "outline", "Save as preset"))
		return runPresetActionsRow(wfID, csrf, actions)
	}

	id := strconv.FormatInt(activeID, 10)
	actions = append(actions, presetPost(base+"/"+id+"/save", "", "outline", "Save"))
	if v.Drifted {
		// Offer, do not perform: re-stamping graph_hash is a separate, explicit act.
		actions = append(actions, presetPost(base+"/"+id+"/save",
			`{"csrf_token":"`+csrf+`","`+presetAdoptField+`":"1"}`,
			"outline", "Adopt current graph"))
	}
	actions = append(actions, presetPost(base+"/"+id+"/delete", "", "subtle", "Delete preset"))
	return runPresetActionsRow(wfID, csrf, actions)
}

// runPresetActionsRow lays out the preset actions on one wrapping row.
//
// The Queue ×N control used to hang below them, deliberately separated so a
// GPU-hours action could not be hit by muscle memory aimed at "Run". That separation
// is now STRUCTURAL rather than positional: this row contains no action that starts a
// run at all, and the count lives on the single run control below.
func runPresetActionsRow(_, _ string, actions []g.Node) g.Node {
	return h.Div(h.Class("flex flex-wrap items-center gap-2 pt-1"), g.Group(actions))
}

// runPresetDriftBanner names EVERY value the reconciler refused to apply and every
// parameter that newly appeared. It renders only when something was actually
// dropped or added — a banner that cries wolf on every open stops being read.
//
// Every name in it is untrusted author text and goes through g.Text.
func runPresetDriftBanner(v presetTabView) g.Node {
	rec := v.Rec
	if !rec.NeedsBanner() {
		if v.Drifted {
			// Nothing was lost, but the preset is still not certified against the
			// current graph. A quiet line, not the amber banner — and it MUST still
			// carry the role-swap caveat: that hazard produces exactly zero drops, so
			// this branch is the one where it is most likely to be doing damage.
			//
			// "Every saved value still matches" is VACUOUSLY true of a preset that
			// stores no values at all, and reads as reassurance about values that do
			// not exist. Nothing lost + nothing applied means the preset is empty (on
			// the drift path every stored entry either applies or is dropped and
			// named), so say that instead.
			line := "This preset was saved against an earlier version of this workflow. " +
				"Every saved value still matches. Use \"Adopt current graph\" to stop showing this."
			if rec.Applied() == 0 {
				line = "This preset was saved against an earlier version of this workflow and " +
					"holds no saved values — every field below is the workflow's own current value."
			}
			return h.Div(h.Class("space-y-1"),
				h.P(h.Class("text-xs text-slate-400"), g.Text(line)),
				presetRoleSwapCaveat(),
			)
		}
		return nil
	}

	// An undecodable stored entry is a defect of the SAVED PRESET, not of the graph
	// or of the mode selection, so it is listed (and titled) separately. Folding it
	// into the graph/mode drops is what let a single bad key render a banner blaming
	// the workflow mode on a preset whose hash proves the graph never moved.
	reset, gone, unreadable := splitDrops(rec.Dropped)

	lines := []g.Node{
		h.P(h.Class("text-sm"), g.Text(strconv.Itoa(rec.Applied())+" of "+
			strconv.Itoa(rec.Applied()+len(rec.Dropped))+" saved values were re-applied.")),
	}
	if len(reset) > 0 {
		lines = append(lines, h.P(h.Class("text-sm"),
			g.Text("Reset to the workflow's current values: "),
			g.Group(namedDrops(reset)),
		))
	}
	if len(gone) > 0 {
		// A Gone drop has NO field below it — the parameter is not part of this
		// workflow as the selected mode renders it, so there was nothing to reset it
		// to. It is still stored, and it comes back when the parameter does.
		lines = append(lines, h.P(h.Class("text-sm"),
			g.Text("Not shown below — this workflow has no matching parameter right now, "+
				"so these were neither applied nor reset: "),
			g.Group(namedDrops(gone)),
			g.Text(". They stay saved in this preset."),
		))
	}
	if len(unreadable) > 0 {
		lines = append(lines, h.P(h.Class("text-sm"),
			g.Text("Stored in a form this preset could not read, so they were skipped: "),
			g.Group(namedDrops(unreadable)),
			g.Text(". Re-enter those values and Save to repair the preset."),
		))
	}
	if len(rec.NewInputs) > 0 {
		lines = append(lines, h.P(h.Class("text-sm"),
			g.Text(pluralNew(len(rec.NewInputs))+": "),
			g.Group(namedStrings(rec.NewInputs)),
		))
	}
	if len(rec.DroppedModes) > 0 {
		lines = append(lines, h.P(h.Class("text-sm"),
			g.Text("Workflow mode not applied: "),
			g.Group(namedDrops(rec.DroppedModes)),
			g.Text(" — pick a mode above."),
		))
	}
	if !rec.Exact {
		// Only on the drift path: an EXACT hash proves the graph did not move, so
		// nothing could have swapped roles under the stored keys.
		lines = append(lines, presetRoleSwapCaveat())
	}
	lines = append(lines, h.P(h.Class("text-xs"),
		g.Text("Model substitutions and option fixes are stored by NAME, not position, "+
			"and were kept unchanged. \"Adopt current graph\" saves these values against "+
			"the workflow as it is now.")))

	return alert("warning", presetBannerTitle(rec), lines...)
}

// presetBannerTitle names the cause the banner is actually reporting.
//
// The EXACT path is the one that needs care. An equal hash proves the graph did not
// move, so on that path a drop can only come from two places: the mode picker
// selecting a pipeline the stored keys do not belong to, or a stored entry whose
// positional key was never decodable. The second has nothing to do with mode
// selection — titling it "no longer apply to the selected workflow mode" told the
// user to go looking at a picker that is not involved, and on a single-mode
// workflow, at a picker that does not exist.
func presetBannerTitle(rec comfy.PresetReconciliation) string {
	if !rec.Exact {
		return "This workflow's graph changed since this preset was saved."
	}
	reset, gone, unreadable := splitDrops(rec.Dropped)
	other := len(reset) + len(gone) + len(rec.DroppedModes)
	switch {
	case len(unreadable) > 0 && other == 0:
		return "Some of this preset's saved values could not be read and were skipped."
	case len(unreadable) > 0:
		return "Some of this preset's saved values could not be read; others no longer " +
			"apply to the selected workflow mode."
	default:
		return "Some saved values no longer apply to the selected workflow mode."
	}
}

// splitDrops separates the three genuinely different things a drop can be. They
// have different causes, different fixes, and therefore different sentences —
// folding any two together produces a line that is false for one of them.
//
//   - RESET: the key resolved to a live input but the value was refused
//     (retargeted / unverifiable), so ReconcileRunPreset emitted a Fields row holding
//     the GRAPH's current value. "Reset to the workflow's current values" is exactly
//     what happened.
//   - GONE: the key matches no live input at all, so there is NO Fields row for it
//     (comfy/run_presets.go's `gone` pass emits a drop and nothing else). Nothing was
//     reset, and there is no control on screen to look at. Filing it under "reset"
//     sent the user hunting for a field that is not rendered.
//   - UNREADABLE: the stored key itself could not be decoded.
func splitDrops(drops []comfy.PresetDrop) (reset, gone, unreadable []comfy.PresetDrop) {
	for _, d := range drops {
		switch d.Reason {
		case comfy.PresetDropMalformed:
			unreadable = append(unreadable, d)
		case comfy.PresetDropGone:
			gone = append(gone, d)
		default:
			reset = append(reset, d)
		}
	}
	return reset, gone, unreadable
}

// presetRoleSwapCaveat names the class of drift NO check in this codebase can
// catch, and it is rendered on EVERY drift-path render — including the one where
// nothing was dropped, which is precisely when the hazard is invisible.
//
// On the drift path values are matched by INPUT IDENTITY (kind, holder class,
// consumer input name). That tuple proves what an input IS, never what it MEANS. If
// the workflow is re-authored so two SAME-TYPE nodes trade ids or titles — the
// classic being a POSITIVE and a NEGATIVE CLIPTextEncode swapping — both tuples
// still match perfectly: zero drops, no banner content, and the positive prompt
// pre-filled into the negative field. Detecting it needs link-graph analysis
// DetectRunInputs does not do, so the honest mitigation is to SAY SO and point the
// user at the labels rather than to imply a guarantee that does not exist.
func presetRoleSwapCaveat() g.Node {
	return h.P(h.Class("text-xs text-slate-400"),
		g.Text("Saved values were matched by input identity (type and input name), not by "+
			"position. If this workflow was re-authored so that two same-type nodes swapped "+
			"roles — for example a POSITIVE and a NEGATIVE prompt node trading ids or titles "+
			"— a saved value can be pre-filled into the WRONG field with nothing flagged. "+
			"Check the label above each value before running."))
}

// namedDrops renders a comma-separated list of dropped names, each escaped, with
// the retargeted ones saying what the slot drives NOW — the visibility mitigation
// for a rewire no widget-key check can detect.
func namedDrops(drops []comfy.PresetDrop) []g.Node {
	out := make([]g.Node, 0, len(drops)*2)
	for i, d := range drops {
		if i > 0 {
			out = append(out, g.Text(", "))
		}
		out = append(out, h.Strong(g.Text(d.Name)))
		if d.Reason == comfy.PresetDropRetargeted && d.Detail != "" {
			out = append(out, g.Text(" (now "), g.Text(d.Detail), g.Text(")"))
		}
	}
	return out
}

func namedStrings(names []string) []g.Node {
	out := make([]g.Node, 0, len(names)*2)
	for i, n := range names {
		if i > 0 {
			out = append(out, g.Text(", "))
		}
		out = append(out, h.Strong(g.Text(n)))
	}
	return out
}

func pluralNew(n int) string {
	if n == 1 {
		return "1 new parameter appeared"
	}
	return strconv.Itoa(n) + " new parameters appeared"
}

// runPresetModeNote makes the mode ownership VISIBLE. The page-level picker is
// authoritative; a preset's stored mode only pre-selects it when the tab is
// opened. Saying so is the difference between a user trusting the panel and a user
// wondering why their saved mode "did not stick" after they changed the dropdown.
func runPresetModeNote(v presetTabView) g.Node {
	if len(v.Rec.Modes) == 0 {
		return nil
	}
	return h.P(h.Class("text-xs text-slate-500"),
		g.Text("Workflow mode comes from the picker above — this preset pre-selected it when "+
			"you opened the tab, and changing the picker overrides it for the next run."))
}

// runModesOOB re-renders #run-modes OUT OF BAND so the picker shows the mode the
// active preset was reconciled against. Without it the panel would be rendered for
// one graph and the run submitted against another (the run controls hx-include the
// picker, not the preset), which for a multi-mode template means converting an
// all-bypassed graph and aborting as "nothing to run".
//
// It is emitted ONLY when the active preset actually carries an applicable mode.
// Emitting it unconditionally would silently reset a pick the user just made.
func runModesOOB(wf *store.Workflow, csrf string, v presetTabView) g.Node {
	if len(v.ModesOOB) == 0 {
		return nil
	}
	return runModesPanelWith(wf, csrf, v.ModesOOB, true)
}
