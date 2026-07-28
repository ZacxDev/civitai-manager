package web

import (
	"context"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// rawWithImages is a cached GetModel body carrying an inline showcase image on its
// primary version (what cardCarouselImages reads).
const rawWithImages = `{"id":42,"name":"Cool Model","modelVersions":[{"id":99,"name":"v1","images":[{"url":"https://img/a.jpg","nsfwLevel":1,"type":"image","width":4,"height":4}]}]}`

// rawWithNSFWImage is a cached body whose only image is NSFW (nsfwLevel high).
const rawWithNSFWImage = `{"id":42,"name":"NSFW Model","modelVersions":[{"id":99,"images":[{"url":"https://img/x.jpg","nsfwLevel":16,"type":"image","width":4,"height":4}]}]}`

// TestWorkflowCardShowsShowcaseWhenLinkedModelHasImages proves a workflow linked to
// a cached model with inline images renders the reused carousel (a showcase tile),
// and that the item still carries the sort/filter data-* attrs + the #wf-<id>
// anchor + the base-model/format badges.
func TestWorkflowCardShowsShowcaseWhenLinkedModelHasImages(t *testing.T) {
	srv := newWorkflowServer(t)
	if err := srv.store.PutModelCache(42, "Cool Model", []byte(rawWithImages)); err != nil {
		t.Fatalf("cache model: %v", err)
	}
	id, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "portrait", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42), VersionID: intp(99), BaseModel: "SDXL 1.0",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := workflowsTabBody(t, srv)

	// Showcase markup present (the reused carousel + a tile for the image).
	if !strings.Contains(body, "cm-carousel") {
		t.Errorf("expected the reused showcase carousel markup:\n%s", body)
	}
	if !strings.Contains(body, "img/a.jpg") {
		t.Errorf("expected the showcase image to render")
	}
	// Sort/filter data-* + anchor preserved on the wrapper.
	if !strings.Contains(body, `id="wf-`+itoa64(id)+`"`) {
		t.Errorf("item anchor missing after redesign")
	}
	for _, attr := range []string{`data-name="portrait"`, `data-format="api"`, `data-source="civitai"`, `data-base="SDXL 1.0"`, "data-created="} {
		if !strings.Contains(body, attr) {
			t.Errorf("sort/filter attr %q missing after redesign:\n%s", attr, body)
		}
	}
	// Base-model + format badges.
	if !strings.Contains(body, "SDXL 1.0") {
		t.Errorf("base-model badge missing")
	}
	if !strings.Contains(body, "Runnable API") {
		t.Errorf("format badge (Runnable API) missing")
	}
}

// TestWorkflowCardPlaceholderWhenNoShowcase proves a workflow with no linked model
// (or no cached images) degrades to the tasteful placeholder — never a broken img.
func TestWorkflowCardPlaceholderWhenNoShowcase(t *testing.T) {
	srv := newWorkflowServer(t)
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "no-model", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := workflowsTabBody(t, srv)
	if !strings.Contains(body, "cm-wf-noimg") {
		t.Errorf("expected the graceful placeholder:\n%s", body)
	}
	if !strings.Contains(body, "UI") {
		t.Errorf("format badge (UI) missing")
	}
}

// TestWorkflowCardShowcaseRespectsNSFWBlur proves that under the (reachable) blur
// mode an NSFW showcase tile renders BLURRED (.cm-blur), not omitted.
func TestWorkflowCardShowcaseRespectsNSFWBlur(t *testing.T) {
	r := workflowResolver{
		cachedModel: func(id int) (string, []byte, bool) { return "NSFW Model", []byte(rawWithNSFWImage), true },
		nsfwMode:    NSFWBlur,
	}
	wf := store.Workflow{ID: 1, Name: "nsfw", Format: store.WorkflowFormatUI, Source: store.WorkflowSourceCivitai, ModelID: intp(42)}
	got := renderString(t, workflowCardShowcase(wf, r))
	if !strings.Contains(got, "img/x.jpg") {
		t.Errorf("blur mode should still render the (blurred) NSFW tile:\n%s", got)
	}
	if !strings.Contains(got, "cm-blur") {
		t.Errorf("blur mode should blur the NSFW tile (.cm-blur):\n%s", got)
	}
}
