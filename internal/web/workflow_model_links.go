package web

import (
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ===========================================================================
// The workflow <-> model LINKAGE surfaces, both directions.
// ===========================================================================
//
// 🔴 TWO RELATIONS, TWO VOCABULARIES. They are rendered with deliberately
// different words everywhere in this file, and nothing may collapse them:
//
//	IMPORTED FROM — workflows.model_id / version_id. A recorded fact: the
//	  importer stamped it. Rendered as "from <model>" / "Imported from this
//	  model" (workflowSourceLinks, workflowImportDetailCard).
//	USES          — workflows.resources naming a file that belongs to the model.
//	  An INFERENCE from filenames (see store.ListWorkflowsUsingModel for exactly
//	  what it does and does not catch). Rendered as "Uses" / "use a file from
//	  this model".
//
// A workflow can be in one, both or neither. Saying a workflow "came from" a
// model it merely loads a LoRA from is a factual error, which is why the copy
// below never reuses a word from the other column.

// usedModel is one civitai model a workflow REFERENCES a file from, resolved
// entirely from the local index (never a fetch).
type usedModel struct {
	ModelID   int
	VersionID int
	// Files are the resource entries (as written in the graph) that resolved to
	// this model, so the UI can name the actual file rather than assert a bare
	// relationship.
	Files []string
}

// usedModelsCap bounds how many DISTINCT models a workflow's "Uses" row names
// inline. A pathological graph can reference dozens of files across many models,
// and a provenance row that wraps to six lines stops being read at all; past the
// cap the row says "+N more" and the per-file chips (which are complete) carry
// the rest.
const usedModelsCap = 4

// workflowUsedModels resolves a workflow's resource list to the DISTINCT civitai
// models those files belong to locally, in first-reference order.
//
// It reuses the SAME resolver the resource chips use, so a model named here is by
// construction a model one of the chips already links to — the row is an
// aggregation of the chips, never a second, differently-derived claim.
//
// Only a file resolving to BOTH a model and a version is counted (resourceInfo.
// linked()), which is store.LocalFileByBasename's own contract: an unknown or
// AMBIGUOUS basename resolves to nothing and is silently skipped rather than
// guessed at. A workflow whose files are all unmatched therefore yields nil, and
// the caller renders no row at all.
func workflowUsedModels(resources []string, resolver workflowResolver) []usedModel {
	var out []usedModel
	idx := map[int]int{} // model id -> position in out
	for _, res := range resources {
		base := store.ResourceBasename(res)
		if base == "" {
			continue
		}
		info, ok := resolver.resource(base)
		if !ok || !info.linked() {
			continue
		}
		if i, seen := idx[info.ModelID]; seen {
			out[i].Files = append(out[i].Files, res)
			continue
		}
		idx[info.ModelID] = len(out)
		out = append(out, usedModel{ModelID: info.ModelID, VersionID: info.VersionID, Files: []string{res}})
	}
	return out
}

// modelHref builds the in-app model link for a resolved resource. Integers only,
// so nothing from a graph reaches the URL.
func (u usedModel) modelHref() string {
	return "/models/" + strconv.Itoa(u.ModelID) + "?modelVersionId=" + strconv.Itoa(u.VersionID)
}

// workflowUsesRow is the workflow DETAIL page's reverse link-back: "Uses" plus a
// link per distinct model whose file the graph references.
//
// It sits BESIDE workflowSourceLinks' provenance row, never inside it, and its
// label is "Uses" against that row's "Source"/"from" — the two relations are
// adjacent on the page precisely so the difference is visible.
//
// Returns nil when nothing resolved, so a workflow with no matched resources
// grows no empty row.
func workflowUsesRow(wf *store.Workflow, resolver workflowResolver) g.Node {
	used := workflowUsedModels(wf.Resources, resolver)
	if len(used) == 0 {
		return nil
	}

	row := []g.Node{
		h.Span(h.Class("text-xs text-slate-500"), g.Text("Uses")),
	}
	shown := used
	extra := 0
	if len(shown) > usedModelsCap {
		extra = len(shown) - usedModelsCap
		shown = shown[:usedModelsCap]
	}
	for _, u := range shown {
		row = append(row, h.A(
			h.Href(u.modelHref()),
			h.Class("cm-uses-link"),
			h.Title(usedModelTitle(u)),
			// The model NAME is untrusted CivitAI text — workflowModelNameText escapes
			// it (or lazy-loads it through the cache-first /title endpoint).
			workflowModelNameText(u.ModelID, resolver),
		))
	}
	if extra > 0 {
		row = append(row, h.Span(h.Class("text-xs text-slate-500"),
			g.Text("+"+strconv.Itoa(extra)+" more")))
	}
	return h.Div(h.Class("flex flex-wrap items-center gap-3 mt-2"), g.Group(row))
}

// usedModelTitle spells out WHICH files produced the link, so the claim is
// legible without clicking. The resource strings are arbitrary graph text and are
// attribute-escaped by gomponents.
func usedModelTitle(u usedModel) string {
	n := len(u.Files)
	head := "This workflow references " + strconv.Itoa(n) + " file" + plural(n) + " from this model: "
	return head + strings.Join(u.Files, ", ")
}

// workflowUsesChips is the compact list-card form of the same relation: a single
// muted "uses N model(s)" phrase in the card's meta row.
//
// It deliberately does NOT list the models inline. The card's meta row already
// carries the format badge, base model, source, the imported-from name and the
// resource popover; adding N more model names is what turned that row into an
// unreadable wall. The detail page (workflowUsesRow) and the resource chips both
// carry the full linkage.
func workflowUsesChips(wf store.Workflow, resolver workflowResolver) g.Node {
	used := workflowUsedModels(wf.Resources, resolver)
	if len(used) == 0 {
		return nil
	}
	if len(used) == 1 {
		return h.A(
			h.Href(used[0].modelHref()),
			h.Class("cm-uses-link text-xs"),
			h.Title(usedModelTitle(used[0])),
			g.Text("uses "), workflowModelNameText(used[0].ModelID, resolver),
		)
	}
	return h.Span(h.Class("text-xs text-slate-400"),
		h.Title("This workflow references files belonging to "+strconv.Itoa(len(used))+" of your local models"),
		g.Text("uses "+strconv.Itoa(len(used))+" models"))
}

// ---------------------------------------------------------------------------
// The MODEL page direction: which of my workflows use this model
// ---------------------------------------------------------------------------

// modelUsageDisplayCap bounds how many workflows the model page LISTS inline. The
// store read is separately capped (store.workflowUsageScanCap); this is purely
// about not turning a model page into a workflow index — past it the card says
// how many more there are and points at the library.
const modelUsageDisplayCap = 12

// workflowUsageCard is the model page's "which of my workflows use this model"
// section. It renders NOTHING (nil) when nothing matched, so a model nobody's
// workflows touch grows no empty card — the quiet degradation the section is
// required to have.
//
// The heading and the explanatory line both use the USES vocabulary and never the
// imported-from one. A row that ALSO carries this model as its imported-from
// linkage gets an explicit second badge, because that row genuinely stands for
// both relations and silently showing only one of them would be a half-truth.
//
// Every value here comes from the LOCAL database — workflow names the user's own
// library recorded, and resource strings out of their own graphs. Both are still
// escaped through g.Text (a workflow name can come from an imported CivitAI zip).
func workflowUsageCard(modelID int, usages []store.WorkflowUsage) g.Node {
	if len(usages) == 0 {
		return nil
	}
	shown := usages
	extra := 0
	if len(shown) > modelUsageDisplayCap {
		extra = len(shown) - modelUsageDisplayCap
		shown = shown[:modelUsageDisplayCap]
	}

	rows := make([]g.Node, 0, len(shown))
	for _, u := range shown {
		name := strings.TrimSpace(u.Name)
		if name == "" {
			name = "workflow #" + strconv.FormatInt(u.ID, 10)
		}
		meta := []g.Node{
			// WHICH file matched — the whole basis of the claim, so it is stated
			// rather than left implicit.
			h.Span(h.Class("cm-uses-file break-all"), g.Text(strings.Join(u.Matched, ", "))),
		}
		// The OTHER relation, named as itself. Only when this same model is the
		// workflow's recorded source.
		if u.ModelID != nil && *u.ModelID == modelID {
			meta = append(meta, badge("also imported from this model", "blue"))
		}
		rows = append(rows, h.Li(
			h.Class("cm-uses-row"),
			h.A(h.Href("/workflows/"+strconv.FormatInt(u.ID, 10)),
				h.Class("cm-uses-link"), g.Text(name)),
			h.Div(h.Class("flex flex-wrap items-center gap-2"), g.Group(meta)),
		))
	}

	body := []g.Node{
		// output.css is a PURGED Tailwind build, so a spacing utility only has a rule
		// if some template already uses it; reaching for a smaller step than the one
		// below would render unstyled (TestEveryTemplateClassExistsInAStylesheet).
		//
		// This comment deliberately does NOT name the rejected class. The content
		// glob scrapes these .go files as plain text and does not know what a Go
		// comment is, so writing the class here MINTED the very rule the comment
		// says does not exist — the note falsified itself and shipped six dead
		// bytes of CSS.
		h.H2(h.Class("text-sm font-semibold text-slate-300 mb-2"),
			g.Text("Workflows that use this model")),
		// State the inference plainly. The relation is derived from FILENAMES, and a
		// user who does not know that cannot judge a wrong entry.
		h.P(h.Class("mb-3 text-xs text-slate-400"),
			g.Text("These workflows in your library reference a file by name that matches one of "+
				"your local files for this model. Matching is by filename, so a renamed file is missed.")),
		h.Ul(h.Class("cm-uses-list"), g.Group(rows)),
	}
	if extra > 0 {
		body = append(body, h.P(h.Class("mt-2 text-xs text-slate-500"),
			g.Text("+"+strconv.Itoa(extra)+" more in your workflow library.")))
	}
	return card(body...)
}
