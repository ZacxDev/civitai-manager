package web

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// Render caps for the post-scan results view. A pathological library (tens of
// thousands of unmatched files, or a broken-detection flood) would otherwise
// build a DOM large enough to crash the browser tab — this already happened once
// (~45k rows). These caps are a RENDER-LAYER limit ONLY: all M files remain
// counted in the summary/totals and in each section's heading; only the number
// of rows/cards actually emitted is bounded. There is deliberately NO "show all"
// escape hatch — an escape hatch reintroduces the crash. When a section caps, it
// renders a "Showing first N of M …" note (renderCapNote).
const (
	maxRenderedMatchedCards  = 200 // model cards in matchedModelsSection
	maxRenderedUnmatchedRows = 500 // rows in the "Other files" table
	maxRenderedCandidateRows = 500 // rows in the deletion-candidates table
)

// humanCount formats a non-negative integer with thousands separators
// (e.g. 4312 -> "4,312") for the truncation notes. Small self-contained helper —
// no dependency (this package deliberately avoids pulling in golang.org/x/text).
func humanCount(n int) string {
	s := strconv.Itoa(n)
	neg := false
	if n < 0 {
		neg, s = true, s[1:]
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// compactCount renders a count in a compact, human-friendly form for the stat
// lines that show download/like/comment/reaction totals: values below 1000 stay
// as-is, then 1_000 -> "1K", 1_234 -> "1.2K", 3_400_000 -> "3.4M",
// 1_500_000_000 -> "1.5B". One decimal place, with a trailing ".0" trimmed so
// round thousands read as "1K" not "1.0K". Negatives (which shouldn't occur)
// render as-is.
func compactCount(n int) string {
	if n < 0 {
		return strconv.Itoa(n)
	}
	switch {
	case n < 1_000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return compactUnit(n, 1_000, "K")
	case n < 1_000_000_000:
		return compactUnit(n, 1_000_000, "M")
	default:
		return compactUnit(n, 1_000_000_000, "B")
	}
}

// compactUnit formats n/div to one decimal place and appends suffix, trimming a
// trailing ".0" (so 1000/1000 -> "1K", 1234/1000 -> "1.2K").
func compactUnit(n, div int, suffix string) string {
	s := strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64)
	if len(s) >= 2 && s[len(s)-2:] == ".0" {
		s = s[:len(s)-2]
	}
	return s + suffix
}

// renderCapNote renders the "Showing first N of M — capped …" truncation
// indicator for a results section that rendered fewer rows/cards (shown) than
// its true total (total). It returns nil when nothing was capped (shown >= total),
// which gomponents renders as nothing. The muted text-sm text-slate-500 styling
// matches the notes already used in these sections (theme-aware via the shared
// classes).
func renderCapNote(shown, total int) g.Node {
	if shown >= total {
		return nil
	}
	return h.P(h.Class("mt-3 text-sm text-slate-500"),
		g.Text(fmt.Sprintf("Showing largest %s of %s — capped to keep the page responsive.",
			humanCount(shown), humanCount(total))))
}

// spinnerGlyph is a small CSS-animated spinner used inside an htmx-indicator so
// a running request reads as active progress, not a hang.
func spinnerGlyph() g.Node {
	return h.Span(h.Class(
		"inline-block h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-slate-500 border-t-transparent"))
}

// ---------------------------------------------------------------------------
// Library dashboard icons.
//
// Inline, self-contained (feather-style) SVG — the SAME idiom as downloadIconSVG /
// thumbsUpIconSVG (pages.go) and clockIconSVG (model_pages.go): no icon font, no
// CDN, `stroke="currentColor"` so each glyph inherits its container's colour and
// both data-theme paths render for free, and aria-hidden + focusable=false so AT
// reads the element's own text/aria-label instead of announcing a graphic.
//
// These are NOT the `.cm-cta-icon` text-glyph vocabulary (→ ＋ ↗ ▶). That set is
// for "go somewhere" affordances on a button; there is no sensible unicode glyph
// for "duplicate copy" or "out of date", and the emoji the summary pills used
// before (📦 ⧉ ⟳ ⚠ ○) render at the mercy of the platform emoji font, ignore
// currentColor, and do not respond to the theme at all.
//
// Sizing lives in app.css (.cm-stat-ico 14px, .cm-btn-ico / .cm-upd-ico /
// .cm-info-ico) so no new Tailwind utility enters the purged output.css build.
// ---------------------------------------------------------------------------
const (
	// modelsIconSVG — a package/box: one identified model in the library.
	modelsIconSVG = `<svg class="cm-stat-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><polyline points="3.27 6.96 12 12.01 20.73 6.96"/><line x1="12" y1="22.08" x2="12" y2="12"/></svg>`
	// duplicateIconSVG — two stacked sheets: a redundant copy of a file you already
	// have (duplicate or superseded).
	duplicateIconSVG = `<svg class="cm-stat-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`
	// outOfDateIconSVG — a refresh cycle: a newer remote version exists.
	outOfDateIconSVG = `<svg class="cm-stat-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10"/><path d="M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>`
	// unmatchedIconSVG — a question mark: a scanned file CivitAI could not identify.
	unmatchedIconSVG = `<svg class="cm-stat-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="12" cy="12" r="10"/><path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>`
	// rescanIconSVG — the refresh cycle again, sized for a button label.
	rescanIconSVG = `<svg class="cm-btn-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><polyline points="23 4 23 10 17 10"/><polyline points="1 20 1 14 7 14"/><path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10"/><path d="M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/></svg>`
	// updateAvailableIconSVG — an up-arrow in a circle: an upgrade is on offer.
	updateAvailableIconSVG = `<svg class="cm-upd-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="12" cy="12" r="10"/><polyline points="16 12 12 8 8 12"/><line x1="12" y1="16" x2="12" y2="8"/></svg>`
	// infoIconSVG — the "what does this do?" affordance beside the Subscribe button.
	infoIconSVG = `<svg class="cm-info-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>`
)

// libraryView bundles the data the library page renders.
type libraryView struct {
	Files       []store.LocalFile
	Candidates  []store.LocalFile
	TotalBytes  int64
	Reclaimable int64
	// OutOfDate is the number of distinct matched models whose CACHED civitai
	// detail shows an update available (latest remote version not in the library).
	// It is populated by the Server via computeOutOfDate BEFORE rendering (cache-
	// only, no fetch), and is 0 for a pure render (no store) or a cold cache — see
	// computeOutOfDate. summarizeLibrary surfaces it as the "out of date" pill count.
	OutOfDate int
	// ModelNames maps a matched model id → its cached display name (cache-only,
	// populated by the Server before rendering). It lets a matched-model card show
	// the real name IMMEDIATELY instead of the "Model #id" placeholder while its
	// full detail lazy-loads. Missing (uncached) ids fall back to "Model #id",
	// which the per-card lazy load then replaces. Nil on a pure render (no store).
	ModelNames map[int]string
}

func buildLibraryView(files []store.LocalFile) libraryView {
	v := libraryView{}
	for _, f := range files {
		if f.Kind == store.LocalKindModel {
			v.Files = append(v.Files, f)
			v.TotalBytes += f.SizeBytes
		}
		if f.IsCandidate() {
			v.Candidates = append(v.Candidates, f)
			v.Reclaimable += f.SizeBytes
		}
	}
	return v
}

// libraryPage is the full Library page, split into two tabs (finding/selecting
// install dirs vs. scanning them for model files). allowExtra gates the arbitrary
// extra-scan-path capability (discovery, the directory browser, manual add, and
// the persisted selection): it is available only on a loopback bind (see
// Server.extraPathsAllowed), so a network-exposed server never offers the remote
// arbitrary-path walk control. selectedDirs pre-fills the persisted selection.
//
// activeTab ("sources"|"files"; default "sources") is server-rendered from ?tab=
// so the active tab is robust across every htmx swap within a panel. Only the
// active panel is rendered, so its htmx targets exist only while it is shown.
// discoverInitial is the initial content of the stable #discover-results
// container (idle controls, or the live scanning/terminal fragment when a crawl
// is in flight); nil falls back to the idle controls.
func libraryPage(v libraryView, csrf string, allowExtra bool, selectedDirs []string, theme, activeTab string, discoverInitial g.Node, matchRemote bool, scanInitial g.Node, mr maturityRange, lw libraryWorkflowsView, rail ...railData) g.Node {
	var panel g.Node
	switch activeTab {
	case "files":
		panel = filesPanel(v, csrf, allowExtra, selectedDirs, matchRemote, scanInitial)
	case "workflows":
		panel = workflowsPanel(lw, csrf, allowExtra)
	default:
		activeTab = "sources"
		panel = sourcesPanel(csrf, allowExtra, selectedDirs, discoverInitial)
	}
	return page("Library", theme, csrf, mr, railOf(rail),
		h.Div(
			// The page's single <h1>; every heading inside the tab panels is an <h2>.
			pageTitle("Library"),
			libraryTabStrip(activeTab),
		),
		h.Div(h.ID("tab-panel"), panel),
	)
}

// libraryTabStrip renders the two-tab navigation as an UNDERLINE tab strip (not
// buttons): a horizontal row of plain text links where the active tab carries an
// accent-colored underline and inactive tabs are muted. The tabs stay real
// full-page navigation links (?tab=…) — only the styling changed — so the strip
// survives every in-panel htmx interaction (those never re-render it). The tab
// styling lives in the vendored app.css (.lib-tab* classes), themed via the
// --civitai-* tokens so it works in both light and dark with no CDN.
func libraryTabStrip(active string) g.Node {
	return h.Div(
		g.Attr("role", "tablist"),
		h.Class(libTabsClass),
		libraryTab("sources", "Install directories", active),
		libraryTab("files", "Model files", active),
		libraryTab("workflows", "Workflows", active),
	)
}

func libraryTab(id, label, active string) g.Node {
	attrs := []g.Node{
		h.Href("/library?tab=" + id),
		g.Attr("role", "tab"),
	}
	if id == active {
		attrs = append(attrs,
			h.Class(libTabActiveClass),
			g.Attr("aria-selected", "true"),
			// aria-current="page" marks the active tab as the current page for AT,
			// on top of the visual accent-underline distinction.
			g.Attr("aria-current", "page"),
		)
	} else {
		attrs = append(attrs,
			h.Class(libTabClass),
			g.Attr("aria-selected", "false"),
		)
	}
	attrs = append(attrs, g.Text(label))
	return h.A(attrs...)
}

// sourcesPanel is Tab A ("Install directories"): FINDING/SELECTING scan dirs
// only — the stable #discover-results container (discovery button + manual add +
// browser when idle; the live scanning card while a crawl runs) and the persisted
// #selected-dirs list (add/remove). It renders NO model-file scan UI. On a
// non-loopback bind the whole capability is disabled, so it shows only the gating
// note.
func sourcesPanel(csrf string, allowExtra bool, selectedDirs []string, discoverInitial g.Node) g.Node {
	if !allowExtra {
		return card(
			sectionTitle("Install directories"),
			h.P(h.Class("text-sm text-slate-400"),
				g.Text("Directory discovery and selection are disabled when the server is bound to a non-loopback address.")),
		)
	}
	if discoverInitial == nil {
		discoverInitial = discoverControls(csrf)
	}
	return card(
		sectionTitle("Install directories"),
		h.P(h.Class("mb-3 text-sm text-slate-400"),
			g.Text("Find and select the ComfyUI / Automatic1111 install directories to scan. Switch to “Model files” to scan them.")),
		// The STABLE poll/results container: only its innerHTML is ever swapped, so
		// the re-arming poller can never orphan a #discover-poll (the re-discover fix).
		h.Div(h.ID("discover-results"), discoverInitial),
		h.Div(
			h.Class("mt-4 space-y-2 border-t border-slate-800 pt-4"),
			h.Div(h.Class("text-xs font-medium text-slate-300"), g.Text("Selected scan directories")),
			h.Div(h.ID("selected-dirs"), selectedDirsList(selectedDirs, csrf)),
		),
	)
}

// filesPanel is Tab B ("Model files"): SCANNING the selected dirs for model
// files — an explicit "Scan for model files" button, the "Match against CivitAI"
// opt-in, and (after a scan) the Summary / Files-by-model / Deletion-candidate /
// quarantine results. It renders NO discovery UI. When no install directories
// have been selected yet (loopback bind), it shows an empty state pointing at Tab
// A rather than a bare scan button.
// scanInitial is the initial content of the STABLE #scan-results container. It is
// the WHOLE swapped body: for the idle/terminal states it is the scan FORM CARD
// followed by the library/results content; for the running state it is the live
// scanning fragment ALONE (no form — progress is the main content). nil falls back
// to the idle body (form card + idle library content). matchRemote pre-checks the
// persisted "Match against CivitAI" toggle on the form.
//
// The scan form lives INSIDE #scan-results (not in filesPanel's always-on area) so
// that each /library/scan/status poll swap naturally hides the form while a scan
// runs and restores it when the scan settles — see filesTabBody / scanScanning.
func filesPanel(v libraryView, csrf string, allowExtra bool, selectedDirs []string, matchRemote bool, scanInitial g.Node) g.Node {
	// Gate EVERY scan affordance on ≥1 ADDED install directory: until the persisted
	// selection has at least one entry, Tab B shows an empty state pointing at Tab A
	// and renders NO scan button/form — regardless of the loopback/non-loopback bind
	// AND regardless of model_root contents. Trade-off (intended): a model_root that
	// already holds auto-downloaded files is not scannable until the user adds a scan
	// directory in Tab A. This mirrors Tab A's own CTA gating (scanForModelsCTA).
	if len(selectedDirs) == 0 {
		// Guided empty state: a single primary CTA over to the Install-directories
		// tab (where dirs are added) rather than a bare gated scan button.
		return card(
			h.Class("cm-lift py-6 text-center"),
			h.H3(h.Class("text-base font-semibold text-slate-200"), g.Text("No install directories yet")),
			h.P(h.Class("mx-auto mt-1 mb-3 max-w-md text-sm text-slate-400"),
				g.Text("Point the scanner at your model folders first. Add install directories, then come back here to scan them for model files and match your library against CivitAI.")),
			h.A(
				h.Href("/library?tab=sources"),
				dataAttr("civitai-ui", "button"), dataAttr("variant", "filled"), dataAttr("size", "md"),
				h.Span(h.Class("cm-cta-icon"), g.Attr("aria-hidden", "true"), g.Text("→ ")),
				g.Text("Add install directories"),
			),
		)
	}
	if scanInitial == nil {
		// Idle: the scan controls above the idle library content. Before the first
		// scan the form is inline; once results exist it moves behind a dialog.
		scanInitial = filesTabBody(libraryContent(v, csrf), csrf, matchRemote, libraryHasModels(v))
	}
	// The STABLE poll/results container is now the ONLY always-on element: only its
	// innerHTML is ever swapped, so the re-arming scan poller can never orphan a
	// #scan-poll (mirrors #discover-results). It bootstraps from the live scan job on
	// reload and holds the scan form (idle/terminal) or the progress fragment
	// (running).
	return h.Div(h.ID("scan-results"), scanInitial)
}

// libraryHasModels reports whether the persisted library holds at least one MODEL
// FILE row — i.e. whether a scan has ever produced models. It gates the whole
// Model-files tab layout: the big guided SCAN CARD on the first run, a compact
// "Rescan" BUTTON + modal once there is a library to manage.
//
// WHY THIS SIGNAL AND NOT A "last scanned" TIMESTAMP: the schema has none. The
// `settings` table stores only theme / maturity_range / match_remote / comfy_cloud
// / outputs_rail_collapsed, and the only other "has a scan happened" state is
// scanJobState().Started, which lives on the Server struct and RESETS ON RESTART —
// so it answers "did a scan run in THIS process", not "was this library ever
// scanned". The local_files row count is the durable answer and it is already
// loaded for the render (ListLocalFiles → buildLibraryView), so this costs nothing.
//
// It counts MODEL files only (v.Files is the model-kind partition), which is
// exactly "never scanned OR holds 0 models": a scan that turned up nothing but
// broken sidecars still lands on the guided first-run card, because the user has no
// models to manage yet. Its predecessor (hasResults) also counted v.Candidates,
// which let a candidates-only library skip the guided state.
func libraryHasModels(v libraryView) bool {
	return len(v.Files) > 0
}

// scanFormDialogID is the native <dialog> holding the scan form once the library
// holds models. Mirrors workflowImportDialogID — opened by an inline showModal()
// (an inline script is allowed; only EXTERNAL scripts/styles are forbidden),
// closed by its own <form method="dialog"> control.
const scanFormDialogID = "scan-form-dialog"

// scanFormDialogTitleID is the heading the dialog is labelled by (aria-labelledby),
// so AT announces "Scan for model files" rather than an unnamed dialog.
const scanFormDialogTitleID = "scan-form-dialog-title"

// modelScanFormCard is the "Model files" scan form wrapped in its titled card. It
// is the INLINE (first-scan) form: shown above the idle library content before any
// scan has produced results.
func modelScanFormCard(csrf string, matchRemote bool) g.Node {
	return card(
		sectionTitle("Model files"),
		modelScanForm(csrf, matchRemote),
	)
}

// scanRescanControl is the populated-library scan control: a single "Rescan
// library" BUTTON (not a card) plus the native <dialog> holding the scan form. The
// form, its POST endpoint, CSRF, loopback gate and the streaming poll into
// #scan-results are UNCHANGED — only the placement moved. The dialog closes on
// submit (via the scan's HX-Redirect reload) and on Cancel/✕ (<form
// method="dialog">).
//
// It replaced a full titled CARD, which spent a whole card of vertical space on a
// control the user touches once per session. A row with one right-aligned button
// costs a line.
//
// 🔴 WHY A NATIVE <dialog>.showModal() AND NOT A HAND-ROLLED PANEL. Everything the
// brief asks of the modal — Escape dismissal, focus moved INTO the dialog on open,
// focus TRAPPED while open (the rest of the document is inert), and focus RESTORED
// to the trigger on close — is behaviour the browser gives us for free, but ONLY
// via showModal(). The `open` attribute and `.show()` render the same box as a
// NON-modal: no top layer, no inertness, no Escape handling, no focus containment.
// That is the whole reason railDrawerScript (outputs_rail.go) has to hand-roll
// role=dialog + aria-modal + an `inert` sweep + a saved activeElement + an Escape
// listener: the rail is a static complementary COLUMN on desktop and only becomes a
// drawer on mobile, so it cannot be a <dialog> at all. This control has no such
// constraint, so it uses the platform and adds nothing but a label. Do not
// "simplify" showModal() to .show().
func scanRescanControl(csrf string, matchRemote bool) g.Node {
	trigger := civButton("outline", "md", []g.Node{
		h.Type("button"),
		g.Attr("onclick", "document.getElementById('"+scanFormDialogID+"').showModal()"),
	}, g.Raw(rescanIconSVG), g.Text("Rescan library"))

	dialog := h.Dialog(
		h.ID(scanFormDialogID),
		// Transparent shell; the inner card is the visible, theme-aware surface.
		h.Class("bg-transparent p-0 border-0 w-full max-w-lg"),
		g.Attr("aria-labelledby", scanFormDialogTitleID),
		card(
			h.Div(h.Class("flex items-center justify-between gap-4 mb-3"),
				h.H2(h.ID(scanFormDialogTitleID),
					h.Class("text-lg font-semibold text-slate-100"), g.Text("Scan for model files")),
				h.Form(h.Method("dialog"), h.Class("inline"),
					civButton("subtle", "sm", []g.Node{h.Type("submit"),
						g.Attr("aria-label", "Close")}, g.Text("✕"))),
			),
			h.P(h.Class("mb-3 text-sm text-slate-400"),
				g.Text("Re-run the scan to pick up new, changed, or removed files.")),
			modelScanForm(csrf, matchRemote),
		),
	)

	return h.Div(
		h.Class("flex items-center justify-end gap-3"),
		trigger,
		dialog,
	)
}

// filesTabBody is the innerHTML of #scan-results for the IDLE and TERMINAL states:
// the scan CONTROLS above the given results body (idle library content, or the
// terminal scanResults view). With NO models yet (hasModels=false) the guided scan
// CARD is rendered inline and prominent — that is the first-run state and it should
// dominate. Once the library holds models the card collapses to the "Rescan
// library" button + modal, so the results own the page. The RUNNING state does NOT
// use this — it swaps in scanScanning alone (no form).
func filesTabBody(body g.Node, csrf string, matchRemote, hasModels bool) g.Node {
	controls := modelScanFormCard(csrf, matchRemote)
	if hasModels {
		controls = scanRescanControl(csrf, matchRemote)
	}
	return h.Div(
		h.Class("space-y-6"),
		controls,
		body,
	)
}

// modelScanForm renders Tab B's model-file scan form: the explicit "Scan for
// model files" submit and the opt-in "Match against CivitAI" checkbox. It carries
// NO scan_dir checkboxes — the dirs to scan are the persisted selection managed in
// Tab A (handleLibraryScan falls back to them when no checkboxes are submitted).
//
// The remote-match checkbox defaults ON (matchRemoteEnabled defaults true when
// unset): by default a web scan matches against CivitAI by hash so the library is
// identified. Matching sends each file's SHA256 to civitai.com's by-hash lookup —
// stated inline beneath the toggle. Unchecking it makes THIS and future scans run
// offline (local duplicate/broken analysis only); that choice persists.
func modelScanForm(csrf string, matchRemote bool) g.Node {
	// The toggle PERSISTS on change (POST /settings/match-remote, no swap) so it is
	// the single source of truth the Tab-A CTA also reads. A single checkbox posts
	// its value only when checked, so presence == enabled.
	cb := []g.Node{
		h.Type("checkbox"), h.Name("match_remote"), h.Value("true"),
		hx("post", "/settings/match-remote"),
		hx("trigger", "change"),
		hx("swap", "none"),
		csrfInline(csrf),
		h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500"),
	}
	if matchRemote {
		cb = append(cb, g.Attr("checked"))
	}
	return h.Form(
		// Submitting starts the async streaming scan; the handler HX-Redirects to the
		// Model files tab. hx-target is only the fallback for a synchronous validation
		// error (rendered into #scan-results); on success the redirect supersedes it.
		hx("post", "/library/scan"),
		hx("target", "#scan-results"),
		hx("swap", "innerHTML"),
		// NOTE: no onsubmit dialog-close. A successful scan HX-Redirects to the Model
		// files tab (a full reload), which removes the "Scan / Rescan" <dialog>
		// naturally — so closing it here is redundant, and a native onsubmit close can
		// race htmx's own submit handling (browser/version-dependent, unverifiable
		// without a real browser). Deterministic path: let the redirect do it.
		h.Class("space-y-3"),
		csrfInput(csrf),
		h.Label(
			h.Class("flex items-center gap-2 text-xs text-slate-400"),
			h.Input(cb...),
			g.Text("Match against CivitAI (sends file hashes to civitai.com)"),
		),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Matches your files against CivitAI by hash (sends file hashes to civitai.com). Uncheck to scan offline.")),
		btnPrimary(g.Text("Scan for model files")),
	)
}

// libraryContent is the fragment swapped after a scan: totals, per-model
// grouping, and the deletion-candidate table.
func libraryContent(v libraryView, csrf string) g.Node {
	matched, unmatched := splitMatchedUnmatched(v.Files)
	return h.Div(
		h.Class("space-y-6"),
		// ONE status card. It replaced the old summaryBanner (a pills row + a
		// quarantine CTA + a "your library is clean" alert) AND the separate "Summary"
		// card (a 4-cell Files / Total size / Candidates / Reclaimable grid). Those two
		// restated the same numbers in two shapes one above the other; every figure
		// they carried now lives in exactly one place — a chip, or that chip's popover.
		libraryStatusCard(v),
		// Matched models + unmatched files, ONE card with two tabs (they used to be two
		// stacked cards, which buried the unmatched list under up to 200 model cards).
		matchedFilesCard(matched, unmatched, v.ModelNames),
		card(
			h.ID("deletion-candidates"),
			sectionTitle("Deletion candidates"),
			candidatesTable(v.Candidates, csrf),
			h.Div(h.ID("quarantine-preview"), h.Class("mt-3")),
		),
		// The shared lightbox + interaction scripts the model-card carousels reuse.
		// Included once here (the results fragment) so a lazy-loaded card's tiles can
		// open the lightbox and the prev/next buttons work. Offline/vendored only.
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// ---------------------------------------------------------------------------
// Matched / unmatched, ONE card with two tabs.
//
// They used to be two stacked cards ("Matched models (N)" then "Other files (M
// unmatched)"), which put the unmatched list BELOW up to maxRenderedMatchedCards
// model cards — in a real library that is several screens of scrolling before the
// files that most need attention.
//
// The strip REUSES the page's existing underline tab idiom (.lib-tabs / .lib-tab /
// .lib-tab-active, shared with libraryTabStrip via the consts below) rather than
// inventing a second one. It differs in ONE way, deliberately: libraryTabStrip's
// tabs are <a href="/library?tab=…"> full-page navigations, because switching the
// PAGE tab re-renders a different panel server-side. These two panels are both
// already in the DOM (the matched cards lazy-load themselves, the unmatched table
// is a plain table), so a round trip would be pure latency — they are <button>s
// toggling `hidden`, which keeps Enter/Space activation for free and adds
// ArrowLeft/Right roving per the ARIA tabs pattern.
// ---------------------------------------------------------------------------

// The shared underline-tab class vocabulary. Both strips read these consts, so the
// two can never drift apart into a fork (and the class-coverage guard resolves
// package-level consts, so the tokens stay covered by the stylesheet check).
const (
	libTabsClass      = "lib-tabs mt-1 mb-4 flex gap-6"
	libTabClass       = "lib-tab"
	libTabActiveClass = "lib-tab lib-tab-active"
)

// Stable ids wiring the in-card tabs to their panels (aria-controls /
// aria-labelledby, and the toggle script's lookup).
const (
	matchedTabID    = "lib-files-tab-matched"
	unmatchedTabID  = "lib-files-tab-unmatched"
	matchedPanelID  = "lib-files-panel-matched"
	unmatchedPanelI = "lib-files-panel-unmatched"
)

// matchedFilesCard renders the identified models and the unidentified files as two
// TABS of one card. The matched tab is selected by default (it is the primary
// content); both panels are always in the DOM, so switching costs no request.
func matchedFilesCard(matched []fileGroup, unmatched []store.LocalFile, names map[int]string) g.Node {
	return card(
		sectionTitle("Model files"),
		h.Div(
			g.Attr("role", "tablist"),
			g.Attr("aria-label", "Model files"),
			h.Class(libTabsClass),
			filesTab(matchedTabID, matchedPanelID,
				fmt.Sprintf("Matched models (%d)", len(matched)), true),
			filesTab(unmatchedTabID, unmatchedPanelI,
				fmt.Sprintf("Unmatched (%d)", len(unmatched)), false),
		),
		h.Div(
			h.ID(matchedPanelID),
			g.Attr("role", "tabpanel"),
			g.Attr("aria-labelledby", matchedTabID),
			// tabindex=0 makes a scrollable panel keyboard-reachable (ARIA APG).
			g.Attr("tabindex", "0"),
			matchedModelsSection(matched, names),
		),
		h.Div(
			h.ID(unmatchedPanelI),
			g.Attr("role", "tabpanel"),
			g.Attr("aria-labelledby", unmatchedTabID),
			g.Attr("tabindex", "0"),
			// The boolean `hidden` attribute (gomponents' h.Hidden is the INPUT type),
			// which is what cmLibFilesTab toggles.
			g.Attr("hidden"),
			otherFilesSection(unmatched),
		),
		libraryFilesTabScript(),
	)
}

// filesTab renders one in-card tab button. The active/inactive class pair and the
// aria-selected flag are the SAME contract libraryTab uses; only the element
// differs (<button> vs <a>), because these tabs switch a panel already in the DOM.
func filesTab(id, panelID, label string, active bool) g.Node {
	cls := libTabClass
	sel := "false"
	if active {
		cls = libTabActiveClass
		sel = "true"
	}
	return h.Button(
		h.ID(id),
		h.Type("button"),
		g.Attr("role", "tab"),
		h.Class(cls),
		g.Attr("aria-selected", sel),
		g.Attr("aria-controls", panelID),
		g.Attr("onclick", "cmLibFilesTab(this)"),
		g.Attr("onkeydown", "cmLibFilesTabKey(event,this)"),
		g.Text(label),
	)
}

// libraryFilesTabScript toggles the in-card matched/unmatched tabs. Vendored
// inline, no framework, idempotent so it survives every htmx swap of the results
// fragment (the scan poller re-renders this whole subtree).
//
// It drives BOTH the visual state (.lib-tab-active) and the a11y state
// (aria-selected + the panels' `hidden`), from the tablist it was clicked in — so
// the same helper would serve a second in-card strip without modification. Arrow
// keys move between tabs per the ARIA tabs pattern; Enter/Space need no handling at
// all because these are real <button>s.
func libraryFilesTabScript() g.Node {
	const js = `
function cmLibFilesTab(btn){
  var strip = btn.closest('[role="tablist"]');
  if(!strip){ return; }
  var tabs = Array.prototype.slice.call(strip.querySelectorAll('[role="tab"]'));
  tabs.forEach(function(t){
    var on = t === btn;
    t.setAttribute('aria-selected', on ? 'true' : 'false');
    t.className = on ? 'lib-tab lib-tab-active' : 'lib-tab';
    var panel = document.getElementById(t.getAttribute('aria-controls'));
    if(panel){ panel.hidden = !on; }
  });
  btn.focus();
}
function cmLibFilesTabKey(e, btn){
  if(e.key !== 'ArrowLeft' && e.key !== 'ArrowRight'){ return; }
  var strip = btn.closest('[role="tablist"]');
  if(!strip){ return; }
  var tabs = Array.prototype.slice.call(strip.querySelectorAll('[role="tab"]'));
  var i = tabs.indexOf(btn);
  if(i < 0){ return; }
  e.preventDefault();
  var next = tabs[(i + (e.key === 'ArrowRight' ? 1 : tabs.length - 1)) % tabs.length];
  if(next){ cmLibFilesTab(next); }
}
`
	return h.Script(g.Raw(js))
}

// matchedModelsSection renders the identified models as enriched, lazy-loaded
// cards (one per model), ordered by total local size descending so the biggest
// reclaimable footprints lead. Each card renders immediately as a placeholder and
// lazy-loads its name + carousel + details.
//
// It returns PANEL CONTENT, not a card: the card + heading now belong to
// matchedFilesCard, and the count moved into the tab label (a heading inside a tab
// panel would restate the tab).
func matchedModelsSection(groups []fileGroup, names map[int]string) g.Node {
	if len(groups) == 0 {
		return h.P(h.Class("text-sm text-slate-500"),
			g.Text("No models identified yet. Enable “Match against CivitAI” and scan to identify your library."))
	}
	total := len(groups)
	// Cap the rendered cards. groups is already sorted biggest-footprint-first
	// (splitMatchedUnmatched), so the cap keeps the most important models; do NOT
	// change that sort. The TAB LABEL shows the TRUE total, not the capped N.
	shown := groups
	if total > maxRenderedMatchedCards {
		shown = groups[:maxRenderedMatchedCards]
	}
	var cards []g.Node
	for _, gr := range shown {
		cards = append(cards, modelCardLazy(gr, names[gr.modelID]))
	}
	return h.Div(
		h.Div(h.Class("grid gap-4 md:grid-cols-2"), g.Group(cards)),
		renderCapNote(len(shown), total),
	)
}

// otherFilesSection renders the unmatched (unidentified) files as a sortable
// table. When everything matched it shows a reassuring note instead. Like
// matchedModelsSection it returns PANEL CONTENT — matchedFilesCard owns the card
// and the tab label owns the count.
func otherFilesSection(unmatched []store.LocalFile) g.Node {
	if len(unmatched) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Every scanned file was identified on CivitAI."))
	}
	return libraryModelTable(unmatched)
}

// splitMatchedUnmatched partitions scanned model files into matched-model groups
// (files carrying a CivitAI model id, grouped by model, ordered by total size
// desc) and the flat list of unmatched files.
func splitMatchedUnmatched(files []store.LocalFile) (matched []fileGroup, unmatched []store.LocalFile) {
	byID := map[int]*fileGroup{}
	var order []int
	for _, f := range files {
		if f.ModelID == nil {
			unmatched = append(unmatched, f)
			continue
		}
		id := *f.ModelID
		gr, ok := byID[id]
		if !ok {
			gr = &fileGroup{modelID: id}
			byID[id] = gr
			order = append(order, id)
		}
		gr.files = append(gr.files, f)
	}
	matched = make([]fileGroup, 0, len(order))
	for _, id := range order {
		matched = append(matched, *byID[id])
	}
	// Biggest total footprint first; ties broken by model id for determinism.
	sort.Slice(matched, func(a, b int) bool {
		sa, sb := groupBytes(matched[a]), groupBytes(matched[b])
		if sa != sb {
			return sa > sb
		}
		return matched[a].modelID < matched[b].modelID
	})
	// Unmatched files are rendered then capped (maxRenderedUnmatchedRows), so sort
	// biggest-first here to make the rendered subset the LARGEST unmatched files —
	// otherwise the store's path-alphabetical order could cap away the most valuable
	// file. Ties broken by path for determinism.
	sort.Slice(unmatched, func(a, b int) bool {
		if unmatched[a].SizeBytes != unmatched[b].SizeBytes {
			return unmatched[a].SizeBytes > unmatched[b].SizeBytes
		}
		return unmatched[a].Path < unmatched[b].Path
	})
	return matched, unmatched
}

// groupBytes sums a group's file sizes.
func groupBytes(gr fileGroup) int64 {
	var total int64
	for _, f := range gr.files {
		total += f.SizeBytes
	}
	return total
}

// librarySummary is the post-scan roll-up the next-steps banner renders.
type librarySummary struct {
	ModelsIdentified int   // distinct matched model ids
	Unmatched        int   // model files not identified on CivitAI
	Duplicates       int   // redundant copies (duplicate + superseded candidates)
	DuplicateBytes   int64 // reclaimable bytes from those redundant copies
	Broken           int   // broken sidecars/partials
	OutOfDate        int   // matched models with a newer remote version (cache-only)
}

// summarizeLibrary derives the banner roll-up from a libraryView: distinct
// identified models, unidentified files, redundant (duplicate/superseded)
// copies + their reclaimable bytes, broken files, and the out-of-date count
// (surfaced from v.OutOfDate, which the Server computes cache-only before render).
func summarizeLibrary(v libraryView) librarySummary {
	var s librarySummary
	models := map[int]bool{}
	for _, f := range v.Files {
		if f.ModelID != nil {
			models[*f.ModelID] = true
		} else {
			s.Unmatched++
		}
	}
	s.ModelsIdentified = len(models)
	for _, c := range v.Candidates {
		switch c.CandidateReason {
		case store.CandidateBroken:
			s.Broken++
		case store.CandidateDuplicate, store.CandidateSuperseded:
			s.Duplicates++
			s.DuplicateBytes += c.SizeBytes
		}
	}
	s.OutOfDate = v.OutOfDate
	return s
}

// modelDetailResolver resolves a model id to its civitai detail, or nil when it
// cannot be resolved. The out-of-date computation passes a CACHE-ONLY resolver so
// the dashboard/library render never blocks on the network.
type modelDetailResolver func(id int) *civitai.ModelDetail

// computeOutOfDate counts the distinct matched models in v whose resolved detail
// shows an update available (the latest remote version is not in the library).
//
// It is cache-only in practice: the Server passes a resolver that returns nil for
// an uncached/stale model, and those are skipped (never counted, never fetched).
// So on a COLD cache the count UNDERSTATES — that is intentional, to keep the
// render fast. resolve may be nil (pure render / no store), yielding 0.
func computeOutOfDate(v libraryView, resolve modelDetailResolver) int {
	if resolve == nil {
		return 0
	}
	filesByModel := map[int][]store.LocalFile{}
	for _, f := range v.Files {
		if f.ModelID != nil {
			id := *f.ModelID
			filesByModel[id] = append(filesByModel[id], f)
		}
	}
	n := 0
	for id, files := range filesByModel {
		m := resolve(id)
		if m == nil {
			continue
		}
		if buildVersionBreakdown(m.ModelVersions, files).UpdateAvailable {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// The ONE library status card.
//
// It replaced TWO cards that sat one above the other and said the same things in
// two shapes: summaryBanner (an emoji pills row + a "Review & quarantine…" CTA + a
// "Your library is clean" alert) and the "Summary" card (a four-cell Files / Total
// size / Candidates / Reclaimable grid). "Candidates" and "duplicates" were the
// same population counted twice; "Files" and "models" were adjacent and easy to
// confuse. De-duplication is by CONSTRUCTION here: every figure has exactly one
// home — either a chip's face or that chip's popover — so the two can never drift.
//
// The card's content IS the chip row, at text-xs (the pills row + the 4-cell grid
// + an alert were roughly five times the height).
//
// POPOVERS: the chips reuse the app's EXISTING popover mechanism verbatim — a
// `.cm-updated` wrapper with a `.cm-updated-pop` child — so they inherit the shared
// hover controller in modelPageScript (delegated on `.cm-vstatus, .cm-updated`,
// which matters because this fragment is htmx-swapped), the ~200 ms grace period,
// the CSS :hover/:focus-within no-JS fallback, and the `.cm-lift:has(...)` popover
// escape rule. `tabindex="0"` is what makes :focus-within reachable from the
// keyboard, so "click/hover" means the same thing with a keyboard.
//
// NO `title=` on a chip: an element owning a custom popover must not also carry the
// native tooltip, or the user gets two overlapping tooltips saying the same thing
// (see updatedPopBody, and popover_no_title_web_test.go). Each chip's accessible
// name is its own visible text ("3 duplicates"); the icons are aria-hidden.
// ---------------------------------------------------------------------------

// libraryStatusCard renders the single roll-up card: four chips — models,
// duplicates, out of date, unmatched — each opening a popover with the detail.
func libraryStatusCard(v libraryView) g.Node {
	s := summarizeLibrary(v)

	models := []g.Node{
		statChipLine(humanBytes(v.TotalBytes) + " on disk"),
		statChipLine(fmt.Sprintf("%s model file(s) across %s model(s)",
			humanCount(len(v.Files)), humanCount(s.ModelsIdentified))),
	}

	// The duplicates chip absorbs the whole quarantine story the old banner told:
	// the reclaimable bytes, the BROKEN count (which is a candidate reason, not a
	// fifth chip — it shares the deletion-candidates table with the duplicates), and
	// the jump to that table.
	dups := []g.Node{
		statChipLine(humanBytes(s.DuplicateBytes) + " reclaimable"),
		statChipLine("Duplicate and superseded copies of files you already have."),
	}
	if s.Broken > 0 {
		dups = append(dups, statChipLine(fmt.Sprintf("Plus %s broken file(s) — same table.", humanCount(s.Broken))))
	}
	if s.Duplicates > 0 || s.Broken > 0 {
		// onclick stops propagation so the click navigates from inside the
		// JS-hover-controlled popover (same trick as the version-status deeplink).
		dups = append(dups, h.Div(h.A(
			h.Href("#deletion-candidates"),
			h.Class("text-indigo-300 hover:text-indigo-200"),
			g.Attr("onclick", "event.stopPropagation()"),
			g.Text("Review deletion candidates →"),
		)))
	} else {
		dups = append(dups, statChipLine("Nothing to reclaim — your library is clean."))
	}

	outdated := []g.Node{
		statChipLine("Models whose newest CivitAI version is not in your library."),
		// Honesty about the cache-only derivation: computeOutOfDate skips a model
		// whose detail is uncached/stale rather than fetching it, so a cold cache
		// UNDERSTATES. Saying so beats a number the user cannot reconcile.
		statChipLine("Counted from cached model details only, so a freshly-scanned library can read low until its cards load."),
	}
	if s.OutOfDate == 0 {
		outdated = []g.Node{statChipLine("Every model whose details are cached is on its newest version.")}
	}

	unmatched := []g.Node{
		statChipLine("Scanned files CivitAI could not identify by hash."),
		statChipLine("Usually a renamed, merged or self-trained file. Open the Unmatched tab to see them."),
	}
	if s.Unmatched == 0 {
		unmatched = []g.Node{statChipLine("Every scanned file was identified on CivitAI.")}
	}

	return card(
		h.Div(
			h.Class("flex flex-wrap items-center gap-2"),
			statChip("neutral", modelsIconSVG, s.ModelsIdentified, "models", models),
			statChip("dup", duplicateIconSVG, s.Duplicates, "duplicates", dups),
			statChip("update", outOfDateIconSVG, s.OutOfDate, "out of date", outdated),
			statChip("neutral", unmatchedIconSVG, s.Unmatched, "unmatched", unmatched),
		),
	)
}

// statChip renders one status chip: an inline SVG icon, the count, a label, and a
// hover/focus popover carrying the detail.
//
// 🔴 A ZERO COUNT RENDERS DIMMED — IT IS NEVER OMITTED. Pinned deliberately, both
// ways round. The old pills row hid every zero, which made the row's SHAPE depend
// on the data (four chips, then two, then three as a scan progressed) and silently
// dropped the reassurance a user actually wants: "0 duplicates" is information, an
// absent chip is not — the old card had to spend a whole `alert("success", "Your
// library is clean")` re-stating what a visible zero says by itself.
//
// The dimming is a COLOUR swap (.cm-chip-zero → the dimmed text token + the neutral
// border), NOT `opacity`. `opacity < 1` creates a stacking context (CSS Color 4
// §12), which would trap the chip's own `z-index: 50` popover inside the chip — the
// exact class of bug the .cm-lift POPOVER ESCAPE block exists for. Do not
// "simplify" this to an opacity.
func statChip(variant, iconSVG string, count int, label string, detail []g.Node) g.Node {
	// Two literal h.Class call sites rather than one built from a local variable:
	// the class-coverage guard resolves literals and `"lit"+param` concatenations,
	// and classCoverageOpaqueBudget is pinned EXACTLY — a shape it cannot resolve
	// would fail that test as a new blind spot.
	cls := h.Class("cm-updated cm-chip-stat cm-pill cm-pill-" + variant)
	if count == 0 {
		cls = h.Class("cm-updated cm-chip-stat cm-pill cm-chip-zero")
	}
	return h.Span(
		cls,
		g.Attr("tabindex", "0"),
		g.Raw(iconSVG),
		h.Span(h.Class("font-semibold"), g.Text(strconv.Itoa(count))),
		g.Text(" "+label),
		h.Span(
			h.Class("cm-updated-pop"),
			g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text(strconv.Itoa(count)+" "+label)),
			g.Group(detail),
		),
	)
}

// statChipLine is one plain detail row inside a chip's popover.
func statChipLine(text string) g.Node { return h.Div(g.Text(text)) }

// pathCell renders a truncated filesystem-path table cell.
//
// Two things a bare `truncate` cell got wrong. It clipped the RIGHT, which throws
// away the only part of a model path that identifies the file — the filename at
// the end — leaving rows of interchangeable "/mnt/models/checkpoints/sta…". And it
// offered no way to see the rest at all: no title, no wrap, no expansion. So the
// cell now flips the ellipsis to the START (.cm-path-ellipsis) and always carries
// the FULL path as a title tooltip, which is also what a screen reader announces.
//
// cls is the cell's class NODE (not a string) so the literal stays at the call
// site and the class-coverage guard can still see every token; the path is escaped
// by g.Text and by the title attribute encoder.
func pathCell(cls g.Node, path string) g.Node {
	return h.Td(
		cls,
		h.Title(path),
		// <bdi> isolates the path's own text direction inside the RTL cell, so the
		// leading "/" cannot be reordered to the tail (see .cm-path-ellipsis).
		g.El("bdi", g.Text(path)),
	)
}

// File-size magnitude thresholds for the color-coded Size cell (see sizeClass).
// Documented tiers: <500MB muted, 500MB–2GB yellow, 2–6GB orange, >6GB red.
const (
	sizeTierMedium int64 = 500 * 1024 * 1024      // 500 MB
	sizeTierLarge  int64 = 2 * 1024 * 1024 * 1024 // 2 GB
	sizeTierHuge   int64 = 6 * 1024 * 1024 * 1024 // 6 GB
)

// sizeClass maps a byte size to its magnitude tier CSS class (defined in
// app.css, theme-aware via --civitai-* tokens): so a multi-GB checkpoint reads
// red at a glance and a small LoRA reads muted.
func sizeClass(b int64) string {
	switch {
	case b >= sizeTierHuge:
		return "cm-size-huge"
	case b >= sizeTierLarge:
		return "cm-size-large"
	case b >= sizeTierMedium:
		return "cm-size-medium"
	default:
		return "cm-size-small"
	}
}

// sizeCell renders a table Size cell: the humanized size colored by magnitude
// (sizeClass) and carrying the RAW byte count in data-sort-value so the
// client-side column sorter orders by bytes, not the humanized string.
func sizeCell(b int64) g.Node {
	return h.Td(
		h.Class("px-3 py-2 "+sizeClass(b)),
		dataAttr("sort-value", strconv.FormatInt(b, 10)),
		g.Text(humanBytes(b)),
	)
}

// sizeText renders a non-table size label colored by magnitude (used on the
// streamed scan-result cards, which are not tables).
func sizeText(b int64) g.Node {
	return h.Span(h.Class("shrink-0 text-xs "+sizeClass(b)), g.Text(humanBytes(b)))
}

func libraryModelTable(files []store.LocalFile) g.Node {
	if len(files) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No files scanned yet. Click “Scan for model files”."))
	}
	total := len(files)
	groups := groupFilesByModel(files)
	// Cap the rendered rows to keep the DOM bounded (see maxRenderedUnmatchedRows).
	// The section heading (otherFilesSection) still shows the true total M — only
	// the emitted rows are limited. Client-side column sort (librarySortScript)
	// only reorders the rows that were actually rendered; on a capped view it sorts
	// the rendered subset, which is acceptable for a firm crash-prevention cap.
	var rows []g.Node
	for _, gr := range groups {
		for _, f := range gr.files {
			if len(rows) >= maxRenderedUnmatchedRows {
				break
			}
			rows = append(rows, h.Tr(
				h.Class("border-b border-slate-800/60"),
				h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(modelLabel(f.ModelID))),
				h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(versionLabel(f.VersionID))),
				h.Td(h.Class("px-3 py-2"), statusBadge(f)),
				sizeCell(f.SizeBytes),
				pathCell(h.Class("cm-path-ellipsis px-3 py-2 text-slate-300 truncate max-w-lg"), f.Path),
			))
		}
		if len(rows) >= maxRenderedUnmatchedRows {
			break
		}
	}
	return h.Div(
		h.Class("overflow-x-auto"),
		h.Table(
			h.Class("cm-sortable-table min-w-full text-sm"),
			h.THead(h.Tr(
				h.Class("text-left text-slate-400 border-b border-slate-800"),
				sortableTh("Model"), sortableTh("Version"), sortableTh("Status"),
				sortableTh("Size"), sortableTh("Path"),
			)),
			h.TBody(g.Group(rows)),
		),
		librarySortScript(),
		renderCapNote(len(rows), total),
	)
}

// sortableTh renders a click-to-sort table header: keyboard-operable (Enter or
// Space), announced to AT via aria-sort (the single source of truth the CSS
// indicator glyph also reads), and marked data-sortable so the inline sort
// script can find it. The size column carries data-sort-value on its cells, so
// that column sorts numerically by bytes (see librarySortScript).
func sortableTh(label string) g.Node {
	return h.Th(
		h.Class("px-3 py-2 font-medium"),
		dataFlag("sortable"),
		g.Attr("role", "columnheader"),
		g.Attr("aria-sort", "none"),
		g.Attr("tabindex", "0"),
		g.Attr("onclick", "cmSortTable(this)"),
		g.Attr("onkeydown", "if(event.key==='Enter'||event.key===' '){event.preventDefault();cmSortTable(this);}"),
		h.Span(g.Text(label)),
		h.Span(dataFlag("sort-ind-cell"), h.Class("cm-sort-ind")),
	)
}

// librarySortScript is the small, self-contained (vendored, no CDN) client-side
// column sorter. Clicking (or Enter/Space on) a data-sortable header sorts the
// loaded tbody rows in-browser and toggles asc/desc, updating aria-sort on the
// headers (which also drives the CSS direction glyph). A cell carrying
// data-sort-value is compared NUMERICALLY (so Size sorts by raw bytes, not the
// humanized string); otherwise a case-insensitive text compare is used. The
// function is (re)defined idempotently so it survives every htmx swap of the
// results fragment; it attaches no duplicate listeners (headers use inline
// onclick).
func librarySortScript() g.Node {
	const js = `
function cmSortTable(th){
  var table = th.closest('table');
  if(!table){ return; }
  var headers = Array.prototype.slice.call(table.querySelectorAll('th[data-sortable]'));
  var idx = headers.indexOf(th);
  if(idx < 0){ return; }
  var dir = th.getAttribute('aria-sort') === 'ascending' ? 'descending' : 'ascending';
  headers.forEach(function(h){ h.setAttribute('aria-sort', 'none'); });
  th.setAttribute('aria-sort', dir);
  var tbody = table.tBodies[0];
  if(!tbody){ return; }
  var mult = dir === 'ascending' ? 1 : -1;
  var rows = Array.prototype.slice.call(tbody.rows);
  rows.sort(function(a, b){
    var ca = a.cells[idx], cb = b.cells[idx];
    if(ca && cb && ca.hasAttribute('data-sort-value') && cb.hasAttribute('data-sort-value')){
      var na = parseFloat(ca.getAttribute('data-sort-value')) || 0;
      var nb = parseFloat(cb.getAttribute('data-sort-value')) || 0;
      return (na - nb) * mult;
    }
    var ta = ca ? ca.textContent.trim().toLowerCase() : '';
    var tb = cb ? cb.textContent.trim().toLowerCase() : '';
    if(ta < tb){ return -1 * mult; }
    if(ta > tb){ return 1 * mult; }
    return 0;
  });
  rows.forEach(function(r){ tbody.appendChild(r); });
}
`
	return h.Script(g.Raw(js))
}

// candidatesTable renders flagged candidates with per-row + bulk quarantine
// (dry-run preview). Every action POSTs with the CSRF token.
func candidatesTable(cands []store.LocalFile, csrf string) g.Node {
	if len(cands) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No deletion candidates."))
	}
	total := len(cands)
	// Sort a COPY biggest-first (ties by id) BEFORE capping so the rendered subset
	// is the LARGEST reclaimable candidates, not an arbitrary path-alphabetical slice
	// (ListCandidates orders by path). Copy first — never mutate the caller's slice.
	// The summary counts use len() only, so this reorder never affects totals.
	sorted := append([]store.LocalFile(nil), cands...)
	sort.Slice(sorted, func(a, b int) bool {
		if sorted[a].SizeBytes != sorted[b].SizeBytes {
			return sorted[a].SizeBytes > sorted[b].SizeBytes
		}
		return sorted[a].ID < sorted[b].ID
	})
	capped := total > maxRenderedCandidateRows
	// Cap the rendered rows (see maxRenderedCandidateRows). All M candidates remain
	// counted in the summary/totals; only the emitted rows are limited.
	shown := sorted
	if capped {
		shown = sorted[:maxRenderedCandidateRows]
	}
	var rows []g.Node
	for _, c := range shown {
		id := strconv.FormatInt(c.ID, 10)
		rows = append(rows, h.Tr(
			h.Class("border-b border-slate-800/60"),
			h.Td(h.Class("px-3 py-2"),
				h.Input(h.Type("checkbox"), h.Name("id"), h.Value(id),
					h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500")),
			),
			h.Td(h.Class("px-3 py-2"), candidateBadge(c.CandidateReason)),
			sizeCell(c.SizeBytes),
			pathCell(h.Class("cm-path-ellipsis px-3 py-2 text-slate-300 truncate max-w-md"), c.Path),
			h.Td(h.Class("px-3 py-2 text-right"),
				civButton("subtle", "sm", []g.Node{
					h.Type("button"),
					hx("post", "/library/quarantine"),
					hx("vals", fmt.Sprintf(`{"id":"%s","apply":"false","csrf_token":"%s"}`, id, csrf)),
					hx("target", "#quarantine-preview"),
					hx("swap", "innerHTML"),
					h.StyleAttr(tokenVars("warning")),
				}, g.Text("Quarantine")),
			),
		))
	}
	// "Quarantine all <reason>" buttons — one per reason that actually has ≥1
	// candidate in the FULL (uncapped) set. These hit the cap-immune reason path in
	// quarantineIDs (ListCandidates resolves ALL of that reason), so candidates past
	// the render cap remain quarantinable. type="button" so they fire their own htmx
	// request instead of submitting the surrounding checkbox form (like the per-row
	// button). Counts come from the full set, so the labels are accurate even capped.
	allBtns := quarantineAllButtons(cands, csrf)

	return h.Form(
		hx("post", "/library/quarantine"),
		hx("vals", fmt.Sprintf(`{"apply":"false","csrf_token":"%s"}`, csrf)),
		hx("target", "#quarantine-preview"),
		hx("swap", "innerHTML"),
		csrfInput(csrf),
		h.Div(
			h.Class("overflow-x-auto"),
			h.Table(
				h.Class("min-w-full text-sm"),
				h.THead(h.Tr(
					h.Class("text-left text-slate-400 border-b border-slate-800"),
					th(""), th("Reason"), th("Size"), th("Path"), th(""),
				)),
				h.TBody(g.Group(rows)),
			),
		),
		renderCapNote(len(shown), total),
		// When capped, tell the user the tail is still actionable via "Quarantine all".
		g.If(capped, h.P(h.Class("mt-1 text-xs text-slate-500"),
			g.Text(`Use "Quarantine all" to act on every candidate.`))),
		h.Div(
			h.Class("mt-3 flex flex-wrap items-center gap-2"),
			civButton("light", "md", []g.Node{
				h.Type("submit"),
				h.StyleAttr(tokenVars("warning")),
			}, g.Text("Preview quarantine (selected)")),
			g.Group(allBtns),
		),
	)
}

// quarantineAllButtons renders one "Quarantine all <count> <reason>" button per
// candidate reason present in the full (uncapped) cands set. Each button posts to
// the cap-immune reason path (quarantineIDs → ListCandidates(reason)), so it acts
// on EVERY candidate of that reason even when the render cap hid the tail. It
// mirrors the per-row Quarantine button (type=button, warning color, its own htmx
// request targeting #quarantine-preview) so it never submits the checkbox form.
func quarantineAllButtons(cands []store.LocalFile, csrf string) []g.Node {
	counts := map[string]int{}
	for _, c := range cands {
		counts[c.CandidateReason]++
	}
	var btns []g.Node
	for _, rl := range []struct{ reason, label string }{
		{store.CandidateDuplicate, "duplicates"},
		{store.CandidateSuperseded, "superseded"},
		{store.CandidateBroken, "broken"},
	} {
		n := counts[rl.reason]
		if n == 0 {
			continue
		}
		btns = append(btns, civButton("subtle", "sm", []g.Node{
			h.Type("button"),
			hx("post", "/library/quarantine"),
			hx("vals", fmt.Sprintf(`{"reason":"%s","apply":"false","csrf_token":"%s"}`, rl.reason, csrf)),
			hx("target", "#quarantine-preview"),
			hx("swap", "innerHTML"),
			h.StyleAttr(tokenVars("warning")),
		}, g.Text(fmt.Sprintf("Quarantine all %s %s", humanCount(n), rl.label))))
	}
	return btns
}

// quarantinePreview renders the dry-run plan with a confirm-apply button, or the
// applied result.
func quarantinePreview(plan *library.QuarantinePlan, ids []int64, csrf string) g.Node {
	if len(plan.Moves) == 0 && len(plan.Skipped) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Nothing to quarantine."))
	}
	var moveRows []g.Node
	for _, m := range plan.Moves {
		tag := m.Reason
		if m.IsSidecar {
			tag = "sidecar"
		}
		moveRows = append(moveRows, h.Li(
			h.Class("text-xs text-slate-300"),
			g.Text(tag+": "+m.OriginalPath),
		))
	}
	var skipRows []g.Node
	for _, sk := range plan.Skipped {
		skipRows = append(skipRows, h.Li(
			h.Class("text-xs text-rose-300"),
			g.Text("skipped "+sk.Path+": "+sk.Reason),
		))
	}

	if plan.Applied {
		return alert("success",
			fmt.Sprintf("Quarantined %d file(s) (%s) as batch #%d. Restore from the Trash page.",
				len(plan.Moves), humanBytes(plan.TotalBytes), plan.BatchID),
			h.Ul(h.Class("mt-2 space-y-1"), g.Group(moveRows)),
			h.Ul(h.Class("mt-2 space-y-1"), g.Group(skipRows)),
			h.Div(h.Class("mt-3"),
				civButton("outline", "sm", []g.Node{
					h.Type("button"),
					hx("get", "/library"),
					hx("target", "body"),
					hx("swap", "outerHTML"),
				}, g.Text("Refresh library")),
			),
		)
	}

	// Dry-run: show plan + confirm-apply button carrying the same ids.
	var idVals string
	for i, id := range ids {
		if i > 0 {
			idVals += ","
		}
		idVals += strconv.FormatInt(id, 10)
	}
	return alert("warning",
		fmt.Sprintf("Dry-run: would move %d file(s) (%s). Confirm to move them to the trash dir (reversible).",
			len(plan.Moves), humanBytes(plan.TotalBytes)),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(moveRows)),
		h.Ul(h.Class("mt-2 space-y-1"), g.Group(skipRows)),
		g.If(len(plan.Moves) > 0,
			h.Div(h.Class("mt-3"),
				civButton("filled", "md", []g.Node{
					h.Type("button"),
					hx("post", "/library/quarantine"),
					hx("vals", fmt.Sprintf(`{"ids":"%s","apply":"true","csrf_token":"%s"}`, idVals, csrf)),
					hx("target", "#quarantine-preview"),
					hx("swap", "innerHTML"),
					hx("confirm", "Move these files to the trash dir?"),
					h.StyleAttr(tokenVarsFilled("warning")),
				}, g.Text("Confirm quarantine")),
			),
		),
	)
}

// trashPage lists quarantine batches with restore controls.
func trashPage(batches []batchView, csrf, theme string, mr maturityRange, rail ...railData) g.Node {
	return page("Trash", theme, csrf, mr, railOf(rail),
		card(
			pageTitle("Quarantine trash"), // the page's single <h1>
			h.Div(h.ID("trash-content"), trashTable(batches, csrf)),
		),
	)
}

type batchView struct {
	Batch store.QuarantineBatch
	Files int
}

func trashTable(batches []batchView, csrf string) g.Node {
	if len(batches) == 0 {
		return emptyState(
			"Nothing in the trash",
			"Quarantining a model file moves it here instead of deleting it, so every "+
				"batch stays restorable to its original location. Flag duplicate, "+
				"superseded or broken files from the Model files tab and they will show up here.",
			"/library?tab=files", "Review model files")
	}
	var rows []g.Node
	for _, bv := range batches {
		b := bv.Batch
		id := strconv.FormatInt(b.ID, 10)
		var action g.Node
		if b.Restored() {
			action = badge("restored", "green")
		} else {
			action = civButton("subtle", "sm", []g.Node{
				h.Type("button"),
				hx("post", "/trash/"+id+"/restore"),
				hx("vals", fmt.Sprintf(`{"csrf_token":"%s"}`, csrf)),
				hx("target", "#trash-content"),
				hx("swap", "innerHTML"),
				hx("confirm", "Restore batch #"+id+" to its original locations?"),
				h.StyleAttr(tokenVars("success")),
			}, g.Text("Restore"))
		}
		rows = append(rows, h.Tr(
			h.ID("batch-"+id),
			h.Class("border-b border-slate-800/60"),
			h.Td(h.Class("px-3 py-2 text-slate-300"), g.Text("#"+id)),
			h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(humanTime(b.CreatedAt))),
			h.Td(h.Class("px-3 py-2"), g.If(b.Reason != "", badge(b.Reason, "amber"))),
			h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(strconv.Itoa(bv.Files))),
			h.Td(h.Class("px-3 py-2 text-right"), action),
		))
	}
	return h.Div(
		h.Class("overflow-x-auto"),
		h.Table(
			h.Class("min-w-full text-sm"),
			h.THead(h.Tr(
				h.Class("text-left text-slate-400 border-b border-slate-800"),
				th("Batch"), th("Created"), th("Reason"), th("Files"), th(""),
			)),
			h.TBody(g.Group(rows)),
		),
	)
}

// --- badges & grouping ---

func statusBadge(f store.LocalFile) g.Node {
	if f.IsCandidate() {
		return candidateBadge(f.CandidateReason)
	}
	switch f.Status {
	case store.LocalStatusMatched:
		return badge("matched", "green")
	case store.LocalStatusUnmatchedPending:
		return badge("pending", "blue")
	case store.LocalStatusBroken:
		return badge("broken", "red")
	default:
		return badge("unmatched", "slate")
	}
}

func candidateBadge(reason string) g.Node {
	switch reason {
	case store.CandidateDuplicate:
		return badge("duplicate", "blue")
	case store.CandidateBroken:
		return badge("broken", "amber")
	default:
		return badge("superseded", "amber")
	}
}

type fileGroup struct {
	modelID int
	files   []store.LocalFile
}

func groupFilesByModel(files []store.LocalFile) []fileGroup {
	byID := map[int]*fileGroup{}
	var order []int
	for _, f := range files {
		id := 0
		if f.ModelID != nil {
			id = *f.ModelID
		}
		gr, ok := byID[id]
		if !ok {
			gr = &fileGroup{modelID: id}
			byID[id] = gr
			order = append(order, id)
		}
		gr.files = append(gr.files, f)
	}
	sort.Ints(order)
	out := make([]fileGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	return out
}

func modelLabel(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

func versionLabel(p *int) string {
	if p == nil {
		return "—"
	}
	return "v" + strconv.Itoa(*p)
}
