package web

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ==========================================================================
// COLLAPSING THE LIBRARY WORKFLOW LIST BY SOURCE
// ==========================================================================
//
// Importing ONE CivitAI Workflows post routinely yields many workflows — 22 for
// a single post in a real library — and the list rendered every one of them as a
// full card, all with the same showcase images and the same "from …" line. The
// list read as duplicates.
//
// The list now renders ONE item per SOURCE, with a <select> choosing which of that
// source's workflows the item is showing. Nothing is hidden: every member is
// reachable from that select, every member keeps a working #wf-<id> anchor (see
// altAnchors), and the text filter still matches on the members that are not
// currently displayed (see data-alt-names).
//
// 🔴 NAMING. What is grouped here is a CivitAI **model id whose type is
// Workflows** — CivitAI serves workflow posts from URLs that look like model
// pages, so https://civitai.com/models/1847730/... is a workflow zip, not a
// checkpoint. The repo already type-checks that distinction with
// strings.EqualFold(m.Type, "Workflows") in three places. Nothing in this file's
// UI copy calls it "the model": the user-facing words are "source" and "from".
// The identifier is still a model id, because that is what CivitAI's URL space
// calls it, and store.Workflow.ModelID is what holds it.

// workflowSourceGroup is one rendered list item: the workflows that came from one
// CivitAI source, plus which of them the item is currently showing.
//
// A workflow with NO source (ModelID == nil — pasted, extracted from a PNG,
// authored locally, or found by a disk scan) gets a group of its own. Bundling
// them would invent a "no source" bucket that is not a source at all, and would
// hide unrelated workflows behind one arbitrary representative.
type workflowSourceGroup struct {
	// SourceID is the CivitAI model id (of a Workflows-type post) the members were
	// imported from, or 0 for a source-less workflow standing alone.
	SourceID int
	// Members are the group's workflows in the order the list supplied them.
	Members []store.Workflow
	// Selected indexes Members — which one this item is showing.
	Selected int
}

// shown returns the member the item renders.
func (grp workflowSourceGroup) shown() store.Workflow { return grp.Members[grp.Selected] }

// multi reports whether the group needs a picker at all. A source with exactly ONE
// workflow renders no <select>: a one-option dropdown is a control that cannot do
// anything, and offering it would imply there is something else to choose.
func (grp workflowSourceGroup) multi() bool { return len(grp.Members) > 1 }

// anchorID is the group's STABLE fragment target across a pick change: the first
// member's id. Every member id resolves inside this item (the shown one carries it
// on the wrapper, the rest as altAnchors), and the first member is the one id that
// does not move when the selection does — so a picker form can point its action at
// it and land the user back on the same item.
func (grp workflowSourceGroup) anchorID() string {
	return "wf-" + strconv.FormatInt(grp.Members[0].ID, 10)
}

// groupWorkflowsBySource collapses the list into one group per source, preserving
// the order the caller supplied (a group takes the position of its FIRST member),
// which is what keeps the existing "newest imported first" server order meaningful.
//
// picks is the set of workflow ids the request asked to show (?pick=). A group
// whose pick names one of its members shows that member; every other group shows
// its first. A pick naming a workflow that is not in the list — stale bookmark,
// hand-edited URL, a workflow deleted since — is simply ignored, so a bad value
// degrades to the default rather than to an empty item.
func groupWorkflowsBySource(wfs []store.Workflow, picks map[int64]bool) []workflowSourceGroup {
	groups := make([]workflowSourceGroup, 0, len(wfs))
	bySource := map[int]int{} // source id → index into groups
	for _, wf := range wfs {
		if wf.ModelID == nil {
			groups = append(groups, workflowSourceGroup{Members: []store.Workflow{wf}})
			continue
		}
		src := *wf.ModelID
		if i, ok := bySource[src]; ok {
			groups[i].Members = append(groups[i].Members, wf)
			continue
		}
		bySource[src] = len(groups)
		groups = append(groups, workflowSourceGroup{SourceID: src, Members: []store.Workflow{wf}})
	}
	for i := range groups {
		for j, m := range groups[i].Members {
			if picks[m.ID] {
				groups[i].Selected = j
				break
			}
		}
	}
	return groups
}

// activeWorkflowPicks lists the selected workflow id of every group that is NOT
// showing its default member. Those are exactly the picks a picker form has to
// carry forward as hidden inputs, so changing one group's selection does not reset
// every other group's.
func activeWorkflowPicks(groups []workflowSourceGroup) []int64 {
	var out []int64
	for _, grp := range groups {
		if grp.Selected != 0 {
			out = append(out, grp.shown().ID)
		}
	}
	return out
}

// libraryPickKeys is the FIXED set of query parameters a source-picker form
// carries forward. It is a whitelist on purpose: echoing the request's whole query
// back into hidden inputs would reflect arbitrary attacker-supplied parameter names
// and values into the page. These four are the Library workflows tab's own state —
// the tab selector and the three browse-by facets (see normalizeLibraryWorkflowFacets).
var libraryPickKeys = []string{"tab", "eco", "use", "model"}

// libraryPickBase extracts those parameters from a request query. The VALUES are
// still untrusted text and are escaped by gomponents' attribute encoding when they
// become hidden inputs; the handler re-normalizes them on the way back in, so a
// junk value round-trips as the same junk and is dropped there exactly as it was
// on the first request.
func libraryPickBase(q url.Values) url.Values {
	out := url.Values{}
	for _, k := range libraryPickKeys {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			out.Set(k, v)
		}
	}
	return out
}

// parseWorkflowPicks reads the repeated ?pick=<workflow id> parameters. Anything
// non-numeric or non-positive is dropped rather than erroring: a pick is a display
// preference, and a malformed one must fall back to the default item, never to an
// error page or an empty list.
func parseWorkflowPicks(q url.Values) map[int64]bool {
	out := map[int64]bool{}
	for _, raw := range q["pick"] {
		if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id > 0 {
			out[id] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// workflowSourcePicker renders the "which of this source's workflows am I looking
// at" control.
//
// 🔴 IT IS A REAL GET FORM AND WORKS WITH JAVASCRIPT OFF. The <select> plus its
// "Show" submit button navigate to /library with the picks in the query — no htmx,
// no fetch, no swap contract to break. That matters here specifically: this list
// lives inside the STABLE #workflow-scan-results container that the workflow-scan
// poller swaps, and a control that swapped part of that container would be one more
// thing that can orphan the poller. A full GET cannot.
//
// The onchange submit is an ENHANCEMENT layered on top, not the mechanism: with JS
// the select applies immediately, without it the button does the same job in one
// more click. Nothing is reachable only via the script.
//
// The form's action carries the group's anchor (/library#wf-<first member>), so the
// reload lands back on this item rather than at the top of the list. A GET form
// submission replaces the action URL's QUERY and leaves its FRAGMENT intact, which
// is what makes that work without any script.
func workflowSourcePicker(grp workflowSourceGroup, base url.Values, otherPicks []int64) g.Node {
	if !grp.multi() {
		return nil
	}
	selectID := "cm-wf-pick-" + strconv.Itoa(grp.SourceID)

	kids := []g.Node{
		h.Class("cm-wf-pick"),
		h.Method("get"),
		// The fragment survives GET submission; the query is replaced.
		h.Action("/library#" + grp.anchorID()),
	}
	for _, k := range libraryPickKeys {
		if v := base.Get(k); v != "" {
			kids = append(kids, h.Input(h.Type("hidden"), h.Name(k), h.Value(v)))
		}
	}
	// Every OTHER group's non-default selection, so applying this one does not reset
	// the rest of the page.
	for _, id := range otherPicks {
		kids = append(kids, h.Input(h.Type("hidden"), h.Name("pick"),
			h.Value(strconv.FormatInt(id, 10))))
	}

	opts := make([]g.Node, 0, len(grp.Members))
	for i, m := range grp.Members {
		attrs := []g.Node{h.Value(strconv.FormatInt(m.ID, 10))}
		if i == grp.Selected {
			attrs = append(attrs, g.Attr("selected"))
		}
		// The name is untrusted text out of somebody else's zip — g.Text escapes it.
		attrs = append(attrs, g.Text(workflowDisplayName(m)))
		opts = append(opts, h.Option(attrs...))
	}

	label := strconv.Itoa(len(grp.Members)) + " workflows from this source"
	kids = append(kids,
		// A real <label for=…>, visually hidden: the group's own heading already says
		// what this is, but the select still needs an accessible name of its own.
		h.Label(h.Class("cm-sr-only"), h.For(selectID), g.Text("Show which of the "+label)),
		h.Span(h.Class("cm-wf-pick-legend"), g.Attr("aria-hidden", "true"), g.Text(label)),
		h.Select(append([]g.Node{
			h.ID(selectID), h.Name("pick"), h.Class("cm-wf-pick-select"),
			// Enhancement only — the submit button below is the mechanism.
			g.Attr("onchange", "if(this.form.requestSubmit){this.form.requestSubmit();}else{this.form.submit();}"),
		}, opts...)...),
		h.Button(h.Type("submit"), h.Class("cm-wf-pick-go"), g.Text("Show")),
	)
	return h.Form(kids...)
}

// altAnchors emits an empty, aria-hidden anchor for every member this item is NOT
// currently showing.
//
// 🔴 THIS IS WHAT KEEPS THE DEEP LINK WORKING. Other surfaces link into the library
// with /library?tab=workflows#wf-<id> ("View in library" on the model page, the
// import flash, the detail page's back link). Collapsing N workflows into one item
// would delete N-1 of those ids from the DOM, so the fragment would resolve to
// nothing and the browser would silently leave the user at the top of the page.
// With the anchors present the NATIVE fragment scroll still lands on the group that
// holds the workflow — no script involved — and workflowDeeplinkScript highlights
// the enclosing item.
func altAnchors(grp workflowSourceGroup) g.Node {
	if !grp.multi() {
		return nil
	}
	nodes := make([]g.Node, 0, len(grp.Members)-1)
	for i, m := range grp.Members {
		if i == grp.Selected {
			continue
		}
		nodes = append(nodes, h.Span(
			h.ID("wf-"+strconv.FormatInt(m.ID, 10)),
			h.Class("cm-wf-alt-anchor"),
			g.Attr("aria-hidden", "true"),
		))
	}
	return g.Group(nodes)
}

// altNames is the space-joined display names of the members this item is NOT
// showing. It rides on data-alt-names so the client-side text filter can still
// match a workflow that is currently behind the select — without it, typing a
// hidden member's name would hide the very item that holds it.
func altNames(grp workflowSourceGroup) string {
	if !grp.multi() {
		return ""
	}
	names := make([]string, 0, len(grp.Members)-1)
	for i, m := range grp.Members {
		if i == grp.Selected {
			continue
		}
		names = append(names, workflowDisplayName(m))
	}
	return strings.Join(names, " ")
}

// workflowDisplayName is the ONE definition of a workflow's list label, so the
// card title, the picker option and the filter haystack can never disagree about
// what an unnamed workflow is called.
func workflowDisplayName(wf store.Workflow) string {
	if n := strings.TrimSpace(wf.Name); n != "" {
		return wf.Name
	}
	return "workflow #" + strconv.FormatInt(wf.ID, 10)
}
