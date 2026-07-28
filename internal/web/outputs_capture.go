package web

import (
	"context"
	"encoding/json"
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
	Substitute      map[string]string     `json:"substitute,omitempty"`
	OptionFixes     []optionFixEntry      `json:"option_fixes,omitempty"`
	WidgetOverrides []widgetOverrideEntry `json:"widget_overrides,omitempty"`
	Resources       []string              `json:"resources,omitempty"`
	BaseModel       string                `json:"base_model,omitempty"`
	Format          string                `json:"format,omitempty"`
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
	if _, err := s.store.InsertGeneration(ctx, gen, images); err != nil {
		s.log.Warn("output capture: insert generation failed", "prompt", res.PromptID, "err", err)
	}
}
