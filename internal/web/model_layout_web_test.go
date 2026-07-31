package web

import (
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
)

// TestModelDetailSectionOrder proves the model detail page renders sections in
// the current order: header actions (download) → version tabs → version metadata
// → showcase → community → Description → Tags.
//
// It changed with the download-in-header rework: the download used to be a
// standalone CARD between the showcase and the community feed. It is now the
// FIRST thing in the header's action group, above the version tabs, and the
// version-metadata disclosure it used to carry sits directly under those tabs.
// Everything below the version region is unmoved.
func TestModelDetailSectionOrder(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	idx := func(s string) int {
		i := strings.Index(body, s)
		if i < 0 {
			t.Fatalf("model page missing %q", s)
		}
		return i
	}

	download := idx("cm-dl-menu") // the header's download control
	tabs := idx("cm-version-tabs")
	meta := idx("cm-meta-reveal") // the version-metadata disclosure
	showcase := idx("cm-showcase-lg")
	community := idx(`id="community-feed"`)
	desc := idx(">Description<")
	tags := idx("cm-tag-chip")

	// The retired card's heading must be gone from the page body entirely.
	if strings.Contains(body, ">Download</h2>") {
		t.Errorf("the standalone download card heading must be gone from the page body")
	}
	if !(download < tabs) {
		t.Errorf("the header download control (%d) should render before the version tabs (%d)", download, tabs)
	}
	if !(tabs < meta) {
		t.Errorf("version tabs (%d) should render before the version metadata (%d)", tabs, meta)
	}
	if !(meta < showcase) {
		t.Errorf("version metadata (%d) should render before the showcase (%d)", meta, showcase)
	}
	if !(showcase < community) {
		t.Errorf("showcase (%d) should render before the community feed (%d)", showcase, community)
	}
	if !(community < desc) {
		t.Errorf("community feed (%d) should render before the Description (%d)", community, desc)
	}
	if !(desc < tags) {
		t.Errorf("Description (%d) should render before the Tags chips (%d)", desc, tags)
	}
	// The old separate gallery grid card must remain gone (images shown once).
	if strings.Contains(body, "grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-4") {
		t.Error("the separate gallery grid card should be removed (images shown once)")
	}
	// Description still wrapped in the overflow-constraining container.
	if !strings.Contains(body, "cm-model-desc") {
		t.Error("description should be wrapped in the cm-model-desc container")
	}
	// The showcase still renders the inline images (safe + blurred NSFW under blur).
	if !strings.Contains(body, "safe.jpeg") || !strings.Contains(body, "nsfw.jpeg") {
		t.Error("showcase should render the inline showcase images")
	}
}

// TestModelVersionTabsMarkup proves the version tab bar renders one tab per model
// version, marks the SELECTED version's tab active, wires each tab with the exact
// htmx swap contract (hx-get/hx-target/hx-swap/hx-push-url) + the no-JS href
// fallback, and shows the ✓ in-library indicator only for owned versions.
func TestModelVersionTabsMarkup(t *testing.T) {
	view := modelDetailView{
		Model: &civitai.ModelDetail{
			ID: 7, Name: "Great Model",
			ModelVersions: []civitai.ModelVersionSummary{
				{ID: 11, Name: "v2", BaseModel: "SDXL"},
				{ID: 10, Name: "v1", BaseModel: "SD 1.5"},
			},
		},
		SelectedVersionID: 11,
		LocalVersionIDs:   map[int]bool{10: true}, // user owns v1 only
	}
	out := renderString(t, modelVersionTabs(view))

	// One tab per version, each carrying the full htmx contract + href fallback.
	for _, verID := range []int{11, 10} {
		href := "/models/7?version=" + strconv.Itoa(verID)
		for _, want := range []string{
			`hx-get="` + href + `"`,
			`hx-target="#version-region"`,
			`hx-swap="innerHTML"`,
			`hx-push-url="true"`,
			`href="` + href + `"`,
		} {
			if strings.Count(out, want) < 1 {
				t.Errorf("version %d tab missing %q:\n%s", verID, want, out)
			}
		}
	}
	// Exactly two tabs (two hx-get occurrences).
	if n := strings.Count(out, "hx-get="); n != 2 {
		t.Errorf("want 2 version tabs, got %d hx-get attrs", n)
	}
	// The active (selected) tab carries the active class + aria-current.
	if !strings.Contains(out, "cm-version-tab-active") {
		t.Error("selected version tab should carry the active class")
	}
	if !strings.Contains(out, `aria-current="true"`) {
		t.Error("selected version tab should carry aria-current")
	}
	// Exactly one active tab.
	if n := strings.Count(out, "cm-version-tab-active"); n != 1 {
		t.Errorf("want exactly 1 active tab, got %d", n)
	}
	// The ✓ in-library indicator shows once (only v1 is owned).
	if n := strings.Count(out, `aria-label="In your library"`); n != 1 {
		t.Errorf("want the ✓ indicator on exactly the 1 owned version, got %d", n)
	}
}

// TestDetailShowcaseEnlargedNotCards proves the DETAIL showcase carries the
// detail-only .cm-showcase-lg modifier and requests the larger thumbnail width
// (detailThumbnailWidth=800), while the shared search/library card carousel does
// NOT carry the large class and keeps the 450px default — so cards are unchanged.
func TestDetailShowcaseEnlargedNotCards(t *testing.T) {
	// A properly-shaped civitai CDN url so civitaiThumbURL applies the transform.
	imgs := []galleryImage{{URL: "https://image.civitai.com/bucket/uuid/pic.jpeg", NSFWLevel: 1, Width: 4096, Height: 2048}}

	detail := renderString(t, showcaseCard(7, imgs, fullMaturityRange()))
	if !strings.Contains(detail, "cm-showcase-lg") {
		t.Error("detail showcase should carry the .cm-showcase-lg enlargement modifier")
	}
	if !strings.Contains(detail, "width=800") {
		t.Errorf("detail showcase should request the larger thumbnail width (800):\n%s", detail)
	}

	card := renderString(t, modelCardCarousel(7, imgs, fullMaturityRange()))
	if strings.Contains(card, "cm-showcase-lg") {
		t.Error("the shared card carousel must NOT carry the detail-only large class")
	}
	if !strings.Contains(card, "width=450") {
		t.Errorf("the shared card carousel should keep the 450px default width:\n%s", card)
	}
	if strings.Contains(card, "width=800") {
		t.Error("the shared card carousel must NOT request the enlarged detail width")
	}
}

// TestModelVersionTabsEscaping proves untrusted version names AND file names are
// escaped (g.Text), so a <script>-bearing name cannot inject markup.
func TestModelVersionTabsEscaping(t *testing.T) {
	verID := 11
	view := modelDetailView{
		Model: &civitai.ModelDetail{
			ID: 7, Name: "m",
			ModelVersions: []civitai.ModelVersionSummary{
				{ID: verID, Name: "<script>alert('ver')</script>", BaseModel: "SDXL"},
			},
		},
		SelectedVersionID: verID,
		Version: &civitai.ModelVersionDetail{
			ID: verID, ModelID: 7, BaseModel: "SDXL",
			// TWO files: file names are only PRINTED by the header download control's
			// multi-file menu shape (one file renders a bare "Download" button), so a
			// single-file fixture would make the escaping assertion below vacuous.
			Files: []civitai.ModelVersionFile{
				{ID: 1, Name: "<script>alert('file')</script>.safetensors", Type: "Model", SizeKB: 1024},
				{ID: 2, Name: "<script>alert('file2')</script>.vae.pt", Type: "VAE", SizeKB: 512},
			},
		},
	}

	tabs := renderString(t, modelVersionTabs(view))
	if strings.Contains(tabs, "<script>alert('ver')") {
		t.Errorf("version name must be escaped in the tab bar:\n%s", tabs)
	}
	if !strings.Contains(tabs, "&lt;script&gt;") {
		t.Error("version name should appear HTML-escaped")
	}

	// The file list now renders inside the header's download menu, not a card.
	files := renderString(t, headerDownloadControl(view, "csrf-token"))
	if strings.Contains(files, "<script>alert('file')") {
		t.Errorf("file name must be escaped in the file list:\n%s", files)
	}
	if !strings.Contains(files, "&lt;script&gt;") {
		t.Error("file name should appear HTML-escaped")
	}
}
