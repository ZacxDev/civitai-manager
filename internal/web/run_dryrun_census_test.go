package web

import (
	"encoding/json"
	"sort"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// The two things comfy.Preflight structurally cannot tell you, both of which
// changed the reading of a real workflow (590) when checked by hand:
//
//  1. WHICH nodes never reached validation. Preflight validates the CONVERTED
//     graph, so a node the converter dropped is invisible to it — and "3 missing
//     models" out of a converted graph is not the same claim as "3 missing models
//     in this workflow". probeNodeCensus accounts for every node so a silent drop
//     cannot hide.
//  2. WHICH resources belong to bypassed branches. Preflight only ever sees the
//     active pipeline, so a workflow's stored resource list can name files it never
//     checked. Un-bypass that branch and they become blockers.
//
// Both live in a _test.go on purpose: they are diagnostics for the probe, not
// production behaviour, and tier B of the deadcode gate never loads test files.

// probeCensus accounts for every node in a stored UI graph.
type probeCensus struct {
	// UINodes is every node in the stored document, including UI-only ones.
	UINodes int
	// Inactive is nodes at mode 2 (mute) or 4 (bypass) — deliberately excluded from
	// the run, not missing from it.
	Inactive int
	// Active is the rest: what the author intended to execute.
	Active int
	// Converted is how many nodes the api graph ended up with.
	Converted int
	// Unconverted maps a class type to the ACTIVE node ids of that class which did
	// not reach the api graph. Two very different causes land here and the class name
	// is what separates them: a class the converter CUT because it is not installed
	// (a real blocker, also reported as a conversion warning), versus a UI-only node
	// the converter correctly drops (rgthree Label / Fast Bypasser, Note, Reroute).
	Unconverted map[string][]string
}

// probeNodeCensus accounts for every node in uiGraph against the converted apiGraph.
//
// ⚠ KNOWN FALSE-POSITIVE SHAPE, stated rather than hidden: a Subgraph node expands
// into several api nodes whose ids are prefixed (`"<instance>:<interior>"`), so the
// subgraph node's own id is legitimately absent from the api graph and is reported
// here as unconverted. The class name makes that readable; do not treat a non-empty
// Unconverted as proof of a defect without looking at what is in it.
func probeNodeCensus(uiGraph, apiGraph json.RawMessage) probeCensus {
	var doc struct {
		Nodes []struct {
			ID   json.RawMessage `json:"id"`
			Type string          `json:"type"`
			Mode int             `json:"mode"`
		} `json:"nodes"`
	}
	c := probeCensus{Unconverted: map[string][]string{}}
	if err := json.Unmarshal(uiGraph, &doc); err != nil {
		return c
	}

	var api map[string]json.RawMessage
	converted := map[string]bool{}
	if err := json.Unmarshal(apiGraph, &api); err == nil {
		c.Converted = len(api)
		for id := range api {
			converted[id] = true
		}
	}

	c.UINodes = len(doc.Nodes)
	for _, n := range doc.Nodes {
		// mode 2 = mute, 4 = bypass. Anything else counts as active: a mode the
		// author's frontend invented must not silently read as "excluded".
		if n.Mode == 2 || n.Mode == 4 {
			c.Inactive++
			continue
		}
		c.Active++
		id := rawIDToString(n.ID)
		if id == "" || converted[id] {
			continue
		}
		class := n.Type
		if class == "" {
			class = "(untyped)"
		}
		c.Unconverted[class] = append(c.Unconverted[class], id)
	}
	for class := range c.Unconverted {
		sort.Strings(c.Unconverted[class])
	}
	return c
}

// probeDormantResources returns the model filenames a stored graph REFERENCES but
// which no running node would load — i.e. they hang off bypassed or muted nodes.
//
// 🔴 This is a SET DIFFERENCE BETWEEN TWO PRODUCTION EXTRACTORS, never a rule of its
// own: comfy.ExtractResourcesAny ("everything this workflow references", what the
// workflow page and workflows.resources show) minus comfy.ExtractActiveResources
// ("what would actually load"). Those two already encode the distinction — what
// counts as a model filename lives once, in extractResourcesUI — so this cannot
// drift from them. Re-deriving "looks like a model file" here would be a third copy.
//
// Order is sorted for a stable report; both extractors dedup already.
func probeDormantResources(format string, graph json.RawMessage) []string {
	all, err := comfy.ExtractResourcesAny(format, graph)
	if err != nil {
		return nil
	}
	active, err := comfy.ExtractActiveResources(format, graph)
	if err != nil {
		return nil
	}
	live := make(map[string]bool, len(active))
	for _, r := range active {
		live[r] = true
	}
	var dormant []string
	for _, r := range all {
		if !live[r] {
			dormant = append(dormant, r)
		}
	}
	sort.Strings(dormant)
	return dormant
}
