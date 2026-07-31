package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ---------------------------------------------------------------------------
// Item 1 — the output detail page's provenance card
// ---------------------------------------------------------------------------

// capturedProvenanceParams is a params blob in the CURRENT format: prompt capture
// ran (prompts_captured), it found a positive and a negative prompt, and the run
// referenced a checkpoint and a LoRA. The resource strings are the real shape seen
// in live workflows — a subdirectory-prefixed checkpoint and a bare LoRA basename.
const capturedProvenanceParams = `{
  "prompts_captured": true,
  "prompts": [
    {"label":"Prompt (POSITIVE)","node_id":"10","class_type":"CLIPTextEncode",
     "input_name":"text","text":"a red barn at dawn, volumetric light"},
    {"label":"Prompt (NEGATIVE)","node_id":"11","class_type":"CLIPTextEncode",
     "input_name":"text","text":"blurry, watermark, extra fingers"}
  ],
  "resources": ["seg-a/waiNSFWIllustrious_v140.safetensors",
                "Add_Details_v1.2-illustrious.safetensors",
                "BYPASSED_PIPELINE.safetensors"],
  "resources_used": ["seg-a/waiNSFWIllustrious_v140.safetensors",
                     "Add_Details_v1.2-illustrious.safetensors"],
  "base_model": "Illustrious"
}`

// preChangeParams is a VERBATIM params blob of the shape written BEFORE prompt
// capture existed. It is deliberately a literal string rather than a re-serialized
// runParamsSnapshot: re-serializing would emit whatever the CURRENT struct emits, so
// the test would silently stop exercising back-compat the moment the struct changed.
const preChangeParams = `{"widget_overrides":[{"node_id":"3","input_name":"seed","value":"42"}],` +
	`"resources":["oldCheckpoint.safetensors"],"base_model":"SDXL 1.0","format":"ui"}`

func TestOutputProvenanceRendersPromptResourcesAndWorkflowLink(t *testing.T) {
	wfID := int64(7)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "Barn maker",
		PromptID: "p1", Params: capturedProvenanceParams}
	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))

	// The prompt that ACTUALLY ran, both halves of it.
	for _, want := range []string{
		"a red barn at dawn, volumetric light",
		"blurry, watermark, extra fingers",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("captured prompt %q is missing from the provenance card:\n%s", want, html)
		}
	}
	// The checkpoint and the LoRA, through the SHARED chip component. Asserting the
	// shared class is the point: a second, divergent resource renderer fails here.
	if !strings.Contains(html, "cm-res-chip") {
		t.Errorf("resources must render through the shared .cm-res-chip component:\n%s", html)
	}
	if strings.Count(html, `class="cm-res-chip"`) != 2 {
		t.Errorf("want one chip per resource (2), got %d:\n%s",
			strings.Count(html, `class="cm-res-chip"`), html)
	}
	for _, want := range []string{
		"waiNSFWIllustrious_v140.safetensors",      // checkpoint (basename of the subdir path)
		"Add_Details_v1.2-illustrious.safetensors", // LoRA
	} {
		if !strings.Contains(html, want) {
			t.Errorf("resource %q missing:\n%s", want, html)
		}
	}
	// The fixture's `resources` (the workflow's full reference list) additionally
	// carries a bypassed pipeline's checkpoint. The card renders `resources_used`, so
	// it must NOT appear under a heading asserting it ran.
	if strings.Contains(html, "BYPASSED_PIPELINE.safetensors") {
		t.Errorf("a model the run did not load must not be claimed as used:\n%s", html)
	}
	if !strings.Contains(html, resourcesUsedHeading) {
		t.Errorf("a captured resource list earns the strong heading:\n%s", html)
	}
	// The source workflow, as a LINK.
	if !strings.Contains(html, `href="/workflows/7"`) {
		t.Errorf("a live source workflow must render as a link:\n%s", html)
	}
	if !strings.Contains(html, "Barn maker") {
		t.Errorf("the workflow's name must be shown:\n%s", html)
	}
}

// TestOutputProvenancePositiveAndNegativePromptsStayDistinct pins that the two
// prompts are not collapsed into one blob — a negative prompt rendered as part of the
// positive one reads as a request for blurry watermarked fingers.
func TestOutputProvenancePositiveAndNegativePromptsStayDistinct(t *testing.T) {
	wfID := int64(7)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "wf",
		Params: capturedProvenanceParams}
	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))

	pos := strings.Index(html, "Prompt (POSITIVE)")
	neg := strings.Index(html, "Prompt (NEGATIVE)")
	if pos < 0 || neg < 0 {
		t.Fatalf("both prompt labels must render (pos=%d neg=%d):\n%s", pos, neg, html)
	}
	if pos >= neg {
		t.Errorf("prompts must render in capture order (positive first): pos=%d neg=%d", pos, neg)
	}
	// Each label must OWN its text: the positive text sits between the two labels.
	between := html[pos:neg]
	if !strings.Contains(between, "a red barn at dawn") {
		t.Errorf("the positive text is not under the POSITIVE label:\n%s", between)
	}
	if strings.Contains(between, "blurry, watermark") {
		t.Errorf("the negative text leaked under the POSITIVE label:\n%s", between)
	}
	// The node id disambiguates a pair whose nodes carry no titles at all.
	if !strings.Contains(html, "node 10") || !strings.Contains(html, "node 11") {
		t.Errorf("each prompt must name the node it came from:\n%s", html)
	}
}

// TestOutputProvenanceWorkflowDeletedIsPlainTextNotADeadLink pins the workflow_id
// NULL case: the snapshot name still shows, but never as a link to a workflow that
// no longer exists.
func TestOutputProvenanceWorkflowDeletedIsPlainTextNotADeadLink(t *testing.T) {
	gen := &store.Generation{ID: 5, WorkflowID: nil, WorkflowName: "Barn maker",
		Params: capturedProvenanceParams}
	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))

	if strings.Contains(html, `href="/workflows/`) {
		t.Errorf("a deleted workflow must NOT be linked:\n%s", html)
	}
	if !strings.Contains(html, "Barn maker") {
		t.Errorf("the snapshot name must survive the deletion:\n%s", html)
	}
	if !strings.Contains(html, "(deleted)") {
		t.Errorf("the deletion must be stated, not implied:\n%s", html)
	}
}

// TestOutputProvenancePreChangeRowSaysNotRecorded is the BACK-COMPAT guard: a row
// written before prompt capture existed must say so explicitly.
//
// It must not blank the section (which reads as "no prompt"), must not guess from the
// workflow's current graph (which would attribute today's prompt to a past image),
// and must not use the OTHER empty message ("no prompt input detected"), which is a
// claim we looked and found none.
func TestOutputProvenancePreChangeRowSaysNotRecorded(t *testing.T) {
	wfID := int64(7)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "wf",
		Params: preChangeParams}

	// Decoding an old blob must not panic and must not invent a capture.
	snap := parseRunParams(gen.Params)
	if snap.PromptsCaptured {
		t.Error("a pre-change blob has no prompts_captured field; it must decode to false")
	}
	if len(snap.Prompts) != 0 {
		t.Errorf("a pre-change blob has no prompts; got %+v", snap.Prompts)
	}
	// Everything the old blob DID carry must still decode (no field-order/rename slip).
	if len(snap.WidgetOverrides) != 1 || snap.WidgetOverrides[0].Value != "42" {
		t.Errorf("pre-change widget overrides no longer decode: %+v", snap.WidgetOverrides)
	}
	if len(snap.Resources) != 1 || snap.Resources[0] != "oldCheckpoint.safetensors" {
		t.Errorf("pre-change resources no longer decode: %+v", snap.Resources)
	}

	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))
	if !strings.Contains(html, promptNotRecordedNote) {
		t.Errorf("a pre-change row must state that the prompt was not recorded:\n%s", html)
	}
	if strings.Contains(html, promptNoneDetectedNote) {
		t.Errorf("'not recorded' and 'none detected' are different facts and must not be "+
			"conflated:\n%s", html)
	}
	// Its resources still render — only the prompt is unknown.
	if !strings.Contains(html, "oldCheckpoint.safetensors") {
		t.Errorf("a pre-change row's resources must still render:\n%s", html)
	}
}

// TestOutputProvenanceNoPromptInputIsItsOwnMessage pins the second empty case: an
// api-format workflow exposes no widgets_values, so capture legitimately records zero
// prompts. That is "we looked and there is none", not "we never looked".
func TestOutputProvenanceNoPromptInputIsItsOwnMessage(t *testing.T) {
	wfID := int64(7)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "wf",
		Params: `{"prompts_captured":true,"resources":["x.safetensors"]}`}
	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))

	if !strings.Contains(html, promptNoneDetectedNote) {
		t.Errorf("a captured run with no prompt input must say so:\n%s", html)
	}
	if strings.Contains(html, "Not recorded for this run") {
		t.Errorf("a captured run must never claim its prompt was not recorded:\n%s", html)
	}
}

// TestOutputProvenanceResourcesFollowSubstitutions pins that the chips name the file
// that ACTUALLY ran. snap.Resources is the workflow's PRE-substitution list, so an
// unmapped render would name the missing original — and mark it "not in your library",
// which is precisely why it was substituted.
func TestOutputProvenanceResourcesFollowSubstitutions(t *testing.T) {
	wfID := int64(7)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "wf",
		Params: `{"prompts_captured":true,` +
			`"resources_used":["missing.safetensors","kept.safetensors"],` +
			`"substitute":{"missing.safetensors":"installed.safetensors"}}`}
	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))

	if !strings.Contains(html, "installed.safetensors") {
		t.Errorf("the chip must name the SUBSTITUTE that actually ran:\n%s", html)
	}
	if !strings.Contains(html, "kept.safetensors") {
		t.Errorf("an unsubstituted resource must still render:\n%s", html)
	}
	if strings.Contains(html, "missing.safetensors") {
		t.Errorf("the pre-substitution filename must not be presented as what ran:\n%s", html)
	}
	if !strings.Contains(html, resourcesUsedHeading) {
		t.Errorf("a captured resource list earns the strong heading:\n%s", html)
	}
	if !strings.Contains(html, "substituted for this run") {
		t.Errorf("the substitution must be disclosed, not silently applied:\n%s", html)
	}
}

// TestOutputProvenanceFolderNoteFollowsTheActualButton pins a wart caught in a real
// browser: the "the folder button opens a window on the server" line was keyed on the
// loopback bind alone, so it sat under two chips that had no folder button at all —
// explaining a control that is not on screen. It is now keyed on a chip ACTUALLY
// carrying one.
func TestOutputProvenanceFolderNoteFollowsTheActualButton(t *testing.T) {
	const note = "The folder button opens a file-manager window"
	gen := &store.Generation{ID: 5, WorkflowName: "wf",
		Params: `{"prompts_captured":true,"resources":["a.safetensors"]}`}

	// Loopback bind, but the resource resolves to nothing → no button, so no note.
	unresolved := workflowResolver{openFolder: true, csrf: "c",
		localResource: func(string) (resourceInfo, bool) { return resourceInfo{}, false }}
	if html := renderString(t, generationProvenanceCard(gen, unresolved)); strings.Contains(html, note) {
		t.Errorf("the note must not explain a button that is not rendered:\n%s", html)
	}

	// A concrete, contained local file → the button IS rendered, so the note belongs.
	resolved := workflowResolver{openFolder: true, csrf: "c",
		localResource: func(string) (resourceInfo, bool) {
			return resourceInfo{Path: "/models/a.safetensors", FileID: 9, Contained: true}, true
		}}
	html := renderString(t, generationProvenanceCard(gen, resolved))
	if !strings.Contains(html, "cm-res-open-btn") {
		t.Fatalf("a revealable file must render the folder control (test is vacuous otherwise):\n%s", html)
	}
	if !strings.Contains(html, note) {
		t.Errorf("a rendered folder button must be explained:\n%s", html)
	}
}

// TestGenerationDetailPageCarriesProvenance is the HTTP-level check: the card is
// wired into the real page, below the media.
func TestGenerationDetailPageCarriesProvenance(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	genID := seedGenWithParams(t, srv, root, &wf, "wf", capturedProvenanceParams, "")

	rec := get(t, srv, "/outputs/"+strconv.FormatInt(genID, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	main := pageMain(rec.Body.String())
	if !strings.Contains(main, "Provenance") {
		t.Errorf("the detail page must carry the provenance card:\n%s", main)
	}
	if !strings.Contains(main, "a red barn at dawn") {
		t.Errorf("the captured prompt must render on the real page:\n%s", main)
	}
	if !strings.Contains(main, "cm-res-chip") {
		t.Errorf("the resource chips must render on the real page:\n%s", main)
	}
	// Below the media: the media card comes first. Its heading is "Outputs", not
	// "Images" — a video-only generation contains no images at all, and the card now
	// renders <video> as well as <img>.
	imgs, prov := strings.Index(main, "Outputs"), strings.Index(main, "Provenance")
	if imgs < 0 || prov < 0 || imgs > prov {
		t.Errorf("provenance must sit BELOW the media (outputs=%d provenance=%d)", imgs, prov)
	}
}

// ---------------------------------------------------------------------------
// The capture side — it MUST read the mode-applied, override-applied graph
// ---------------------------------------------------------------------------

// provenanceModePromptGraph is a two-pipeline template in the shape real packs use
// (an ACTIVE rgthree Fast Groups Bypasser with toggleRestriction "max one" over two
// purple groups), where EACH pipeline holds its OWN prompt.
//
// The asymmetry is the whole point, and it mirrors queueModeSeedGraph: pipeline A's
// nodes ship ACTIVE and pipeline B's ship BYPASSED, so the RAW graph and the
// MODE-APPLIED graph expose DIFFERENT prompts. DetectRunInputs never surfaces a
// bypassed node, so reading wf.Graph records the barn while the run made the whale.
//
// Each pipeline also carries its OWN checkpoint loader, so the SAME fixture proves
// the resource half: comfy.ExtractResourcesAny (no mode check) returns BOTH
// filenames, while the provenance capture must return only the selected one's.
const provenanceModePromptGraph = `{"nodes":[
  {"id":1,"type":"Fast Groups Bypasser (rgthree)","mode":0,"pos":[-800,-800],"size":[200,80],
   "title":"Workflows","properties":{"matchColors":"purple","matchTitle":"","toggleRestriction":"max one"}},
  {"id":10,"type":"CLIPTextEncode","mode":0,"pos":[50,80],"size":[200,100],
   "title":"A-POSITIVE","widgets_values":["a red barn at dawn"]},
  {"id":20,"type":"CLIPTextEncode","mode":4,"pos":[650,80],"size":[200,100],
   "title":"B-POSITIVE","widgets_values":["a blue whale underwater"]},
  {"id":11,"type":"CheckpointLoaderSimple","mode":0,"pos":[50,250],"size":[200,100],
   "widgets_values":["PIPELINE_A_ONLY.safetensors"]},
  {"id":21,"type":"CheckpointLoaderSimple","mode":4,"pos":[650,250],"size":[200,100],
   "widgets_values":["PIPELINE_B_ONLY.safetensors"]}],
 "links":[],
 "groups":[
  {"title":"MODE-A","bounding":[0,0,500,400],"color":"#a1309b"},
  {"title":"MODE-B","bounding":[600,0,500,400],"color":"#a1309b"}]}`

// TestCapturedPromptComesFromTheModeAppliedGraph is the prompt-side twin of
// TestQueueSeedKeysComeFromTheModeAppliedGraph. Mutating capturePrompts to read
// wf.Graph instead of modeAppliedGraph(...) records the BYPASSED pipeline's prompt —
// a permanently wrong record of what made the image, with nothing on screen admitting
// it.
func TestCapturedPromptComesFromTheModeAppliedGraph(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI, Graph: provenanceModePromptGraph}

	// Sanity-pin the fixture's asymmetry, so a DetectRunInputs change that made the two
	// graphs agree surfaces here rather than silently defanging the guard below.
	raw := comfy.DetectRunInputs([]byte(provenanceModePromptGraph), nil)
	if len(raw) != 1 || raw[0].Current != "a red barn at dawn" {
		t.Fatalf("raw graph prompts = %+v, want exactly the MODE-A barn", raw)
	}

	_, byTitle := modeKeys(t, provenanceModePromptGraph)
	modeB := byTitle["MODE-B"]
	if modeB == "" {
		t.Fatalf("fixture exposes no MODE-B: %+v", byTitle)
	}
	opts := runOptions{ModeSelection: map[string]string{
		modeKeySelector(t, provenanceModePromptGraph): modeB,
	}}

	got := capturePrompts(runSubmittedGraph(wf, opts))
	if len(got) != 1 {
		t.Fatalf("captured %d prompts, want exactly the selected pipeline's one: %+v", len(got), got)
	}
	if got[0].Text != "a blue whale underwater" {
		t.Errorf("captured %q — the capture read the RAW graph, so it recorded a "+
			"BYPASSED pipeline's prompt instead of the one this run submitted", got[0].Text)
	}
	if got[0].NodeID != "20" {
		t.Errorf("captured node %q, want the selected pipeline's node 20", got[0].NodeID)
	}
}

// TestCapturedResourcesExcludeBypassedPipelines is audit finding F1: the resource
// half of provenance must follow the mode exactly like the prompt half.
//
// wf.Resources is comfy.ExtractResourcesAny over the STORED graph with NO mode check,
// so on a template it names every pipeline's models. Rendering that under "Resources
// used" asserts a checkpoint ran that demonstrably did not — the same false claim this
// branch closed for prompts, three lines away in the same function.
func TestCapturedResourcesExcludeBypassedPipelines(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI, Graph: provenanceModePromptGraph}

	// Sanity-pin the asymmetry: the UNFILTERED extractor sees BOTH pipelines. Without
	// this, an extractor change that stopped returning the bypassed file would make the
	// guard below pass for the wrong reason.
	all, err := comfy.ExtractResourcesAny(store.WorkflowFormatUI, []byte(provenanceModePromptGraph))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("the workflow's reference list should name BOTH pipelines' checkpoints, got %v", all)
	}

	_, byTitle := modeKeys(t, provenanceModePromptGraph)
	opts := runOptions{ModeSelection: map[string]string{
		modeKeySelector(t, provenanceModePromptGraph): byTitle["MODE-B"],
	}}

	got := captureActiveResources(wf, runSubmittedGraph(wf, opts))
	if len(got) != 1 || got[0] != "PIPELINE_B_ONLY.safetensors" {
		t.Errorf("captured %v — provenance must record ONLY the selected pipeline's "+
			"models; naming a bypassed pipeline's checkpoint claims a model ran that "+
			"did not", got)
	}

	// And end-to-end through the snapshot the store actually persists.
	round := parseRunParams(marshalRunParams(buildRunParamsSnapshot(wf, opts)))
	if len(round.ResourcesUsed) != 1 || round.ResourcesUsed[0] != "PIPELINE_B_ONLY.safetensors" {
		t.Errorf("resources_used = %v after marshal → parse, want only the selected "+
			"pipeline's checkpoint", round.ResourcesUsed)
	}
	// The workflow's full reference list is still snapshotted separately — the params
	// card below the provenance card shows it, honestly labeled.
	if len(round.Resources) != 0 {
		t.Logf("resources (reference list) = %v", round.Resources)
	}
}

// TestOutputProvenanceFallsBackToTheReferenceHeading pins the degradation path for a
// row with no captured resource list (every pre-change row): it may still show the
// workflow's reference list, but ONLY under a heading that claims reference rather
// than use, plus the sentence saying the list can include bypassed pipelines.
func TestOutputProvenanceFallsBackToTheReferenceHeading(t *testing.T) {
	wfID := int64(7)
	gen := &store.Generation{ID: 5, WorkflowID: &wfID, WorkflowName: "wf",
		Params: preChangeParams} // no resources_used at all

	html := renderString(t, generationProvenanceCard(gen, workflowResolver{}))
	if !strings.Contains(html, resourcesReferencedHeading) {
		t.Errorf("an uncaptured resource list must use the weaker heading:\n%s", html)
	}
	if strings.Contains(html, resourcesUsedHeading) {
		t.Errorf("a row that did not record what it loaded must not claim USE:\n%s", html)
	}
	if !strings.Contains(html, resourcesReferencedNote) {
		t.Errorf("the fallback must say the list can include models that did not run:\n%s", html)
	}
	if !strings.Contains(html, "oldCheckpoint.safetensors") {
		t.Errorf("the reference list is still worth showing:\n%s", html)
	}

	// The strong heading is reachable — otherwise this test proves nothing about the
	// distinction, only that one string is absent.
	captured := &store.Generation{ID: 6, WorkflowID: &wfID, WorkflowName: "wf",
		Params: `{"prompts_captured":true,"resources_used":["ran.safetensors"]}`}
	strong := renderString(t, generationProvenanceCard(captured, workflowResolver{}))
	if !strings.Contains(strong, resourcesUsedHeading) || strings.Contains(strong, resourcesReferencedHeading) {
		t.Errorf("a captured list must use the strong heading and only that one:\n%s", strong)
	}
}

// TestCapturedPromptAppliesRunOverrides pins the second half: the Parameters panel's
// edit IS the prompt the user ran, so capture must read the OVERRIDE-applied graph.
// Reading the mode-applied graph alone records the pre-edit text.
func TestCapturedPromptAppliesRunOverrides(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI, Graph: provenanceModePromptGraph}
	_, byTitle := modeKeys(t, provenanceModePromptGraph)
	opts := runOptions{
		ModeSelection: map[string]string{
			modeKeySelector(t, provenanceModePromptGraph): byTitle["MODE-B"],
		},
		UIWidgetOverrides: map[comfy.UIWidgetKey]string{
			{NodeID: "20", Widget: 0}: "an octopus playing chess",
		},
	}
	got := capturePrompts(runSubmittedGraph(wf, opts))
	if len(got) != 1 || got[0].Text != "an octopus playing chess" {
		t.Errorf("captured %+v — the per-run prompt edit was not applied before capture", got)
	}
}

// TestCapturedPromptsKeepPositiveAndNegativeApart pins that a two-encoder graph
// captures TWO entries carrying the author's own titles, rather than one merged blob.
func TestCapturedPromptsKeepPositiveAndNegativeApart(t *testing.T) {
	const graph = `{"nodes":[
	  {"id":10,"type":"CLIPTextEncode","mode":0,"pos":[0,0],"size":[200,100],
	   "title":"POSITIVE","widgets_values":["a red barn"]},
	  {"id":11,"type":"CLIPTextEncode","mode":0,"pos":[0,200],"size":[200,100],
	   "title":"NEGATIVE","widgets_values":["blurry, watermark"]}],
	 "links":[]}`
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI, Graph: graph}

	got := capturePrompts(runSubmittedGraph(wf, runOptions{}))
	if len(got) != 2 {
		t.Fatalf("captured %d prompts, want 2: %+v", len(got), got)
	}
	byLabel := map[string]string{}
	for _, p := range got {
		byLabel[p.Label] = p.Text
	}
	if byLabel["Prompt (POSITIVE)"] != "a red barn" {
		t.Errorf("positive prompt lost its identity: %+v", got)
	}
	if byLabel["Prompt (NEGATIVE)"] != "blurry, watermark" {
		t.Errorf("negative prompt lost its identity: %+v", got)
	}
}

// TestBuildRunParamsSnapshotMarksCaptureEvenWithNoPrompts pins the flag that makes
// the two empty cases distinguishable: a workflow with no prompt input still records
// prompts_captured, so its row can never be mistaken for a pre-change one.
func TestBuildRunParamsSnapshotMarksCaptureEvenWithNoPrompts(t *testing.T) {
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatAPI,
		Graph: `{"1":{"class_type":"X","inputs":{}}}`}
	snap := buildRunParamsSnapshot(wf, runOptions{})
	if !snap.PromptsCaptured {
		t.Error("every capture must mark that it ran, even when it finds no prompt")
	}
	if len(snap.Prompts) != 0 {
		t.Errorf("an api-format graph exposes no widgets_values: %+v", snap.Prompts)
	}
	// And it survives the JSON round trip the store actually performs.
	if !parseRunParams(marshalRunParams(snap)).PromptsCaptured {
		t.Error("prompts_captured did not survive marshal → parse")
	}
}

// promptTextGraph wraps one prompt string in a minimal single-encoder UI graph.
func promptTextGraph(t *testing.T, text string) *store.Workflow {
	t.Helper()
	quoted, err := json.Marshal(text)
	if err != nil {
		t.Fatalf("marshal fixture text: %v", err)
	}
	return &store.Workflow{ID: 1, Format: store.WorkflowFormatUI,
		Graph: `{"nodes":[{"id":10,"type":"CLIPTextEncode","mode":0,"pos":[0,0],"size":[9,9],` +
			`"widgets_values":[` + string(quoted) + `]}],"links":[]}`}
}

// TestCapturedPromptIsBounded pins the BYTE ceiling on what one capture writes into
// the params blob, and that a clipped prompt says so in the stored text rather than
// silently presenting a fragment as the whole prompt.
//
// The fixture size is a LITERAL, deliberately not derived from maxCapturedPromptBytes:
// sizing it by the constant under test made the old version self-referential — raising
// the cap to 1 GiB also passed, because the input grew with it.
func TestCapturedPromptIsBounded(t *testing.T) {
	const fixtureBytes = 32768 // fixed; must stay comfortably above any sane cap
	if maxCapturedPromptBytes >= fixtureBytes {
		t.Fatalf("the cap (%d) has grown past this test's fixed %d-byte fixture — the "+
			"test can no longer prove clipping; raise the fixture deliberately",
			maxCapturedPromptBytes, fixtureBytes)
	}
	got := capturePrompts(runSubmittedGraph(
		promptTextGraph(t, strings.Repeat("a", fixtureBytes)), runOptions{}))
	if len(got) != 1 {
		t.Fatalf("captured %d prompts, want 1", len(got))
	}
	if len(got[0].Text) > maxCapturedPromptBytes+len(promptTruncatedMarker) {
		t.Errorf("captured prompt is %d bytes, past the %d cap",
			len(got[0].Text), maxCapturedPromptBytes)
	}
	if !strings.Contains(got[0].Text, "truncated") {
		t.Errorf("a clipped prompt must say it was clipped (%d bytes in, %d out)",
			fixtureBytes, len(got[0].Text))
	}
}

// TestCapturedPromptCountIsBounded pins the OTHER cap (maxCapturedPrompts). It was
// previously unguarded: deleting the break in capturePrompts left every test green.
//
// The encoder count is a literal for the same reason the byte fixture is.
func TestCapturedPromptCountIsBounded(t *testing.T) {
	const encoders = 26 // fixed; must stay above maxCapturedPrompts
	if maxCapturedPrompts >= encoders {
		t.Fatalf("the prompt cap (%d) has reached this test's fixed %d-encoder fixture "+
			"— it can no longer prove clamping; raise the fixture deliberately",
			maxCapturedPrompts, encoders)
	}
	var nodes []string
	for i := 0; i < encoders; i++ {
		nodes = append(nodes, fmt.Sprintf(
			`{"id":%d,"type":"CLIPTextEncode","mode":0,"pos":[0,%d],"size":[9,9],`+
				`"widgets_values":["prompt %d"]}`, 100+i, i*20, i))
	}
	wf := &store.Workflow{ID: 1, Format: store.WorkflowFormatUI,
		Graph: `{"nodes":[` + strings.Join(nodes, ",") + `],"links":[]}`}

	got := capturePrompts(runSubmittedGraph(wf, runOptions{}))
	if len(got) != maxCapturedPrompts {
		t.Errorf("captured %d prompts from %d encoders; the cap of %d was not applied",
			len(got), encoders, maxCapturedPrompts)
	}
}

// TestCapturedPromptClipLandsOnARuneBoundary is the F2 rewrite. The previous version
// COULD NOT FAIL, for two independent reasons:
//
//  1. its fixture was 2-byte runes, so byte index maxCapturedPromptBytes (an even
//     number) was ALREADY a rune start and the walk-back loop never executed;
//  2. it checked for U+FFFD on the in-memory string, but U+FFFD only appears AFTER a
//     JSON round trip — and the test never marshalled.
//
// Both are fixed here: a 3-byte rune whose repetition guarantees the cut lands
// mid-rune, and the assertion made on the value that survives marshalRunParams →
// parseRunParams, which is what the store actually persists.
func TestCapturedPromptClipLandsOnARuneBoundary(t *testing.T) {
	// \u754c is 3 bytes. The cut lands INSIDE a rune only when the cap is not a
	// multiple of 3 — assert that rather than assume it, so a future cap change cannot
	// quietly defang this test the way the old 2-byte fixture did.
	const r = "\u754c"
	if maxCapturedPromptBytes%len(r) == 0 {
		t.Fatalf("the cap (%d) is now a multiple of %d, so a byte-exact cut would land ON "+
			"a rune boundary and this test would prove nothing — repoint the fixture",
			maxCapturedPromptBytes, len(r))
	}
	wf := promptTextGraph(t, strings.Repeat(r, maxCapturedPromptBytes)) // far over the cap

	// Through the REAL persistence path: encoding/json is what turns a split rune into
	// U+FFFD, so an in-memory check can never see the corruption.
	round := parseRunParams(marshalRunParams(buildRunParamsSnapshot(wf, runOptions{})))
	if len(round.Prompts) != 1 {
		t.Fatalf("round-tripped %d prompts, want 1", len(round.Prompts))
	}
	text := round.Prompts[0].Text
	if !utf8.ValidString(text) {
		t.Errorf("the persisted prompt is not valid UTF-8 — the clip split a rune")
	}
	if strings.ContainsRune(text, utf8.RuneError) {
		t.Errorf("the persisted prompt carries U+FFFD — encoding/json replaced a rune "+
			"the clip had split (tail: %q)", text[max(0, len(text)-48):])
	}
}

// ---------------------------------------------------------------------------
// Item 2 — the per-workflow "Recent outputs" strip
// ---------------------------------------------------------------------------

func TestWorkflowOutputsStripRendersNothingWithoutOutputs(t *testing.T) {
	if node := workflowOutputsStrip(3, nil); node != nil {
		t.Error("a workflow with no outputs must render NO strip, not an empty one")
	}
	if node := workflowOutputsStrip(3, []store.Generation{}); node != nil {
		t.Error("an empty (non-nil) slice must also render no strip")
	}
}

func TestWorkflowOutputsStripRendersTilesAndViewAll(t *testing.T) {
	wfID := int64(3)
	gens := []store.Generation{
		{ID: 1, WorkflowID: &wfID, WorkflowName: "wf", FirstImageID: 11, ImageCount: 1},
		{ID: 2, WorkflowID: &wfID, WorkflowName: "wf", FirstImageID: 12, ImageCount: 1},
		{ID: 3, WorkflowID: &wfID, WorkflowName: "wf", FirstImageID: 13, ImageCount: 1},
	}
	html := renderString(t, workflowOutputsStrip(wfID, gens))

	if n := strings.Count(html, "/outputs/img/"); n != len(gens) {
		t.Errorf("strip rendered %d thumbnails, want %d:\n%s", n, len(gens), html)
	}
	for _, gen := range gens {
		want := `href="/outputs/` + strconv.FormatInt(gen.ID, 10) + `"`
		if !strings.Contains(html, want) {
			t.Errorf("each tile must link to its generation (%s):\n%s", want, html)
		}
	}
	if !strings.Contains(html, `href="/outputs?workflow=3"`) {
		t.Errorf("the strip must offer a view-all into the filtered gallery:\n%s", html)
	}
	// It reuses the SHARED gallery tile, not a third renderer.
	if !strings.Contains(html, "cm-masonry-item") {
		t.Errorf("the strip must reuse generationTile:\n%s", html)
	}
}

// TestWorkflowDetailStripQueryIsBounded pins the discipline
// store.ListGenerations' bounded callers document: a strip rendered on every workflow view
// must read a small FIXED number of rows, never the whole gallery.
//
// It is asserted through the rendered page — the observable consequence — with more
// seeded outputs than the limit, so a handler that dropped the Limit would show them
// all and fail here.
func TestWorkflowDetailStripQueryIsBounded(t *testing.T) {
	if workflowOutputsStripLimit <= 0 || workflowOutputsStripLimit > outputsRailLimit {
		t.Fatalf("the strip limit (%d) must be small and fixed", workflowOutputsStripLimit)
	}
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	const seeded = workflowOutputsStripLimit + 4
	for i := 0; i < seeded; i++ {
		seedGenWithParams(t, srv, root, &wf, "wf-"+strconv.Itoa(i), capturedProvenanceParams, "")
	}

	main := pageMain(get(t, srv, "/workflows/"+strconv.FormatInt(wf, 10)).Body.String())
	if n := strings.Count(main, "/outputs/img/"); n != workflowOutputsStripLimit {
		t.Errorf("the strip rendered %d of %d seeded outputs; it must be clamped to %d",
			n, seeded, workflowOutputsStripLimit)
	}
}

// TestWorkflowDetailStripIsPerWorkflow pins that the strip shows THIS workflow's
// outputs and not the global feed — that is the whole reason it exists beside the
// cross-workflow rail.
func TestWorkflowDetailStripIsPerWorkflow(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	mine := seedWF(t, srv, "mine")
	theirs := seedWF(t, srv, "theirs")
	seedGen(t, srv, root, &mine, "mine-out", []byte("X"))
	seedGen(t, srv, root, &theirs, "theirs-out", []byte("Y"))

	main := pageMain(get(t, srv, "/workflows/"+strconv.FormatInt(mine, 10)).Body.String())
	if !strings.Contains(main, "mine-out") {
		t.Errorf("this workflow's own output is missing from its strip:\n%s", main)
	}
	if strings.Contains(main, "theirs-out") {
		t.Errorf("another workflow's output leaked into the strip:\n%s", main)
	}
	// And a never-run workflow gets no strip at all.
	other := pageMain(get(t, srv, "/workflows/"+strconv.FormatInt(seedWF(t, srv, "unused"), 10)).Body.String())
	if strings.Contains(other, "Recent outputs") {
		t.Errorf("a workflow with no outputs must render no strip:\n%s", other)
	}
}

// TestWorkflowOutputsStripUsesTheExistingPerWorkflowFilter pins that the bounded read
// goes through store.ListGenerationsOpts.WorkflowID rather than a new query — the
// store-level contract the handler depends on.
func TestWorkflowOutputsStripUsesTheExistingPerWorkflowFilter(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	mine := seedWF(t, srv, "mine")
	other := seedWF(t, srv, "other")
	for i := 0; i < workflowOutputsStripLimit+3; i++ {
		seedGenWithParams(t, srv, root, &mine, "m"+strconv.Itoa(i), "", "")
	}
	seedGenWithParams(t, srv, root, &other, "o", "", "")

	got, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{
		WorkflowID: &mine, Limit: workflowOutputsStripLimit,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != workflowOutputsStripLimit {
		t.Fatalf("got %d rows, want the limit (%d)", len(got), workflowOutputsStripLimit)
	}
	for _, gen := range got {
		if gen.WorkflowID == nil || *gen.WorkflowID != mine {
			t.Fatalf("the filter leaked another workflow's row: %+v", gen)
		}
	}
}
