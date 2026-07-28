package web

import (
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// showcaseResolver builds a resolver whose model_cache returns raw for model 42.
func showcaseResolver(raw string, mode string) workflowResolver {
	return workflowResolver{
		cachedModel: func(id int) (string, []byte, bool) {
			if id == 42 {
				return "Cool Model", []byte(raw), true
			}
			return "", nil, false
		},
		nsfwMode: mode,
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
	got := renderString(t, workflowDetailPage(wf, "{}", "csrf", "dark", NSFWShow, nil, false,
		comfyHelperView{}, showcaseResolver(rawWithImages, NSFWShow)))

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
	got := renderString(t, workflowDetailPage(noModel, "{}", "csrf", "dark", NSFWShow, nil, false, comfyHelperView{}, workflowResolver{}))
	if strings.Contains(got, "cm-showcase-lg") {
		t.Error("no linked model → no showcase card")
	}
	if strings.Contains(got, `id="cm-lightbox"`) {
		t.Error("no showcase → no dangling lightbox")
	}

	// Linked model but uncached (no images).
	linkedUncached := &store.Workflow{ID: 3, Name: "y", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42)}
	got2 := renderString(t, workflowDetailPage(linkedUncached, "{}", "csrf", "dark", NSFWShow, nil, false,
		comfyHelperView{}, workflowResolver{cachedModel: func(int) (string, []byte, bool) { return "", nil, false }}))
	if strings.Contains(got2, "cm-showcase-lg") {
		t.Error("uncached model → no showcase card")
	}
}

// TestWorkflowDetailShowcaseRespectsNSFW proves the reused carousel honors the NSFW
// mode: `show` reveals the NSFW tile plainly, while `blur`/`hide` obscure it behind
// .cm-blur. (The shared card carousel migrates `hide`→`blur` at this layer — see
// normalizeNSFWMode — the same behavior as the model-detail showcaseCard and the
// workflow list cards; reusing the exact component inherits it.)
func TestWorkflowDetailShowcaseRespectsNSFW(t *testing.T) {
	wf := &store.Workflow{ID: 4, Name: "nsfw", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceCivitai, ModelID: intp(42)}

	show := renderString(t, workflowDetailPage(wf, "{}", "csrf", "dark", NSFWShow, nil, false,
		comfyHelperView{}, showcaseResolver(rawWithNSFWImage, NSFWShow)))
	if !strings.Contains(show, "img/x.jpg") {
		t.Errorf("show mode should render the NSFW tile:\n%s", show)
	}
	// data-blurred="1" is the per-tile blur marker (cm-blur also appears in the shared
	// reveal script, so target the tile attribute).
	if strings.Contains(show, `data-blurred="1"`) {
		t.Error("show mode must not blur the tile")
	}

	for _, mode := range []string{NSFWBlur, NSFWHide} {
		body := renderString(t, workflowDetailPage(wf, "{}", "csrf", "dark", mode, nil, false,
			comfyHelperView{}, showcaseResolver(rawWithNSFWImage, mode)))
		if !strings.Contains(body, "img/x.jpg") || !strings.Contains(body, `data-blurred="1"`) {
			t.Errorf("%s mode should render the NSFW tile blurred:\n%s", mode, body)
		}
	}
}
