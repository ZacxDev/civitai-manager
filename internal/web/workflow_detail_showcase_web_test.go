package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// showcaseResolver builds a resolver whose model_cache returns raw for model 42.
func showcaseResolver(raw string, mr maturityRange) workflowResolver {
	return workflowResolver{
		cachedModel: func(id int) (string, []byte, bool) {
			if id == 42 {
				return "Cool Model", []byte(raw), true
			}
			return "", nil, false
		},
		mr: mr,
	}
}

// TestWorkflowDetailShowsShowcaseWhenModelHasImages proves the detail page renders
// the reused showcase carousel (the model-detail showcaseCard) + the shared lightbox/
// carousel scripts when the linked model has cached images.
func TestWorkflowDetailShowsShowcaseWhenModelHasImages(t *testing.T) {
	wf := &store.Workflow{
		ID: 1, Name: "portrait", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42), VersionID: intp(99),
	}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false,
		comfyHelperView{}, showcaseResolver(rawWithImages, fullMaturityRange())))

	if !strings.Contains(got, "cm-showcase-lg") {
		t.Errorf("detail should render the reused showcase card:\n%s", got)
	}
	if !strings.Contains(got, "img/a.jpg") {
		t.Error("showcase image should render")
	}
	// The shared lightbox + carousel scripts must be present so tiles open/scroll.
	if !strings.Contains(got, `id="cm-lightbox"`) || !strings.Contains(got, "cmCarouselScroll") {
		t.Error("detail showcase should include the shared lightbox + carousel scripts")
	}
}

// TestWorkflowDetailNoShowcaseWithoutModelOrImages proves nothing is rendered when
// the workflow has no linked model, or the model has no cached images — and no
// dangling lightbox/scripts are emitted.
func TestWorkflowDetailNoShowcaseWithoutModelOrImages(t *testing.T) {
	// No linked model.
	noModel := &store.Workflow{ID: 2, Name: "x", Format: store.WorkflowFormatUI, Graph: "{}", Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(noModel, "csrf", fullMaturityRange(), false, comfyHelperView{}, workflowResolver{}))
	if strings.Contains(got, "cm-showcase-lg") {
		t.Error("no linked model → no showcase card")
	}
	if strings.Contains(got, `id="cm-lightbox"`) {
		t.Error("no showcase → no dangling lightbox")
	}

	// Linked model but uncached (no images).
	linkedUncached := &store.Workflow{ID: 3, Name: "y", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42)}
	got2 := renderString(t, detailPageNode(linkedUncached, "csrf", fullMaturityRange(), false,
		comfyHelperView{}, workflowResolver{cachedModel: func(int) (string, []byte, bool) { return "", nil, false }}))
	if strings.Contains(got2, "cm-showcase-lg") {
		t.Error("uncached model → no showcase card")
	}
}

// TestWorkflowDetailShowcaseRespectsTheRange proves the reused carousel OMITS an
// out-of-band showcase tile server-side and renders an in-band one plain. It is
// the exact component the model-detail showcase and the workflow list cards use,
// so reusing it inherits the behaviour rather than reimplementing it.
func TestWorkflowDetailShowcaseRespectsTheRange(t *testing.T) {
	wf := &store.Workflow{ID: 4, Name: "nsfw", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42)}

	full := fullMaturityRange()
	show := renderString(t, detailPageNode(wf, "csrf", full, false,
		comfyHelperView{}, showcaseResolver(rawWithNSFWImage, full)))
	if !strings.Contains(show, "img/x.jpg") {
		t.Errorf("the full range should render the tile:\n%s", show)
	}
	for _, dead := range []string{`data-blurred="1"`, "cm-blur"} {
		if strings.Contains(show, dead) {
			t.Errorf("the showcase still emits the dead blur marker %q:\n%s", dead, show)
		}
	}

	pgOnly := maturityRange{maturityPG, maturityPG}
	body := renderString(t, detailPageNode(wf, "csrf", pgOnly, false,
		comfyHelperView{}, showcaseResolver(rawWithNSFWImage, pgOnly)))
	if strings.Contains(body, "img/x.jpg") {
		t.Errorf("a PG-only range LEAKED the out-of-band tile URL:\n%s", body)
	}
}
