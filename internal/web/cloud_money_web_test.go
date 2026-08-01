package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ── the nodepack limitation ──────────────────────────────────────────────────

// customNodeGraph references a class_type outside the core-node set, so
// comfy.ResolveResources emits a ResolveCustomNode row for it. It is the fixture
// that REACHES the interesting case — asserted explicitly below rather than assumed,
// because a graph whose nodes all resolve would make every check here vacuous.
const customNodeGraph = `{
  "1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"foo.safetensors"}},
  "2":{"class_type":"ImpactWildcardProcessor","inputs":{}}
}`

// emptyLookup is a ResourceLookup that knows nothing, so every model ref resolves
// to "unresolved" and every non-core class_type to "custom node" — which is exactly
// the state a fresh install is in, and the one the blocker is about.
type emptyLookup struct{}

func (emptyLookup) LocalFileByBasename(string) (*comfy.LocalMatch, error) { return nil, nil }
func (emptyLookup) ModelTypeBaseModel(int, int) (string, string, bool)    { return "", "", false }

func resolveForTest(t *testing.T, graph string) []comfy.ResolvedResource {
	t.Helper()
	rows, err := comfy.ResolveResources(json.RawMessage(graph), emptyLookup{})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return rows
}

// TestCloudNodepackBlockerRendersBeforeTheResourceTable is the placement guard, and
// placement is the whole feature.
//
// A custom-node row renders a "custom node" badge and "(fill in below)" in the AIR
// URN column, which reads as the user's unfinished homework. It is not: CustomComfy
// rejects a bare comfy:nodepack URN at submit, so filling those rows in perfectly
// still fails. Saying so BELOW the table would be saying it after the effort.
func TestCloudNodepackBlockerRendersBeforeTheResourceTable(t *testing.T) {
	rows := resolveForTest(t, customNodeGraph)

	// The fixture reaches the case: at least one row really is a custom node.
	custom := 0
	for _, r := range rows {
		if r.Status == comfy.ResolveCustomNode {
			custom++
		}
	}
	if custom == 0 {
		t.Fatalf("fixture produced no custom-node row — every assertion below would be vacuous: %+v", rows)
	}

	body := renderString(t, cloudPanelFragment(cloudPanelView{
		wfID: 1, enabled: true, runnable: true, rows: rows,
	}, "tok"))

	blocker := strings.Index(body, "CivitAI Cloud cannot run yet")
	table := strings.Index(body, "<table")
	if blocker < 0 {
		t.Fatalf("no nodepack blocker for a workflow full of custom nodes:\n%s", body)
	}
	if table < 0 {
		t.Fatalf("no resource table at all:\n%s", body)
	}
	if blocker > table {
		t.Errorf("the blocker must render ABOVE the resource table (blocker=%d, table=%d) — "+
			"below it, the user has already worked through the URN column", blocker, table)
	}
	// It must say the URN column is NOT the fix, which is the specific misreading.
	if !strings.Contains(body, "will not change that") {
		t.Errorf("the blocker must say that filling in the URNs does not help:\n%s", body)
	}
	// It must not silently disable the free estimate — that is how you find out what
	// CivitAI does today rather than what this repo recorded.
	if !strings.Contains(body, "Estimate cost") {
		t.Errorf("the free estimate must stay available:\n%s", body)
	}
}

// TestCloudNodepackBlockerIsAbsentWithoutCustomNodes is the other half: a workflow
// whose resources all resolve must NOT be told about a limitation it does not hit.
// Without this, a blocker that rendered unconditionally would pass the test above.
func TestCloudNodepackBlockerIsAbsentWithoutCustomNodes(t *testing.T) {
	rows := resolveForTest(t,
		`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"foo.safetensors"}}}`)
	for _, r := range rows {
		if r.Status == comfy.ResolveCustomNode {
			t.Fatalf("fixture unexpectedly produced a custom-node row: %+v", rows)
		}
	}
	body := renderString(t, cloudPanelFragment(cloudPanelView{
		wfID: 1, enabled: true, runnable: true, rows: rows,
	}, "tok"))
	if strings.Contains(body, "CivitAI Cloud cannot run yet") {
		t.Errorf("a fully-resolved workflow must not be warned about node packs:\n%s", body)
	}
}

// TestCloudNodepackBlockerEscapesClassTypes — the class types come out of an
// untrusted imported graph.
func TestCloudNodepackBlockerEscapesClassTypes(t *testing.T) {
	body := renderString(t, cloudNodepackBlocker([]comfy.ResolvedResource{
		{Filename: `<script>alert(1)</script>`, Status: comfy.ResolveCustomNode},
	}))
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("class_type must be escaped:\n%s", body)
	}
}

// ── who spends the Buzz ──────────────────────────────────────────────────────

// TestCloudConnectSaysTheTokenSpendsTheBuzz pins the sentence that was missing.
//
// There is no OAuth in this app and no per-run approval from CivitAI: the configured
// API token IS the spending authority. A user hit "Run for real (spends Buzz)" and
// reasonably expected to be asked to authorise it. Nothing was broken — it had never
// been said. This asserts it is said in the connect block, which renders above the
// panel on every cloud surface.
func TestCloudConnectSaysTheTokenSpendsTheBuzz(t *testing.T) {
	body := renderString(t, cloudConnectFragment(cloudConnectView{
		wfID: 1, hasToken: true, redactedToken: "****cdef", enabled: true,
	}, "tok"))

	for _, want := range []string{
		"SPENDS your Buzz",    // what the token does
		"no separate sign-in", // why nothing else will ask
		"per-run approval",    // …and that CivitAI will not either
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the connect block never says %q:\n%s", want, body)
		}
	}
	// The redaction rule is untouched: last four only, never the raw value.
	if !strings.Contains(body, "****cdef") {
		t.Errorf("the redacted token line disappeared:\n%s", body)
	}
}

// ── a rejected token ─────────────────────────────────────────────────────────

// TestCloudRejectedTokenIsNamedAsSuch pins the 401/403 classification.
//
// CivitAI answers a bad/expired/revoked token with bodies like "Unauthorized" — text
// that cannot tell the user that THIS APP resolves a token from four layers and one
// of them is stale. The remote detail is kept (the API is the authority on what went
// wrong); the local cause is added in front of it.
func TestCloudRejectedTokenIsNamedAsSuch(t *testing.T) {
	for _, code := range []int{401, 403} {
		fake := &fakeCloud{whatifErr: &comfy.CloudProblem{
			StatusCode: code, Title: "Unauthorized", Detail: "Unauthorized",
		}}
		srv := newCloudTestServer(t, fake)
		id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
		body := post(t, srv, "/workflows/"+id+"/cloud/whatif",
			url.Values{"resources": {"u"}}, true).Body.String()

		if !strings.Contains(body, "rejected your API token") {
			t.Errorf("a %d must be named as a token rejection, not left as raw remote text:\n%s",
				code, body)
		}
		// The remote text survives — we ADD context, we never replace the authority.
		if !strings.Contains(body, "CivitAI said: Unauthorized") {
			t.Errorf("the remote detail must still be shown for a %d:\n%s", code, body)
		}
	}
}

// TestCloudNon401ErrorsAreLeftAlone bounds the change. A 500 must NOT be blamed on
// the token — that would send the user to rotate a credential that is fine. Without
// this, a prefix applied to every error would pass the test above.
func TestCloudNon401ErrorsAreLeftAlone(t *testing.T) {
	fake := &fakeCloud{whatifErr: &comfy.CloudProblem{
		StatusCode: 500, Title: "Server Error", Detail: "upstream exploded",
	}}
	srv := newCloudTestServer(t, fake)
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, `{"1":{"class_type":"X","inputs":{}}}`)
	body := post(t, srv, "/workflows/"+id+"/cloud/whatif",
		url.Values{"resources": {"u"}}, true).Body.String()

	if strings.Contains(body, "rejected your API token") {
		t.Errorf("a 500 must not be blamed on the token:\n%s", body)
	}
	if !strings.Contains(body, "upstream exploded") {
		t.Errorf("the remote detail must still be surfaced:\n%s", body)
	}
}
