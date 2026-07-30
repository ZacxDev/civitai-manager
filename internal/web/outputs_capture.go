package web

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

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
}

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
	}
	return snap
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
		// The comfy server is untrusted: constrain a non-image content-type so a
		// hostile server cannot get an html/JS type persisted + served in our origin.
		if !strings.HasPrefix(ct, "image/") {
			ct = "application/octet-stream"
		}
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
		Status:       status,
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
