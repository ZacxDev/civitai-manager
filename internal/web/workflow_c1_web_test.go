package web

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// readAppCSS returns the hand-written stylesheet that ships (never a copy of its
// values), so a rule the markup depends on is asserted against the real file.
func readAppCSS(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	return string(b)
}

// detailPageNode assembles the workflow detail page exactly the way
// handleWorkflowDetail does: build the ONE Generate section, then hand it to
// workflowDetailPage. Tests render through this rather than calling
// workflowDetailPage with a nil section, so what they assert is what production
// emits — the run controls, the "Open in ComfyUI" hand-off and the helper
// disclosure all live inside the Generate section now.
func detailPageNode(wf *store.Workflow, csrf string, mr maturityRange, comfyConfigured bool,
	hv comfyHelperView, res workflowResolver) g.Node {
	gen := generateSection(wf, runSnapshot{}, csrf, true, false, mr,
		implicitPresetView(wf, nil), comfyConfigured, hv)
	return workflowDetailPage(wf, csrf, mr, gen, nil, res)
}

// uiGraphWithParams is a minimal UI-format graph carrying one of every editable
// RunInput KIND the Parameters panel can render: a prompt (text), a seed, an int, a
// float and two enums (sampler/scheduler).
const uiGraphWithParams = `{
  "nodes": [
    {"id": 1, "type": "CLIPTextEncode", "pos": [0,0], "size": [200,100],
     "widgets_values": ["a scenic mountain"]},
    {"id": 2, "type": "KSampler", "pos": [300,0], "size": [200,200],
     "widgets_values": [123456, "fixed", 20, 7.5, "euler", "normal", 1.0]}
  ],
  "links": []
}`

// --- A. Library workflow LIST item ------------------------------------------

// TestWorkflowListRunCTAIsPrimaryAndDeepLinks pins item A2 + A3: the list item's
// primary call to action is Run, it is the FILLED (primary) button variant, and it
// deep-links to the detail page's Generate section by fragment.
func TestWorkflowListRunCTAIsPrimaryAndDeepLinks(t *testing.T) {
	srv := newWorkflowServer(t)
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "runnable", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported,
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	body := workflowsTabBody(t, srv)

	if !strings.Contains(body, `href="/workflows/1#cm-generate"`) {
		t.Errorf("the Run CTA must deep-link to the Generate section fragment:\n%s", body)
	}
	// The CTA is the primary (filled) variant; "View" stays secondary (outline).
	if !strings.Contains(body, `href="/workflows/1#cm-generate" data-civitai-ui="button" data-variant="filled"`) {
		t.Errorf("the Run CTA must be the primary (filled) button:\n%s", body)
	}
	// The old anchor pointed at the run-status container, which is not a section a
	// user can act on — assert it is gone so this cannot silently regress.
	if strings.Contains(body, `href="/workflows/1#`+runStatusContainerID+`"`) {
		t.Errorf("the Run CTA must not point at the bare run-status container:\n%s", body)
	}
}

// TestGenerateSectionCarriesTheDeepLinkTarget is the other half of A3: the fragment
// the CTA names actually exists on the detail page, and the element it lands on
// carries the CSS highlight hooks (.cm-generate on the section, .cm-generate-cta on
// the primary button). The highlight itself is pure CSS `:target` — asserted in
// TestGenerateHighlightIsCSSTargetAndReducedMotionSafe.
func TestGenerateSectionCarriesTheDeepLinkTarget(t *testing.T) {
	wf := &store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), true, comfyHelperView{}, workflowResolver{}))

	if !strings.Contains(got, `id="cm-generate"`) {
		t.Errorf("the Generate section must carry the deep-link id:\n%s", got)
	}
	if !strings.Contains(got, `class="cm-generate"`) {
		t.Errorf("the Generate section must carry the .cm-generate highlight hook:\n%s", got)
	}
	// The CTA itself lives in the lazily-loaded reachability fragment.
	frag := renderString(t, runComfyStatusFragment(1, "csrf",
		comfyStatusView{configured: true, reachable: true}))
	if !strings.Contains(frag, "cm-generate-cta") {
		t.Errorf("the primary Generate button must carry the highlight hook:\n%s", frag)
	}
}

// TestGenerateHighlightIsCSSTargetAndReducedMotionSafe asserts the deep-link
// highlight is implemented in CSS (`:target`), not JS, and that it degrades to a
// static ring under prefers-reduced-motion.
func TestGenerateHighlightIsCSSTargetAndReducedMotionSafe(t *testing.T) {
	css := readAppCSS(t)
	for _, want := range []string{
		".cm-generate:target",
		".cm-generate:target .cm-generate-cta",
		"@keyframes cm-generate-pulse",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q — the deep-link highlight would do nothing", want)
		}
	}
	// The reduced-motion block must turn the animation OFF for the CTA.
	idx := strings.Index(css, "@media (prefers-reduced-motion: reduce) {\n  .cm-generate:target .cm-generate-cta {\n    animation: none;")
	if idx < 0 {
		t.Errorf("the Generate CTA pulse must be disabled under prefers-reduced-motion")
	}
}

// TestWorkflowListResourcesArePopoverChips pins A4 + C12/C13/C14 on the LIST item:
// the referenced resources render as chips inside the shared popover mechanism, a
// CivitAI-matched file links to its model+version, an unmatched one carries NO link,
// and a matched file reveals its absolute path on hover.
func TestWorkflowListResourcesArePopoverChips(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()

	// One matched-and-linked file, one file that is simply absent.
	if err := srv.store.UpsertLocalFile(store.LocalFile{
		Path: "/mnt/models/loras/present.safetensors", SizeBytes: 10,
		Status: store.LocalStatusMatched, Kind: store.LocalKindModel,
		ModelID: intp(42), VersionID: intp(99),
	}); err != nil {
		t.Fatalf("seed local file: %v", err)
	}
	if _, err := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Name: "res", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source:    store.WorkflowSourceImported,
		Resources: []string{"present.safetensors", "absent.safetensors"},
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	body := workflowsTabBody(t, srv)

	// The popover REUSES .cm-updated / .cm-updated-pop — no second implementation.
	if !strings.Contains(body, `class="cm-updated cm-res-trigger"`) {
		t.Errorf("resources must use the shared .cm-updated popover trigger:\n%s", body)
	}
	if !strings.Contains(body, `class="cm-updated-pop cm-res-pop"`) {
		t.Errorf("resources must use the shared .cm-updated-pop popover body:\n%s", body)
	}
	if !strings.Contains(body, "2 resources") {
		t.Errorf("the trigger must carry the resource count:\n%s", body)
	}
	// MATCHED → a chip that LINKS to /models/<id>?modelVersionId=<v> and reveals the
	// absolute path on hover.
	if !strings.Contains(body, `href="/models/42?modelVersionId=99"`) {
		t.Errorf("a CivitAI-matched resource must link to its model+version:\n%s", body)
	}
	// The absolute path is revealed by the chip's OWN popover, not by a title=
	// (which would paint the native tooltip over that popover). Assert the popover's
	// value cell, not a bare substring — the path also appears in the "uses" link's
	// title elsewhere on the card, so a loose Contains would pass for the wrong
	// element.
	if !strings.Contains(body,
		`<span class="cm-res-detail-value break-all">/mnt/models/loras/present.safetensors</span>`) {
		t.Errorf("a matched resource must reveal its absolute path in its detail popover:\n%s", body)
	}
	if !strings.Contains(body, `data-have="yes"`) || !strings.Contains(body, `data-have="no"`) {
		t.Errorf("chips must mark have/missing:\n%s", body)
	}
	// UNMATCHED → a <span> chip, never a link.
	if strings.Contains(body, `<a class="cm-res-chip" data-have="no"`) {
		t.Errorf("an unmatched resource must NOT be rendered as a link:\n%s", body)
	}
	// The old always-expanding <details> list is gone from the card.
	if strings.Contains(body, "have ✓") || strings.Contains(body, "missing ✗") {
		t.Errorf("the old resource <details> badges should be replaced by chips:\n%s", body)
	}
}

// resChipTag returns the OPENING TAG of the chip element itself — the one
// carrying class="cm-res-chip".
//
// 🔴 IT EXISTS BECAUSE strings.HasPrefix AND strings.Contains ARE BOTH WRONG HERE.
// The chip used to be the outermost element, so `HasPrefix(got, "<a ")` named it
// by accident; it is now wrapped in .cm-res-chip-wrap together with its popover,
// which breaks the prefix. Relaxing that to `Contains(got, "<a ")` is what the
// audit caught: any <a> ANYWHERE in the fragment satisfies it, so a chip that
// stopped being a link entirely still passed. Naming the element is stronger than
// either.
func resChipTag(t *testing.T, html string) string {
	t.Helper()
	return openTagOf(t, html, "cm-res-chip")
}

// TestWorkflowResourceChipRenderStates is the table-driven pin on the chip renderer
// itself, covering every resolution state a resource can be in.
func TestWorkflowResourceChipRenderStates(t *testing.T) {
	res := workflowResolver{
		haveFile:      func(b string) bool { return b != "gone.safetensors" && b != "comfyonly.safetensors" },
		comfyResource: func(b string) bool { return b == "comfyonly.safetensors" },
		localResource: func(b string) (resourceInfo, bool) {
			switch b {
			case "linked.safetensors":
				return resourceInfo{Path: "/lib/linked.safetensors", ModelID: 7, VersionID: 8}, true
			case "unlinked.safetensors":
				// Indexed locally but never matched to civitai (or a HuggingFace-sourced
				// file, which persists NO provenance at all).
				return resourceInfo{Path: "/lib/unlinked.safetensors"}, true
			case "ambiguous.safetensors":
				// HasLocalFileNamed says yes; LocalFileByBasename refuses to resolve it.
				return resourceInfo{}, false
			}
			return resourceInfo{}, false
		},
	}
	for _, tc := range []struct {
		name       string
		resource   string
		wantLink   bool
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name: "matched + civitai-linked", resource: "linked.safetensors", wantLink: true,
			wantSubstr: []string{`href="/models/7?modelVersionId=8"`, `data-have="yes"`,
				`<span class="cm-res-detail-value break-all">/lib/linked.safetensors</span>`, "✓"},
		},
		{
			name: "matched, no civitai linkage (incl. HuggingFace)", resource: "unlinked.safetensors",
			wantSubstr: []string{`data-have="yes"`,
				`<span class="cm-res-detail-value break-all">/lib/unlinked.safetensors</span>`},
			notSubstr: []string{"href="},
		},
		{
			name: "present but ambiguous basename", resource: "ambiguous.safetensors",
			wantSubstr: []string{`data-have="yes"`, "present in your library"},
			notSubstr:  []string{"href="},
		},
		{
			// The THIRD state: not in the local library, but ComfyUI can resolve it.
			name: "not in the library but ComfyUI has it", resource: "comfyonly.safetensors",
			wantSubstr: []string{`data-have="comfy"`, "in ComfyUI, not in your library", "◎"},
			notSubstr:  []string{"href=", `data-have="yes"`, `data-have="no"`, "✓", "✗"},
		},
		{
			name: "not in the library", resource: "gone.safetensors",
			wantSubstr: []string{`data-have="no"`, "not in your library", "✗"},
			// "◎" and data-have="comfy" must be absent: a resolver that says ComfyUI
			// does NOT have the file must produce the plain missing chip.
			notSubstr: []string{"href=", "◎", `data-have="comfy"`},
		},
		{
			name: "subdirectory-qualified reference shows its basename", resource: "bbox/linked.safetensors",
			wantLink:   true,
			wantSubstr: []string{`href="/models/7?modelVersionId=8"`, ">linked.safetensors<"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderString(t, workflowResourceChip(tc.resource, res))
			// Assert the CHIP ELEMENT's own tag, never a substring of the fragment.
			tag := resChipTag(t, got)
			if tc.wantLink && !strings.HasPrefix(tag, "<a ") {
				t.Errorf("expected the chip itself to be a link, its opening tag is:\n%s\nin:\n%s", tag, got)
			}
			if !tc.wantLink && !strings.HasPrefix(tag, "<span ") {
				t.Errorf("expected the chip itself to be a plain span, its opening tag is:\n%s\nin:\n%s", tag, got)
			}
			// One hover unit, one hover affordance: the chip owns a popover, so it
			// must not also carry title=.
			if strings.Contains(tag, "title=") {
				t.Errorf("the chip owns a .cm-res-chip-pop popover, so title= would double-hover:\n%s", tag)
			}
			for _, w := range tc.wantSubstr {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, n := range tc.notSubstr {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q in:\n%s", n, got)
				}
			}
		})
	}
}

// TestWorkflowResourceChipEscapesHostileNames proves an untrusted graph cannot
// inject markup through a resource filename or through the on-disk path it resolves
// to (both reach attributes AND text).
func TestWorkflowResourceChipEscapesHostileNames(t *testing.T) {
	res := workflowResolver{
		haveFile: func(string) bool { return true },
		localResource: func(string) (resourceInfo, bool) {
			return resourceInfo{Path: `/lib/"><script>alert(2)</script>`, ModelID: 1, VersionID: 2}, true
		},
	}
	got := renderString(t, workflowResourceChip(`<img src=x onerror=alert(1)>.safetensors`, res))
	// No tag can be opened, and no attribute value can be broken out of: gomponents
	// escapes < > and " everywhere the untrusted string lands (text AND attributes).
	for _, bad := range []string{"<script", "<img", `="/lib/">`} {
		if strings.Contains(got, bad) {
			t.Errorf("hostile resource name/path leaked unescaped (%q):\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;img") {
		t.Errorf("expected the escaped filename:\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected the escaped on-disk path:\n%s", got)
	}
}

// TestWorkflowListViewPostReplacesTheRawModelLink pins A5: the raw /models/<id>
// text link in the card body is replaced by an explicit "View post" button, and the
// resolved model NAME still renders (inline when cached, lazy otherwise).
func TestWorkflowListViewPostReplacesTheRawModelLink(t *testing.T) {
	srv := newWorkflowServer(t)
	ctx := context.Background()
	raw := []byte(`{"id":42,"name":"Cool Model","modelVersions":[{"id":99,"name":"v2"}]}`)
	if err := srv.store.PutModelCache(42, "Cool Model", raw); err != nil {
		t.Fatalf("cache model: %v", err)
	}
	if _, err := srv.store.InsertWorkflow(ctx, &store.Workflow{
		Name: "p", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported, ModelID: intp(42), VersionID: intp(99),
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	body := workflowsTabBody(t, srv)

	if !strings.Contains(body, "View post") {
		t.Errorf("the card must offer a View post button:\n%s", body)
	}
	if !strings.Contains(body, `href="/models/42" data-civitai-ui="button"`) {
		t.Errorf("View post must be a button-styled anchor to the model page:\n%s", body)
	}
	// The old bare text link is gone; the name is now plain text beside it.
	if strings.Contains(body, `href="/models/42" class="text-sm text-indigo-400`) {
		t.Errorf("the raw /models/<id> text link should be gone:\n%s", body)
	}
	if !strings.Contains(body, "Cool Model") {
		t.Errorf("the resolved model name must still render:\n%s", body)
	}
}

// TestWorkflowDetailViewPostReplacesTheRawModelLink is B7 — the same treatment on
// the detail page, alongside the (validated) external civitai.com link.
func TestWorkflowDetailViewPostReplacesTheRawModelLink(t *testing.T) {
	wf := &store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceImported, ModelID: intp(42), VersionID: intp(99)}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false, comfyHelperView{}, workflowResolver{}))

	if !strings.Contains(got, `href="/models/42" data-civitai-ui="button"`) {
		t.Errorf("the detail page must offer the same View post button:\n%s", got)
	}
	if !strings.Contains(got, `href="https://civitai.com/models/42?modelVersionId=99"`) {
		t.Errorf("the external CivitAI link must survive:\n%s", got)
	}
}

// --- B. Workflow DETAILS page ------------------------------------------------

// TestWorkflowDetailHidesShowcaseCopyAndRawJSON pins B6 + D17.
func TestWorkflowDetailHidesShowcaseCopyAndRawJSON(t *testing.T) {
	raw := `{"id":42,"name":"m","modelVersions":[{"id":1,"images":[{"url":"https://img/x.jpg","nsfwLevel":1}]}]}`
	wf := &store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatUI,
		Graph:  `{"nodes":[{"id":1,"type":"KSampler","pos":[0,0],"size":[100,50]}],"links":[]}`,
		Source: store.WorkflowSourceImported, ModelID: intp(42)}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false,
		comfyHelperView{}, showcaseResolver(raw, fullMaturityRange())))

	// The carousel is still there — only its caption is gone.
	if !strings.Contains(got, "cm-showcase-lg") {
		t.Errorf("the showcase carousel must still render:\n%s", got)
	}
	if strings.Contains(got, "Showcase images") {
		t.Errorf("the 'Showcase images' copy must be hidden on a workflow page:\n%s", got)
	}
	// The raw-JSON dump is gone entirely.
	if strings.Contains(got, "View raw JSON") {
		t.Errorf("the raw JSON disclosure must be gone:\n%s", got)
	}
	if strings.Contains(got, "<pre") {
		t.Errorf("no <pre> graph dump should remain on the detail page:\n%s", got)
	}
}

// TestWorkflowDetailFoldsNonKeyDetails pins B8: the raw ids/flags and the attach
// form both sit behind a collapsed <details>, and neither is visible up front.
func TestWorkflowDetailFoldsNonKeyDetails(t *testing.T) {
	wf := &store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceScanned, SourcePath: "/disk/wf.json",
		ModelID: intp(42), VersionID: intp(99)}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false, comfyHelperView{}, workflowResolver{}))

	for _, want := range []string{
		`class="cm-meta-reveal mt-3"`,
		"Workflow metadata",
		"Attach to a civitai version",
		// The on-disk path moved INTO the disclosure but is still shown (escaped).
		"/disk/wf.json",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q on the reworked detail page:\n%s", want, got)
		}
	}
	// A collapsed <details> must not be `open`.
	if strings.Contains(got, `class="cm-meta-reveal mt-3" open`) {
		t.Errorf("the metadata disclosure must be collapsed by default:\n%s", got)
	}
	// The attach endpoint + its CSRF token are unchanged.
	if !strings.Contains(got, `action="/workflows/1/attach"`) ||
		!strings.Contains(got, `name="csrf_token"`) {
		t.Errorf("the attach form must keep its endpoint and CSRF token:\n%s", got)
	}
}

// TestWorkflowDetailEscapesHostileNameAndPath proves the workflow name and the
// on-disk source path (both arbitrary, both reaching text nodes) are escaped.
func TestWorkflowDetailEscapesHostileNameAndPath(t *testing.T) {
	wf := &store.Workflow{
		ID: 1, Name: `<script>alert('n')</script>`, Format: store.WorkflowFormatAPI, Graph: "{}",
		Source: store.WorkflowSourceScanned, SourcePath: `/disk/<img src=x onerror=alert(1)>.json`,
	}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false, comfyHelperView{}, workflowResolver{}))
	for _, bad := range []string{"<script>alert(", "<img src=x"} {
		if strings.Contains(got, bad) {
			t.Errorf("hostile name/path leaked unescaped (%q):\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;alert(") || !strings.Contains(got, "&lt;img src=x") {
		t.Errorf("expected the escaped name and path:\n%s", got)
	}
}

// --- B9/B10. The combined Generate section ----------------------------------

// TestGenerateSectionCombinesTheThreeRunSurfaces pins B9: one section holds the
// local run, the editor hand-off and the cloud run — and the three separate cards
// that used to compete for the same job are gone.
func TestGenerateSectionCombinesTheThreeRunSurfaces(t *testing.T) {
	wf := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), true, comfyHelperView{}, workflowResolver{}))

	if strings.Count(got, `>Generate<`) < 1 {
		t.Errorf("the section must be titled Generate:\n%s", got)
	}
	// All three surfaces are inside it.
	for _, want := range []string{
		`id="run-comfy-status"`,                 // local run controls
		`action="/workflows/3/open-in-comfyui"`, // editor hand-off
		`id="cloud-panel"`,                      // cloud run
		// The cloud surface is now NAMED by the destination tab rather than by a
		// heading of its own — the heading was the visual grammar of "a different
		// section below", which is the reading runDestination exists to remove.
		`for="cm-dest-cloud"`,
		"CivitAI Cloud",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the Generate section is missing %q:\n%s", want, got)
		}
	}
	// The superseded standalone headings are gone.
	for _, gone := range []string{`>Open in ComfyUI</h2>`, `>Run</h2>`} {
		if strings.Contains(got, gone) {
			t.Errorf("a superseded standalone card heading is back (%q):\n%s", gone, got)
		}
	}
	// Document order inside the ONE section. The cloud panel now sits in the
	// destination control ABOVE #run-status rather than below it, because #run-status
	// was deliberately hoisted OUT of both destination panels so a local run in
	// flight stays visible on either tab. What is pinned is unchanged in substance:
	// the cloud run is never a card of its own, and it lives inside #cm-generate.
	genAt := strings.Index(got, `id="cm-generate"`)
	destAt := strings.Index(got, `class="cm-dest"`)
	cloudAt := strings.Index(got, `id="cloud-panel"`)
	statusAt := strings.Index(got, `id="`+runStatusContainerID+`"`)
	if genAt < 0 || destAt < genAt || cloudAt < destAt || statusAt < cloudAt {
		t.Errorf("expected #cm-generate < .cm-dest < #cloud-panel < #run-status, got %d/%d/%d/%d",
			genAt, destAt, cloudAt, statusAt)
	}
}

// TestOpenInComfyStaysARealPostForm is the 🔴 regression guard. The control MUST
// stay a real <form method="post" target="_blank"> — the browser has to open the
// tab synchronously from the click so the handler can 303-redirect it into
// <comfy_url>/?cm_open=<path>. An htmx POST can only answer with markup, which is
// exactly how this once shipped as "we saved it, now click this OTHER link".
func TestOpenInComfyStaysARealPostForm(t *testing.T) {
	wf := &store.Workflow{ID: 3, Name: "ui", Format: store.WorkflowFormatUI, Graph: "{}",
		Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(wf, "csrf-tok", fullMaturityRange(), true, comfyHelperView{}, workflowResolver{}))

	if !strings.Contains(got, `<form method="post" action="/workflows/3/open-in-comfyui" target="_blank"`) {
		t.Errorf("Open in ComfyUI must be a real POST form opening a new tab:\n%s", got)
	}
	if !strings.Contains(got, `<input type="hidden" name="csrf_token" value="csrf-tok">`) {
		t.Errorf("the open form must carry the CSRF token as a field:\n%s", got)
	}
	// NOT an htmx button, in any spelling.
	for _, bad := range []string{
		`hx-post="/workflows/3/open-in-comfyui"`,
		`hx-get="/workflows/3/open-in-comfyui"`,
	} {
		if strings.Contains(got, bad) {
			t.Errorf("Open in ComfyUI must NOT be an htmx request (%q):\n%s", bad, got)
		}
	}
	// It is the SECONDARY action: outline, next to the filled primary.
	if !strings.Contains(got, `data-variant="outline"`) {
		t.Errorf("Open in ComfyUI must render as the secondary (outline) action:\n%s", got)
	}
}

// TestGenerateSectionDegradesWhenComfyIsUnreachable covers the reachable /
// unreachable / unconfigured renderings of the primary CTA + the icon indicator
// (items B9 and B10).
func TestGenerateSectionDegradesWhenComfyIsUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		view      comfyStatusView
		want      []string
		notWanted []string
	}{
		{
			// canQueue false — an API-format workflow, which the batch endpoint
			// refuses — so the ONE primary control posts to the params endpoint.
			name: "reachable",
			view: comfyStatusView{configured: true, reachable: true, version: "0.27.1"},
			want: []string{
				`data-state="ok"`, "ComfyUI reachable", "0.27.1",
				`hx-post="/workflows/5/run-with-params"`, ">Generate<", "cm-generate-cta",
				// 🔴 The include is the whole point of the consolidation: this button
				// used to carry ONLY #run-modes and silently drop every edit in the
				// Parameters panel it sits under.
				`hx-include="` + runZoneInclude + `"`,
			},
			// `type="button" disabled` is the discriminator — hx-disabled-elt (which
			// disables the button only for the duration of its own request) is expected
			// here and merely contains the substring "disabled".
			notWanted: []string{`type="button" disabled`, "Recheck"},
		},
		{
			name: "unreachable",
			view: comfyStatusView{configured: true, comfyURL: "http://127.0.0.1:8188"},
			want: []string{
				`data-state="down"`, "No ComfyUI reachable at http://127.0.0.1:8188",
				`type="button" disabled`, `title="ComfyUI is not reachable`,
				// The recheck is an ICON button but keeps its words for AT/hover.
				`aria-label="Recheck the ComfyUI connection"`, "↻",
				`hx-get="/workflows/5/run/comfy-status"`,
			},
			notWanted: []string{`hx-post="/workflows/5/run"`},
		},
		{
			name:      "unconfigured",
			view:      comfyStatusView{},
			want:      []string{`data-state="off"`, "not configured"},
			notWanted: []string{`hx-post="/workflows/5/run"`, "Recheck"},
		},
		{
			// canQueue true — a UI-format workflow, so the SAME button carries the
			// count segment's value to the batch endpoint. One button, both jobs.
			name: "reachable and queueable",
			view: comfyStatusView{configured: true, reachable: true, canQueue: true},
			want: []string{
				`hx-post="/workflows/5/run/queue"`,
				`hx-include="` + runZoneInclude + `"`,
			},
			notWanted: []string{`hx-post="/workflows/5/run-with-params"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderString(t, runComfyStatusFragment(5, "tok", tc.view))
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q:\n%s", w, got)
				}
			}
			for _, n := range tc.notWanted {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q:\n%s", n, got)
				}
			}
			// The status indicator always reuses the ONE popover mechanism.
			if !strings.Contains(got, `class="cm-updated"`) ||
				!strings.Contains(got, `class="cm-updated-pop"`) {
				t.Errorf("the reachability indicator must reuse the shared popover:\n%s", got)
			}
			// Never colour-only: the state is in the accessible name too.
			if !strings.Contains(got, "aria-label=") {
				t.Errorf("the reachability icon must carry an aria-label:\n%s", got)
			}
		})
	}
}

// TestGenerateSectionGatedOffLoopback proves the off-loopback gate still short
// -circuits the whole section (no run controls, no cloud block, no editor hand-off).
func TestGenerateSectionGatedOffLoopback(t *testing.T) {
	wf := &store.Workflow{ID: 3, Format: store.WorkflowFormatUI, Graph: "{}"}
	got := renderString(t, generateSection(wf, runSnapshot{}, "csrf", false, false, fullMaturityRange(),
		implicitPresetView(wf, nil), true, comfyHelperView{}))
	if !strings.Contains(got, "non-loopback") {
		t.Errorf("the gated section must explain itself:\n%s", got)
	}
	for _, gone := range []string{"run-comfy-status", "cloud-panel", "open-in-comfyui"} {
		if strings.Contains(got, gone) {
			t.Errorf("the gated section must not render %q:\n%s", gone, got)
		}
	}
}

// TestGenerateSectionKeepsRunParamsAndRunStatusSiblings is the streaming-invariant
// guard. #run-params and #run-status must stay SIBLINGS: the 1 s run poller swaps
// #run-status's innerHTML, so nesting the preset tabs inside it would let a poll
// clobber a half-typed prompt.
func TestGenerateSectionKeepsRunParamsAndRunStatusSiblings(t *testing.T) {
	wf := &store.Workflow{ID: 3, Format: store.WorkflowFormatUI, Graph: uiGraphWithParams}
	got := renderString(t, generateSection(wf, runSnapshot{}, "csrf", true, false, fullMaturityRange(),
		implicitPresetView(wf, nil), true, comfyHelperView{}))

	pi := strings.Index(got, `id="`+runParamsContainerID+`"`)
	si := strings.Index(got, `id="`+runStatusContainerID+`"`)
	if pi < 0 || si < 0 {
		t.Fatalf("both stable containers must be present:\n%s", got)
	}
	if pi > si {
		t.Errorf("#run-params must still precede #run-status")
	}
	// Sibling, not nested: the params container must be CLOSED before the status
	// container opens. The params div's subtree ends where the status div begins, so
	// the status id must not appear inside the params container's own markup.
	if strings.Contains(got[pi:si], `id="`+runStatusContainerID+`"`) {
		t.Errorf("#run-status must not be nested inside #run-params")
	}
}

// --- B11. Parameter form sizing ---------------------------------------------

// TestRunParamFieldsAreSizedByKind pins item 11: each control's wrapper carries the
// sizing class for its comfy.RunInput.Kind, so a seed, a step count and a prompt
// are laid out at three different sizes. The signal is Kind — never the label —
// which is why every case below uses the SAME label.
//
// It also pins the CONTROL each kind renders, so a sizing class can never end up
// on a control it does not fit (a max-width meant for a number field applied to a
// textarea, say).
func TestRunParamFieldsAreSizedByKind(t *testing.T) {
	css := readAppCSS(t)
	for _, tc := range []struct {
		kind        comfy.RunInputKind
		wantClass   string
		wantControl string
	}{
		{comfy.RunInputText, "cm-param cm-param-text", "<textarea"},
		{comfy.RunInputSelect, "cm-param cm-param-select", `type="text"`}, // no choices → text fallback
		{comfy.RunInputSeed, "cm-param cm-param-seed", `type="number"`},
		{comfy.RunInputInt, "cm-param cm-param-int", `type="number"`},
		{comfy.RunInputFloat, "cm-param cm-param-float", `type="number"`},
		{comfy.RunInputKind("something-new"), "cm-param cm-param-other", `type="text"`},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			ri := comfy.RunInput{
				NodeID: "1", Kind: tc.kind, Label: "same label everywhere", Current: "v",
			}
			field := renderString(t, runParamFieldValue(0, ri, ri.Current))
			if !strings.Contains(field, `class="`+tc.wantClass+`"`) {
				t.Errorf("field for kind %q is missing its sizing class %q:\n%s",
					tc.kind, tc.wantClass, field)
			}
			if !strings.Contains(field, tc.wantControl) {
				t.Errorf("field for kind %q should render %s:\n%s", tc.kind, tc.wantControl, field)
			}
			// The class must actually paint something — .cm-param-* is hand-written CSS
			// in app.css, so a typo here would be silently invisible.
			for _, tok := range strings.Fields(tc.wantClass) {
				if !cssHasClass(css, tok) {
					t.Errorf("class %q has no rule in app.css", tok)
				}
			}
		})
	}
}

// TestRunParamsPanelIsAGridAndSubmitsTheSameFields is the ⚠ guard on item 11: the
// layout changed, what is SUBMITTED did not. The parallel wp_node/wp_widget/wp_value
// arrays are paired BY DOM POSITION, so this asserts the grid wrapper exists AND
// that the triples still appear in the same order, one per field.
func TestRunParamsPanelIsAGridAndSubmitsTheSameFields(t *testing.T) {
	wf := &store.Workflow{ID: 5, Format: store.WorkflowFormatUI, Graph: uiGraphWithParams}
	got := renderString(t, runParametersPanel(wf, "tok"))

	if !strings.Contains(got, `class="cm-param-grid"`) {
		t.Fatalf("the parameters panel must lay its fields out in the grid:\n%s", got)
	}
	// Every field still emits exactly one wp_node/wp_widget/wp_value triple, and the
	// three arrays stay index-aligned in DOM order.
	nodes := strings.Count(got, `name="wp_node"`)
	widgets := strings.Count(got, `name="wp_widget"`)
	values := strings.Count(got, `name="wp_value"`)
	if nodes == 0 || nodes != widgets || nodes != values {
		t.Errorf("wp_node/wp_widget/wp_value must stay 1:1:1 (got %d/%d/%d):\n%s",
			nodes, widgets, values, got)
	}
	// The detected kinds actually reached the DOM (prompt + seed + numerics + enums).
	for _, want := range []string{"cm-param-text", "cm-param-seed", "cm-param-int",
		"cm-param-float", "cm-param-select"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected a %s field in the panel:\n%s", want, got)
		}
	}
}

// TestRunParamOverrideParsingIsUnchangedByTheLayout is the behavioural half of the
// same guard: the grid is presentation only, so the override map parsed back out of
// a submitted form is exactly what it was before.
func TestRunParamOverrideParsingIsUnchangedByTheLayout(t *testing.T) {
	wf := &store.Workflow{ID: 5, Format: store.WorkflowFormatUI, Graph: uiGraphWithParams}
	inputs := comfy.DetectRunInputs([]byte(uiGraphWithParams), nil)
	if len(inputs) == 0 {
		t.Fatal("the fixture graph must expose editable inputs")
	}
	form := map[string][]string{}
	for _, ri := range inputs {
		form["wp_node"] = append(form["wp_node"], ri.NodeID)
		form["wp_widget"] = append(form["wp_widget"], strconv.Itoa(ri.WidgetIndex))
		form["wp_value"] = append(form["wp_value"], "edited")
	}
	out := parseWidgetOverridesForModes(form, wf, nil)
	if len(out) != len(inputs) {
		t.Errorf("parsed %d overrides for %d detected inputs — the allow-list changed",
			len(out), len(inputs))
	}
	for _, ri := range inputs {
		if out[comfy.UIWidgetKey{NodeID: ri.NodeID, Widget: ri.WidgetIndex}] != "edited" {
			t.Errorf("override for %s/%d did not round-trip", ri.NodeID, ri.WidgetIndex)
		}
	}
}

// --- D. Graph ----------------------------------------------------------------

// TestGraphPreviewIsPannableAndKeyboardReachable pins D16: the scroll container
// carries the drag hook and stays operable from the keyboard, and the vendored
// script is inline, guarded and fail-safe.
func TestGraphPreviewIsPannableAndKeyboardReachable(t *testing.T) {
	graph := `{"nodes":[{"id":1,"type":"KSampler","pos":[0,0],"size":[100,50]}],"links":[]}`
	wf := &store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatUI, Graph: graph,
		Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false, comfyHelperView{}, workflowResolver{}))

	if !strings.Contains(got, "cm-graph cm-graph-pan") {
		t.Errorf("the graph container must carry the pan hook:\n%s", got)
	}
	// Keyboard: focusable + labelled, so arrow keys scroll it with no JS at all.
	if !strings.Contains(got, `tabindex="0"`) || !strings.Contains(got, `role="region"`) {
		t.Errorf("the graph container must be keyboard-reachable and labelled:\n%s", got)
	}
	// The script is vendored INLINE, and the page pulls nothing off a CDN
	// (offline/no-CDN invariant — the only src= is the embedded /assets/ htmx).
	if !strings.Contains(got, "<script>\n(function(){") {
		t.Errorf("the pan script must be inline, not an external asset:\n%s", got)
	}
	for _, cdn := range []string{`<script src="http`, `<script src="//`} {
		if strings.Contains(got, cdn) {
			t.Errorf("no script may be loaded from outside the binary (%q):\n%s", cdn, got)
		}
	}
	for _, want := range []string{
		"__cmGraphPanBound",   // bound exactly once, survives htmx swaps
		"window.PointerEvent", // feature-detected
		"Element.prototype.closest",
		"catch", // fails silently
		"cm-graph-panning",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the pan script is missing its %q guard:\n%s", want, got)
		}
	}
}

// TestGraphPanScriptIsAbsentWithoutAnSVGPreview is a scope check: an API-format
// graph renders the structured listing, which is not a pannable canvas.
func TestGraphPanScriptIsAbsentWithoutAnSVGPreview(t *testing.T) {
	wf := &store.Workflow{ID: 1, Name: "w", Format: store.WorkflowFormatAPI,
		Graph: `{"3":{"class_type":"KSampler","inputs":{}}}`, Source: store.WorkflowSourceImported}
	got := renderString(t, detailPageNode(wf, "csrf", fullMaturityRange(), false, comfyHelperView{}, workflowResolver{}))
	if strings.Contains(got, "cm-graph-pan") {
		t.Errorf("a structured (non-SVG) graph view must not claim to be pannable:\n%s", got)
	}
}
