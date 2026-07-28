package web

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
type uiWidgetOverrideEntry struct {
	NodeID string `json:"node_id"`
	Widget int    `json:"widget"`
	Value  string `json:"value"`
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
			NodeID: key.NodeID, Widget: key.Widget, Value: val,
		})
	}
	// Map iteration is unordered — sort so the persisted params JSON is stable for a
	// given run (comparable across re-runs, diffable in the gallery).
	sort.Slice(snap.UIWidgetOverrides, func(i, j int) bool {
		if snap.UIWidgetOverrides[i].NodeID != snap.UIWidgetOverrides[j].NodeID {
			return snap.UIWidgetOverrides[i].NodeID < snap.UIWidgetOverrides[j].NodeID
		}
		return snap.UIWidgetOverrides[i].Widget < snap.UIWidgetOverrides[j].Widget
	})
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
func runOptionsFromParams(params string) runOptions {
	snap := parseRunParams(params)
	opts := runOptions{}
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
	if len(snap.UIWidgetOverrides) > 0 {
		opts.UIWidgetOverrides = make(map[comfy.UIWidgetKey]string, len(snap.UIWidgetOverrides))
		for _, e := range snap.UIWidgetOverrides {
			opts.UIWidgetOverrides[comfy.UIWidgetKey{NodeID: e.NodeID, Widget: e.Widget}] = e.Value
		}
	}
	return opts
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
		Status:       status,
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
