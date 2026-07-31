package civitai_test

import (
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	sdk "github.com/civitai/cli/pkg/civitai"
)

// TestIsWorkflowPostOnBothPayloadShapes pins the predicate against the two
// shapes the app actually renders — the DETAIL shape (ModelDetail, the model
// page) and the LIST shape (ModelListItem, every search/discover card, which
// carries no version data at all). Both must answer identically, because the
// download half of the shipped bug survived precisely by the render layer and
// the action layer disagreeing about how to spell this test.
func TestIsWorkflowPostOnBothPayloadShapes(t *testing.T) {
	// Detail shape, built to the MEASURED workflow-post payload: a populated
	// baseModel, a downloadUrl, and a primary file with a real SHA256 — i.e.
	// nothing here is distinguishable from a checkpoint by ABSENCE.
	detail := &sdk.ModelDetail{
		ID:   1847730,
		Name: "WAN 2.2 AIO workflow",
		Type: "Workflows",
		ModelVersions: []sdk.ModelVersionSummary{{
			ID:        2179254,
			Name:      "v1.0",
			BaseModel: "Wan Video 14B t2v",
			Files: []sdk.ModelVersionFile{{
				ID:          1000,
				Name:        "WAN22_AIO.zip",
				Type:        "Archive",
				SizeKB:      812.5,
				Primary:     true,
				DownloadURL: "https://civitai.com/api/download/models/2179254",
				Hashes:      sdk.FileHashes{SHA256: "AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899"},
			}},
		}},
	}
	if !civitai.IsWorkflowPost(detail.Type) {
		t.Fatalf("detail shape: IsWorkflowPost(%q) = false, want true", detail.Type)
	}
	// Guard against a fixture that is workflow-shaped only in the type string:
	// the version must really look like a downloadable checkpoint, or this test
	// is not exercising the case that shipped the bug.
	v := detail.ModelVersions[0]
	if v.BaseModel == "" || len(v.Files) == 0 {
		t.Fatalf("fixture does not reach the interesting case: a workflow post carries "+
			"a populated baseModel and files[], got %+v", v)
	}
	if f := v.Files[0]; f.DownloadURL == "" || f.Hashes.SHA256 == "" || !f.Primary || f.SizeKB == 0 {
		t.Fatalf("fixture does not reach the interesting case: a workflow post's file carries "+
			"downloadUrl/SHA256/primary/sizeKB like a checkpoint's, got %+v", f)
	}

	// List shape: the cards only ever have this.
	item := sdk.ModelListItem{ID: 1847730, Name: "WAN 2.2 AIO workflow", Type: "Workflows"}
	if !civitai.IsWorkflowPost(item.Type) {
		t.Fatalf("list shape: IsWorkflowPost(%q) = false, want true", item.Type)
	}

	real := sdk.ModelListItem{ID: 4384, Name: "DreamShaper", Type: "Checkpoint"}
	if civitai.IsWorkflowPost(real.Type) {
		t.Fatalf("list shape: IsWorkflowPost(%q) = true, want false", real.Type)
	}
}

func TestIsWorkflowPostTypeStrings(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"Workflows", true},
		{"workflows", true},
		{"WORKFLOWS", true},
		{" Workflows ", true},
		// The types that must be entirely unaffected — subscribing to these is
		// the behaviour this whole fix must NOT regress.
		{"Checkpoint", false},
		{"LORA", false},
		{"LoCon", false},
		{"LyCORIS", false},
		{"TextualInversion", false},
		{"VAE", false},
		{"Other", false},
		// Fails OPEN: an absent type is never ASSERTED to be a workflow.
		{"", false},
		{"   ", false},
		// Near-misses must not match.
		{"Workflow", false},
		{"Workflowss", false},
	}
	for _, c := range cases {
		if got := civitai.IsWorkflowPost(c.typ); got != c.want {
			t.Errorf("IsWorkflowPost(%q) = %v, want %v", c.typ, got, c.want)
		}
	}
}

// TestIsKnownNonWorkflowPostStaysOpenOnAnEmptyType is the guard the brief asks
// for explicitly: the Discover import gate must keep ACCEPTING a model whose
// type is absent. A future "tightening" that refuses an empty type trips here
// rather than shipping — 94 of the top 100 `types=Other` models carry an Archive
// zip, so the gate cannot be widened either.
func TestIsKnownNonWorkflowPostStaysOpenOnAnEmptyType(t *testing.T) {
	if civitai.IsKnownNonWorkflowPost("") {
		t.Error(`IsKnownNonWorkflowPost("") = true — the import gate must FAIL OPEN on an absent type`)
	}
	if civitai.IsKnownNonWorkflowPost("   ") {
		t.Error(`IsKnownNonWorkflowPost("   ") = true — whitespace is an absent type, the gate must stay open`)
	}
	if civitai.IsKnownNonWorkflowPost("Workflows") {
		t.Error(`IsKnownNonWorkflowPost("Workflows") = true, want false`)
	}
	// …and it must still REFUSE a model that positively says it is something
	// else, or the gate is not a gate.
	for _, typ := range []string{"Checkpoint", "LORA", "Other"} {
		if !civitai.IsKnownNonWorkflowPost(typ) {
			t.Errorf("IsKnownNonWorkflowPost(%q) = false, want true", typ)
		}
	}
}
