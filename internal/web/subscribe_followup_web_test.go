package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// unknownEcoRaw is a Checkpoint whose baseModel is in NO curated ecosystem, so
// the model page's "Workflows for this model" section renders empty and `hidden`.
// It is the fixture for the case where the follow-up link must NOT be offered:
// an anchor to a hidden element scrolls nowhere, and the user would arrive at an
// unchanged page having been promised workflows.
const unknownEcoRaw = `{
  "id": 4244, "name": "Obscure Model", "type": "Checkpoint",
  "modelVersions": [
    {"id": 30, "name": "v1", "baseModel": "Totally Made Up Base 9000",
     "publishedAt": "2025-06-01T00:00:00.000Z",
     "files": [{"id": 4, "name": "obscure.safetensors", "type": "Model", "sizeKB": 1048576, "primary": true}]}
  ]
}`

// subscribeOK posts the subscribe form and returns the rendered control fragment,
// failing the test unless the subscription actually persisted — otherwise every
// assertion below would be about the failure fragment.
func subscribeOK(t *testing.T, srv *Server, modelID int, path string) string {
	t.Helper()
	rec := post(t, srv, path, url.Values{"mode": {"auto_download"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s = %d", path, rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Subscribed ✓") {
		t.Fatalf("the subscribe did not succeed — the follow-up checks would be vacuous:\n%s", body)
	}
	if sub := srv.modelSubscription(modelID); sub == nil {
		t.Fatalf("no subscription persisted for model %d", modelID)
	}
	return body
}

// TestSubscribeSuccessLinksToWorkflowsForThatModel: after a successful subscribe
// the control offers the model-SCOPED workflow section, not a generic browse.
//
// The href is pinned to /models/{id}#related-workflows because the scoping is the
// entire value: that section is already filtered to the model's base-model
// ecosystem and each card carries a working Import. A link to /workflows/discover
// would be one step further from the user with less information — the section's
// own "Browse more →" already hands off there.
func TestSubscribeSuccessLinksToWorkflowsForThatModel(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)

	body := subscribeOK(t, srv, 4242, "/models/4242/subscribe")

	want := `href="/models/4242#` + relatedWorkflowsID + `"`
	if !strings.Contains(body, want) {
		t.Errorf("the subscribed control must offer %s:\n%s", want, body)
	}
	if !strings.Contains(body, "Workflows for this model") {
		t.Errorf("the follow-up must name what it leads to:\n%s", body)
	}
	// It must not point at the workflow-IMPORT section: this model has no
	// workflows of its own to import, so that anchor leads to a card that does
	// not exist on its page.
	if strings.Contains(body, "#"+workflowImportCardID) {
		t.Errorf("a Checkpoint's follow-up must not point at the workflow-import card:\n%s", body)
	}
}

// TestSubscribeSuccessOnAWorkflowPostOffersImport: for a Workflows post the
// follow-up is IMPORT, because that is the action that works there.
//
// Its subscription can only ever notify and its graphs are imported rather than
// downloaded, so pointing it at "workflows built for this base model" would send
// the user past the one control that does something on that page.
func TestSubscribeSuccessOnAWorkflowPostOffersImport(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4243, "Nice Workflow Pack", workflowRaw)

	body := subscribeOK(t, srv, 4243, "/models/4243/subscribe")

	want := `href="/models/4243#` + workflowImportCardID + `"`
	if !strings.Contains(body, want) {
		t.Errorf("a workflow post's follow-up must lead to its import section (%s):\n%s", want, body)
	}
	if !strings.Contains(body, "Import") {
		t.Errorf("the follow-up must name Import as the action:\n%s", body)
	}
	// FIXTURE REACH + the other direction: this really is the workflow arm, and it
	// does NOT get the base-model section a checkpoint gets.
	if !strings.Contains(body, "notify only") {
		t.Fatalf("this is not the workflow-post arm (a workflow post is always notify-only):\n%s", body)
	}
	if strings.Contains(body, "#"+relatedWorkflowsID) {
		t.Errorf("a workflow post's follow-up must not point at the base-model workflow section:\n%s", body)
	}
}

// TestSubscribeFollowupIsOmittedWhenThereIsNothingToLandOn runs BOTH DIRECTIONS
// over two Checkpoints that differ only in their baseModel string.
//
// relatedWorkflowsContainer renders the section empty and `hidden` when the
// selected version's base model resolves to no ecosystem, and an anchor to a
// hidden element scrolls nowhere — so the link would promise workflows and
// deliver an unchanged page. The SDXL direction is what makes the unknown-base
// direction mean something: without it, never emitting a follow-up at all would
// pass.
func TestSubscribeFollowupIsOmittedWhenThereIsNothingToLandOn(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)    // baseModel "SDXL 1.0"
	seedModelCache(t, srv, 4244, "Obscure Model", unknownEcoRaw) // baseModel in no ecosystem

	known := subscribeOK(t, srv, 4242, "/models/4242/subscribe")
	if !strings.Contains(known, "#"+relatedWorkflowsID) {
		t.Fatalf("a model WITH an ecosystem must get the follow-up — otherwise the absence "+
			"check below is satisfied by the feature not existing:\n%s", known)
	}

	unknown := subscribeOK(t, srv, 4244, "/models/4244/subscribe")
	for _, gone := range []string{"#" + relatedWorkflowsID, "Workflows for this model"} {
		if strings.Contains(unknown, gone) {
			t.Errorf("a model whose base model resolves to no ecosystem must NOT be offered %q — "+
				"the section it points at renders hidden:\n%s", gone, unknown)
		}
	}
}

// TestSubscribeFollowupIsOnlyOfferedRightAfterSubscribing pins the follow-up to
// the MOMENT of subscribing rather than to the control's steady state.
//
// Every page-load render of an already-subscribed model — the model page, a
// search card, a suggestion card, the options panel's Cancel — goes through
// subscribeControl, which passes no follow-up. That keeps an already-subscribed
// card as compact as it is today; the destination is permanently on the model
// page regardless.
func TestSubscribeFollowupIsOnlyOfferedRightAfterSubscribing(t *testing.T) {
	srv := newDisclosureServer(t, Config{})
	seedModelCache(t, srv, 4242, "Nice Model", checkpointRaw)

	// FIXTURE REACH: subscribe first, and confirm the POST really does carry it.
	if body := subscribeOK(t, srv, 4242, "/models/4242/subscribe"); !strings.Contains(body, "#"+relatedWorkflowsID) {
		t.Fatalf("the POST did not carry the follow-up — this test would prove nothing:\n%s", body)
	}

	rec := get(t, srv, "/models/4242/subscribe-control")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET subscribe-control = %d", rec.Code)
	}
	steady := rec.Body.String()
	if !strings.Contains(steady, "Subscribed ✓") {
		t.Fatalf("the steady-state render is not the subscribed state:\n%s", steady)
	}
	if strings.Contains(steady, "#"+relatedWorkflowsID) {
		t.Errorf("the steady-state control carries the post-subscribe follow-up:\n%s", steady)
	}
}
