package web

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ---------------------------------------------------------------------------
// Model / workflow DETAIL page rework.
//
// HONEST SCOPE NOTE, once, for the whole file: no browser is available in this
// environment, so every assertion below is on the rendered MARKUP and on the
// shipped CSS text. Hover, the tab-underline animation, the <details> open/close
// motion and the popover's visibility are proven to be SHIPPED and WIRED TO THE
// RIGHT SELECTORS — they are not observed rendering.
// ---------------------------------------------------------------------------

// modelRawWithVersions builds a GetModel-shaped raw body whose modelVersions[]
// carry ids + publishedAt, in the exact order given.
func modelRawWithVersions(t *testing.T, vers []struct {
	ID          int
	PublishedAt string
}) []byte {
	t.Helper()
	items := make([]map[string]any, 0, len(vers))
	for _, v := range vers {
		items = append(items, map[string]any{"id": v.ID, "publishedAt": v.PublishedAt})
	}
	b, err := json.Marshal(map[string]any{"modelVersions": items})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// tabHTML slices the rendered tab bar down to the single tab whose hx-get names
// the given version, so a per-tab assertion cannot accidentally be satisfied by a
// NEIGHBOURING tab's content. Tabs are <a> elements, so the slice runs to the
// closing </a>.
func tabHTML(t *testing.T, out string, modelID, versionID int) string {
	t.Helper()
	marker := fmt.Sprintf(`hx-get="/models/%d?version=%d"`, modelID, versionID)
	i := strings.Index(out, marker)
	if i < 0 {
		t.Fatalf("no tab for version %d in:\n%s", versionID, out)
	}
	start := strings.LastIndex(out[:i], "<a ")
	if start < 0 {
		t.Fatalf("version %d tab has no opening <a>:\n%s", versionID, out)
	}
	end := strings.Index(out[start:], "</a>")
	if end < 0 {
		t.Fatalf("version %d tab has no closing </a>:\n%s", versionID, out)
	}
	return out[start : start+end]
}

// --- 1. Header stats: icons, numbers, and NO comment count -------------------

// TestModelHeaderStatsAreIconsWithoutComments proves the header stat row renders
// the shared icon glyphs + their counts, keeps the labels available to assistive
// tech (the words themselves are gone), and drops the COMMENT count entirely —
// both the word and the number.
func TestModelHeaderStatsAreIconsWithoutComments(t *testing.T) {
	m := &civitai.ModelDetail{
		ID: 7, Name: "Great Model", Type: "Checkpoint",
		// Deliberately distinct, non-overlapping, sub-1000 counts so compactCount
		// leaves them verbatim and no assertion can match the wrong number.
		Stats: civitai.ModelStats{DownloadCount: 111, ThumbsUpCount: 222, CommentCount: 999},
	}
	out := renderString(t, modelHeaderCard(
		modelDetailView{Model: m}, nil, "csrf", "https://civitai.com"))

	present := []struct{ what, want string }{
		{"the shared stat row class", `class="cm-stats`},
		{"the download glyph", downloadIconSVG},
		{"the thumbs-up glyph", thumbsUpIconSVG},
		{"the downloads count", ">111<"},
		{"the likes count", ">222<"},
		{"the downloads label for AT", `aria-label="downloads"`},
		{"the likes label for AT", `aria-label="likes"`},
	}
	for _, c := range present {
		if !strings.Contains(out, c.want) {
			t.Errorf("header should render %s (%q):\n%s", c.what, c.want, out)
		}
	}

	absent := []struct{ what, want string }{
		{"the Downloads word", "Downloads:"},
		{"the Likes word", "Likes:"},
		{"the Comments word", "Comments"},
		{"the comment count", "999"},
		{"a comments aria-label", `aria-label="comments"`},
	}
	for _, c := range absent {
		if strings.Contains(out, c.want) {
			t.Errorf("header must not render %s (%q):\n%s", c.what, c.want, out)
		}
	}
}

// --- 2. The header and the version tabs are ONE card -------------------------

// TestModelHeaderAndTabsAreOneCard proves the merge: modelHeaderCard emits
// EXACTLY ONE card wrapper and that single card contains both the <h1> and the
// version tab strip — i.e. no card boundary separates them any more.
func TestModelHeaderAndTabsAreOneCard(t *testing.T) {
	view := modelDetailView{
		Model: &civitai.ModelDetail{
			ID: 7, Name: "Great Model", Type: "Checkpoint",
			ModelVersions: []civitai.ModelVersionSummary{{ID: 11, Name: "v2", BaseModel: "SDXL"}},
		},
		SelectedVersionID: 11,
	}
	out := renderString(t, modelHeaderCard(view, nil, "csrf", "https://civitai.com"))

	if n := strings.Count(out, `data-civitai-ui="card"`); n != 1 {
		t.Errorf("header+tabs must be exactly ONE card, got %d card wrappers:\n%s", n, out)
	}
	for _, want := range []string{"<h1", "cm-version-tabs", "Great Model"} {
		if !strings.Contains(out, want) {
			t.Errorf("the combined card is missing %q:\n%s", want, out)
		}
	}
	// The old standalone "Versions" card heading is gone (the tabs label themselves
	// via role=tablist + aria-label now).
	if strings.Contains(out, ">Versions<") {
		t.Errorf("the separate Versions card heading should be gone:\n%s", out)
	}
	if !strings.Contains(out, `aria-label="Model versions"`) {
		t.Errorf("the tablist should be labeled for AT now that the heading is gone:\n%s", out)
	}
}

// TestModelPageHasNoCardBoundaryBetweenTitleAndTabs is the page-level counterpart:
// on the real page, no card wrapper opens between the model <h1> and the version
// tab strip.
func TestModelPageHasNoCardBoundaryBetweenTitleAndTabs(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	h1 := strings.Index(body, "<h1")
	tabs := strings.Index(body, "cm-version-tabs")
	if h1 < 0 || tabs < 0 {
		t.Fatalf("page is missing the h1 (%d) or the tab strip (%d)", h1, tabs)
	}
	if h1 > tabs {
		t.Fatalf("the model title should render before the version tabs (h1=%d tabs=%d)", h1, tabs)
	}
	if n := strings.Count(body[h1:tabs], `data-civitai-ui="card"`); n != 0 {
		t.Errorf("a card boundary (%d) opens between the title and the version tabs — they must "+
			"share one card:\n%s", n, body[h1:tabs])
	}
	// The combined card still lives inside the swapped region, so the active-tab
	// highlight re-renders on a version change.
	region := strings.Index(body, `id="version-region"`)
	if region < 0 || region > h1 {
		t.Errorf("the combined header card must sit INSIDE #version-region (region=%d h1=%d)", region, h1)
	}
}

// --- 3 + 4. Version tabs: grouping preserved, per-version publish popover -----

// TestVersionPublishedTimes covers the by-ID parse that feeds the tab popovers,
// including every way the raw body can be malformed.
func TestVersionPublishedTimes(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want map[int]string // version id -> RFC3339 (absent key == expected absent)
	}{
		{"nil raw", nil, map[int]string{}},
		{"garbage", []byte("not json"), map[int]string{}},
		{"no versions", []byte(`{"modelVersions":[]}`), map[int]string{}},
		{
			"keyed by id, not position",
			[]byte(`{"modelVersions":[{"id":11,"publishedAt":"2020-01-02T03:04:05.000Z"},` +
				`{"id":10,"publishedAt":"2026-07-01T00:00:00.000Z"}]}`),
			map[int]string{11: "2020-01-02T03:04:05Z", 10: "2026-07-01T00:00:00Z"},
		},
		{
			"unparseable / missing / zero-id entries are dropped, good ones kept",
			[]byte(`{"modelVersions":[{"id":1,"publishedAt":"nope"},{"id":2},` +
				`{"id":0,"publishedAt":"2026-01-01T00:00:00Z"},` +
				`{"id":3,"publishedAt":"2026-02-03T00:00:00Z"}]}`),
			map[int]string{3: "2026-02-03T00:00:00Z"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := versionPublishedTimes(c.raw)
			if got == nil {
				t.Fatal("must return a non-nil map")
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d entries %v, want %d %v", len(got), got, len(c.want), c.want)
			}
			for id, wantRFC := range c.want {
				want, err := time.Parse(time.RFC3339, wantRFC)
				if err != nil {
					t.Fatal(err)
				}
				if !got[id].Equal(want) {
					t.Errorf("version %d: got %v, want %v", id, got[id], want)
				}
			}
		})
	}
}

// TestVersionTabPublishDateIsPerVersionNotPositional is the ADVERSARIAL fixture
// for the documented CivitAI gotcha: modelVersions[] is ordered by the creator's
// `index`, NOT by publish date. Here position 0 is the OLDEST version, so any
// implementation that reads dates positionally renders both tabs' dates swapped.
func TestVersionTabPublishDateIsPerVersionNotPositional(t *testing.T) {
	oldStamp := time.Now().Add(-400 * 24 * time.Hour).UTC() // > 1 year → "1 year ago"
	newStamp := time.Now().Add(-21 * 24 * time.Hour).UTC()  // 3 weeks  → "3 weeks ago"

	raw := modelRawWithVersions(t, []struct {
		ID          int
		PublishedAt string
	}{
		// index 0 (the creator's PRIMARY version) is the OLDER one — on purpose.
		{ID: 11, PublishedAt: oldStamp.Format(time.RFC3339)},
		{ID: 10, PublishedAt: newStamp.Format(time.RFC3339)},
	})

	view := modelDetailView{
		Model: &civitai.ModelDetail{
			ID: 7, Name: "M",
			ModelVersions: []civitai.ModelVersionSummary{
				{ID: 11, Name: "primary", BaseModel: "SDXL"},
				{ID: 10, Name: "later", BaseModel: "SDXL"},
			},
		},
		SelectedVersionID:  11,
		VersionPublishedAt: versionPublishedTimes(raw),
	}
	out := renderString(t, modelVersionTabs(view))

	cases := []struct {
		versionID int
		stamp     time.Time
	}{
		{11, oldStamp},
		{10, newStamp},
	}
	for _, c := range cases {
		tab := tabHTML(t, out, 7, c.versionID)
		wantRel := "Published " + humanSince(c.stamp)
		wantAbs := c.stamp.Local().Format("2006-01-02")
		if !strings.Contains(tab, wantRel) {
			t.Errorf("version %d tab should carry %q (its OWN publishedAt):\n%s", c.versionID, wantRel, tab)
		}
		if !strings.Contains(tab, wantAbs) {
			t.Errorf("version %d tab popover should carry the absolute date %q:\n%s", c.versionID, wantAbs, tab)
		}
	}

	// The popover reuses the EXISTING mechanism (the .cm-updated wrapper the shared
	// hover controller delegates on), not a second one.
	for _, want := range []string{"cm-updated cm-vdate", "cm-updated-pop", `role="tooltip"`, "cm-vdate-ico"} {
		if !strings.Contains(out, want) {
			t.Errorf("version tabs missing the reused popover wiring %q:\n%s", want, out)
		}
	}
	// One date affordance per version, no more.
	if n := strings.Count(out, "cm-vdate-ico"); n != 2 {
		t.Errorf("want one date icon per version (2), got %d", n)
	}
}

// TestVersionTabPublishDateOmittedWhenUnknown proves a version with no parseable
// date renders no date affordance at all (rather than a wrong or empty one).
func TestVersionTabPublishDateOmittedWhenUnknown(t *testing.T) {
	view := modelDetailView{
		Model: &civitai.ModelDetail{ID: 7, Name: "M",
			ModelVersions: []civitai.ModelVersionSummary{{ID: 11, Name: "v2", BaseModel: "SDXL"}}},
		SelectedVersionID:  11,
		VersionPublishedAt: map[int]time.Time{}, // nothing parseable
	}
	out := renderString(t, modelVersionTabs(view))
	for _, banned := range []string{"cm-vdate", "cm-updated-pop", "Published "} {
		if strings.Contains(out, banned) {
			t.Errorf("a version with no known date must render no date affordance (%q):\n%s", banned, out)
		}
	}
}

// TestVersionTabsGroupingStillApplies proves the base-model grouping behaviour is
// UNCHANGED by the rework: few versions (or one base model) → a flat strip; many
// versions across several base models → the pill selector with one panel per base
// model, the active version's group shown server-side.
func TestVersionTabsGroupingStillApplies(t *testing.T) {
	mk := func(n int, baseModels ...string) []civitai.ModelVersionSummary {
		var out []civitai.ModelVersionSummary
		for i := 0; i < n; i++ {
			out = append(out, civitai.ModelVersionSummary{
				ID:        i + 1,
				Name:      fmt.Sprintf("v%d", i+1),
				BaseModel: baseModels[i%len(baseModels)],
			})
		}
		return out
	}
	cases := []struct {
		name        string
		versions    []civitai.ModelVersionSummary
		wantGrouped bool
	}{
		{"few versions, several base models", mk(4, "SDXL", "Pony"), false},
		{"many versions, ONE base model", mk(12, "SDXL"), false},
		{"at the threshold", mk(versionGroupThreshold, "SDXL", "Pony"), false},
		{"many versions, several base models", mk(versionGroupThreshold+1, "SDXL", "Pony"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			view := modelDetailView{
				Model:             &civitai.ModelDetail{ID: 7, Name: "M", ModelVersions: c.versions},
				SelectedVersionID: c.versions[0].ID,
			}
			out := renderString(t, modelVersionTabs(view))
			grouped := strings.Contains(out, "cm-vgroup-pill")
			if grouped != c.wantGrouped {
				t.Fatalf("grouped=%v, want %v:\n%s", grouped, c.wantGrouped, out)
			}
			// Every version gets a tab either way.
			if n := strings.Count(out, "hx-get="); n != len(c.versions) {
				t.Errorf("want %d version tabs, got %d", len(c.versions), n)
			}
			if !c.wantGrouped {
				return
			}
			// Grouped: one pill + one panel per distinct base model, and exactly one
			// panel is visible (the rest carry the boolean `hidden`).
			if n := strings.Count(out, `data-cm-vgroup="0"`); n != 2 { // pill + panel
				t.Errorf("group 0 should have one pill and one panel, got %d markers", n)
			}
			panels := strings.Count(out, "cm-version-tabs cm-vgroup")
			hidden := strings.Count(out, "hidden=")
			if panels-hidden != 1 {
				t.Errorf("exactly one group panel should be visible, got %d of %d", panels-hidden, panels)
			}
			// The visible group is the ACTIVE version's, chosen server-side.
			if !strings.Contains(out, "cm-vgroup-pill cm-vgroup-pill-active") {
				t.Errorf("the active group's pill should be marked server-side:\n%s", out)
			}
		})
	}
}

// --- 5. The download card ----------------------------------------------------

// downloadCardView builds a view with a selected version carrying files and
// metadata, for the download-card tests.
func downloadCardView(files []civitai.ModelVersionFile) modelDetailView {
	return modelDetailView{
		Model: &civitai.ModelDetail{ID: 7, Name: "M"},
		Version: &civitai.ModelVersionDetail{
			ID: 11, ModelID: 7, BaseModel: "SDXL",
			TrainedWords: []string{"mytoken"},
			Files:        files,
		},
		SelectedVersionID: 11,
		// A full civitai ISO stamp — the card must show only its date part.
		PublishedAt: "2026-01-15T20:50:47.173Z",
	}
}

// TestDownloadCardCollapsedByDefault proves the reworked "Files & metadata" card:
// the download action is the visible primary action, and the metadata is a native
// <details> that is COLLAPSED by default.
func TestDownloadCardCollapsedByDefault(t *testing.T) {
	out := renderString(t, versionDownloadCard(downloadCardView([]civitai.ModelVersionFile{
		{ID: 1, Name: "primary.safetensors", Type: "Model", SizeKB: 2048, DownloadURL: "https://civitai.com/f/1"},
		{ID: 2, Name: "extra.pt", Type: "VAE", SizeKB: 512, DownloadURL: "https://civitai.com/f/2"},
	}), "csrf"))

	for _, want := range []string{
		">Download</h2>",        // the card is named for its primary job
		`class="cm-meta-reveal`, // the disclosure
		"<summary",              // native, keyboard-operable, no JS
		"Version metadata",      // its label
		"cm-meta-chevron",       // the rotating affordance
		"mytoken",               // metadata content is present, just collapsed
		">2026-01-15<",          // the ISO stamp is shown as its date only
	} {
		if !strings.Contains(out, want) {
			t.Errorf("download card missing %q:\n%s", want, out)
		}
	}
	// COLLAPSED: the <details> must not carry the `open` boolean attribute.
	details := out[strings.Index(out, "<details"):]
	if head := details[:strings.Index(details, ">")]; strings.Contains(head, "open") {
		t.Errorf("the metadata disclosure must be collapsed by default, got %q", head)
	}
	// The old heading is gone.
	if strings.Contains(out, "Files &amp; metadata") {
		t.Errorf("the old 'Files & metadata' heading should be gone:\n%s", out)
	}
}

// TestDownloadActionSurvivesBothDisclosureStates proves the download action can
// never be hidden by the metadata disclosure: every Download control renders
// BEFORE the <details> opens, i.e. outside it. Whether the user has expanded the
// metadata or not therefore cannot affect it.
func TestDownloadActionSurvivesBothDisclosureStates(t *testing.T) {
	files := []civitai.ModelVersionFile{
		{ID: 1, Name: "primary.safetensors", Type: "Model", SizeKB: 2048, DownloadURL: "https://civitai.com/f/1"},
		{ID: 2, Name: "extra.pt", Type: "VAE", SizeKB: 512, DownloadURL: "https://civitai.com/f/2"},
	}
	out := renderString(t, versionDownloadCard(downloadCardView(files), "csrf-token"))

	detailsAt := strings.Index(out, "<details")
	if detailsAt < 0 {
		t.Fatalf("expected a metadata disclosure:\n%s", out)
	}
	before, inside := out[:detailsAt], out[detailsAt:]

	if n := strings.Count(before, `hx-post="/models/7/download"`); n != len(files) {
		t.Errorf("all %d download actions must render OUTSIDE the disclosure, found %d before it:\n%s",
			len(files), n, before)
	}
	if strings.Contains(inside, "/models/7/download") {
		t.Errorf("no download action may live inside the collapsible metadata:\n%s", inside)
	}
	// Each POST still carries the CSRF token and its own stable swap target.
	for _, f := range files {
		id := downloadFileID(7, 11, f.ID)
		for _, want := range []string{
			// The hx-vals JSON is HTML-escaped in the attribute value.
			"csrf_token&#34;:&#34;csrf-token",
			fmt.Sprintf("fileId&#34;:&#34;%d", f.ID),
			`id="` + id + `"`,
			`hx-target="#` + id + `"`,
		} {
			if !strings.Contains(before, want) {
				t.Errorf("file %d download control missing %q:\n%s", f.ID, want, before)
			}
		}
	}
	// Exactly one visually-primary (filled) action: the version's first file.
	if n := strings.Count(before, `data-variant="filled"`); n != 1 {
		t.Errorf("exactly one download button should be filled (the primary file), got %d", n)
	}
}

// TestDownloadCardDegradesWithoutVersionOrFiles proves the card never renders a
// broken shell: no selected version → a muted line; a version with no files → the
// "No files." note and NO empty metadata disclosure when there is nothing to show.
func TestDownloadCardDegradesWithoutVersionOrFiles(t *testing.T) {
	none := renderString(t, versionDownloadCard(modelDetailView{
		Model: &civitai.ModelDetail{ID: 7, Name: "M"}}, "csrf"))
	if !strings.Contains(none, "Select a version") {
		t.Errorf("no selected version should degrade to a muted line:\n%s", none)
	}

	empty := renderString(t, versionDownloadCard(modelDetailView{
		Model:   &civitai.ModelDetail{ID: 7, Name: "M"},
		Version: &civitai.ModelVersionDetail{ID: 11, ModelID: 7},
	}, "csrf"))
	if !strings.Contains(empty, "No files.") {
		t.Errorf("a version with no files should say so:\n%s", empty)
	}
	if strings.Contains(empty, "<details") {
		t.Errorf("a version with no metadata must not render an empty disclosure:\n%s", empty)
	}
}

// --- 6 + 7. The workflow-import section --------------------------------------

// TestWorkflowImportSectionStates proves the two states of the import section and
// that the explanatory paragraph under the button is gone from BOTH.
func TestWorkflowImportSectionStates(t *testing.T) {
	cases := []struct {
		name     string
		imported int
		want     []string
		banned   []string
	}{
		{
			name:     "not imported → the import CTA",
			imported: 0,
			want: []string{
				"Import workflow(s)",
				`hx-post="/workflows/discover/7/import"`,
				"csrf_token&#34;:&#34;csrf-token", // hx-vals JSON is HTML-escaped
			},
			banned: []string{"View in library", "/library?tab=workflows"},
		},
		{
			name:     "already imported → View in library, no import CTA",
			imported: 3,
			want: []string{
				"View in library",
				`href="/library?tab=workflows&amp;model=7"`,
				"Already imported",
				"3 workflows",
			},
			banned: []string{"Import workflow(s)", "hx-post=", "/workflows/discover/7/import"},
		},
		{
			name:     "singular copy for exactly one",
			imported: 1,
			want:     []string{"1 workflow from this model is in your workflow library"},
			banned:   []string{"1 workflows"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderString(t, workflowImportDetailCard(7, "csrf-token", c.imported))
			for _, want := range c.want {
				if !strings.Contains(out, want) {
					t.Errorf("import section missing %q:\n%s", want, out)
				}
			}
			for _, banned := range c.banned {
				if strings.Contains(out, banned) {
					t.Errorf("import section must not contain %q:\n%s", banned, out)
				}
			}
			// The copy below the button is removed in EVERY state: the egress note may
			// only survive as an attribute (title / aria-label), never as body text.
			if strings.Contains(out, importEgressNote+"</p>") {
				t.Errorf("the paragraph under the import button should be removed:\n%s", out)
			}
		})
	}
}

// TestDiscoverCardsKeepTheImportNote pins the OTHER side of that removal: the
// discover browse cards still show the egress paragraph, so dropping it on the
// detail page cannot silently strip the disclosure everywhere.
func TestDiscoverCardsKeepTheImportNote(t *testing.T) {
	out := renderString(t, workflowImportAction(7, "csrf"))
	if !strings.Contains(out, importEgressNote+"</p>") {
		t.Errorf("the discover cards must keep the egress paragraph:\n%s", out)
	}
	// And the detail-page variant is the same control minus exactly that node.
	bare := renderString(t, workflowImportActionBare(7, "csrf"))
	if strings.Contains(bare, importEgressNote+"</p>") {
		t.Errorf("the detail-page variant must drop the paragraph:\n%s", bare)
	}
	for _, want := range []string{`hx-post="/workflows/discover/7/import"`, "Import workflow(s)", importEgressNote} {
		if !strings.Contains(bare, want) {
			t.Errorf("the detail-page variant must keep %q (the note moves into title/aria-label):\n%s", want, bare)
		}
	}
}

// TestWorkflowsModelPageDetectsImport drives the real handler end to end: a
// Workflows-type model whose workflows are already in the store renders the
// "View in library" state, and the same model with an empty library renders the
// import CTA.
func TestWorkflowsModelPageDetectsImport(t *testing.T) {
	newWorkflowsServer := func(t *testing.T) *Server {
		t.Helper()
		reader := newModelReader(t)
		m := *reader.model
		m.Type = "Workflows"
		reader.model = &m
		return newModelServer(t, reader)
	}

	t.Run("not imported", func(t *testing.T) {
		srv := newWorkflowsServer(t)
		body := getModelPage(t, srv, "/models/7")
		if !strings.Contains(body, "Import workflow(s)") {
			t.Errorf("an un-imported Workflows model should offer the import CTA")
		}
		if strings.Contains(body, "View in library") {
			t.Errorf("an un-imported Workflows model should not claim it is in the library")
		}
	})

	t.Run("imported", func(t *testing.T) {
		srv := newWorkflowsServer(t)
		modelID := 7
		if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
			Name: "wf", Format: store.WorkflowFormatAPI,
			Graph:  `{"1":{"class_type":"KSampler","inputs":{"seed":1}}}`,
			Source: store.WorkflowSourceCivitai, ModelID: &modelID,
		}); err != nil {
			t.Fatal(err)
		}
		body := getModelPage(t, srv, "/models/7")
		if !strings.Contains(body, "View in library") {
			t.Errorf("an imported Workflows model should offer 'View in library':\n%s", body)
		}
		if strings.Contains(body, "Import workflow(s)") {
			t.Errorf("an imported Workflows model should not re-offer the import CTA")
		}
	})

	t.Run("non-workflows models have no import section at all", func(t *testing.T) {
		srv := newModelServer(t, newModelReader(t)) // Checkpoint
		body := getModelPage(t, srv, "/models/7")
		for _, banned := range []string{"Import workflow(s)", "View in library"} {
			if strings.Contains(body, banned) {
				t.Errorf("a Checkpoint model page must not carry %q", banned)
			}
		}
	})
}

// --- 8. Escaping + URL safety ------------------------------------------------

// TestModelDetailEscapesHostileStrings proves every untrusted civitai string on
// the reworked surfaces — model name, version name, file name — is escaped rather
// than rendered as live markup.
func TestModelDetailEscapesHostileStrings(t *testing.T) {
	const evil = `<script>alert('x')</script>`
	view := modelDetailView{
		Model: &civitai.ModelDetail{
			ID: 7, Name: evil + "-model", Type: evil + "-type",
			Creator:       &civitai.Creator{Username: evil},
			ModelVersions: []civitai.ModelVersionSummary{{ID: 11, Name: evil + "-ver", BaseModel: evil + "-base"}},
		},
		SelectedVersionID:  11,
		VersionPublishedAt: map[int]time.Time{11: time.Now().Add(-time.Hour)},
		Version: &civitai.ModelVersionDetail{
			ID: 11, ModelID: 7, BaseModel: evil + "-base",
			TrainedWords: []string{evil + "-word"},
			Files: []civitai.ModelVersionFile{
				{ID: 1, Name: evil + ".safetensors", Type: "Model", SizeKB: 1024,
					DownloadURL: "https://civitai.com/f/1"},
			},
		},
	}
	fragments := map[string]string{
		"header":   renderString(t, modelHeaderCard(view, nil, "csrf", "https://civitai.com")),
		"tabs":     renderString(t, modelVersionTabs(view)),
		"download": renderString(t, versionDownloadCard(view, "csrf")),
	}
	for what, out := range fragments {
		if strings.Contains(out, "<script>alert(") {
			t.Errorf("%s rendered a hostile string as LIVE markup:\n%s", what, out)
		}
		if !strings.Contains(out, "&lt;script&gt;") {
			t.Errorf("%s should render the hostile string HTML-escaped:\n%s", what, out)
		}
	}
}

// TestViewOnCivitaiLinkSchemeValidated proves the outbound header link is
// scheme-validated: only http/https with a host becomes an href, and anything
// else (javascript:, data:, garbage) renders NO link at all rather than an
// injectable one.
func TestViewOnCivitaiLinkSchemeValidated(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantURL string // "" == expect no link
	}{
		{"https base", "https://civitai.com", "https://civitai.com/models/7"},
		{"https base with trailing slash", "https://civitai.com/", "https://civitai.com/models/7"},
		{"http base", "http://localhost:3000", "http://localhost:3000/models/7"},
		{"javascript scheme", "javascript:alert(1)", ""},
		{"data scheme", "data:text/html,<script>alert(1)</script>", ""},
		{"scheme-less garbage", "not a url at all", ""},
		{"empty base", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderString(t, modelHeaderCard(
				modelDetailView{Model: &civitai.ModelDetail{ID: 7, Name: "M"}},
				nil, "csrf", c.baseURL))
			if c.wantURL == "" {
				if strings.Contains(out, "View on CivitAI") {
					t.Errorf("an unsafe base URL must render no outbound link:\n%s", out)
				}
				for _, banned := range []string{`href="javascript:`, `href="data:`, `href="not a url`} {
					if strings.Contains(out, banned) {
						t.Errorf("an unsafe URL must never become an href (%q):\n%s", banned, out)
					}
				}
				return
			}
			if !strings.Contains(out, `href="`+c.wantURL+`"`) {
				t.Errorf("expected the outbound link %q:\n%s", c.wantURL, out)
			}
			if !strings.Contains(out, `rel="noopener noreferrer"`) {
				t.Errorf("the outbound link must stay hardened:\n%s", out)
			}
		})
	}
}

// --- 9. The shipped CSS the rework depends on --------------------------------

// TestModelDetailReworkCSSPresent proves the custom CSS the rework relies on
// actually ships in app.css (which survives the Tailwind purge), that every piece
// of motion has a prefers-reduced-motion escape, and that the version tab strip is
// no longer a scroll container — which is what would clip the new popover.
func TestModelDetailReworkCSSPresent(t *testing.T) {
	css := appCSS(t)

	for _, want := range []string{
		".cm-version-tab::after",       // the animated underline
		"@keyframes cm-tab-underline",  // re-runs on an htmx-swapped tab
		".cm-vdate",                    // the per-tab date icon
		".cm-vdate-ico",                //
		"@keyframes cm-vgroup-in",      // the group-switch fade
		".cm-dl-file",                  // the download rows
		".cm-meta-reveal[open]",        // the metadata disclosure
		".cm-meta-chevron",             //
		"@keyframes cm-meta-in",        //
		"prefers-reduced-motion",       //
		"--civitai-color-primary-text", // the AA foreground token, not the fill
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q", want)
		}
	}

	// The tab strip must NOT be a scroll container: `overflow-x: auto` computes
	// overflow-y to auto as well, which clips the absolutely-positioned date
	// popover. It wraps instead.
	block := cssBlock(t, css, ".cm-version-tabs {")
	if strings.Contains(block, "overflow") {
		t.Errorf(".cm-version-tabs must not establish a scroll container (it would clip the "+
			"per-tab date popover); got:\n%s", block)
	}
	if !strings.Contains(block, "flex-wrap: wrap") {
		t.Errorf(".cm-version-tabs should wrap instead of scrolling; got:\n%s", block)
	}

	// Every animated selector is answered by a reduced-motion override.
	for _, sel := range []string{".cm-version-tab", ".cm-vdate", ".cm-vgroup", ".cm-dl-file", ".cm-meta-chevron"} {
		if !reducedMotionCovers(css, sel) {
			t.Errorf("%s animates but has no prefers-reduced-motion override", sel)
		}
	}
}

// cssBlock returns the declaration block that follows the given selector text.
func cssBlock(t *testing.T, css, selector string) string {
	t.Helper()
	i := strings.Index(css, selector)
	if i < 0 {
		t.Fatalf("app.css has no %q rule", selector)
	}
	rest := css[i+len(selector):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("unterminated %q rule", selector)
	}
	return rest[:end]
}

// reducedMotionCovers reports whether the selector appears inside at least one
// @media (prefers-reduced-motion: reduce) block.
func reducedMotionCovers(css, selector string) bool {
	const marker = "@media (prefers-reduced-motion: reduce) {"
	for i := 0; ; {
		j := strings.Index(css[i:], marker)
		if j < 0 {
			return false
		}
		start := i + j + len(marker)
		// Walk to the matching close brace of the media block.
		depth, k := 1, start
		for ; k < len(css) && depth > 0; k++ {
			switch css[k] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if strings.Contains(css[start:k], selector) {
			return true
		}
		i = k
	}
}
