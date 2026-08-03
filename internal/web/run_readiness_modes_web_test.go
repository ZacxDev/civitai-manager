package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// MULTI-MODE TEMPLATES AND THE READINESS LINE.
//
// realRun applies comfy.ApplyModeSelection BEFORE converting; the readiness
// fragment converts the STORED graph. On a template those are different graphs, so
// the line was computing an answer about a pipeline that is not the one that will
// run. This file both PROVES that divergence on a real fixture and pins the
// suppression that closes it.
// ─────────────────────────────────────────────────────────────────────────────

// readinessTwoPipelineGraph is a template pack in miniature: ONE rgthree Fast
// Groups Bypasser declaring exclusivity (toggleRestriction "max one") over two
// purple groups, each holding one loader.
//
//	Pipeline A — ACTIVE (mode 0), loads the file readinessInfo's combo lists.
//	Pipeline B — BYPASSED (mode 4), loads a file NOTHING has.
//
// So the STORED graph is clean (the converter drops the bypassed node) and the
// graph a run with B picked produces is not. That asymmetry is the whole point:
// with both pipelines missing something, or neither, the fixture could not
// discriminate and every assertion below would be over-determined.
const readinessTwoPipelineGraph = `{"nodes":[
  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[-900,-900],"size":[100,50],
   "properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"max one"}},
  {"id":10,"type":"CheckpointLoaderSimple","mode":0,"pos":[40,40],"size":[20,20],
   "widgets_values":["comfy_has_this.safetensors"]},
  {"id":11,"type":"CheckpointLoaderSimple","mode":4,"pos":[540,40],"size":[20,20],
   "widgets_values":["pipeline_b_only.safetensors"]}],
 "links":[],
 "groups":[
  {"title":"Pipeline A","bounding":[0,0,400,400],"color":"#a1309b"},
  {"title":"Pipeline B","bounding":[500,0,400,400],"color":"#a1309b"}]}`

// readinessOneModeGraph is byte-identical to the fixture above except that the
// toggler declares NO exclusivity ("default"), which is how comfy.modes decides
// this is not a mode selector at all. It is the control: same nodes, same groups,
// same bypass state, and the readiness line must answer normally.
const readinessOneModeGraph = `{"nodes":[
  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[-900,-900],"size":[100,50],
   "properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"default"}},
  {"id":10,"type":"CheckpointLoaderSimple","mode":0,"pos":[40,40],"size":[20,20],
   "widgets_values":["comfy_has_this.safetensors"]},
  {"id":11,"type":"CheckpointLoaderSimple","mode":4,"pos":[540,40],"size":[20,20],
   "widgets_values":["pipeline_b_only.safetensors"]}],
 "links":[],
 "groups":[
  {"title":"Pipeline A","bounding":[0,0,400,400],"color":"#a1309b"},
  {"title":"Pipeline B","bounding":[500,0,400,400],"color":"#a1309b"}]}`

// TestModeSelectionReallyChangesWhatAWorkflowNeeds is the DIVERGENCE PROOF, and it
// is deliberately at the comfy layer rather than the HTTP one: the readiness route
// takes no mode, so it structurally cannot show both sides of the disagreement.
//
// It runs FIRST in this file's story. If it ever goes green-by-agreement — both
// pipelines needing the same things — the suppression it justifies should be
// revisited rather than kept out of habit, and this test is what would say so.
func TestModeSelectionReallyChangesWhatAWorkflowNeeds(t *testing.T) {
	var info comfy.ObjectInfo
	if err := json.Unmarshal([]byte(readinessInfo), &info); err != nil {
		t.Fatalf("decode fixture object_info: %v", err)
	}
	raw := json.RawMessage(readinessTwoPipelineGraph)

	// PRECONDITION: the fixture really expresses the contest. A graph the detector
	// does not see as a template would make every assertion below vacuous.
	sels := comfy.DetectModeSelectors(raw)
	if len(sels) != 1 || len(sels[0].Modes) != 2 {
		t.Fatalf("fixture does not express a two-mode selector: %+v", sels)
	}

	preflight := func(g json.RawMessage) comfy.PreflightReport {
		t.Helper()
		c, err := comfy.ConvertUIToAPIResult(g, info)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		// The rgthree toggler must not itself be cut or warned about, or the fixture
		// would be measuring the converter instead of the mode selection.
		if len(c.Warnings) > 0 || len(c.MissingNodeTypes) > 0 {
			t.Fatalf("fixture is noisy — warnings=%v cut=%v", c.Warnings, c.MissingNodeTypes)
		}
		return comfy.Preflight(c.APIGraph, info, func(string) bool { return false })
	}

	stored := preflight(raw)
	if !stored.OK || len(stored.MissingModels) != 0 {
		t.Fatalf("the STORED graph must be clean, or the line would not have said Ready: %+v", stored)
	}

	byTitle := map[string]string{}
	for _, m := range sels[0].Modes {
		byTitle[m.Title] = m.Key
	}
	picked := preflight(comfy.ApplyModeSelection(raw, map[string]string{
		sels[0].Key: byTitle["Pipeline B"],
	}))
	if picked.OK {
		t.Fatal("picking pipeline B changed nothing — the fixture cannot show the divergence")
	}
	if len(picked.MissingModels) != 1 || picked.MissingModels[0] != "pipeline_b_only.safetensors" {
		t.Fatalf("want pipeline B to need pipeline_b_only.safetensors, got %+v", picked.MissingModels)
	}
}

// TestReadinessSuppressesTheClaimForAMultiPipelineTemplate is the fix.
//
// 🔴 REGRESSION. On pre-change code this fixture rendered data-readiness="ready"
// about a template whose selected pipeline can need a file that is not installed.
// Red at 5fd08fb, green at HEAD.
func TestReadinessSuppressesTheClaimForAMultiPipelineTemplate(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessTwoPipelineGraph)

	body := readiness(t, srv, id)
	wantState(t, body, "unknown", "modes")
	// The reason must be actionable: point at the picker, not at the cache.
	if !strings.Contains(body, "which one you pick") {
		t.Errorf("the suppressed answer must name the pick as the reason:\n%s", body)
	}
}

// TestReadinessStillAnswersForASingleModeGraph is the control that stops the
// suppression from swallowing ordinary workflows. Same nodes, same groups, same
// bypass state — only toggleRestriction differs, which is exactly the signal
// comfy.modes keys on. 68 of the operator's 71 real workflows are on this side of
// the line.
func TestReadinessStillAnswersForASingleModeGraph(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessOneModeGraph)

	// PRECONDITION on the control itself: the detector must NOT see a selector here,
	// or this test would pass for the wrong reason (suppressed either way).
	if sels := comfy.DetectModeSelectors(json.RawMessage(readinessOneModeGraph)); len(sels) != 0 {
		t.Fatalf("control fixture is a template after all: %+v", sels)
	}
	wantState(t, readiness(t, srv, id), "ready", "")
}
