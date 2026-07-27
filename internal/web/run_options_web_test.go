package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// badOptionObjectInfo has a detector-provider whose model_name combo lists two
// installed files (neither is the graph's saved value) and a single-choice wildcard
// picker — so a graph carrying the drifted values fails preflight ONLY on bad
// options (no missing nodes, no missing models).
const badOptionObjectInfo = `{
  "UltralyticsDetectorProvider":{"input":{"required":{"model_name":[["bbox/face_yolov8m.pt","bbox/hand_yolov8s.pt"],{}]}},"input_order":{"required":["model_name"]}},
  "ImpactWildcardProcessor":{"input":{"required":{"Select to add Wildcard":[["Select the Wildcard to add to the text"],{}]}},"input_order":{"required":["Select to add Wildcard"]}}
}`

// badOptionGraph references the detector value bbox/face_yolov9c.pt (not installed)
// on two nodes and the wildcard label drift on one node.
const badOptionGraph = `{
  "42":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}},
  "9":{"class_type":"UltralyticsDetectorProvider","inputs":{"model_name":"bbox/face_yolov9c.pt"}},
  "3":{"class_type":"ImpactWildcardProcessor","inputs":{"Select to add Wildcard":"Select Wildcard 🟢 Full Cache"}}
}`

func newBadOptionServer(t *testing.T) (*Server, *fakeComfy, string) {
	t.Helper()
	srv := newLibraryTestServer(t, t.TempDir())
	fake := &fakeComfy{
		info: mustObjectInfo(t, badOptionObjectInfo),
		history: &comfy.HistoryEntry{
			Outputs: map[string]comfy.NodeOutput{"42": {Images: []comfy.ImageRef{{Filename: "o.png", Type: "output"}}}},
			Status:  comfy.HistoryStatus{Completed: true, StatusStr: "success"},
		},
	}
	srv.comfyClientFn = func() comfyClient { return fake }
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, badOptionGraph)
	return srv, fake, id
}

// TestIncompatibleOptionsSectionRenders drives the real preflight-fail path and
// asserts the Incompatible-options section: each group's class/input/current value,
// a <select> of choices (single-choice pre-selected), the CSRF-carrying run button,
// escaped untrusted values, no external asset, and NO poller (terminal fragment).
func TestIncompatibleOptionsSectionRenders(t *testing.T) {
	srv, _, id := newBadOptionServer(t)

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	for _, want := range []string{
		"Incompatible options",
		"UltralyticsDetectorProvider",            // group 1 class
		"model_name",                             // group 1 input
		"bbox/face_yolov9c.pt",                   // current invalid value
		"ImpactWildcardProcessor",                // group 2 class
		"Select to add Wildcard",                 // group 2 input
		`name="opt_new"`,                         // the choice select
		`name="opt_input"`,                       // hidden group input
		`name="opt_old"`,                         // hidden group old value
		`<option value="bbox/face_yolov8m.pt"`,   // a real choice option
		"Run with selected options",              // the submit
		"/workflows/" + id + "/run-with-options", // the endpoint
	} {
		if !strings.Contains(body, want) {
			t.Errorf("section missing %q:\n%s", want, body)
		}
	}

	// Single-choice wildcard group: the only option must be pre-selected.
	if !strings.Contains(body, `<option value="Select the Wildcard to add to the text" selected`) {
		t.Errorf("single-choice group should pre-select its only option:\n%s", body)
	}
	// The wildcard emoji value is untrusted — it must appear escaped as an attribute/
	// text, never as raw markup injection (no <script>, and the JSON/attr escaping via
	// gomponents keeps it inert). Assert the value is present (escaped text form).
	if !strings.Contains(body, "Select Wildcard") {
		t.Errorf("current wildcard value should be shown:\n%s", body)
	}
	// CSRF hidden input carrying the server token.
	if !strings.Contains(body, `name="csrf_token"`) || !strings.Contains(body, srv.csrf) {
		t.Errorf("run form missing CSRF token:\n%s", body)
	}
	// The section lives in the terminal fragment → NO poller.
	if hasRunPoller(body) {
		t.Errorf("incompatible-options section must be in the poller-free terminal fragment:\n%s", body)
	}
	// Offline invariant: the section introduces no inline <script> asset.
	if strings.Contains(body, "<script") {
		t.Errorf("section must not introduce a <script>:\n%s", body)
	}
}

// TestRunWithOptionsAppliesFixAndRuns drives the real run path: the chosen valid
// values swap the drifted combos, preflight passes, and the FIXED graph is submitted
// — while the stored workflow row stays byte-identical.
func TestRunWithOptionsAppliesFixAndRuns(t *testing.T) {
	srv, fake, id := newBadOptionServer(t)

	rec := post(t, srv, "/workflows/"+id+"/run-with-options", url.Values{
		"opt_input": {"model_name", "Select to add Wildcard"},
		"opt_old":   {"bbox/face_yolov9c.pt", "Select Wildcard 🟢 Full Cache"},
		"opt_new":   {"bbox/face_yolov8m.pt", "Select the Wildcard to add to the text"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run-with-options = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	if !fake.submitCalled {
		t.Fatal("Submit should have been called after a passing option-fix preflight")
	}
	sg := string(fake.submittedGraph)
	if !strings.Contains(sg, "bbox/face_yolov8m.pt") {
		t.Errorf("submitted graph missing the chosen model_name value: %s", sg)
	}
	if strings.Contains(sg, "bbox/face_yolov9c.pt") {
		t.Errorf("submitted graph still references the drifted model_name value: %s", sg)
	}
	if !strings.Contains(sg, "Select the Wildcard to add to the text") ||
		strings.Contains(sg, "Full Cache") {
		t.Errorf("wildcard combo was not fixed in the submitted graph: %s", sg)
	}
	if !strings.Contains(body, "Run complete") {
		t.Errorf("terminal body missing completion:\n%s", body)
	}
	// The STORED workflow row must be untouched.
	wfID, _ := strconv.ParseInt(id, 10, 64)
	wf, err := srv.store.GetWorkflow(context.Background(), wfID)
	if err != nil {
		t.Fatalf("reload workflow: %v", err)
	}
	if wf.Graph != badOptionGraph {
		t.Errorf("stored workflow graph was mutated:\ngot:  %s\nwant: %s", wf.Graph, badOptionGraph)
	}
}

// TestRunWithOptionsOffListRejected asserts an off-list chosen value is NOT injected:
// preflight still fails on the bad option and nothing is submitted.
func TestRunWithOptionsOffListRejected(t *testing.T) {
	srv, fake, id := newBadOptionServer(t)

	rec := post(t, srv, "/workflows/"+id+"/run-with-options", url.Values{
		"opt_input": {"model_name"},
		"opt_old":   {"bbox/face_yolov9c.pt"},
		"opt_new":   {"bbox/EVIL_not_installed.pt"}, // off-list → must be refused
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run-with-options = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)

	if fake.submitCalled {
		t.Errorf("an off-list value must NOT be injected/submitted; submitted: %s", fake.submittedGraph)
	}
	// The run should still report the incompatible option (preflight failed again).
	if !strings.Contains(body, "Incompatible options") {
		t.Errorf("expected the incompatible-options section to persist:\n%s", body)
	}
	if strings.Contains(body, "EVIL_not_installed") && strings.Contains(body, `name="opt_new"`) {
		// The rejected value must not appear as an injected choice; it may appear only
		// in a benign context, but never as a valid <option>.
		if strings.Contains(body, `<option value="bbox/EVIL_not_installed.pt"`) {
			t.Errorf("off-list value leaked into the choice list:\n%s", body)
		}
	}
}

// TestRunWithOptionsGating asserts CSRF-reject + loopback-gating for the endpoint.
func TestRunWithOptionsGating(t *testing.T) {
	srv, _, id := newBadOptionServer(t)
	form := url.Values{
		"opt_input": {"model_name"},
		"opt_old":   {"bbox/face_yolov9c.pt"},
		"opt_new":   {"bbox/face_yolov8m.pt"},
	}
	if rec := post(t, srv, "/workflows/"+id+"/run-with-options", form, false); rec.Code != http.StatusForbidden {
		t.Fatalf("run-with-options without CSRF = %d, want 403", rec.Code)
	}

	// Non-loopback bind → gated note (no run started).
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	gated := NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour, Addr: "0.0.0.0:8787",
	}, nil)
	gid := seedWorkflow(t, gated, store.WorkflowFormatAPI, badOptionGraph)
	rec := post(t, gated, "/workflows/"+gid+"/run-with-options", form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Errorf("gated run-with-options = %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestRunWithOptionsOneAtATime asserts a run-with-options call is a no-op while
// another run is in flight (the one-run-at-a-time invariant).
func TestRunWithOptionsOneAtATime(t *testing.T) {
	srv := newLibraryTestServer(t, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"3":{"class_type":"X","inputs":{}}}`)

	started := make(chan struct{})
	release := make(chan struct{})
	var calls int32
	srv.runFn = func(ctx context.Context, wf *store.Workflow, up runUpdater, opts runOptions) (*runResult, error) {
		calls++
		up.setPhase(runPhaseRunning, "Generating…", 0)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &runResult{Images: []comfy.ImageRef{{Filename: "o.png", Type: "output"}}, PromptID: "p1"}, nil
	}

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("first run = %d", rec.Code)
	}
	<-started
	rec := post(t, srv, "/workflows/"+id+"/run-with-options", url.Values{
		"opt_input": {"model_name"}, "opt_old": {"x"}, "opt_new": {"y"},
	}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("run-with-options while running = %d", rec.Code)
	}
	close(release)
	pollRunUntilDone(t, srv, id)
	if calls != 1 {
		t.Errorf("runFn called %d times, want 1 (one run at a time)", calls)
	}
}
