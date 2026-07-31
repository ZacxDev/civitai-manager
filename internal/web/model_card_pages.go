package web

import (
	"fmt"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// matchedModelCardView bundles everything the enriched (lazy-loaded) matched-model card
// renders. It is assembled by buildMatchedModelCardView from a cached/fetched
// ModelDetail plus the model's local files.
type matchedModelCardView struct {
	ModelID    int
	Name       string
	Creator    string // civitai creator username ("" when unknown), linked to /creators/{username}
	Type       string
	BaseModel  string
	Versions   int
	FileCount  int
	TotalBytes int64
	Images     []galleryImage
	Maturity   maturityRange

	// Version breakdown (Item C). Local is the versions the user actually has
	// (grouped from local files); Available is the model's version list (civitai
	// newest-first) marked with in-library / newer flags. UpdateAvailable is true
	// when the latest available version is NOT in the library, in which case
	// Latest{ID,Name} name it.
	Local           []localVersionGroup
	Available       []availableVersion
	UpdateAvailable bool
	LatestID        int
	LatestName      string

	// Subscription state for this model (Subscribed=false, SubscriptionID=0 when
	// not subscribed).
	Subscribed     bool
	SubscriptionID int64
}

// localVersionGroup is one version the user has locally: its resolved name, the
// total size of its local files, and how many files it comprises.
type localVersionGroup struct {
	VersionID int
	Name      string
	Bytes     int64
	FileCount int
}

// availableVersion is one of the model's civitai versions, tagged with whether
// the user already has it locally (InLibrary) and whether it is newer than the
// user's newest local version in list order (Newer).
type availableVersion struct {
	ID        int
	Name      string
	InLibrary bool
	Newer     bool
}

// versionBreakdown is the pure result of cross-referencing a model's available
// versions against the user's local files. It is computed by buildVersionBreakdown
// and folded into matchedModelCardView.
type versionBreakdown struct {
	Local           []localVersionGroup
	TotalBytes      int64
	Available       []availableVersion
	UpdateAvailable bool
	LatestID        int
	LatestName      string
}

// buildVersionBreakdown groups the model's local files by version and cross-references
// them against the model's available versions.
//
// Newness assumption: the civitai model-detail version summary carries NO timestamp,
// so we rely on civitai's list order, which is NEWEST-FIRST — i.e. ModelVersions[0]
// is the latest. A version is "newer" than the user's holdings if it sits ABOVE
// (lower index than) the user's newest local version in that list. When the user has
// no version that appears in the list, every listed version is treated as newer.
func buildVersionBreakdown(versions []civitai.ModelVersionSummary, files []store.LocalFile) versionBreakdown {
	var b versionBreakdown

	nameByID := make(map[int]string, len(versions))
	indexByID := make(map[int]int, len(versions))
	for i, ver := range versions {
		nameByID[ver.ID] = ver.Name
		indexByID[ver.ID] = i
	}

	// Group local files by version id (nil version id → 0, an "unknown version").
	type agg struct {
		bytes int64
		count int
	}
	byVer := map[int]*agg{}
	var seen []int // first-seen order of local version ids
	for _, f := range files {
		vid := 0
		if f.VersionID != nil {
			vid = *f.VersionID
		}
		a := byVer[vid]
		if a == nil {
			a = &agg{}
			byVer[vid] = a
			seen = append(seen, vid)
		}
		a.bytes += f.SizeBytes
		a.count++
		b.TotalBytes += f.SizeBytes
	}
	localSet := make(map[int]bool, len(byVer))
	for vid := range byVer {
		localSet[vid] = true
	}

	// Emit local groups: known versions in list (newest-first) order first, then any
	// unknown local version ids in first-seen order, for a deterministic display.
	emitted := map[int]bool{}
	add := func(vid int) {
		if emitted[vid] {
			return
		}
		emitted[vid] = true
		name := nameByID[vid]
		if name == "" {
			if vid == 0 {
				name = "Unknown version"
			} else {
				name = fmt.Sprintf("Version #%d", vid)
			}
		}
		a := byVer[vid]
		b.Local = append(b.Local, localVersionGroup{
			VersionID: vid, Name: name, Bytes: a.bytes, FileCount: a.count,
		})
	}
	for _, ver := range versions {
		if localSet[ver.ID] {
			add(ver.ID)
		}
	}
	for _, vid := range seen {
		add(vid)
	}

	// The user's newest local version index (versions are newest-first, so the FIRST
	// in-library one is the newest). len(versions) when they have none from the list.
	newestLocalIndex := len(versions)
	for i, ver := range versions {
		if localSet[ver.ID] {
			newestLocalIndex = i
			break
		}
	}

	b.Available = make([]availableVersion, 0, len(versions))
	for i, ver := range versions {
		av := availableVersion{ID: ver.ID, Name: ver.Name, InLibrary: localSet[ver.ID]}
		av.Newer = !av.InLibrary && i < newestLocalIndex
		b.Available = append(b.Available, av)
	}

	if len(versions) > 0 {
		b.LatestID = versions[0].ID
		b.LatestName = versions[0].Name
		b.UpdateAvailable = !localSet[versions[0].ID]
	}
	return b
}

// buildMatchedModelCardView assembles a matchedModelCardView from the model detail (typed +
// raw), the model's local files, and the persisted NSFW display mode. Showcase
// images are sourced from the model raw JSON's inline images[] via
// parseVersionImages — the SAME inline-image path the model page uses, never a
// separate /api/v1/images call.
func buildMatchedModelCardView(id int, m *civitai.ModelDetail, raw []byte, files []store.LocalFile, mr maturityRange, sub *store.Subscription) matchedModelCardView {
	v := matchedModelCardView{ModelID: id, Maturity: mr}
	for _, f := range files {
		v.TotalBytes += f.SizeBytes
	}
	v.FileCount = len(files)
	var versions []civitai.ModelVersionSummary
	if m != nil {
		v.Name = m.Name
		v.Type = m.Type
		if m.Creator != nil {
			v.Creator = m.Creator.Username
		}
		versions = m.ModelVersions
		v.Versions = len(versions)
		if len(versions) > 0 {
			v.BaseModel = versions[0].BaseModel
		}
	}
	// Version breakdown: which versions you have (grouped), which are available, and
	// whether the latest is newer than your newest local version.
	bd := buildVersionBreakdown(versions, files)
	v.Local = bd.Local
	v.Available = bd.Available
	v.UpdateAvailable = bd.UpdateAvailable
	v.LatestID = bd.LatestID
	v.LatestName = bd.LatestName
	// Subscription state.
	if sub != nil {
		v.Subscribed = true
		v.SubscriptionID = sub.ID
	}
	// versionID 0 → the first listed version's inline images.
	v.Images = parseVersionImages(nil, raw, 0)
	return v
}

// versionStatusLazy is the STABLE own-row container a suggestion card renders
// BELOW its title/footprint; it lazy-loads the version-status badge (hx-get on
// load) so the dashboard render never blocks on civitai. The endpoint
// (cache-first) returns either the "new version" pill + popover or an EMPTY
// fragment (up to date). The .cm-vstatus-lazy class hides the container while it
// is empty (before load AND when the fragment is empty), so an up-to-date card
// shows no stray badge and no empty gap in the grid.
func versionStatusLazy(modelID int) g.Node {
	return h.Div(
		h.ID(fmt.Sprintf("version-status-%d", modelID)),
		h.Class("cm-vstatus-lazy"),
		hx("get", fmt.Sprintf("/models/%d/version-status", modelID)),
		hx("trigger", "load"),
		hx("swap", "innerHTML"),
	)
}

// versionStatusFragment renders the suggestion's version-status: when an update is
// available (the latest remote version is not in the library) it renders a small
// violet "new version" badge with a CSS hover/focus popover naming the user's
// newest local version → the latest available version (+ its publish date when
// known). When up to date it renders NOTHING (g.Text("")) to keep the grid clean.
// All model/version names + dates are untrusted civitai strings — g.Text escapes
// every one. raw is the cached GetModel body (for the publish date); it may be nil.
func versionStatusFragment(modelID int, bd versionBreakdown, raw []byte) g.Node {
	if !bd.UpdateAvailable || bd.LatestID == 0 {
		return g.Text("") // up to date (or unknown) → render nothing
	}
	latest := bd.LatestName
	if latest == "" {
		latest = fmt.Sprintf("Version #%d", bd.LatestID)
	}
	// Both publish dates come from the SAME cached raw GetModel body — zero extra
	// calls. Each is appended inline ("· Published YYYY-MM-DD") only when present,
	// and omitted gracefully when the version isn't in the model's version list
	// (versionPublishedDate → ""). All names/dates are untrusted → g.Text escapes.
	haveLine := "You have: none in library"
	if len(bd.Local) > 0 {
		haveLine = "You have: " + bd.Local[0].Name // Local is newest-first (list order)
		if d := versionPublishedDate(raw, bd.Local[0].VersionID); d != "" {
			haveLine += " · Published " + d
		}
	}
	// The latest-version name is a deeplink to that version's detail page
	// (/models/{id}?version={LatestID}); only the title is inside the link, the
	// "· Published {date}" suffix follows outside it. onclick stops propagation so
	// the click navigates even inside the JS-hover-controlled popover. The name is
	// untrusted → g.Text escapes it.
	latestChildren := []g.Node{
		h.A(
			h.Href(fmt.Sprintf("/models/%d?version=%d", modelID, bd.LatestID)),
			h.Class("text-indigo-300 hover:text-indigo-200"),
			g.Attr("onclick", "event.stopPropagation()"),
			g.Text("Latest: "+latest),
		),
	}
	if d := versionPublishedDate(raw, bd.LatestID); d != "" {
		latestChildren = append(latestChildren, g.Text(" · Published "+d))
	}

	pop := []g.Node{
		h.Div(h.Class("cm-vstatus-title"), g.Text("Update available")),
		h.Div(g.Text(haveLine)),
		h.Div(g.Group(latestChildren)),
	}

	return h.Span(
		h.Class("cm-vstatus"),
		g.Attr("tabindex", "0"),
		// Accessible summary for AT / non-hover users (escaped).
		g.Attr("aria-label", "Update available: latest version "+latest),
		g.Attr("title", "Update available: "+latest),
		h.Span(h.Class("cm-vstatus-badge"), g.Text("⟳ new version")),
		h.Span(h.Class("cm-vstatus-pop"), g.Attr("role", "tooltip"), g.Group(pop)),
	)
}

// modelCardLazy is the placeholder container rendered IMMEDIATELY in the results
// view for one matched model: it shows what is already known (model id, file
// count, total size) and lazy-loads the enriched card (name + carousel +
// details) via htmx (hx-get load), replacing itself (outerHTML) with the
// server-rendered modelCard. The browser naturally throttles the concurrent
// lazy loads.
func modelCardLazy(gr fileGroup, name string) g.Node {
	id := gr.modelID
	var total int64
	for _, f := range gr.files {
		total += f.SizeBytes
	}
	// Show the cached name immediately when we have it; otherwise the "Model #id"
	// placeholder, which the lazy full-card load below then replaces with the name.
	label := name
	if label == "" {
		label = "Model #" + strconv.Itoa(id)
	}
	return card(
		h.ID(fmt.Sprintf("model-card-%d", id)),
		hx("get", fmt.Sprintf("/library/model-card/%d", id)),
		hx("trigger", "load"),
		hx("swap", "outerHTML"),
		h.Class("space-y-3"),
		h.Div(
			h.Class("flex items-center justify-between gap-3"),
			h.A(
				h.Href("/models/"+strconv.Itoa(id)),
				h.Class("text-base font-semibold text-indigo-300 hover:text-indigo-200"),
				g.Text(label),
			),
			sizeText(total),
		),
		h.Div(
			h.Class("flex items-center gap-2 text-xs text-slate-500"),
			spinnerGlyph(),
			g.Text(fmt.Sprintf("Loading details… %d file(s)", len(gr.files))),
		),
	)
}

// modelCard is the enriched matched-model card served by handleModelCard: the
// model name (linked to its page), type + base-model badges, a showcase-image
// carousel (NSFW-respecting), and key details (versions, local file count,
// total size).
func matchedModelCard(v matchedModelCardView, csrf string) g.Node {
	name := v.Name
	if name == "" {
		name = "Model #" + strconv.Itoa(v.ModelID)
	}
	return card(
		h.ID(fmt.Sprintf("model-card-%d", v.ModelID)),
		h.Class("space-y-3"),
		h.Div(
			h.Class("flex items-start justify-between gap-3"),
			h.Div(
				h.A(
					h.Href("/models/"+strconv.Itoa(v.ModelID)),
					h.Class("text-base font-semibold text-indigo-300 hover:text-indigo-200"),
					g.Text(name),
				),
				g.If(v.Creator != "", h.A(
					h.Href("/creators/"+v.Creator),
					h.Class("mt-0.5 block text-xs text-slate-400 hover:text-slate-200"),
					g.Text("@"+v.Creator),
				)),
				h.Div(
					h.Class("mt-1 flex flex-wrap items-center gap-1.5"),
					g.If(v.Type != "", badge(v.Type, "indigo")),
					g.If(v.BaseModel != "", badge(v.BaseModel, "blue")),
				),
			),
			sizeText(v.TotalBytes),
		),
		modelCardCarousel(v.ModelID, v.Images, v.Maturity),
		versionBreakdownSection(v, csrf),
	)
}

// versionBreakdownSection renders the matched card's version story: an
// update-available banner when the latest version is not local, an expandable
// list of the versions the user has (with sizes), an expandable list of the
// model's available versions marked in-library/newer, and the subscribe toggle.
func versionBreakdownSection(v matchedModelCardView, csrf string) g.Node {
	var parts []g.Node

	// 1. Prominent "update available" banner → the in-app model page at that version.
	if v.UpdateAvailable && v.LatestID > 0 {
		parts = append(parts, updateAvailableCTA(v))
	}

	// 2. Versions you have (expandable), with a total-size line and size-by-version.
	parts = append(parts, h.Details(
		h.Class("rounded-md border border-slate-800 px-3 py-2 text-xs text-slate-400"),
		h.Summary(
			h.Class("cursor-pointer select-none font-medium text-slate-300"),
			g.Text(fmt.Sprintf("Versions in your library (%d)", len(v.Local))),
		),
		h.Div(
			h.Class("mt-2 space-y-1"),
			statInline("Total size", humanBytes(v.TotalBytes)+" · "+strconv.Itoa(v.FileCount)+" file(s)"),
			g.Group(localVersionRows(v.Local)),
		),
	))

	// 3. Available versions (expandable), each marked in-library / newer.
	if len(v.Available) > 0 {
		parts = append(parts, h.Details(
			h.Class("rounded-md border border-slate-800 px-3 py-2 text-xs text-slate-400"),
			h.Summary(
				h.Class("cursor-pointer select-none font-medium text-slate-300"),
				g.Text(fmt.Sprintf("Available versions (%d)", len(v.Available))),
			),
			h.Div(h.Class("mt-2 space-y-1"), g.Group(availableVersionRows(v.ModelID, v.Available))),
		))
	}

	// 4. Subscribe / unsubscribe toggle.
	parts = append(parts, h.Div(h.Class("pt-1"), subscribeToggle(v.ModelID, subFromView(v), csrf)))

	return h.Div(h.Class("space-y-2"), g.Group(parts))
}

// updateAvailableCTA is the matched card's "Update available" control: a custom
// SVG glyph, the latest version's name, and the DETAILS in a hover/focus popover
// instead of inline.
//
// COLOUR — 🔴 the split-token invariant. It used to be `.cm-fix-cta`, the WARNING
// (amber) tint shared with the run-failure "Fix" CTA. Amber is the app's "something
// is wrong" colour and an available update is not a fault; the card now paints
// `.cm-upd-cta`, whose foreground is `--civitai-color-success-text` — the `-text`
// half of the split pair, NEVER the bare `--civitai-color-success` fill token. The
// base token is the tint under it; reaching for it to colour TEXT reintroduces
// exactly the WCAG failures v0.1.79 fixed. The pair is pinned in
// contrast_web_test.go's uiPairs() table, and TestUpdateCTAUsesTheTextToken parses
// the shipped rule so a swap back to the fill token fails the build.
//
// STRUCTURE — the popover wrapper is a <div>, not the <a> itself. `.cm-updated` has
// to be the positioned wrapper of `.cm-updated-pop`, and making the anchor the
// wrapper would nest the popover's content inside a link. `:focus-within` on the
// wrapper is what opens the popover when the anchor is tabbed to, so the wrapper
// needs no tabindex of its own.
func updateAvailableCTA(v matchedModelCardView) g.Node {
	label := v.LatestName
	if label == "" {
		label = fmt.Sprintf("Version #%d", v.LatestID)
	}
	have := "none of its versions are in your library"
	if len(v.Local) > 0 {
		have = v.Local[0].Name // Local is newest-first (civitai list order)
	}
	return h.Div(
		h.Class("cm-updated block"),
		h.A(
			h.Href(fmt.Sprintf("/models/%d?version=%d", v.ModelID, v.LatestID)),
			h.Class("cm-upd-cta flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium"),
			g.Raw(updateAvailableIconSVG),
			// The version name is untrusted civitai data — g.Text escapes it. truncate +
			// min-w-0 because it is a flex ITEM printing an unbounded string.
			h.Span(h.Class("truncate min-w-0"), g.Text("Update available: "+label)),
		),
		h.Span(
			h.Class("cm-updated-pop"),
			g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text("Update available")),
			// break-all: a civitai version name is untrusted and can be one unbroken
			// 88-char token, which would otherwise widen the popover past its max-width.
			h.Div(h.Class("break-all"), g.Text("You have: "+have)),
			h.Div(h.Class("break-all"), g.Text("Latest: "+label)),
			h.Div(h.Class("cm-updated-date"),
				g.Text(fmt.Sprintf("%d version(s) published · %d in your library",
					len(v.Available), len(v.Local)))),
		),
	)
}

// subFromView reconstructs the minimal *store.Subscription the toggle needs from
// the view's subscription state (nil when not subscribed).
func subFromView(v matchedModelCardView) *store.Subscription {
	if !v.Subscribed {
		return nil
	}
	return &store.Subscription{ID: v.SubscriptionID, Kind: store.KindModel, ModelID: &v.ModelID}
}

// localVersionRows renders one row per local version: name, size, file count.
func localVersionRows(groups []localVersionGroup) []g.Node {
	if len(groups) == 0 {
		return []g.Node{h.Div(h.Class("text-slate-500"), g.Text("No local versions."))}
	}
	rows := make([]g.Node, 0, len(groups))
	for _, gr := range groups {
		rows = append(rows, h.Div(
			h.Class("flex items-center justify-between gap-2"),
			h.Span(h.Class("truncate min-w-0 text-slate-300"), g.Text(gr.Name)),
			h.Span(h.Class("shrink-0 text-slate-500"),
				g.Text(humanBytes(gr.Bytes)+" · "+strconv.Itoa(gr.FileCount)+" file(s)")),
		))
	}
	return rows
}

// availableVersionRows renders one row per available version, marking each as
// "in library" or "newer" (the latter linked to the in-app model page at that
// version). Version names are escaped via g.Text (untrusted civitai strings).
func availableVersionRows(modelID int, avs []availableVersion) []g.Node {
	rows := make([]g.Node, 0, len(avs))
	for _, av := range avs {
		var mark g.Node
		switch {
		case av.InLibrary:
			mark = badge("in library", "green")
		case av.Newer:
			mark = h.A(
				h.Href(fmt.Sprintf("/models/%d?version=%d", modelID, av.ID)),
				h.Class("text-indigo-300 hover:text-indigo-200"),
				g.Text("newer →"),
			)
		default:
			mark = h.Span(h.Class("cm-disabled text-slate-500"), g.Text("—"))
		}
		rows = append(rows, h.Div(
			h.Class("flex items-center justify-between gap-2"),
			h.Span(h.Class("truncate min-w-0 text-slate-300"), g.Text(av.Name)),
			h.Span(h.Class("shrink-0"), mark),
		))
	}
	return rows
}

// subscribeControlID is the stable container id every state of the reusable
// model-subscribe control shares, so each htmx fragment can outerHTML-swap the
// whole control by targeting #subscribe-control-{id}.
func subscribeControlID(modelID int) string {
	return fmt.Sprintf("subscribe-control-%d", modelID)
}

// subscribeControl renders the reusable 3-state model-subscribe control in its
// CURRENT state: the subscribed feedback (with an Unsubscribe button) when the
// model is subscribed, otherwise the collapsed "Subscribe" button that opens the
// options panel. All three states (collapsed / options / subscribed) share the
// #subscribe-control-{id} container and swap it via outerHTML, so the flow is:
// Subscribe → options (auto-download vs notify-only + Confirm/Cancel) →
// subscribed feedback → Unsubscribe → collapsed. Used for MODEL subscribes on
// the matched card, search cards, suggestions, and the model page.
//
// `workflow` says the model is a CivitAI Workflows post (civitai.IsWorkflowPost
// on whichever shape the caller holds — ModelDetail.Type on the model page,
// ModelListItem.Type on a search card, which is all a card has). It is NOT a
// cosmetic flag: for a workflow post the control becomes NOTIFY-ONLY end to end,
// because the poller permanently refuses to download such a post (its Archive
// .zip lands outside library.DefaultExtensions and is never scanned, counted,
// deduped or quarantined). Offering an Auto-download choice whose only possible
// outcome is a skip event is exactly the affordance that shipped the bug.
func subscribeControl(modelID int, sub *store.Subscription, csrf string, workflow bool) g.Node {
	if sub != nil {
		return subscribeControlSubscribed(modelID, sub, csrf, "", workflow)
	}
	return subscribeControlCollapsed(modelID, csrf, "", workflow)
}

// subscribeToggle is the matched-card entry point (kept for its callers); it
// renders the shared subscribe control.
//
// It passes workflow=false unconditionally, and that is CORRECT rather than a
// gap: a matched card exists because the library scanner matched a LOCAL FILE to
// this model, and a workflow post's only artifact is a `.zip`, which is not in
// library.DefaultExtensions and is therefore never scanned into the library at
// all. A workflow post cannot reach this surface.
func subscribeToggle(modelID int, sub *store.Subscription, csrf string) g.Node {
	return subscribeControl(modelID, sub, csrf, false)
}

// subscribeControlCollapsed is state 1: the "Subscribe" button that GETs the
// options panel into the shared container, plus an info affordance explaining what
// subscribing actually does. note, when non-empty, appends a small status line
// (e.g. "Unsubscribed") beneath it.
//
// SIZE: `md`, not `sm`. Subscribing is the primary standing action on a matched
// model card and it was the smallest control there, losing to the two <details>
// disclosures above it. The size is raised HERE, in the shared control, rather than
// forked per surface — one Subscribe button should not be two different sizes on
// the library card and the search card.
// For a WORKFLOW POST the button says "Notify me", not "Subscribe": the only
// thing this control can do for such a model is notify (see subscribeControl),
// and a button labelled identically to the one that downloads a checkpoint is
// the lie this fix exists to remove. The `?workflow=1` on the options request is
// belt to the handler's braces — the handler re-derives the flag from the model
// cache, and the parameter only ever NARROWS the panel to notify-only, so a
// forged one cannot grant anything.
func subscribeControlCollapsed(modelID int, csrf, note string, workflow bool) g.Node {
	id := subscribeControlID(modelID)
	label, aria := "Subscribe", "Subscribe to this model"
	if workflow {
		label, aria = "Notify me", "Get notified about new versions of this workflow post"
	}
	btn := civButton("outline", "md",
		[]g.Node{
			h.Type("button"),
			hx("get", subscribeOptionsPath(modelID, workflow)),
			hx("target", "#"+id),
			hx("swap", "outerHTML"),
			g.Attr("aria-label", aria),
		},
		g.Text(label),
	)
	return h.Div(h.ID(id), h.Class("flex flex-col items-start gap-1"),
		h.Div(h.Class("flex items-center gap-2"), btn, subscribeInfoPopover(workflow)),
		subscribeNote(note))
}

// subscribeOptionsPath / subscribeControlPath build the two GET endpoints the
// control swaps from, carrying the workflow flag so the panel the user lands on
// (and the collapsed control they Cancel back to) keeps the right shape.
func subscribeOptionsPath(modelID int, workflow bool) string {
	return fmt.Sprintf("/models/%d/subscribe-options%s", modelID, workflowQuery(workflow))
}

func subscribeControlPath(modelID int, workflow bool) string {
	return fmt.Sprintf("/models/%d/subscribe-control%s", modelID, workflowQuery(workflow))
}

// workflowQuery is the `?workflow=1` suffix (empty when false). One helper so the
// parameter name cannot drift between the emitters and workflowParam, which reads
// it back.
func workflowQuery(workflow bool) string {
	if workflow {
		return "?" + workflowParamName + "=1"
	}
	return ""
}

// workflowParamName is the query/form key carrying "this model is a Workflows
// post" across an htmx swap. It is a HINT, never an authority: every handler ORs
// it with a server-side check, and it can only narrow the control to notify-only.
const workflowParamName = "workflow"

// subscribeInfoPopover is the ⓘ beside the Subscribe button: what subscribing does,
// stated plainly BEFORE the click, because the default mode DOWNLOADS FILES.
//
// The consequence is the point. "Subscribe" reads like a notification opt-in, but
// the default (subscribeOptionsPanel pre-checks Auto-download) makes the poller
// enqueue and fetch every new version into the model folder — multi-GB checkpoints,
// unattended, with no second confirmation. Naming that here is the difference
// between an informed click and a surprise.
//
// It reuses the SAME popover mechanism as the status chips — a `.cm-updated`
// wrapper with a `.cm-updated-pop` child — so it inherits the shared hover
// controller (delegation matters: this control is htmx-swapped), the grace period
// and the CSS-only fallback. role="img" + aria-label give the icon-only trigger an
// accessible name; with role="img" the aria-label IS the name, so there is nothing
// left for a `title=` to add and adding one would stack the native tooltip on top
// of this popover (see updatedPopBody / popover_no_title_web_test.go).
// For a WORKFLOW POST the copy is different because the FACTS are different:
// there is no auto-download to warn about, so warning about one would be the
// same lie as the button label. It says instead what actually happens and names
// Import as the action that puts the workflows somewhere useful.
func subscribeInfoPopover(workflow bool) g.Node {
	title := "What subscribing does"
	body := []g.Node{
		h.Div(g.Text("civitai-manager watches this model for new versions.")),
		h.Div(g.Text("With Auto-download (the default) each new version is queued and " +
			"downloaded to your model folder automatically — using disk space and " +
			"bandwidth without asking again.")),
		h.Div(h.Class("cm-updated-date"),
			g.Text("Choose Notify only to hear about a new version without downloading it.")),
	}
	if workflow {
		title = "What this does"
		body = []g.Node{
			h.Div(g.Text("civitai-manager watches this workflow post for new versions and " +
				"tells you when one appears.")),
			h.Div(g.Text("It never downloads it: a workflow post is a zip of ComfyUI graphs, " +
				"not model weights, so nothing here would land in your model library.")),
			// Deliberately NOT the literal CTA label "Import workflows": that string
			// is what TestWorkflowsModelPageDetectsImport uses to assert the import
			// CTA is not re-offered once a model's workflows are already in the
			// library, and this popover renders on that page.
			h.Div(h.Class("cm-updated-date"),
				g.Text("To get the graphs themselves, import them from the model page.")),
		}
	}
	return h.Span(
		h.Class("cm-updated cm-subinfo"),
		g.Attr("role", "img"),
		g.Attr("tabindex", "0"),
		g.Attr("aria-label", title),
		g.Raw(infoIconSVG),
		h.Span(
			h.Class("cm-updated-pop"),
			g.Attr("role", "tooltip"),
			h.Div(h.Class("cm-updated-title"), g.Text(title)),
			g.Group(body),
		),
	)
}

// subscribeOptionsPanel is state 2: a form (carrying the shared container id) with
// the Auto-download (default) vs Notify-only radio group, a "Subscribe to <name>?"
// heading, a Confirm submit (POST /models/{id}/subscribe, CSRF via the hidden
// field) and a Cancel (GET /models/{id}/subscribe-control → back to collapsed).
//
// For a WORKFLOW POST there is NO radio group. The Auto-download option would
// resolve to a permanent poller skip, so the panel states the one thing that
// will happen and posts a hidden mode=notify_only. Removing the choice (rather
// than pre-selecting Notify only) is deliberate: a disabled-looking radio still
// reads as "this is a mode you could pick".
func subscribeOptionsPanel(modelID int, name, csrf string, workflow bool) g.Node {
	id := subscribeControlID(modelID)
	radio := func(value, label string, checked bool) g.Node {
		attrs := []g.Node{
			h.Type("radio"), h.Name("mode"), h.Value(value),
			h.Class("text-indigo-500"),
		}
		if checked {
			attrs = append(attrs, g.Attr("checked"))
		}
		return h.Label(
			h.Class("flex items-center gap-1.5 text-sm text-slate-300"),
			h.Input(attrs...),
			g.Text(label),
		)
	}
	heading := "Subscribe to " + name + "?"
	// The mode chooser, or — for a workflow post — the single honest sentence that
	// replaces it, plus the hidden mode the form posts instead of a radio.
	modes := []g.Node{
		h.Div(h.Class("flex flex-col gap-1"),
			radio("auto_download", "Auto-download", true),
			radio("notify_only", "Notify only", false),
		),
	}
	if workflow {
		heading = "Notify me about " + name + "?"
		modes = []g.Node{
			h.Input(h.Type("hidden"), h.Name("mode"), h.Value("notify_only")),
			h.Input(h.Type("hidden"), h.Name(workflowParamName), h.Value("1")),
			h.P(h.Class("text-xs text-slate-400"),
				g.Text("Workflow posts are imported, not downloaded — this only tells you "+
					"when a new version appears. To add the graphs to your workflow "+
					"library, import them from the model page.")),
		}
	}
	return h.Form(
		h.ID(id),
		h.Class("flex flex-col gap-2 rounded-md border border-slate-700 bg-slate-900/40 p-3"),
		hx("post", fmt.Sprintf("/models/%d/subscribe", modelID)),
		hx("target", "#"+id),
		hx("swap", "outerHTML"),
		csrfInput(csrf),
		// name is untrusted civitai data — g.Text auto-escapes it.
		h.P(h.Class("text-sm font-medium text-slate-200"), g.Text(heading)),
		g.Group(modes),
		h.Div(h.Class("flex items-center gap-2"),
			civButton("filled", "sm", []g.Node{h.Type("submit")}, g.Text("Confirm")),
			civButton("subtle", "sm", []g.Node{
				h.Type("button"),
				hx("get", subscribeControlPath(modelID, workflow)),
				hx("target", "#"+id),
				hx("swap", "outerHTML"),
			}, g.Text("Cancel")),
		),
	)
}

// subscribeControlSubscribed is state 3: the "Subscribed ✓ · <mode>" feedback with
// an Unsubscribe button (POST /models/{id}/unsubscribe, CSRF via hx-vals). note,
// when non-empty, appends a status line.
// The workflow flag rides along in the Unsubscribe hx-vals so the collapsed
// control the user lands back on keeps its "Notify me" shape instead of
// reverting to a plain "Subscribe" for one render.
func subscribeControlSubscribed(modelID int, sub *store.Subscription, csrf, note string, workflow bool) g.Node {
	id := subscribeControlID(modelID)
	mode := "auto-download"
	if sub != nil && sub.NotifyOnly {
		mode = "notify only"
	}
	vals := fmt.Sprintf(`{"csrf_token":%q}`, csrf)
	if workflow {
		vals = fmt.Sprintf(`{"csrf_token":%q,%q:"1"}`, csrf, workflowParamName)
	}
	unsub := civButton("subtle", "sm",
		[]g.Node{
			h.Type("button"),
			hx("post", fmt.Sprintf("/models/%d/unsubscribe", modelID)),
			hx("vals", vals),
			hx("target", "#"+id),
			hx("swap", "outerHTML"),
			g.Attr("aria-label", "Unsubscribe from this model"),
		},
		g.Text("Unsubscribe"),
	)
	return h.Div(h.ID(id), h.Class("flex flex-col items-start gap-1"),
		h.Div(h.Class("flex items-center gap-2"),
			// text-emerald-400, NOT text-green-500: tailwind.config.js REPLACES
			// theme.colors, so no `green` scale exists and text-green-500 was never
			// emitted. emerald maps to --civitai-color-success in both themes.
			h.Span(h.Class("text-sm font-medium text-emerald-400"), g.Text("Subscribed ✓ · "+mode)),
			unsub,
		),
		subscribeNote(note),
	)
}

// subscribeNote renders a small status line for the subscribe control (or nothing
// when msg is empty).
func subscribeNote(msg string) g.Node {
	if msg == "" {
		return nil
	}
	return h.Span(h.Class("text-xs text-slate-500"), g.Text(msg))
}

// modelCardError renders a graceful fallback card when the model detail could
// not be fetched (and no cache entry exists): the file count/size still show,
// with a muted note, so the results view degrades rather than erroring.
func modelCardError(id, fileCount int, total int64, msg string) g.Node {
	return card(
		h.ID(fmt.Sprintf("model-card-%d", id)),
		h.Class("space-y-2"),
		h.Div(
			h.Class("flex items-center justify-between gap-3"),
			h.A(
				h.Href("/models/"+strconv.Itoa(id)),
				h.Class("text-base font-semibold text-indigo-300 hover:text-indigo-200"),
				g.Text("Model #"+strconv.Itoa(id)),
			),
			sizeText(total),
		),
		h.P(h.Class("text-xs text-amber-400"), g.Text(msg)),
		h.Div(
			h.Class("flex flex-wrap gap-x-4 text-xs text-slate-400"),
			statInline("Local files", strconv.Itoa(fileCount)),
			statInline("Size", humanBytes(total)),
		),
	)
}

// modelCardCarousel renders the model's showcase images as a horizontal
// scroll-snap carousel, honoring the app-wide maturity RANGE exactly as the model
// page does: an image whose level falls outside the band is OMITTED — its URL is
// never emitted — and everything inside renders plain. Each tile reuses galleryTile
// (and thus the shared lightbox on the results page) with a per-model-namespaced
// meta id so multiple carousels don't collide.
func modelCardCarousel(modelID int, images []galleryImage, mr maturityRange) g.Node {
	return modelCardCarouselW(modelID, images, mr, thumbnailWidth)
}

// modelCardCarouselW is modelCardCarousel with an explicit tile thumbnail width.
// The shared search/library card carousel calls modelCardCarousel (450px); the
// model DETAIL showcase calls this with detailThumbnailWidth so its enlarged
// tiles stay crisp — the markup is otherwise identical (same .cm-carousel strip,
// NSFW handling, and lightbox), so the card carousel is unaffected.
// A model itself is NEVER omitted by the range. A model's own `nsfwLevel` is a
// BITMASK UNION of the levels of its images (measured live: 31 = 1|2|4|8|16,
// 60 = 4|8|16|32), not one comparable level, so there is nothing to compare a
// band against; the filtering happens per IMAGE, which does carry a single level.
// A model whose every showcase image falls outside the band renders the honest
// "No showcase images." line rather than vanishing from the results.
func modelCardCarouselW(modelID int, images []galleryImage, mr maturityRange, tileWidth int) g.Node {
	var tiles []g.Node
	shown := 0
	for i, im := range images {
		if !mr.containsBrowsingLevel(im.NSFWLevel) {
			continue // outside the maturity range — the URL never reaches the DOM
		}
		tiles = append(tiles, h.Div(
			h.Class("cm-carousel-item"),
			galleryTileW(im, fmt.Sprintf("cm-meta-m%d-%d", modelID, i), tileWidth),
		))
		shown++
	}
	if shown == 0 {
		return h.P(h.Class("text-xs text-slate-500"), g.Text("No showcase images."))
	}
	strip := h.Div(h.Class("cm-carousel"), g.Group(tiles))
	if shown <= 1 {
		return h.Div(h.Class("cm-carousel-wrap"), strip)
	}
	return h.Div(
		h.Class("cm-carousel-wrap"),
		strip,
		carouselButton("prev", "‹"),
		carouselButton("next", "›"),
	)
}

// cardCarousel renders arbitrary CARDS in the same horizontal scroll-snap
// carousel the image strips use — same .cm-carousel-wrap, same .cm-carousel
// strip, same .cm-carousel-btn controls, same cmCarouselScroll helper. Only the
// per-item box differs (.cm-carousel-card, a fixed-WIDTH box, vs
// .cm-carousel-item, a fixed-HEIGHT image tile); see the CSS block in app.css.
//
// It follows modelCardCarouselW's two rules exactly: nothing at all for an empty
// input (never an empty strip or a stray wrapper), and the prev/next buttons
// only once there is something to scroll TO.
//
// The caller is responsible for emitting libraryCarouselScript() once on the
// page — every page that already renders an image carousel does.
func cardCarousel(cards []g.Node) g.Node {
	if len(cards) == 0 {
		return nil
	}
	items := make([]g.Node, 0, len(cards))
	for _, c := range cards {
		items = append(items, h.Div(h.Class("cm-carousel-card"), c))
	}
	strip := h.Div(h.Class("cm-carousel cm-carousel-cards"), g.Group(items))
	if len(cards) <= 1 {
		return h.Div(h.Class("cm-carousel-wrap"), strip)
	}
	return h.Div(
		h.Class("cm-carousel-wrap"),
		strip,
		carouselButton("prev", "‹"),
		carouselButton("next", "›"),
	)
}

// carouselButton renders a prev/next scroll control for the carousel; the tiny
// cmCarouselScroll helper (libraryCarouselScript) scrolls the sibling strip.
func carouselButton(dir, glyph string) g.Node {
	delta := "-1"
	cls := "cm-carousel-btn cm-carousel-btn-prev"
	aria := "Scroll to previous images"
	if dir == "next" {
		delta = "1"
		cls = "cm-carousel-btn cm-carousel-btn-next"
		aria = "Scroll to next images"
	}
	return h.Button(
		h.Type("button"),
		h.Class(cls),
		g.Attr("aria-label", aria),
		g.Attr("onclick", "cmCarouselScroll(this,"+delta+")"),
		g.Text(glyph),
	)
}

// libraryCarouselScript is the tiny, self-contained (no CDN) prev/next scroller
// for the model-card carousels. Scrolling/snapping itself is CSS-only
// (.cm-carousel); this only wires the optional buttons. Defined idempotently so
// it survives every htmx swap of the results fragment.
func libraryCarouselScript() g.Node {
	const js = `
function cmCarouselScroll(btn, dir){
  var wrap = btn.closest('.cm-carousel-wrap');
  if(!wrap){ return; }
  var strip = wrap.querySelector('.cm-carousel');
  if(!strip){ return; }
  strip.scrollBy({ left: dir * strip.clientWidth * 0.8, behavior: 'smooth' });
}
`
	return h.Script(g.Raw(js))
}
