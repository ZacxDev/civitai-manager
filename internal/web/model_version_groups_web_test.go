package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// groupedTabsView builds a modelDetailView whose model carries the given version
// summaries, with the selected version + local set, for unit-testing
// modelVersionTabs's flat vs grouped rendering.
func groupedTabsView(sel int, vers []civitai.ModelVersionSummary, local map[int]bool) modelDetailView {
	if local == nil {
		local = map[int]bool{}
	}
	return modelDetailView{
		Model:             &civitai.ModelDetail{ID: 7, Name: "M", ModelVersions: vers},
		SelectedVersionID: sel,
		LocalVersionIDs:   local,
	}
}

// manyMultiBaseVersions builds 9 versions spanning 3 EXACT base models
// (SDXL 1.0 ×4, Pony ×3, Illustrious ×2) — over the threshold AND multi-base, so
// the grouped path applies. IDs are 1..9; the group order is first-seen:
// [SDXL 1.0, Pony, Illustrious].
func manyMultiBaseVersions() []civitai.ModelVersionSummary {
	bases := []string{
		"SDXL 1.0", "SDXL 1.0", "SDXL 1.0", "SDXL 1.0",
		"Pony", "Pony", "Pony",
		"Illustrious", "Illustrious",
	}
	out := make([]civitai.ModelVersionSummary, 0, len(bases))
	for i, b := range bases {
		out = append(out, civitai.ModelVersionSummary{ID: i + 1, Name: fmt.Sprintf("v%d", i+1), BaseModel: b})
	}
	return out
}

// TestVersionGroupsRendersSelectorWhenManyMultiBase: >8 versions spanning >1 base
// model → the base-model selector renders (one pill per distinct base model), the
// selected version's group is the default-shown one, and every version tab keeps
// the identical htmx contract + the in-library ✓.
func TestVersionGroupsRendersSelectorWhenManyMultiBase(t *testing.T) {
	vers := manyMultiBaseVersions()
	// Selected version 6 is in the "Pony" group (the 2nd group, index 1).
	// Version 2 (SDXL) is in the user's library.
	out := renderString(t, modelVersionTabs(groupedTabsView(6, vers, map[int]bool{2: true})))

	if !strings.Contains(out, `data-cm-vgroups="true"`) {
		t.Fatalf("grouped path should render the vgroups wrapper:\n%s", out)
	}
	// One pill per distinct base model (3), each labeled with the base model.
	for _, bm := range []string{"SDXL 1.0", "Pony", "Illustrious"} {
		if !strings.Contains(out, ">"+bm+"</span>") {
			t.Errorf("selector missing a pill for base model %q:\n%s", bm, out)
		}
	}
	if n := strings.Count(out, `onclick="cmVGroup(this)"`); n != 3 {
		t.Errorf("want 3 base-model pills, got %d:\n%s", n, out)
	}
	// The active version's group (Pony, index 1) is the default-shown group: its
	// pill is active and exactly one pill is active.
	if strings.Count(out, "cm-vgroup-pill-active") != 1 {
		t.Errorf("exactly one pill should be active:\n%s", out)
	}
	if !strings.Contains(out, `class="cm-vgroup-pill cm-vgroup-pill-active" data-cm-vgroup="1" aria-pressed="true" onclick="cmVGroup(this)"><span>Pony</span>`) {
		t.Errorf("the selected version's group (Pony) should be the active pill:\n%s", out)
	}
	// Exactly one group panel is visible (the active one); the other 2 are hidden.
	if n := strings.Count(out, `hidden=""`); n != 2 {
		t.Errorf("want 2 hidden group panels (all but the active group), got %d:\n%s", n, out)
	}
	// Every version tab keeps the full htmx contract + no-JS href.
	for _, vid := range []int{1, 6, 9} {
		for _, want := range []string{
			fmt.Sprintf(`hx-get="/models/7?version=%d"`, vid),
			fmt.Sprintf(`href="/models/7?version=%d"`, vid),
		} {
			if !strings.Contains(out, want) {
				t.Errorf("version %d tab missing %q:\n%s", vid, want, out)
			}
		}
	}
	for _, want := range []string{
		`hx-target="#version-region"`,
		`hx-swap="innerHTML"`,
		`hx-push-url="true"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("version tabs missing shared contract attr %q:\n%s", want, out)
		}
	}
	// The active version's tab (6) carries aria-current, and the in-library ✓ shows
	// for the owned version (2).
	if !strings.Contains(out, `aria-current="true"`) {
		t.Errorf("the selected version tab should carry aria-current:\n%s", out)
	}
	if !strings.Contains(out, "In your library") {
		t.Errorf("an in-library version should render the ✓:\n%s", out)
	}
}

// TestVersionGroupsFlatWhenFewVersions: <= threshold versions → the flat strip
// renders UNCHANGED and NO selector markup appears, even across multiple base
// models.
func TestVersionGroupsFlatWhenFewVersions(t *testing.T) {
	vers := []civitai.ModelVersionSummary{
		{ID: 1, Name: "v1", BaseModel: "SDXL 1.0"},
		{ID: 2, Name: "v2", BaseModel: "Pony"},
		{ID: 3, Name: "v3", BaseModel: "Illustrious"},
	}
	out := renderString(t, modelVersionTabs(groupedTabsView(2, vers, nil)))

	if strings.Contains(out, "cm-vgroup-pill") || strings.Contains(out, "data-cm-vgroups") {
		t.Errorf("few versions must NOT render the base-model selector:\n%s", out)
	}
	if !strings.Contains(out, `class="cm-version-tabs"`) {
		t.Errorf("flat strip should render the cm-version-tabs row:\n%s", out)
	}
	// All 3 tabs present with their contract; the selected one active.
	if strings.Count(out, `onclick="cmVGroup(this)"`) != 0 {
		t.Errorf("no pill onclick handlers in the flat path:\n%s", out)
	}
	if strings.Count(out, "cm-version-tab-active") != 1 {
		t.Errorf("exactly one active tab in the flat path:\n%s", out)
	}
}

// TestVersionGroupsFlatWhenSingleBaseModel: MANY versions but all ONE base model
// → flat strip (grouping can't help), NO selector.
func TestVersionGroupsFlatWhenSingleBaseModel(t *testing.T) {
	var vers []civitai.ModelVersionSummary
	for i := 1; i <= 12; i++ {
		vers = append(vers, civitai.ModelVersionSummary{ID: i, Name: fmt.Sprintf("v%d", i), BaseModel: "SDXL 1.0"})
	}
	out := renderString(t, modelVersionTabs(groupedTabsView(1, vers, nil)))

	if strings.Contains(out, "cm-vgroup-pill") || strings.Contains(out, "data-cm-vgroups") {
		t.Errorf("a single-base-model list must NOT render the selector:\n%s", out)
	}
	if !strings.Contains(out, `class="cm-version-tabs"`) {
		t.Errorf("single-base-model many-version list should stay a flat strip:\n%s", out)
	}
}

// TestVersionGroupsSwapReRendersActiveGroup: an HX version swap to a version in a
// DIFFERENT group re-renders the card with that version's group shown by default.
func TestVersionGroupsSwapReRendersActiveGroup(t *testing.T) {
	reader := newModelReader(t)
	reader.model = &civitai.ModelDetail{
		ID: 7, Name: "Great Model", Type: "Checkpoint",
		ModelVersions: manyMultiBaseVersions(),
	}
	srv := newModelServer(t, reader)

	// Version 8 is in the "Illustrious" group (index 2, the 3rd group).
	code, body := hxGet(t, srv, "/models/7?version=8")
	if code != 200 {
		t.Fatalf("HX swap = %d", code)
	}
	if !strings.Contains(body, `data-cm-vgroups="true"`) {
		t.Fatalf("swap fragment should carry the grouped selector:\n%s", body)
	}
	// The Illustrious pill (index 2) is the active/default-shown group after the swap.
	if !strings.Contains(body, `class="cm-vgroup-pill cm-vgroup-pill-active" data-cm-vgroup="2" aria-pressed="true" onclick="cmVGroup(this)"><span>Illustrious</span>`) {
		t.Errorf("after swapping to a version in the Illustrious group, that group should be the active pill:\n%s", body)
	}
	if strings.Count(body, "cm-vgroup-pill-active") != 1 {
		t.Errorf("exactly one active pill after swap:\n%s", body)
	}
}

// TestVersionGroupsEscaping: a <script> in a version name and markup in a base
// model string are escaped in both the version tab and the pill label.
func TestVersionGroupsEscaping(t *testing.T) {
	vers := manyMultiBaseVersions()
	// Inject markup into a version name and a distinct base-model group so both the
	// tab label AND the pill label are exercised.
	vers = append(vers, civitai.ModelVersionSummary{
		ID: 99, Name: "<script>alert(1)</script>", BaseModel: "<b>Flux</b>",
	})
	out := renderString(t, modelVersionTabs(groupedTabsView(6, vers, nil)))

	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Errorf("a version name with markup must be escaped, not rendered raw:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("version name should appear escaped:\n%s", out)
	}
	if strings.Contains(out, "<b>Flux</b>") {
		t.Errorf("a base-model string with markup must be escaped in the pill label:\n%s", out)
	}
	if !strings.Contains(out, "&lt;b&gt;Flux&lt;/b&gt;") {
		t.Errorf("base-model string should appear escaped:\n%s", out)
	}
}
