package web

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ===========================================================================
// The two workflow<->model relations, rendered.
// ===========================================================================
//
// The load-bearing assertion in this file is not "a link appears" but that the
// USES relation and the IMPORTED-FROM relation are described with DIFFERENT
// words. Conflating them tells a user a workflow came from a model it merely
// loads a file from.

func linkIntPtr(v int) *int { return &v }

// seedLinkFile indexes a local file linked to a civitai model/version.
func seedLinkFile(t *testing.T, srv *Server, path string, modelID, versionID int) {
	t.Helper()
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: path, SHA256: "sha-" + path, Kind: store.LocalKindModel,
		Status:  store.LocalStatusMatched,
		ModelID: linkIntPtr(modelID), VersionID: linkIntPtr(versionID),
	}); err != nil {
		t.Fatalf("upsert local file: %v", err)
	}
}

// seedLinkWF stores a workflow with resources and an optional imported-from model.
func seedLinkWF(t *testing.T, srv *Server, name string, resources []string, fromModel int) int64 {
	t.Helper()
	wf := &store.Workflow{
		Name: name, Format: store.WorkflowFormatAPI,
		Graph: `{"1":{"class_type":"X","inputs":{}}}`, Source: "authored",
		Resources: resources,
	}
	if fromModel > 0 {
		wf.ModelID = linkIntPtr(fromModel)
		wf.VersionID = linkIntPtr(900)
	}
	id, err := srv.store.InsertWorkflow(context.Background(), wf)
	if err != nil {
		t.Fatalf("insert workflow: %v", err)
	}
	return id
}

// TestModelPageListsWorkflowsThatUseIt is the forward direction: the model page
// lists the library workflows that REFERENCE one of its files, and only those.
func TestModelPageListsWorkflowsThatUseIt(t *testing.T) {
	srv := newTestServer(t)
	// stubReader's GetModel always answers model id 1.
	seedLinkFile(t, srv, "/models/loras/wai_v140.safetensors", 1, 900)

	seedLinkWF(t, srv, "USES THE FILE", []string{"seg-a/wai_v140.safetensors"}, 0)
	// Shares the model's NAME but references nothing of it.
	seedLinkWF(t, srv, "wai_v140 companion pack", []string{"unrelated.safetensors"}, 0)
	// Neither relation at all.
	seedLinkWF(t, srv, "TOTALLY UNRELATED", []string{"other.safetensors"}, 0)

	body := get(t, srv, "/models/1").Body.String()

	if !strings.Contains(body, "Workflows that use this model") {
		t.Fatalf("the model page has no usage section; body = %q", firstN(body, 1200))
	}
	if !strings.Contains(body, "USES THE FILE") {
		t.Error("a workflow referencing one of this model's files must be listed")
	}
	if strings.Contains(body, "companion pack") {
		t.Error("a workflow that merely SHARES A NAME with the model must NOT be listed")
	}
	if strings.Contains(body, "TOTALLY UNRELATED") {
		t.Error("a workflow with neither relation must appear in neither list")
	}
	// The claim names the file it rests on.
	if !strings.Contains(body, "seg-a/wai_v140.safetensors") {
		t.Error("the usage row must name the matched file — it is the whole basis of the claim")
	}
	// …and it links to the workflow.
	if !strings.Contains(body, `href="/workflows/`) {
		t.Error("a usage row must link to the workflow")
	}
}

// TestUsesAndImportedFromRenderWithDifferentLabels is the anti-conflation guard.
//
// NOTE: everything here goes through ONE server on purpose. store.Open(":memory:")
// uses `file::memory:?cache=shared`, so two Servers built in the same test share
// ONE database — a second server would not give the isolation it looks like it
// gives (this test failed exactly that way when first written).
func TestUsesAndImportedFromRenderWithDifferentLabels(t *testing.T) {
	srv := newTestServer(t)
	seedLinkFile(t, srv, "/models/loras/f1.safetensors", 1, 900)
	// A workflow carrying BOTH relations to model 1 — the case where the two labels
	// have to coexist on one row without merging.
	seedLinkWF(t, srv, "BOTH RELATIONS", []string{"f1.safetensors"}, 1)
	// …and one carrying ONLY the imported-from relation. It must not appear in the
	// USES list, because it references nothing of this model.
	seedLinkWF(t, srv, "IMPORTED ONLY", []string{"something-else.safetensors"}, 1)

	body := get(t, srv, "/models/1").Body.String()

	// The USES vocabulary.
	if !strings.Contains(body, "Workflows that use this model") {
		t.Error("the uses section must be labelled with the USES vocabulary")
	}
	if !strings.Contains(body, "BOTH RELATIONS") {
		t.Error("the workflow that references one of this model's files must be listed")
	}
	// The IMPORTED-FROM vocabulary, as a separate badge on the same row.
	if !strings.Contains(body, "also imported from this model") {
		t.Error("a row carrying BOTH relations must additionally name the imported-from one")
	}
	// The uses heading must never borrow the imported-from wording.
	if strings.Contains(body, "Workflows imported from this model that use") {
		t.Error("the two relations' copy has been merged")
	}
	// 🔴 The whole point: imported-from is NOT uses.
	if strings.Contains(body, "IMPORTED ONLY") {
		t.Error("a workflow that was merely IMPORTED FROM this model must not be listed " +
			"as one that USES it — that is the conflation this section exists to avoid")
	}
}

// TestModelPageWithNoWorkflowUsageRendersNoSection: the section degrades to
// nothing rather than to an empty heading.
func TestModelPageWithNoWorkflowUsageRendersNoSection(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/models/1")
	if rec.Code != 200 {
		t.Fatalf("model page status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Workflows that use this model") {
		t.Error("a model nothing references must render no usage section at all")
	}
}

// TestModelPageSurvivesAUsageQueryFailure: the section is an informational
// cross-reference; a local query failing must never break the page.
func TestModelPageSurvivesAUsageQueryFailure(t *testing.T) {
	srv := newTestServer(t)
	if got := srv.workflowsUsingModel(context.Background(), 0); got != nil {
		t.Errorf("a non-positive model id must yield nil, got %+v", got)
	}
	if err := srv.store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := srv.workflowsUsingModel(context.Background(), 1); got != nil {
		t.Errorf("a failed usage query must degrade to nil, got %+v", got)
	}
}

// TestWorkflowDetailLinksBackToTheModelsItUses is the REVERSE direction on the
// detail page, and that it stays distinct from the provenance row.
func TestWorkflowDetailLinksBackToTheModelsItUses(t *testing.T) {
	srv := newTestServer(t)
	seedLinkFile(t, srv, "/models/loras/used_by_wf.safetensors", 77, 901)
	id := seedLinkWF(t, srv, "wf", []string{"loras/used_by_wf.safetensors"}, 0)

	body := get(t, srv, "/workflows/"+strconv.FormatInt(id, 10)).Body.String()

	if !strings.Contains(body, "cm-uses-link") {
		t.Fatalf("the workflow detail page has no reverse model link; body = %q", firstN(body, 1500))
	}
	if !strings.Contains(body, `href="/models/77?modelVersionId=901"`) {
		t.Error("the reverse link must point at the model+version the file belongs to")
	}
	// Labelled "Uses", NOT "from" (which is the imported-from wording two rows up).
	if !strings.Contains(body, ">Uses<") {
		t.Error(`the reverse row must be labelled "Uses"`)
	}
}

// TestWorkflowWithNoResolvedResourcesHasNoUsesRow: nothing resolved → no row,
// never an empty label.
func TestWorkflowWithNoResolvedResourcesHasNoUsesRow(t *testing.T) {
	srv := newTestServer(t)
	// A resource that is NOT in the local index at all.
	id := seedLinkWF(t, srv, "wf", []string{"never-seen.safetensors"}, 0)

	body := get(t, srv, "/workflows/"+strconv.FormatInt(id, 10)).Body.String()
	if strings.Contains(body, "cm-uses-link") {
		t.Error("an unresolved resource must produce no reverse model link")
	}
	if strings.Contains(body, ">Uses<") {
		t.Error("an unresolved resource must produce no empty Uses row")
	}
}

// TestWorkflowListItemShowsTheUsesRelation: the in-library list card carries the
// same reverse link-back, in its compact form — and still says "from" for the
// separate imported-from linkage.
func TestWorkflowListItemShowsTheUsesRelation(t *testing.T) {
	srv := newTestServer(t)
	seedLinkFile(t, srv, "/models/loras/lc.safetensors", 55, 902)
	// Imported FROM model 1, USING a file of model 55 — the exact case the two
	// vocabularies exist to keep apart.
	seedLinkWF(t, srv, "mixed", []string{"lc.safetensors"}, 1)

	body := pageMain(get(t, srv, "/library?tab=workflows").Body.String())

	if !strings.Contains(body, "cm-uses-link") {
		t.Fatalf("the workflow list card has no uses link; body = %q", firstN(body, 2000))
	}
	if !strings.Contains(body, `href="/models/55?modelVersionId=902"`) {
		t.Error("the list card's uses link must point at the USED model (55), not the imported-from one (1)")
	}
	if !strings.Contains(body, "uses ") {
		t.Error(`the list card must describe the relation as "uses"`)
	}
	// The imported-from linkage keeps its own, different wording.
	if !strings.Contains(body, "from ") {
		t.Error(`the imported-from linkage must keep its "from" wording`)
	}
}

// TestUsedModelsAreDedupedAndCapped pins the aggregation rules of the reverse
// row without needing a rendered page.
func TestUsedModelsAreDedupedAndCapped(t *testing.T) {
	// A resolver whose files all map to model 5 except one.
	resolver := workflowResolver{
		localResource: func(basename string) (resourceInfo, bool) {
			switch basename {
			case "a.safetensors", "b.safetensors":
				return resourceInfo{ModelID: 5, VersionID: 50}, true
			case "c.safetensors":
				return resourceInfo{ModelID: 6, VersionID: 60}, true
			case "unlinked.safetensors":
				// Present locally but with no civitai linkage — must be skipped, not
				// rendered as a link to model 0.
				return resourceInfo{Path: "/x/unlinked.safetensors"}, true
			}
			return resourceInfo{}, false
		},
	}
	used := workflowUsedModels([]string{
		"a.safetensors", "dir/b.safetensors", "c.safetensors",
		"unlinked.safetensors", "unknown.safetensors",
	}, resolver)

	if len(used) != 2 {
		t.Fatalf("used models = %+v, want exactly models 5 and 6", used)
	}
	if used[0].ModelID != 5 || len(used[0].Files) != 2 {
		t.Errorf("model 5 should collect BOTH of its files: %+v", used[0])
	}
	if used[1].ModelID != 6 {
		t.Errorf("second model = %d, want 6 (first-reference order)", used[1].ModelID)
	}
	// A zero resolver claims nothing.
	if got := workflowUsedModels([]string{"a.safetensors"}, workflowResolver{}); got != nil {
		t.Errorf("a resolver with no local lookup must claim nothing, got %+v", got)
	}
}
