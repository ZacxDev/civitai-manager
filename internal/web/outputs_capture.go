package web

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// captureBudget bounds the whole best-effort capture (N image /view fetches +
// disk writes + one insert). It uses its OWN context derived from the server base
// context — never the run's cancelled ctx — so a slow /view cannot wedge the
// process but a finished run does not abort a legitimate copy.
const captureBudget = 60 * time.Second

// runParamsSnapshot is the JSON persisted in generations.params: the applied run
// overrides (so a re-run reproduces the parameterized run) plus resource/base
// snapshots. The map-keyed override sets are flattened to explicit lists so they
// round-trip cleanly through JSON and back into runOptions (runOptionsFromParams).
type runParamsSnapshot struct {
	Substitute  map[string]string `json:"substitute,omitempty"`
	OptionFixes []optionFixEntry  `json:"option_fixes,omitempty"`
	// WidgetOverrides is the LEGACY api-graph-keyed override list (node + input name),
	// kept so generations recorded before the Parameters panel moved to UI-graph
	// widget indices still replay.
	WidgetOverrides   []widgetOverrideEntry   `json:"widget_overrides,omitempty"`
	UIWidgetOverrides []uiWidgetOverrideEntry `json:"ui_widget_overrides,omitempty"`
	Resources         []string                `json:"resources,omitempty"`
	BaseModel         string                  `json:"base_model,omitempty"`
	Format            string                  `json:"format,omitempty"`
	// ModeSelection is the multi-mode template pick that was applied, keyed
	// comfy.ModeSelector.Key → comfy.ModeGroup.Key (exactly runOptions.ModeSelection).
	// Before it existed, "Re-run this" could not restore which pipeline a captured
	// generation ran — the documented deferred gap. A ModeGroup.Key is
	// "<selector node id>:<group index>", i.e. POSITIONAL in the group array, so it
	// is gated by the same graph-hash check as the positional widget overrides.
	ModeSelection map[string]string `json:"mode_selection,omitempty"`

	// PromptsCaptured marks that this run went through capturePrompts AT ALL. It is
	// the ONLY way to tell a row written before prompt capture existed ("we do not
	// know what the prompt was") from one whose graph simply exposes no prompt input
	// ("we looked, there is none"). Rendering those two the same way would either
	// invent a fact or hide one, so the detail page branches on this flag.
	//
	// params is a JSON blob, so no migration was needed — but that is exactly why the
	// flag has to be explicit: an OLD row decodes to the zero value, silently, with
	// no error to notice.
	PromptsCaptured bool `json:"prompts_captured,omitempty"`
	// Prompts are the EFFECTIVE prompt texts this run submitted, in graph order, each
	// still labeled with which prompt it was (positive/negative, text_g/text_l).
	// Empty with PromptsCaptured set means the graph exposes no prompt inputs.
	Prompts []promptEntry `json:"prompts,omitempty"`

	// ResourcesUsed are the model files the ACTIVE nodes of the submitted graph load
	// — the provenance claim. It is deliberately SEPARATE from Resources above:
	//
	//   Resources     = wf.Resources = comfy.ExtractResourcesAny over the STORED graph
	//                   with NO mode check, so a multi-mode template's list contains
	//                   every pipeline's models including the bypassed ones. That is
	//                   the right answer to "what can this workflow need" and the
	//                   WRONG answer to "what made this image".
	//   ResourcesUsed = comfy.ExtractActiveResources over the submitted graph.
	//
	// Empty means "not captured / nothing derivable", and the detail page then falls
	// back to Resources under a heading that only claims REFERENCE, never USE. A
	// pre-change row lands there naturally, with no flag needed.
	ResourcesUsed []string `json:"resources_used,omitempty"`
}

// promptEntry is ONE captured prompt input: the text that actually ran, plus enough
// identity to keep a positive/negative (or text_g/text_l) pair distinguishable.
//
// Label is comfy.RunInput.Label — "Prompt (POSITIVE)", "Prompt (G)" — derived from
// the graph author's own node title, so it is UNTRUSTED text like everything else in
// this blob and every caller renders it through g.Text. ClassType/InputName are the
// structural fallback for a graph whose nodes carry no titles: two CLIPTextEncode
// nodes with no titles both label as bare "Prompt", and the node id is then the only
// thing telling them apart, which is why it is recorded too.
type promptEntry struct {
	Label     string `json:"label,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	ClassType string `json:"class_type,omitempty"`
	InputName string `json:"input_name,omitempty"`
	Text      string `json:"text"`
}

// maxCapturedPromptBytes / maxCapturedPrompts bound what one capture writes into the
// params blob. Every capture writes a row that is kept until the user deletes it, and
// a prompt is arbitrary user/graph text (a wildcard-expanded prompt can be large), so
// the blob needs a ceiling. A clipped prompt is marked in the stored text itself
// (promptTruncatedMarker) so the detail page can never present a partial prompt as
// the whole one.
const (
	maxCapturedPromptBytes = 4096
	maxCapturedPrompts     = 16
	promptTruncatedMarker  = "\n… (prompt truncated at capture time)"
)

type optionFixEntry struct {
	InputName string `json:"input_name"`
	OldValue  string `json:"old_value"`
	NewValue  string `json:"new_value"`
}

type widgetOverrideEntry struct {
	NodeID    string `json:"node_id"`
	InputName string `json:"input_name"`
	Value     string `json:"value"`
}

// uiWidgetOverrideEntry is one Parameters-panel edit, keyed the way the panel emits
// it: the UI-graph node that HOLDS the value plus that value's widgets_values index.
//
// Widget is held as RawMessage, NOT a bare int: parseRunParams ignores the unmarshal
// error, so a wrong-typed `"widget":"x"` in a hand-edited/corrupt blob would otherwise
// leave the field at its zero value and silently retarget the edit at widget 0 of that
// node — and a strictly-typed field would make ONE bad entry discard the entire
// snapshot. widgetIndex() parses each entry on its own and reports whether it was a
// real integer.
type uiWidgetOverrideEntry struct {
	NodeID string          `json:"node_id"`
	Widget json.RawMessage `json:"widget"`
	Value  string          `json:"value"`

	// Kind/ClassType/InputName are the DRIFT TUPLE a run PRESET snapshots from the
	// live comfy.RunInput at save time, so a preset opened against a changed graph
	// can be reconciled STRUCTURALLY and not only by hash equality
	// (comfy.ReconcileRunPreset). All omitempty: a generation capture does not write
	// them, and an entry with no tuple is treated as "unverifiable" — dropped and
	// named on drift, never silently trusted.
	//
	// Label is DISPLAY ONLY and deliberately not part of the match key: it is
	// derived from untrusted author node titles and can change harmlessly.
	Kind      string `json:"kind,omitempty"`
	ClassType string `json:"class_type,omitempty"`
	InputName string `json:"input_name,omitempty"`
	Label     string `json:"label,omitempty"`
}

// widgetIndex parses the entry's widget slot index (a JSON number, or a quoted
// integer); ok=false for a missing or non-integer value, so the entry is dropped
// rather than aimed at slot 0.
func (e uiWidgetOverrideEntry) widgetIndex() (int, bool) {
	s := strings.Trim(strings.TrimSpace(string(e.Widget)), `"`)
	i, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return i, true
}

// widgetDisplay renders the entry's slot for the generation detail page; a malformed
// value is shown as "?" rather than as a plausible-looking 0.
func (e uiWidgetOverrideEntry) widgetDisplay() string {
	if i, ok := e.widgetIndex(); ok {
		return strconv.Itoa(i)
	}
	return "?"
}

// buildRunParamsSnapshot captures the applied runOptions + workflow resource/base
// snapshots into a serializable form.
func buildRunParamsSnapshot(wf *store.Workflow, opts runOptions) runParamsSnapshot {
	snap := runParamsSnapshot{}
	if len(opts.Substitute) > 0 {
		snap.Substitute = make(map[string]string, len(opts.Substitute))
		for k, v := range opts.Substitute {
			snap.Substitute[k] = v
		}
	}
	for key, newVal := range opts.OptionFixes {
		snap.OptionFixes = append(snap.OptionFixes, optionFixEntry{
			InputName: key.InputName, OldValue: key.OldValue, NewValue: newVal,
		})
	}
	for key, val := range opts.WidgetOverrides {
		snap.WidgetOverrides = append(snap.WidgetOverrides, widgetOverrideEntry{
			NodeID: key.NodeID, InputName: key.InputName, Value: val,
		})
	}
	for key, val := range opts.UIWidgetOverrides {
		snap.UIWidgetOverrides = append(snap.UIWidgetOverrides, uiWidgetOverrideEntry{
			NodeID: key.NodeID, Widget: json.RawMessage(strconv.Itoa(key.Widget)), Value: val,
		})
	}
	// Map iteration is unordered — sort BOTH override lists so the persisted params
	// JSON is stable for a given run (comparable across re-runs, diffable in the
	// gallery). Option fixes are sorted for the same reason.
	sort.Slice(snap.UIWidgetOverrides, func(i, j int) bool {
		a, b := snap.UIWidgetOverrides[i], snap.UIWidgetOverrides[j]
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		ai, _ := a.widgetIndex()
		bi, _ := b.widgetIndex()
		return ai < bi
	})
	sort.Slice(snap.WidgetOverrides, func(i, j int) bool {
		a, b := snap.WidgetOverrides[i], snap.WidgetOverrides[j]
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		return a.InputName < b.InputName
	})
	sort.Slice(snap.OptionFixes, func(i, j int) bool {
		a, b := snap.OptionFixes[i], snap.OptionFixes[j]
		if a.InputName != b.InputName {
			return a.InputName < b.InputName
		}
		return a.OldValue < b.OldValue
	})
	if len(opts.ModeSelection) > 0 {
		snap.ModeSelection = make(map[string]string, len(opts.ModeSelection))
		for k, v := range opts.ModeSelection {
			snap.ModeSelection[k] = v
		}
	}
	if wf != nil {
		snap.Resources = wf.Resources
		snap.BaseModel = wf.BaseModel
		snap.Format = wf.Format
		// Neither the effective prompt NOR the effective resource list is derivable
		// later: wf.Graph is replaced in place by a rescan and workflow_id goes NULL on
		// delete, so reading either at render time would describe TODAY's workflow for a
		// PAST run. Capture both here, once, from the ONE graph this run submitted.
		submitted := runSubmittedGraph(wf, opts)
		snap.PromptsCaptured = true
		snap.Prompts = capturePrompts(submitted)
		snap.ResourcesUsed = captureActiveResources(wf, submitted)
	}
	return snap
}

// runSubmittedGraph rebuilds the graph this run actually converted and submitted: the
// stored graph with the chosen mode un-bypassed and the Parameters-panel edits
// applied, in the SAME ORDER realRun applies them (modes, then UI widget overrides —
// an override key is positional in the mode-applied graph).
//
// 🔴 Every provenance fact must be derived from THIS, never from wf.Graph:
//
//   - MODE: a multi-mode template ships every pipeline in one graph with all but one
//     bypassed. Read raw, a template records whichever pipeline happens to be
//     un-bypassed in storage — the WRONG prompt and the WRONG models, silently, for
//     every run of every other mode. (Same trap
//     TestQueueSeedKeysComeFromTheModeAppliedGraph pins for seeds.)
//   - OVERRIDES: the Parameters panel's edit IS what the user ran. The graph's stored
//     value is the pre-edit text.
//
// It exists as ONE helper so the prompt half and the resource half cannot drift: the
// resource half was originally read from the stored wf.Resources list and therefore
// named bypassed pipelines' checkpoints under a heading asserting they ran.
func runSubmittedGraph(wf *store.Workflow, opts runOptions) json.RawMessage {
	if wf == nil {
		return nil
	}
	graph := modeAppliedGraph(wf, opts.ModeSelection)
	if wf.Format == store.WorkflowFormatUI && len(opts.UIWidgetOverrides) > 0 {
		graph = comfy.ApplyUIWidgetOverrides(graph, opts.UIWidgetOverrides)
	}
	return graph
}

// captureActiveResources records the model files the run's graph actually loads —
// ACTIVE nodes only (comfy.ExtractActiveResources), so a bypassed pipeline's
// checkpoint is never presented as something this image was made with.
//
// It reuses the shared extractor rather than a second implementation, so what counts
// as a resource is defined in exactly one place (internal/comfy/format.go).
//
// Best-effort like the rest of capture: an error or an unknown format yields nil, and
// the detail page then falls back to the workflow's own reference list under an
// honest heading rather than claiming anything.
func captureActiveResources(wf *store.Workflow, submitted json.RawMessage) []string {
	if wf == nil || len(submitted) == 0 {
		return nil
	}
	res, err := comfy.ExtractActiveResources(wf.Format, submitted)
	if err != nil {
		return nil
	}
	return res
}

// capturePrompts reads the effective prompt text out of the graph this run actually
// submitted (runSubmittedGraph — read its doc for why nothing here may touch
// wf.Graph).
//
// Only UI-format workflows yield anything: an api-format graph has no widgets_values,
// so DetectRunInputs returns nothing and the row honestly records "no prompt inputs
// detected" rather than a guess. Best-effort throughout — a capture must never fail a
// run — so a graph that cannot be parsed simply yields no entries.
func capturePrompts(submitted json.RawMessage) []promptEntry {
	var out []promptEntry
	for _, ri := range comfy.DetectRunInputs(submitted, nil) {
		if ri.Kind != comfy.RunInputText {
			continue // seeds/steps/cfg are already covered by the override list
		}
		if len(out) >= maxCapturedPrompts {
			break
		}
		out = append(out, promptEntry{
			Label:     ri.Label,
			NodeID:    ri.NodeID,
			ClassType: ri.ClassType,
			InputName: ri.InputName,
			Text:      clipPromptText(ri.Current),
		})
	}
	return out
}

// clipPromptText bounds ONE captured prompt, marking the clip inside the stored text
// so a truncated prompt can never be displayed as if it were complete.
//
// The cut walks BACK to a rune boundary: a byte-exact slice can split a multi-byte
// rune, and the resulting invalid UTF-8 would be re-encoded by encoding/json as a
// U+FFFD replacement character — a silent corruption at the tail of the very field
// this whole feature exists to record faithfully.
func clipPromptText(s string) string {
	if len(s) <= maxCapturedPromptBytes {
		return s
	}
	cut := maxCapturedPromptBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + promptTruncatedMarker
}

// marshalRunParams serializes a snapshot to JSON, returning "" on failure (the
// generation is still stored — params is nullable).
func marshalRunParams(snap runParamsSnapshot) string {
	b, err := json.Marshal(snap)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseRunParams decodes a params JSON blob; a blank/invalid blob yields a zero
// snapshot (never an error — the detail page degrades to "no params").
func parseRunParams(params string) runParamsSnapshot {
	var snap runParamsSnapshot
	if strings.TrimSpace(params) == "" {
		return snap
	}
	_ = json.Unmarshal([]byte(params), &snap)
	return snap
}

// runOptionsFromParams reconstructs the per-run overrides from a stored params
// snapshot, for "Re-run this". OptionFixes are re-validated against live
// object_info inside realRun (ValidateOptionFixes), so injecting them here is
// safe; WidgetOverrides/Substitutions are applied the same ephemeral way as the
// original run.
//
// UIWidgetOverrides are POSITIONAL — (node id, widgets_values index). A workflow row's
// graph is replaced in place by a rescan (store.UpsertWorkflowByPath), so a stored key
// can point at a completely different widget on a later graph and
// ApplyUIWidgetOverrides would type-coerce a value straight into it (a captured seed
// overwriting a prompt). The legacy name-keyed WidgetOverrides degrade safely — a
// missing input name is a no-op — so only the positional set needs guarding.
//
// The guard is the workflow's content hash: the keys are applied ONLY when the
// generation's recorded graph hash still matches the workflow's current one, so the
// index means exactly what it meant when captured. A blank hash on either side is
// treated as a mismatch (it cannot be proven equal). staleReason is non-empty when
// positional overrides were withheld; the caller must surface it rather than re-run
// with different parameters than the generation records.
//
// ModeSelection rides the SAME gate: a ModeGroup.Key is "<selector node id>:<group
// index>" — positional in the group array — so restoring it across a graph change
// could enable a different pipeline than the one that produced the images.
func runOptionsFromParams(params, genGraphHash, curGraphHash string) (opts runOptions, staleReason string) {
	snap := parseRunParams(params)
	if len(snap.Substitute) > 0 {
		opts.Substitute = make(map[string]string, len(snap.Substitute))
		for k, v := range snap.Substitute {
			opts.Substitute[k] = v
		}
	}
	if len(snap.OptionFixes) > 0 {
		opts.OptionFixes = make(map[comfy.OptionFixKey]string, len(snap.OptionFixes))
		for _, e := range snap.OptionFixes {
			opts.OptionFixes[comfy.OptionFixKey{InputName: e.InputName, OldValue: e.OldValue}] = e.NewValue
		}
	}
	if len(snap.WidgetOverrides) > 0 {
		opts.WidgetOverrides = make(map[comfy.WidgetOverrideKey]string, len(snap.WidgetOverrides))
		for _, e := range snap.WidgetOverrides {
			opts.WidgetOverrides[comfy.WidgetOverrideKey{NodeID: e.NodeID, InputName: e.InputName}] = e.Value
		}
	}
	// Both the UI widget overrides AND the mode selection are POSITIONAL, so they
	// share ONE hash gate: a ModeGroup.Key is "<selector node id>:<group index>",
	// an index into the group array, and crossing a graph change with it would
	// enable a different pipeline than the generation records.
	if len(snap.UIWidgetOverrides) > 0 || len(snap.ModeSelection) > 0 {
		if genGraphHash == "" || curGraphHash == "" || genGraphHash != curGraphHash {
			return opts, "this workflow's graph has changed since this generation was " +
				"recorded, so its parameter edits can no longer be re-applied safely " +
				"(they are stored by widget position). Open the workflow and run it " +
				"from the Parameters panel instead."
		}
		if len(snap.ModeSelection) > 0 {
			opts.ModeSelection = make(map[string]string, len(snap.ModeSelection))
			for k, v := range snap.ModeSelection {
				opts.ModeSelection[k] = v
			}
		}
	}
	if len(snap.UIWidgetOverrides) > 0 {
		opts.UIWidgetOverrides = make(map[comfy.UIWidgetKey]string, len(snap.UIWidgetOverrides))
		for _, e := range snap.UIWidgetOverrides {
			widx, ok := e.widgetIndex()
			if !ok || e.NodeID == "" {
				continue // malformed entry — never default it onto slot 0
			}
			opts.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: widx}] = e.Value
		}
		if len(opts.UIWidgetOverrides) == 0 {
			opts.UIWidgetOverrides = nil
		}
	}
	return opts, ""
}

// captureGeneration is the best-effort, off-run-mutex, success-path-only capture:
// it copies each of a completed run's output images out of ComfyUI (via
// client.View — an HTTP fetch, so a remote ComfyUI captures fine) into the
// app-owned outputs dir, then records a generation row snapshotting the applied
// run params. EVERY error is logged and swallowed — a capture failure must NEVER
// fail or alter the run outcome (the run already reported "Run complete"). If some
// images land and others fail, the generation is stored 'partial'; if zero land,
// nothing is inserted.
func (s *Server) captureGeneration(wf *store.Workflow, opts runOptions, res *runResult) {
	if s == nil || wf == nil || res == nil || len(res.Images) == 0 {
		return
	}
	root := strings.TrimSpace(s.cfg.OutputsDir)
	if root == "" {
		return // capture disabled (no outputs dir configured — e.g. a bare test server)
	}
	client := s.comfy()
	if client == nil {
		return
	}
	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, captureBudget)
	defer cancel()

	var images []store.GenerationImage
	for i, ref := range res.Images {
		data, ct, err := client.View(ctx, ref)
		if err != nil {
			s.log.Warn("output capture: fetch image failed", "prompt", res.PromptID, "filename", ref.Filename, "err", err)
			continue
		}
		relPath, err := outputRelPath(res.PromptID, i, ref.Filename)
		if err != nil {
			s.log.Warn("output capture: build rel path failed", "prompt", res.PromptID, "err", err)
			continue
		}
		n, err := writeOutputImage(root, relPath, data, maxOutputImageBytes)
		if err != nil {
			s.log.Warn("output capture: write failed", "prompt", res.PromptID, "rel", relPath, "err", err)
			continue
		}
		// The comfy server is untrusted: resolve the stored type through the
		// whitelist so a hostile server cannot get an html/JS type persisted +
		// served in our origin — and so a VIDEO gets a real, playable type instead
		// of being flattened to application/octet-stream (which is what the old
		// bare `image/` prefix check did to every mp4).
		//
		// ref.Format is passed in ONLY so the whitelist can refuse it explicitly;
		// it is never echoed. See outputs_media.go.
		ct = outputMediaType(ref.Filename, ref.Format, ct)
		basename, _ := safePathSegment(ref.Filename)
		images = append(images, store.GenerationImage{
			Idx:         i,
			RelPath:     relPath,
			Filename:    basename,
			ContentType: ct,
			SizeBytes:   n,
		})
	}
	if len(images) == 0 {
		s.log.Warn("output capture: no images captured", "prompt", res.PromptID)
		return
	}

	status := store.GenerationStatusReady
	if len(images) < len(res.Images) {
		status = store.GenerationStatusPartial
	}
	wfID := wf.ID
	gen := &store.Generation{
		WorkflowID:   &wfID,
		WorkflowName: wf.Name,
		PromptID:     res.PromptID,
		BaseModel:    wf.BaseModel,
		GraphHash:    wf.GraphHash,
		Params:       marshalRunParams(buildRunParamsSnapshot(wf, opts)),
		PresetName:   opts.PresetName,
		// Batch attribution is carried straight off runOptions. This is the ONLY
		// bridge between runBatch (which sets the three fields per item) and
		// InsertGeneration (which writes them): captureGeneration is the only caller
		// of InsertGeneration, so dropping them here silently NULLs every row and
		// makes migration 0016's columns, ix_generations_batch and
		// ListGenerationsByBatch unreachable in the shipped binary. A single run
		// carries "",0,0 and stays NULL in all three — see nullPositiveInt.
		BatchID:    opts.BatchID,
		BatchIndex: opts.BatchIndex,
		BatchTotal: opts.BatchTotal,
		Status:     status,
	}
	// preset_id is a real FK (ON DELETE SET NULL). A preset deleted between the run
	// starting and the capture landing would fail the insert — and losing the images
	// over a label is not a trade worth making — so the id is attached only when the
	// preset still exists; preset_name is a snapshot and always survives.
	if opts.PresetID > 0 {
		if _, err := s.store.GetRunPreset(ctx, opts.PresetID); err == nil {
			pid := opts.PresetID
			gen.PresetID = &pid
		}
	}
	genID, err := s.store.InsertGeneration(ctx, gen, images)
	if err != nil {
		// The files are already on disk but no row references them: they would be
		// invisible to the gallery AND to the cap's byte accounting (which measures
		// recorded rows), i.e. a permanent untracked leak. Unlink them, best-effort,
		// through the same path-contained helper the delete/eviction paths use.
		relPaths := make([]string, 0, len(images))
		for _, img := range images {
			relPaths = append(relPaths, img.RelPath)
		}
		s.log.Warn("output capture: insert generation failed; removing the files just written",
			"prompt", res.PromptID, "files", len(relPaths), "err", err)
		s.removeOutputFiles(relPaths)
		return
	}
	// Bound the outputs tree: evict the oldest generations if this capture pushed
	// the total over the configured disk cap. Best-effort — every error is logged
	// and swallowed, exactly like the rest of capture.
	s.enforceOutputsCap(genID)
}

// evictionBudget bounds one cap-enforcement pass. It is a SEPARATE budget from
// captureBudget (and a separate context) so a slow /view fetch that ate most of
// the capture budget cannot leave the tree permanently over-cap.
const evictionBudget = 30 * time.Second

// maxEvictionBatch bounds how many generations ONE pass may consider/delete. It
// keeps the loop provably finite AND bounds its cost: the store runs on a SINGLE
// SQLite connection (store.go SetMaxOpenConns(1)), so every DeleteGeneration here
// blocks every HTTP handler for its duration. 100 is far more than one capture can
// push over the cap; anything left over is reported and handled by the next pass.
const maxEvictionBatch = 100

// enforceOutputsCap deletes the OLDEST generations (rows + their files) until the
// total captured bytes are back under cfg.OutputsMaxBytes. A cap of 0 (or
// negative) means UNLIMITED — the function returns immediately.
//
// keepID is the generation the calling capture just inserted. Nothing with an id
// >= keepID is ever evicted: captures are NOT serialized with each other (the run
// job clears `running` under runMu BEFORE the capture runs off the mutex, so two
// captures can overlap), and generation ids are monotonic, so `>= keepID` protects
// this capture AND any fresher generation another capture inserted meanwhile.
//
// Generations with no recorded bytes are skipped entirely: deleting them frees
// nothing, so counting them as progress toward the target would walk the loop past
// the cap and over-evict.
//
// The whole pass is serialized on evictMu so two overlapping captures cannot act
// on each other's stale totals. Every failure is logged and swallowed: eviction
// must never alter a run outcome. Each eviction is logged at INFO level with the
// id and bytes reclaimed, because silently deleting the user's own generated
// images must be observable.
func (s *Server) enforceOutputsCap(keepID int64) {
	capBytes := s.cfg.OutputsMaxBytes
	if capBytes <= 0 {
		return // unlimited
	}
	base := s.baseCtx
	if base == nil {
		base = context.Background()
	}

	// One pass at a time: the measure→delete sequence below is only sound if no
	// other pass deletes rows underneath it. Take the lock BEFORE starting the
	// budget clock — a pass queued behind another must get its full budget for its
	// own work, not spend it waiting.
	s.evictMu.Lock()
	defer s.evictMu.Unlock()

	ctx, cancel := context.WithTimeout(base, evictionBudget)
	defer cancel()

	total, err := s.store.SumGenerationImageBytes(ctx)
	if err != nil {
		s.log.Warn("outputs cap: sum captured bytes failed", "err", err)
		return
	}
	if total <= capBytes {
		return
	}

	candidates, err := s.store.ListOldestEvictableGenerations(ctx, maxEvictionBatch)
	if err != nil {
		s.log.Warn("outputs cap: list oldest generations failed", "err", err)
		return
	}
	evicted := 0
	for _, cand := range candidates {
		if total <= capBytes {
			break // back under the cap — stop immediately
		}
		if err := ctx.Err(); err != nil {
			// Budget spent / server shutting down. Stop now rather than hammering the
			// single shared DB connection with calls that will all fail.
			s.log.Warn("outputs cap: eviction stopped early", "err", err,
				"evicted", evicted, "total_bytes", total, "cap_bytes", capBytes)
			return
		}
		if keepID > 0 && cand.ID >= keepID {
			// The just-captured generation, or one a concurrent capture inserted after
			// it. Never evict something fresher than the capture that triggered us.
			continue
		}
		if cand.Bytes <= 0 {
			// Belt-and-suspenders: the query already excludes these. Evicting one frees
			// nothing, so counting it as progress would walk the loop past the cap.
			continue
		}
		relPaths, err := s.store.DeleteGeneration(ctx, cand.ID)
		if errors.Is(err, store.ErrNotFound) {
			// Already gone (a concurrent /outputs/{id}/delete). Its bytes are no longer
			// in the tree, so they MUST still come off the running total — otherwise
			// this pass keeps evicting to reach a target that is already met.
			total -= cand.Bytes
			continue
		}
		if err != nil {
			s.log.Warn("outputs cap: delete generation failed", "generation_id", cand.ID, "err", err)
			continue
		}
		// Shared with the delete handler: path-contained, error-swallowing unlink
		// (a file already gone on disk still leaves the row deleted).
		s.removeOutputFiles(relPaths)
		total -= cand.Bytes
		evicted++
		s.log.Info("outputs cap: evicted oldest generation",
			"generation_id", cand.ID, "bytes_reclaimed", cand.Bytes,
			"total_bytes", total, "cap_bytes", capBytes)
	}
	if total > capBytes {
		// Not an error: the batch bound, a gallery of one huge generation, or a tree
		// made entirely of fresher-than-keepID rows can all leave it over-cap. Say so
		// rather than looping forever.
		s.log.Warn("outputs cap: still over the disk cap after eviction",
			"total_bytes", total, "cap_bytes", capBytes, "evicted", evicted)
	}
}
