package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// A DRY RUN of the local run path against a REAL database, stopping immediately
// before submission. It answers "would this workflow be refused, and by what" using
// the SAME production functions the app uses — comfy.ConvertUIToAPIResult,
// comfy.Preflight, Server.localHaveFile and evalRunGate — so it cannot drift from
// what the run and the readiness line actually decide.
//
//	CM_PROBE_DB=/path/to/a/COPY.db CM_PROBE_WORKFLOW=590 \
//	  go test ./internal/web/ -run TestDryRunProbe -v
//
// Optionally refresh the node schema from a live ComfyUI first (strongly preferred —
// see the staleness note below):
//
//	CM_PROBE_COMFY=http://127.0.0.1:8188 CM_PROBE_DB=… go test …
//
// 🔴 WHAT THIS CANNOT PROVE. It is a LOWER BOUND on brokenness, never a proof of
// success. Everything below is outside what it checks:
//   - ComfyUI's own submit-time validation (/prompt) is the authority and is
//     strictly stricter — required inputs, type compatibility, node-specific
//     validate_inputs. A clean probe can still be rejected at submit.
//   - Nothing about RUNTIME: VRAM/OOM, a custom node that throws, a corrupt
//     safetensors, a wrong-architecture LoRA, an unsupported dtype.
//   - The schema is a SNAPSHOT. Without CM_PROBE_COMFY it reads the cached
//     comfy_model_cache row, which a library scan invalidates and which can predate
//     custom nodes you have since installed or removed.
//   - It reads a COPY of the database, so it answers about the state at copy time.
//
// It refuses to open the canonical database path: this must run against a copy, so
// a probe can never write to (or lock) the database the app is using.
func TestDryRunProbe(t *testing.T) {
	dbPath := os.Getenv("CM_PROBE_DB")
	if dbPath == "" {
		t.Skip("set CM_PROBE_DB to a COPY of the database to run the dry-run probe")
	}
	if home, err := os.UserHomeDir(); err == nil {
		canonical := filepath.Join(home, ".config", "civitai-manager", "civitai-manager.db")
		if abs, aerr := filepath.Abs(dbPath); aerr == nil && abs == canonical {
			t.Fatalf("refusing to probe the live database at %s — copy it first", canonical)
		}
	}

	wfID := int64(590)
	if v := os.Getenv("CM_PROBE_WORKFLOW"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("CM_PROBE_WORKFLOW=%q: %v", v, err)
		}
		wfID = n
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	srv := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL:             "https://civitai.com",
		DefaultPollInterval: time.Hour,
		Addr:                "127.0.0.1:8787",
	}, nil)

	// Schema provenance is the single biggest determinant of whether this answer is
	// worth anything, so it is reported first and always.
	probeReportSchema(t, st)

	ctx := context.Background()
	wf, err := st.GetWorkflow(ctx, wfID)
	if err != nil {
		t.Fatalf("get workflow %d: %v", wfID, err)
	}
	if wf == nil {
		t.Fatalf("workflow %d not found in %s", wfID, dbPath)
	}
	t.Logf("workflow %d %q — format=%s source=%s base_model=%s graph=%d bytes",
		wf.ID, wf.Name, wf.Format, wf.Source, wf.BaseModel, len(wf.Graph))

	// A multi-mode template has no single answer: realRun applies the mode selection
	// BEFORE converting, so the stored graph and the graph that would actually run are
	// different documents. Probe each mode separately rather than inventing one verdict.
	selectors := detectWorkflowModes(wf)
	if len(selectors) == 0 {
		t.Log("single-pipeline workflow (no rgthree mode selector)")
		blocked := probeGraph(t, srv, json.RawMessage(wf.Graph), wf.Format, "")

		// Cross-check the OTHER surface that answers this same question. The run path
		// and the pre-click readiness line share evalRunGate precisely so they can
		// never disagree; if they do, one of them is lying to the user and that is
		// worth more than the verdict itself. Only meaningful for a single-pipeline
		// workflow — readiness deliberately answers "unknown" for a template, because
		// realRun applies the mode selection before converting.
		view := srv.workflowReadiness(wf)
		t.Logf("readiness line says: state=%v reason=%v (missing nodes=%d models=%d bad options=%d)",
			view.state, view.reason, view.missingNodes, view.missingModels, view.badOptions)
		if blocked && view.state == readinessReady {
			t.Errorf("SURFACES DISAGREE: the run path would refuse this graph, but the readiness line calls it Ready")
		}
		if !blocked && view.state == readinessNeeds {
			t.Errorf("SURFACES DISAGREE: the run path would submit this graph, but the readiness line says it needs work")
		}

		if blocked {
			t.Errorf("workflow %d would be REFUSED before submission — see the itemised report above", wfID)
		}
		return
	}

	t.Logf("MULTI-MODE template: %d selector(s) — every mode probed separately", len(selectors))
	anyBlocked := false
	for _, sel := range selectors {
		t.Logf("selector %q (%s): %d mode(s), currently selected=%q",
			sel.Label, sel.Key, len(sel.Modes), sel.Selected())
		for _, mode := range sel.Modes {
			applied := comfy.ApplyModeSelection(json.RawMessage(wf.Graph), map[string]string{sel.Key: mode.Key})
			if probeGraph(t, srv, applied, wf.Format, sel.Key+"="+mode.Key) {
				anyBlocked = true
			}
		}
	}
	if anyBlocked {
		t.Errorf("workflow %d: at least one mode would be REFUSED before submission", wfID)
	}
}

// probeReportSchema prints where the node schema came from and how big it is, and
// optionally refreshes it from a live ComfyUI into the COPY. A cold or stale schema
// makes every downstream answer suspect, so this can never be silent.
func probeReportSchema(t *testing.T, st *store.Store) {
	t.Helper()
	if base := strings.TrimRight(os.Getenv("CM_PROBE_COMFY"), "/"); base != "" {
		body, err := probeFetchObjectInfo(base)
		if err != nil {
			t.Fatalf("CM_PROBE_COMFY=%s: %v (start ComfyUI, or unset it to use the cached schema)", base, err)
		}
		if err := st.PutComfyObjectInfo(body); err != nil {
			t.Fatalf("cache object_info into the copy: %v", err)
		}
		t.Logf("schema: LIVE from %s — %d bytes", base, len(body))
	}

	ent, err := st.GetComfyObjectInfo()
	if err != nil {
		t.Fatalf("read cached object_info: %v", err)
	}
	if ent == nil || len(ent.ObjectInfoJSON) == 0 {
		t.Fatal("schema: COLD CACHE — no /object_info payload. Set CM_PROBE_COMFY to a running ComfyUI; " +
			"without a schema every node class would look missing and the probe would be meaningless.")
	}
	var info comfy.ObjectInfo
	if err := json.Unmarshal(ent.ObjectInfoJSON, &info); err != nil {
		t.Fatalf("schema: cached payload does not decode: %v", err)
	}
	t.Logf("schema: %d node types, %d bytes, updated_at=%s", len(info), len(ent.ObjectInfoJSON), ent.UpdatedAt)
	if os.Getenv("CM_PROBE_COMFY") == "" {
		t.Log("⚠ schema is the CACHED row, not a live read. A library scan invalidates this cache, and " +
			"it can predate custom nodes you have installed since. Prefer CM_PROBE_COMFY=http://127.0.0.1:8188.")
	}
}

func probeFetchObjectInfo(base string) ([]byte, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(base + "/object_info")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

type errStatus int

func (e errStatus) Error() string { return "GET /object_info: HTTP " + strconv.Itoa(int(e)) }

// probeGraph runs the real convert → preflight → gate pipeline over one graph and
// reports every blocking item by NAME. Returns whether the run would be refused.
func probeGraph(t *testing.T, srv *Server, graph json.RawMessage, format, label string) bool {
	t.Helper()
	prefix := ""
	if label != "" {
		prefix = "[" + label + "] "
	}

	info, reason := srv.readinessSchema()
	if reason != "" {
		t.Errorf("%sno usable schema: %s", prefix, reason)
		return true
	}

	apiGraph := graph
	var conv comfy.ConversionResult
	if format == string(store.WorkflowFormatUI) {
		c, err := comfy.ConvertUIToAPIResult(graph, info)
		if err != nil {
			t.Errorf("%sUI→API conversion FAILED: %v", prefix, err)
			return true
		}
		conv = c
		apiGraph = conv.APIGraph
	}

	report := comfy.Preflight(apiGraph, info, srv.localHaveFile)
	// evalRunGate MUTATES report (it unions the converter's cut classes into
	// MissingNodes and forces OK=false), so this must run before the report is read —
	// exactly as realRun and the readiness fragment do it.
	gate := evalRunGate(conv, &report)

	t.Logf("%snodes=%d  conv.warnings=%d  conv.cut_classes=%d  missing_nodes=%d  missing_models=%d  bad_options=%d  preflight.OK=%v",
		prefix, report.Nodes, len(conv.Warnings), len(conv.MissingNodeTypes),
		len(report.MissingNodes), len(report.MissingModels), len(report.BadOptions), report.OK)

	for _, w := range conv.Warnings {
		t.Logf("%s  BLOCKER conversion warning: %s", prefix, w)
	}
	for _, c := range report.MissingNodes {
		t.Logf("%s  BLOCKER missing node class: %s", prefix, c)
	}
	for _, m := range report.MissingModels {
		t.Logf("%s  BLOCKER missing model file: %s%s%s",
			prefix, m, probeWhoWants(apiGraph, m), probeExplainModel(srv, m))
	}
	for _, b := range report.BadOptions {
		t.Logf("%s  BLOCKER bad option: %s.%s = %q (nodes %s); %d valid choices%s",
			prefix, b.ClassType, b.InputName, b.Current, strings.Join(b.NodeIDs, ","),
			len(b.Choices), probeSampleChoices(b.Choices))
	}

	if !gate.blocked() {
		t.Logf("%s✅ NOT REFUSED by the local gate — the app would submit this graph. "+
			"That is not a prediction that it succeeds; ComfyUI's own validation runs next.", prefix)
		return false
	}
	t.Logf("%s❌ REFUSED by the local gate: ConvWarned=%v GraphIncomplete=%v NoNodes=%v ReportOK=%v",
		prefix, gate.ConvWarned, gate.GraphIncomplete, gate.NoNodes, gate.ReportOK)
	return true
}

// probeWhoWants names the node(s) referencing a model filename, so a missing file
// is actionable: WHICH loader wants it decides what can be substituted for it.
// comfy.Preflight reports the filename alone.
func probeWhoWants(apiGraph json.RawMessage, ref string) string {
	var nodes map[string]struct {
		ClassType string                     `json:"class_type"`
		Inputs    map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal(apiGraph, &nodes); err != nil {
		return ""
	}
	var hits []string
	for id, n := range nodes {
		for name, raw := range n.Inputs {
			var s string
			if json.Unmarshal(raw, &s) == nil && s == ref {
				hits = append(hits, n.ClassType+"."+name+" (node "+id+")")
			}
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return "  ← wanted by " + strings.Join(hits, ", ")
}

// probeExplainModel is the diagnostic half: a missing model is reported by the exact
// string the graph carries, which on a Windows-authored workflow is a BACKSLASH path.
// Say whether the library has the file under either separator's basename, so a real
// "not downloaded" is distinguishable from a path-shape mismatch.
func probeExplainModel(srv *Server, ref string) string {
	slash := ref[strings.LastIndex(ref, "/")+1:]
	back := ref[strings.LastIndex(ref, "\\")+1:]
	var notes []string
	if slash != ref || back != ref {
		notes = append(notes, "basename="+back)
	}
	if srv.localHaveFile(ref) {
		notes = append(notes, "library HAS the full string")
	}
	if back != ref && srv.localHaveFile(back) {
		notes = append(notes, "🔴 library HAS the basename but NOT the stored path — path-shape mismatch, not a missing download")
	}
	if slash != ref && slash != back && srv.localHaveFile(slash) {
		notes = append(notes, "library has the /-basename")
	}
	if len(notes) == 0 {
		return "  (not in the library under any basename)"
	}
	return "  (" + strings.Join(notes, "; ") + ")"
}

// probeSampleChoices shows a few valid options so a drifted value is actionable.
func probeSampleChoices(choices []string) string {
	if len(choices) == 0 {
		return ""
	}
	c := append([]string(nil), choices...)
	sort.Strings(c)
	if len(c) > 6 {
		c = c[:6]
		return ", e.g. " + strings.Join(c, ", ") + ", …"
	}
	return ": " + strings.Join(c, ", ")
}
