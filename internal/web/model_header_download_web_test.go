package web

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
)

// Coverage for the download-in-header rework (see headerDownloadControl in
// model_pages.go): the two download SHAPES, the honest zero-file degradation,
// CSRF on both shapes, the relocated version-metadata disclosure, the retirement
// of the standalone card, and the section gutter.

// headerDLView builds a model view whose selected version carries the given files.
func headerDLView(files ...civitai.ModelVersionFile) modelDetailView {
	return modelDetailView{
		Model: &civitai.ModelDetail{ID: 7, Name: "M"},
		Version: &civitai.ModelVersionDetail{
			ID: 11, ModelID: 7, BaseModel: "SDXL",
			TrainedWords: []string{"mytoken"},
			Files:        files,
		},
		SelectedVersionID: 11,
		PublishedAt:       "2026-01-15T20:50:47.173Z",
	}
}

// renderMaybeNil is renderString for a node that is ALLOWED to be nil. renderString
// calls Render unconditionally and panics on a nil interface, which would turn
// "renders nothing" — the exact contract under test below — into a crash rather
// than an assertion. gomponents itself skips nil children, so nil is a legitimate
// node here and "" is its correct rendering.
func renderMaybeNil(t *testing.T, n g.Node) string {
	t.Helper()
	if n == nil {
		return ""
	}
	return renderString(t, n)
}

func dlFile(id int, name, typ string, kb float64) civitai.ModelVersionFile {
	return civitai.ModelVersionFile{
		ID: id, Name: name, Type: typ, SizeKB: kb,
		DownloadURL: fmt.Sprintf("https://civitai.com/f/%d", id),
	}
}

// TestHeaderDownloadSingleFileIsOneClick pins the ONE-file shape: a direct
// Download button and NO menu markup at all.
//
// The "no menu markup" half is the load-bearing one — a menu that merely LOOKS
// collapsed still costs the common case a click, and the whole point of the two
// shapes is that the single-file case never pays for the many-file case.
//
// MUTATION-VERIFIED: dropping the `len(ver.Files) == 1` early return in
// headerDownloadControl (so one file falls through to the menu) fails this with
// `a single file must NOT render menu markup, found "cm-dl-menu"`.
func TestHeaderDownloadSingleFileIsOneClick(t *testing.T) {
	out := renderString(t, headerDownloadControl(
		headerDLView(dlFile(1, "only.safetensors", "Model", 2048)), "csrf-token"))

	// A real button that POSTs the enqueue, targeting its own stable id.
	for _, want := range []string{
		`hx-post="/models/7/download"`,
		`fileId&#34;:&#34;1`,
		`versionId&#34;:&#34;11`,
		`id="` + downloadFileID(7, 11, 1) + `"`,
		">Download</button>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the single-file shape must be a direct Download button, missing %q:\n%s", want, out)
		}
	}
	// NO menu, in any of its parts.
	for _, banned := range []string{"cm-dl-menu", "<details", "<summary", "cm-dl-file", "<ul"} {
		if strings.Contains(out, banned) {
			t.Errorf("a single file must NOT render menu markup, found %q:\n%s", banned, out)
		}
	}
	// Exactly ONE control, so there is nothing to choose between.
	if n := strings.Count(out, "hx-post="); n != 1 {
		t.Errorf("the single-file shape must emit exactly one action, got %d:\n%s", n, out)
	}
}

// TestHeaderDownloadMultiFileIsOneTriggerPlusPopover pins the >1-file shape: ONE
// trigger, and a popover listing EVERY file with its size and type. The trigger
// count is what bounds the header's width — a version shipping a dozen files must
// still add exactly one control to the action group.
func TestHeaderDownloadMultiFileIsOneTriggerPlusPopover(t *testing.T) {
	files := []civitai.ModelVersionFile{
		dlFile(1, "primary.safetensors", "Model", 2048),
		dlFile(2, "extra.pt", "VAE", 512),
		dlFile(3, "config.yaml", "Config", 2),
	}
	out := renderString(t, headerDownloadControl(headerDLView(files...), "csrf-token"))

	// ONE trigger.
	if n := strings.Count(out, "cm-dl-menu-sum"); n != 1 {
		t.Errorf("expected exactly one download trigger, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, "cm-dl-menu-pop"); n != 1 {
		t.Errorf("expected exactly one popover panel, got %d:\n%s", n, out)
	}
	// Native disclosure → keyboard-operable with no JS.
	if !strings.Contains(out, "<details") || !strings.Contains(out, "<summary") {
		t.Errorf("the trigger must be a native <details>/<summary>:\n%s", out)
	}
	// The count is in the VISIBLE label, so the affordance says how much it hides.
	if !strings.Contains(out, "Download (3 files)") {
		t.Errorf("the trigger should name the file count:\n%s", out)
	}
	// EVERY file, with its size AND type, and its own action.
	for _, f := range files {
		for _, want := range []string{
			">" + f.Name + "<",                           // the name
			">" + f.Type + "<",                           // the type badge
			">" + humanBytes(int64(f.SizeKB*1024)) + "<", // the size
			`id="` + downloadFileID(7, 11, f.ID) + `"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("the popover must list file %d with size+type, missing %q:\n%s", f.ID, want, out)
			}
		}
	}
	if n := strings.Count(out, "cm-dl-file"); n != len(files) {
		t.Errorf("expected one row per file (%d), got %d:\n%s", len(files), n, out)
	}
	// Exactly one visually-primary per-file action (the version's first file); the
	// trigger itself is the other filled control.
	if n := strings.Count(out, `data-variant="filled"`); n != 2 {
		t.Errorf("expected the trigger + exactly one filled per-file button, got %d:\n%s", n, out)
	}
}

// TestHeaderDownloadZeroFilesRendersNothing pins the honest degradation: a version
// with no files renders NO control — not an empty menu, and not a button that
// could only ever fail.
//
// The DownloadURL sub-case is the subtle one. POST /models/{id}/download requires
// a fileId > 0 (handleModelDownload answers "Invalid request" below that), so a
// version-level download URL with no file rows is not actionable through this
// endpoint at all; offering a control for it would be a lie.
//
// MUTATION-VERIFIED: relaxing the guard to `ver == nil || v.Model == nil` (so a
// zero-file version falls through) fails this with
// `a version with no files must render NO control, got: <details class="cm-dl-menu">…`.
func TestHeaderDownloadZeroFilesRendersNothing(t *testing.T) {
	cases := map[string]modelDetailView{
		"no files at all": headerDLView(),
		"no selected version": {
			Model: &civitai.ModelDetail{ID: 7, Name: "M"},
		},
	}
	// A version carrying a version-level DownloadURL but NO file list.
	urlOnly := headerDLView()
	urlOnly.Version.DownloadURL = "https://civitai.com/api/download/models/11"
	cases["version DownloadURL but no file list"] = urlOnly

	for name, view := range cases {
		t.Run(name, func(t *testing.T) {
			out := renderMaybeNil(t, headerDownloadControl(view, "csrf-token"))
			if out != "" {
				t.Errorf("a version with no files must render NO control, got: %s", out)
			}
		})
	}
}

// TestHeaderDownloadCarriesCSRFInBothShapes pins the CSRF token on the enqueue
// POST in BOTH shapes. /models/{id}/download is state-changing, and the token
// rides in the button's hx-vals JSON (HTML-escaped in the attribute), not in a
// hidden input — so a shape that dropped it would 403 at click time with nothing
// in the markup to show for it.
func TestHeaderDownloadCarriesCSRFInBothShapes(t *testing.T) {
	shapes := map[string]modelDetailView{
		"single file": headerDLView(dlFile(1, "only.safetensors", "Model", 2048)),
		"multi file": headerDLView(
			dlFile(1, "primary.safetensors", "Model", 2048),
			dlFile(2, "extra.pt", "VAE", 512),
		),
	}
	for name, view := range shapes {
		t.Run(name, func(t *testing.T) {
			out := renderString(t, headerDownloadControl(view, "tok-1234"))
			posts := strings.Count(out, `hx-post="/models/7/download"`)
			if posts == 0 {
				t.Fatalf("expected at least one download POST:\n%s", out)
			}
			if n := strings.Count(out, "csrf_token&#34;:&#34;tok-1234"); n != posts {
				t.Errorf("every download POST must carry the CSRF token: %d POSTs, %d tokens:\n%s",
					posts, n, out)
			}
		})
	}
}

// TestHeaderDownloadNoURLDegradesToANote pins the per-file degradation: a file we
// cannot resolve a download URL for renders the disabled note, never a button that
// would fail on click.
func TestHeaderDownloadNoURLDegradesToANote(t *testing.T) {
	out := renderString(t, headerDownloadControl(headerDLView(
		civitai.ModelVersionFile{ID: 1, Name: "orphan.safetensors", Type: "Model", SizeKB: 10},
	), "csrf"))
	if !strings.Contains(out, "no URL") {
		t.Errorf("a file with no resolvable download URL should say so:\n%s", out)
	}
	if strings.Contains(out, "hx-post=") {
		t.Errorf("a file with no download URL must not render an enqueue action:\n%s", out)
	}
}

// TestVersionMetadataIsReachableAndKeyboardOperableInTheHeader pins the relocated
// disclosure: its content still renders (at its NEW home, the header card), and it
// is operable from the keyboard alone.
//
// "Keyboard-operable" is pinned STRUCTURALLY rather than by simulating a keypress:
// a native <summary> is focusable and toggles on Enter/Space by the HTML spec, so
// the guarantee is that it stays a real <details>/<summary> — not a div with an
// onclick, and not one taken out of the tab order with tabindex="-1". The
// trigger-word chips inside it are real <button>s for the same reason.
func TestVersionMetadataIsReachableAndKeyboardOperableInTheHeader(t *testing.T) {
	out := renderString(t, modelHeaderCard(
		headerDLView(dlFile(1, "only.safetensors", "Model", 2048)),
		nil, "csrf", "https://civitai.com"))

	// Content: every fact the disclosure used to carry on the download card.
	for _, want := range []string{
		"Version metadata",
		">Base model<", ">SDXL<",
		">Published<", ">2026-01-15<",
		">Trigger words<", ">mytoken<",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the relocated metadata disclosure is missing %q:\n%s", want, out)
		}
	}

	// Structure: a native disclosure, in the tab order, with no JS toggle.
	at := strings.Index(out, `class="cm-meta-reveal`)
	if at < 0 {
		t.Fatalf("the metadata disclosure must render in the header card:\n%s", out)
	}
	openTag := out[strings.LastIndex(out[:at], "<details"):]
	end := strings.Index(openTag, "</details>")
	if end < 0 {
		t.Fatalf("unterminated <details>:\n%s", out)
	}
	block := openTag[:end]
	if !strings.Contains(block, "<summary") {
		t.Errorf("the disclosure must use a native <summary> (keyboard-operable by spec):\n%s", block)
	}
	if strings.Contains(block, `tabindex="-1"`) {
		t.Errorf("the disclosure must not be removed from the tab order:\n%s", block)
	}
	if strings.Contains(block[:strings.Index(block, ">")], "onclick") {
		t.Errorf("the disclosure must not depend on a JS click handler:\n%s", block)
	}
	// The trigger-word chips are real focusable buttons, not click-only spans.
	if !strings.Contains(block, `<button type="button" data-copy="mytoken"`) {
		t.Errorf("trigger words must stay real <button>s so they are focusable:\n%s", block)
	}
}

// TestDownloadCardIsGoneFromThePageBody pins the retirement itself, end to end
// through the real handler: the standalone card's heading must not appear anywhere
// on the rendered page, while the download action itself is still there (in the
// header). Asserting only the absence would pass on a page that lost the download
// entirely.
func TestDownloadCardIsGoneFromThePageBody(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	for _, gone := range []string{
		">Download</h2>",                    // the card's heading
		"Files &amp; metadata",              // the heading it had before that
		"Select a version to see its files", // the card's no-version shell
		"No files.",                         // the card's no-files shell
	} {
		if strings.Contains(body, gone) {
			t.Errorf("the retired download card still renders %q", gone)
		}
	}
	if !strings.Contains(body, `hx-post="/models/7/download"`) {
		t.Errorf("the download action must survive the card's retirement")
	}
	if !strings.Contains(body, "cm-dl-menu") {
		t.Errorf("the download action should render as the header control")
	}
}

// cardOpenTag matches one card's opening tag, so its class attribute can be read.
var cardOpenTag = regexp.MustCompile(`<div data-civitai-ui="card"[^>]*>`)

// marginClass matches a Tailwind vertical-margin utility (mt-4, mb-6, my-2, -mt-1,
// sm:mt-4, …). Horizontal margins are irrelevant to a vertical gutter.
var marginClass = regexp.MustCompile(`(^|[\s"])-?(sm:|md:|lg:|xl:)?m[tby]-`)

// TestSectionCardsAreSpacedAtTheContainer pins the section gutter and, just as
// importantly, pins WHERE it lives.
//
// #version-region wraps several stacked cards and is itself one child of a <main>
// that already spaces ITS children by space-y-6 — so before this fix the cards
// inside the region rendered flush against each other. The fix belongs on the
// CONTAINER: a per-card margin would double up against <main>'s gutter for every
// card that is a direct child of <main> instead, and every future card added to
// the region would have to remember to carry it.
func TestSectionCardsAreSpacedAtTheContainer(t *testing.T) {
	srv := newModelServer(t, newModelReader(t))
	body := getModelPage(t, srv, "/models/7")

	// 1. <main> spaces its own direct children.
	if !strings.Contains(body, "px-4 py-6 space-y-6") {
		t.Errorf("<main> should carry the shared section gutter")
	}

	// 2. #version-region carries it too, so the cards it wraps are spaced.
	at := strings.Index(body, `id="version-region"`)
	if at < 0 {
		t.Fatalf("no #version-region on the model page")
	}
	tagStart := strings.LastIndex(body[:at], "<div")
	tag := body[tagStart : tagStart+strings.Index(body[tagStart:], ">")+1]
	if !strings.Contains(tag, "space-y-6") {
		t.Errorf("#version-region must carry the section gutter itself — its cards are "+
			"NOT direct children of <main>, so <main>'s space-y-6 does not reach them.\ngot: %s", tag)
	}

	// 3. And NO card carries its own vertical margin — two spacing mechanisms meeting
	//    at a card that is a direct child of <main> would double the gutter.
	for _, open := range cardOpenTag.FindAllString(body, -1) {
		i := strings.Index(open, ` class="`)
		if i < 0 {
			continue
		}
		rest := open[i+len(` class="`):]
		cls := rest[:strings.Index(rest, `"`)]
		if marginClass.MatchString(cls) {
			t.Errorf("a section card carries its OWN vertical margin (%q) on top of the "+
				"container gutter — the two double up. Space at the container instead.\ntag: %s",
				cls, open)
		}
	}
	// The scan is worthless if no card rendered a class attribute at all, but it is
	// legitimate for every card to be class-less; assert the cards exist instead.
	if n := len(cardOpenTag.FindAllString(body, -1)); n < 2 {
		t.Fatalf("expected several section cards on the model page, found %d — re-point this test", n)
	}
}
