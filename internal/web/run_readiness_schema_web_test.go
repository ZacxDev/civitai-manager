package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// TestReadinessDecodesTheNodeSchemaOncePerPayload pins the memo.
//
// 🔴 IT COUNTS DECODES; IT DOES NOT TIME THEM. A timing assertion over an ~80 ms
// decode is precisely the guard that goes flaky under -race and then gets deleted,
// and a fast second request is equally consistent with an OS page cache. The
// counter is the observable that can only move when the payload is actually parsed.
//
// The three assertions are separate claims and each can fail on its own:
//
//  1. repeated views of the SAME payload decode once — the memo is consulted;
//  2. a CHANGED payload decodes again — the memo is keyed on content, so a ComfyUI
//     whose node list changed cannot be answered from a stale schema;
//  3. the answer TRACKS the change — proof that (2) is not merely a counter moving
//     while the readiness line keeps reporting the old schema's verdict.
func TestReadinessDecodesTheNodeSchemaOncePerPayload(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, readinessInfo)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	for i := 0; i < 4; i++ {
		wantState(t, readiness(t, srv, id), "ready", "")
	}
	if got := srv.schemaMemo.decodes.Load(); got != 1 {
		t.Fatalf("decoded the node schema %d times across 4 page views, want 1", got)
	}

	// A DIFFERENT payload: same node types minus KSampler's scheduler choices, so the
	// graph's saved "normal" becomes a bad option. Content-keyed means this must
	// re-decode AND must change the answer.
	seedObjectInfo(t, srv, `{
	  "CheckpointLoaderSimple": {"python_module":"nodes",
	    "input":{"required":{"ckpt_name":[["comfy_has_this.safetensors"],{}]}},
	    "input_order":{"required":["ckpt_name"]}},
	  "KSampler": {"python_module":"nodes",
	    "input":{"required":{
	      "sampler_name":[["euler","dpmpp_2m"],{}],
	      "scheduler":[["karras"],{}]}},
	    "input_order":{"required":["sampler_name","scheduler"]}}
	}`)

	body := readiness(t, srv, id)
	if got := srv.schemaMemo.decodes.Load(); got != 2 {
		t.Errorf("a CHANGED payload decoded %d times in total, want 2 — the memo is "+
			"serving a stale schema", got)
	}
	wantState(t, body, "needs", "")
	if !strings.Contains(body, "1 saved option value") {
		t.Errorf("the answer did not track the changed schema:\n%s", body)
	}
}

// TestReadinessNeverMemoisesAnUnusableSchema pins that a corrupt row cannot park an
// unusable value where a later good write would be shadowed by it.
func TestReadinessNeverMemoisesAnUnusableSchema(t *testing.T) {
	srv := newReadinessServer(t)
	seedObjectInfo(t, srv, `{"CheckpointLoaderSimple": THIS IS NOT JSON`)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, readinessReadyGraph)

	wantState(t, readiness(t, srv, id), "unknown", "schema")
	wantState(t, readiness(t, srv, id), "unknown", "schema")
	// PRECONDITION on the fixture: it really reached the decode both times. If the
	// bad row were memoised the second attempt would not have parsed anything.
	if got := srv.schemaMemo.decodes.Load(); got != 2 {
		t.Fatalf("a corrupt row was decoded %d times, want 2 — a failed decode must "+
			"never be cached", got)
	}
	// And a later GOOD write is answered normally rather than shadowed.
	seedObjectInfo(t, srv, readinessInfo)
	wantState(t, readiness(t, srv, id), "ready", "")
}
