package comfy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestModelsRootOnTheLivePayload pins the inference against a REAL
// /internal/folder_paths body captured from a live ComfyUI 0.27.1 (2026-08-02),
// not a synthetic one.
//
// This repo's own rule: a fake-reader test can encode the wrong assumption about
// an external API and stay green while reality is different. The captured body is
// the ground truth for the two things that actually matter here — that a category
// carries SEVERAL roots, and that packs register directories of their own.
func TestModelsRootOnTheLivePayload(t *testing.T) {
	data, err := os.ReadFile("testdata/folder_paths_live.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fp map[string][]string
	if err := json.Unmarshal(data, &fp); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}

	// PRECONDITIONS — a fixture that cannot express the contest must fail loudly
	// rather than pass quietly.
	if len(fp) < 20 {
		t.Fatalf("fixture is not the real payload: %d categories", len(fp))
	}
	if got := len(fp["checkpoints"]); got < 2 {
		t.Fatalf("fixture cannot exercise multi-root: checkpoints has %d roots", got)
	}
	if _, ok := fp["custom_nodes"]; !ok {
		t.Fatal("fixture lacks custom_nodes, so the exclusion rule is untested")
	}

	const want = "/home/zach/workspace/fast/comfyui/ComfyUI/models"
	if got := ModelsRoot(fp); got != want {
		t.Fatalf("ModelsRoot = %q, want %q", got, want)
	}
}

// TestModelsRootPrefersTheMajorityNotTheFirstEntry is the DISCRIMINATING case:
// the two signals are deliberately OPPOSED, so a naive `checkpoints[0]`
// implementation gets it wrong and this test can tell the two apart.
//
// Without it the live-payload test above is over-determined — on that install
// checkpoints[0] happens to be the right answer too, so it would pass against an
// implementation that just read the first entry.
func TestModelsRootPrefersTheMajorityNotTheFirstEntry(t *testing.T) {
	fp := map[string][]string{
		// A user whose extra_model_paths.yaml lists a network share FIRST.
		"checkpoints": {"/mnt/share/checkpoints", "/opt/ComfyUI/models/checkpoints"},
		"loras":       {"/opt/ComfyUI/models/loras"},
		"vae":         {"/opt/ComfyUI/models/vae"},
		"controlnet":  {"/opt/ComfyUI/models/controlnet"},
	}
	if got, want := ModelsRoot(fp), "/opt/ComfyUI/models"; got != want {
		t.Fatalf("ModelsRoot = %q, want %q (a first-entry implementation returns /mnt/share)", got, want)
	}
}

// TestModelsRootIgnoresCustomNodesAndOffNameDirs pins the two exclusions
// separately, so deleting either one is caught.
func TestModelsRootIgnoresCustomNodesAndOffNameDirs(t *testing.T) {
	fp := map[string][]string{
		// custom_nodes' parent is the INSTALL root, not the models root.
		"custom_nodes": {"/opt/ComfyUI/custom_nodes"},
		// A pack registering a directory inside its own folder: the dir's base name
		// does not match the category, so it must not vote.
		"ultralytics": {"/opt/ComfyUI/custom_nodes/kjnodes/yolo"},
		"checkpoints": {"/opt/ComfyUI/models/checkpoints"},
		"loras":       {"/opt/ComfyUI/models/loras"},
	}
	if got, want := ModelsRoot(fp), "/opt/ComfyUI/models"; got != want {
		t.Fatalf("ModelsRoot = %q, want %q", got, want)
	}
}

// TestModelsRootReturnsEmptyWhenAmbiguous pins the fail-to-ask behaviour: a tie is
// not a winner, because guessing decides where gigabytes are written.
func TestModelsRootReturnsEmptyWhenAmbiguous(t *testing.T) {
	cases := map[string]map[string][]string{
		"tie": {
			"checkpoints": {"/a/models/checkpoints"},
			"loras":       {"/b/models/loras"},
		},
		"empty":       {},
		"nil":         nil,
		"no-matches":  {"checkpoints": {"/somewhere/else"}},
		"blank-entry": {"checkpoints": {"   "}},
	}
	for name, fp := range cases {
		if got := ModelsRoot(fp); got != "" {
			t.Errorf("%s: ModelsRoot = %q, want \"\"", name, got)
		}
	}
}

// TestModelsRootRefusesAOneCategoryNomination is the hostile-payload floor.
//
// 🔴 The payload comes from a remote ComfyUI over HTTP, and the thing nominated is a
// category directory's PARENT — which never has to look like a models root. Measured
// before minModelsRootVotes: {"checkpoints":["/tmp/checkpoints"]} nominated /tmp, and
// the re-validation downstream accepted it (it exists, it is a directory, it is
// writable — that is the whole of that check). The 57-vs-11 margin the design rests
// on is a fact about an HONEST payload and carries no weight against a chosen one.
func TestModelsRootRefusesAOneCategoryNomination(t *testing.T) {
	cases := map[string]map[string][]string{
		"single fabricated category": {"checkpoints": {"/tmp/checkpoints"}},
		// A lone real-looking vote is refused for the same reason: one category is not
		// agreement, whoever sent it.
		"single honest category": {"checkpoints": {"/opt/ComfyUI/models/checkpoints"}},
		// The off-name and custom_nodes exclusions do not count toward the floor, so a
		// payload padded with them still has only ONE real vote.
		"padded with non-voting entries": {
			"checkpoints":  {"/home/zach/checkpoints"},
			"custom_nodes": {"/home/zach/custom_nodes"},
			"ultralytics":  {"/home/zach/yolo"},
		},
	}
	for name, fp := range cases {
		if got := ModelsRoot(fp); got != "" {
			t.Errorf("%s: ModelsRoot = %q, want \"\" — one agreeing category is not evidence", name, got)
		}
	}
}

// TestModelsRootStillAcceptsTwoAgreeingCategories is the POSITIVE CONTROL for the
// floor above: without it, a test asserting "" everywhere passes just as well
// against a ModelsRoot that always returns "".
//
// Two is the smallest payload a real install can produce, so it is the boundary the
// floor must not overshoot.
func TestModelsRootStillAcceptsTwoAgreeingCategories(t *testing.T) {
	fp := map[string][]string{
		"checkpoints": {"/opt/ComfyUI/models/checkpoints"},
		"loras":       {"/opt/ComfyUI/models/loras"},
	}
	if got, want := ModelsRoot(fp), "/opt/ComfyUI/models"; got != want {
		t.Fatalf("ModelsRoot = %q, want %q — the floor must not reject a real install", got, want)
	}
}

// TestModelsRootCategoryDirsReportsTheVoters pins the corroboration input: the
// caller stats exactly the directories that voted, so the existence check and the
// vote can never drift apart.
func TestModelsRootCategoryDirsReportsTheVoters(t *testing.T) {
	fp := map[string][]string{
		"checkpoints":  {"/mnt/share/checkpoints", "/opt/ComfyUI/models/checkpoints"},
		"loras":        {"/opt/ComfyUI/models/loras"},
		"custom_nodes": {"/opt/ComfyUI/custom_nodes"},
		"ultralytics":  {"/opt/ComfyUI/models/yolo"},
	}
	got := ModelsRootCategoryDirs(fp, "/opt/ComfyUI/models")
	want := []string{"/opt/ComfyUI/models/checkpoints", "/opt/ComfyUI/models/loras"}
	if len(got) != len(want) {
		t.Fatalf("ModelsRootCategoryDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ModelsRootCategoryDirs = %v, want %v", got, want)
		}
	}
	// A directory that did not vote for THIS root is not reported.
	if dirs := ModelsRootCategoryDirs(fp, "/mnt/share"); len(dirs) != 1 || dirs[0] != "/mnt/share/checkpoints" {
		t.Fatalf("ModelsRootCategoryDirs(/mnt/share) = %v, want just its own voter", dirs)
	}
}

// TestFolderPathsDecodesAndDegrades covers the client: a 200 decodes, and a 404
// (an older ComfyUI that has no such endpoint) is an ERROR the caller can degrade
// on rather than a silent empty success.
func TestFolderPathsDecodesAndDegrades(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/internal/folder_paths" {
				t.Errorf("path = %q", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"checkpoints":["/opt/ComfyUI/models/checkpoints"]}`))
		}))
		defer srv.Close()
		fp, err := NewClient(srv.URL, "").FolderPaths(context.Background())
		if err != nil {
			t.Fatalf("FolderPaths: %v", err)
		}
		if got := fp["checkpoints"]; len(got) != 1 || got[0] != "/opt/ComfyUI/models/checkpoints" {
			t.Fatalf("decoded %v", fp)
		}
	})

	t.Run("404 is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		}))
		defer srv.Close()
		if _, err := NewClient(srv.URL, "").FolderPaths(context.Background()); err == nil {
			t.Fatal("want an error for 404, got nil")
		}
	})
}
