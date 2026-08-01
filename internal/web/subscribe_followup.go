package web

import (
	"fmt"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ===========================================================================
// "You subscribed — now here are workflows that use this model."
// ===========================================================================
//
// A model is an ingredient; a workflow is the recipe that uses it. Subscribing
// is the moment the user has committed to an ingredient, which is exactly when
// "what do I run this in?" becomes the next question — and until now the answer
// was to go looking for it.
//
// WHY IT LINKS TO THE MODEL PAGE AND NOT TO /workflows/discover. Both exist and
// both are real, but only one of them is SCOPED TO THIS MODEL:
//
//   - /models/{id}#related-workflows is the "Workflows for this model" section
//     (model_related_workflows.go). It is already filtered to the base-model
//     ECOSYSTEM of the version the page shows, self-filters the model out of its
//     own results, offers the model's own tag-derived use-case chips, and each
//     card carries a working one-click Import. It is the answer, not a search box.
//   - /workflows/discover is the unscoped browse page. The related section's own
//     "Browse more →" link already hands off to it with the same facets, so
//     linking there directly would be one step further from the user with less
//     information, and reachable in one more click anyway.
//
// FOR A WORKFLOWS POST THE TARGET IS DIFFERENT, because the useful action is.
// Its subscription can only ever notify, and its workflows are IMPORTED rather
// than downloaded — so it links to the import card (#workflow-import), which is
// the control that actually does something on that page.
//
// 🔴 IT IS EMITTED ONLY WHEN THERE IS SOMETHING REAL TO LAND ON. The related
// section renders itself EMPTY and `hidden` when the selected version's base
// model resolves to no ecosystem (relatedWorkflowsContainer), and an anchor to a
// hidden element scrolls nowhere — the user would click "Workflows for this
// model" and arrive at the top of an unchanged page with no workflows in sight.
// So the same predicate the section uses (modelWorkflowFacets) decides whether
// the link exists at all. No ecosystem, no link.

// subscribeWorkflowLink builds the post-subscribe follow-up, or nil when there is
// no honest target.
//
// It takes the model DETAIL rather than an id because both decisions — which of
// the two sections to point at, and whether the related one will render anything
// — are properties of the model, and guessing either produces a link that lies.
//
// selectedVersionID 0 is deliberate: modelWorkflowFacets falls back to
// ModelVersions[0], which is exactly the version loadModelView selects when the
// link's own url carries no ?version=. The prediction is therefore made about the
// same version the destination page will show.
func subscribeWorkflowLink(modelID int, m *civitai.ModelDetail) g.Node {
	if m == nil {
		return nil
	}
	if civitai.IsWorkflowPost(m.Type) {
		return subscribeFollowupLink(
			fmt.Sprintf("/models/%d#%s", modelID, workflowImportCardID),
			"Import its workflows →",
			"Go to the workflow import section for this model")
	}
	if modelWorkflowFacets(m, 0).Eco == nil {
		return nil
	}
	return subscribeFollowupLink(
		fmt.Sprintf("/models/%d#%s", modelID, relatedWorkflowsID),
		"Workflows for this model →",
		"Browse ComfyUI workflows built for this model's base-model family")
}

// subscribeFollowupLink is the follow-up's one visual shape: a small text link,
// not a button.
//
// A button here would compete with Unsubscribe, which sits on the same row and
// is the one control on it that changes state. This is a navigation, so it looks
// like one. The classes mirror the related-workflows section's own
// "Browse more →" link, which is the same kind of affordance pointing at the
// same kind of destination.
func subscribeFollowupLink(href, label, aria string) g.Node {
	return h.A(
		h.Href(href),
		h.Class("text-xs text-indigo-400 hover:text-indigo-300"),
		g.Attr("aria-label", aria),
		g.Text(label),
	)
}
