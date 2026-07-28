package web

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// librarySubscribeSuggestions derives subscribe suggestions from the local
// library: it groups MATCHED local files (non-nil ModelID) by model id, sums
// their bytes, drops any model already subscribed to, and returns the rest
// ordered by total local bytes desc (model id asc as a stable tiebreak), capped
// to limit (<=0 means no cap). Pure — no civitai API calls.
func librarySubscribeSuggestions(files []store.LocalFile, subs []store.Subscription, limit int) []suggestion {
	subscribed := make(map[int]bool)
	for _, s := range subs {
		if s.Kind == store.KindModel && s.ModelID != nil {
			subscribed[*s.ModelID] = true
		}
	}
	byModel := make(map[int]*suggestion)
	var order []int // first-seen order, to keep the sort deterministic
	for _, f := range files {
		if f.ModelID == nil {
			continue // unmatched file — no model to suggest
		}
		id := *f.ModelID
		if subscribed[id] {
			continue
		}
		sg := byModel[id]
		if sg == nil {
			sg = &suggestion{ModelID: id}
			byModel[id] = sg
			order = append(order, id)
		}
		sg.FileCount++
		sg.TotalBytes += f.SizeBytes
	}
	out := make([]suggestion, 0, len(order))
	for _, id := range order {
		out = append(out, *byModel[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalBytes != out[j].TotalBytes {
			return out[i].TotalBytes > out[j].TotalBytes
		}
		return out[i].ModelID < out[j].ModelID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// dashboardPage is the full dashboard: add-a-subscription (integrated civitai
// search + library-derived suggestions + a demoted manual form), subscriptions,
// activity feed, and queue.
func dashboardPage(subs []store.Subscription, suggestions []suggestion, csrf, theme, nsfwMode string, rail ...railData) g.Node {
	return page("Dashboard", theme, csrf, nsfwMode, railOf(rail),
		card(
			sectionTitle("Add a subscription"),
			// Primary: search civitai and subscribe with one click.
			subscribeSearchBox(csrf),
			// Secondary: the manual model-id/URL form, demoted into a collapsed
			// <details> so it stays functional but out of the way.
			h.Details(
				h.Class("mt-5"),
				h.Summary(
					h.Class("cursor-pointer select-none text-sm text-slate-400 hover:text-slate-200"),
					g.Text("Add by model id / URL"),
				),
				h.Div(h.Class("mt-3"), subscribeForm(csrf)),
			),
		),
		g.If(len(suggestions) > 0, card(
			sectionTitle("Subscribe suggestions from your library"),
			h.P(h.Class("mb-3 text-sm text-slate-400"),
				g.Text("Models you have local files for but are not subscribed to.")),
			suggestionsList(suggestions, csrf),
		)),
		card(
			sectionTitle("Subscriptions"),
			subscriptionsTable(subs, "", csrf),
		),
		h.Div(
			h.Class("grid gap-6 md:grid-cols-2"),
			card(
				sectionTitle("Download queue"),
				h.Div(
					h.ID("queue"),
					hx("get", "/fragments/queue"),
					hx("trigger", "load, every 5s"),
					hx("swap", "innerHTML"),
					g.Text("Loading…"),
				),
			),
			card(
				sectionTitle("Activity"),
				h.Div(
					h.ID("events"),
					hx("get", "/fragments/events"),
					hx("trigger", "load, every 10s"),
					hx("swap", "innerHTML"),
					g.Text("Loading…"),
				),
			),
		),
		// The subscribe search cards carry showcase carousels → share the lightbox.
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// subscribeSearchBox renders the dashboard's integrated civitai search: a query
// box that GETs /subscribe/search and swaps subscribe-enabled result cards into
// its results container.
func subscribeSearchBox(csrf string) g.Node {
	return h.Div(
		h.Form(
			h.Class("flex items-end gap-3"),
			hx("get", "/subscribe/search"),
			hx("target", "#subscribe-results"),
			hx("swap", "innerHTML"),
			hx("trigger", "submit"),
			h.Div(
				h.Class("flex-1"),
				textInput("text-input", "subscribe-q", "Search civitai to subscribe",
					h.Type("text"), h.Name("q"),
					h.Placeholder("Search by name, tag, …")),
			),
			btnPrimary(g.Text("Search")),
		),
		h.Div(h.ID("subscribe-results"), h.Class("mt-4")),
	)
}

// subscribeSearchResults renders the dashboard subscribe-search result grid:
// image model cards, each with a one-click auto-download Subscribe button.
func subscribeSearchResults(res *civitai.ModelSearchResult, subs map[int]*store.Subscription, mode, csrf string) g.Node {
	if res == nil {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Search to find models to subscribe to."))
	}
	if len(res.Items) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No results."))
	}
	images := parseSearchImages(res.Raw)
	updated := newestVersionInfoByModel(res.Raw)
	return h.Div(
		h.Class("cm-cardgrid"),
		g.Map(res.Items, func(it civitai.ModelListItem) g.Node {
			return modelCardWith(it, images[it.ID], subs, mode, csrf, updated[it.ID])
		}),
	)
}

// suggestion is one library-derived subscribe suggestion: a matched model id the
// user has local files for (but is not subscribed to), with its aggregate local
// footprint.
type suggestion struct {
	ModelID    int
	FileCount  int
	TotalBytes int64
	// Name is the model's display title when resolvable from the local
	// model_cache (filled by the dashboard handler, zero civitai calls). Empty on
	// a cache miss, in which case the card lazily fetches the title.
	Name string
}

// suggestionsList renders the library-derived subscribe suggestions, each with a
// one-click auto-download Subscribe button. The card title shows the real model
// name when it was resolved from the model_cache (sg.Name); otherwise it renders
// a lazy title container that fetches the name on load (cache-first, see
// handleModelTitle) so the dashboard never blocks on civitai to render.
func suggestionsList(suggestions []suggestion, csrf string) g.Node {
	return h.Div(
		h.Class("cm-cardgrid cm-cardgrid-tight"),
		g.Map(suggestions, func(sg suggestion) g.Node {
			return card(
				h.Class("flex flex-col gap-2"),
				// Lazy showcase carousel at the TOP of the card (above the title),
				// mirroring where the carousel sits on search cards. It loads on
				// demand (cache-first) so the dashboard render never blocks on civitai.
				cardImagesLazy(sg.ModelID),
				h.Div(
					h.Class("flex items-center justify-between gap-3"),
					h.Div(
						h.Class("min-w-0"),
						suggestionTitle(sg),
						h.Div(h.Class("text-xs text-slate-500"),
							g.Text(fmt.Sprintf("%d file(s) · %s", sg.FileCount, humanBytes(sg.TotalBytes)))),
					),
					subscribeInline("model", strconv.Itoa(sg.ModelID), "Subscribe", csrf),
				),
				// Lazy version-status pill on its OWN ROW below the title/footprint
				// (cache-first on load): a violet "new version" button + hover popover
				// when a remote update exists; collapses to nothing when up to date.
				versionStatusLazy(sg.ModelID),
			)
		}),
	)
}

// suggestionTitle renders the suggestion's linked title. When the name was
// resolved (from model_cache) it renders it directly; on a cache miss it renders
// a lazy container that htmx-fetches the title on load (GET /models/{id}/title),
// showing "Loading…" until it resolves — so the dashboard render stays cache-only.
func suggestionTitle(sg suggestion) g.Node {
	href := "/models/" + strconv.Itoa(sg.ModelID)
	linkClass := "font-medium text-indigo-300 hover:text-indigo-200"
	if sg.Name != "" {
		return h.A(h.Href(href), h.Class(linkClass), g.Text(sg.Name))
	}
	return h.A(
		h.Href(href), h.Class(linkClass),
		hx("get", fmt.Sprintf("/models/%d/title", sg.ModelID)),
		hx("trigger", "load"),
		hx("swap", "innerHTML"),
		g.Text("Loading…"),
	)
}

// cardImagesLazy is the STABLE own container a suggestion card renders at its top;
// it lazy-loads the showcase carousel (hx-get on load, one-shot innerHTML swap —
// not a poll, so the self-replace rule does not apply) so the dashboard render
// never blocks on civitai. The endpoint (cache-first) returns either the carousel
// or an EMPTY fragment (no images / offline). The .cm-card-images-lazy class hides
// the container while it is empty (before load AND when the fragment is empty), so
// a card with no showcase images reserves no vertical space.
func cardImagesLazy(modelID int) g.Node {
	return h.Div(
		h.ID(fmt.Sprintf("card-images-%d", modelID)),
		h.Class("cm-card-images-lazy"),
		hx("get", fmt.Sprintf("/models/%d/card-images", modelID)),
		hx("trigger", "load"),
		hx("swap", "innerHTML"),
	)
}

func subscribeForm(csrf string) g.Node {
	return h.Form(
		hx("post", "/subscribe"),
		hx("target", "#subscriptions-table"),
		hx("swap", "outerHTML"),
		h.Class("flex flex-wrap items-end gap-3"),
		csrfInput(csrf),
		labeledInput("model", "Model id or civitai.com/models/… URL", "e.g. 12345", true),
		h.Div(
			h.Class("flex items-center gap-3"),
			checkbox("auto_download", "Auto-download", true),
			checkbox("notify_only", "Notify only", false),
			checkbox("backfill_latest", "Backfill latest", false),
		),
		btnPrimary(g.Text("Subscribe")),
	)
}

// labeledInput renders a civitai text-input (label + control) sized for inline
// form rows.
func labeledInput(name, label, placeholder string, required bool) g.Node {
	ctrl := []g.Node{h.Type("text"), h.Name(name), h.Placeholder(placeholder)}
	if required {
		ctrl = append(ctrl, g.Attr("required"))
	}
	return h.Div(h.Class("w-80"), textInput("text-input", "f-"+name, label, ctrl...))
}

func checkbox(name, label string, checked bool) g.Node {
	attrs := []g.Node{
		h.Type("checkbox"),
		h.Name(name),
		h.Value("true"),
		h.Class("rounded border-slate-600 bg-slate-800 text-indigo-500"),
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

// csrfInput renders the hidden CSRF field embedded in every state-changing form.
func csrfInput(csrf string) g.Node {
	return h.Input(h.Type("hidden"), h.Name("csrf_token"), h.Value(csrf))
}

func subscriptionsTable(subs []store.Subscription, errMsg, csrf string) g.Node {
	var rows []g.Node
	if len(subs) == 0 {
		rows = append(rows, h.Tr(
			h.Td(h.ColSpan("6"), h.Class("px-3 py-4 text-center text-slate-500"),
				g.Text("No subscriptions yet. Add one above or from the Search page.")),
		))
	} else {
		for _, s := range subs {
			rows = append(rows, subscriptionRow(s, csrf))
		}
	}
	return h.Div(
		h.ID("subscriptions-table"),
		h.Class("overflow-x-auto"),
		g.If(errMsg != "",
			h.Div(h.Class("mb-3"), alert("error", "", g.Text(errMsg))),
		),
		h.Table(
			h.Class("min-w-full text-sm"),
			h.THead(
				h.Tr(
					h.Class("text-left text-slate-400 border-b border-slate-800"),
					th("Target"), th("Kind"), th("Flags"), th("Interval"), th("Last polled"), th(""),
				),
			),
			h.TBody(g.Group(rows)),
		),
	)
}

func th(text string) g.Node {
	return h.Th(h.Class("px-3 py-2 font-medium"), g.Text(text))
}

func subscriptionRow(s store.Subscription, csrf string) g.Node {
	target := s.Label()
	var targetNode g.Node = g.Text(target)
	if s.Kind == store.KindModel && s.ModelID != nil {
		targetNode = h.A(h.Href("/models/"+strconv.Itoa(*s.ModelID)),
			h.Class("text-indigo-400 hover:underline"), g.Text(target))
	} else if s.Kind == store.KindCreator {
		targetNode = h.A(h.Href("/creators/"+s.Username),
			h.Class("text-indigo-400 hover:underline"), g.Text(target))
	}

	last := "never"
	if s.LastPolledAt != nil && !s.LastPolledAt.IsZero() {
		last = humanTime(*s.LastPolledAt)
	}

	return h.Tr(
		h.ID("sub-"+strconv.FormatInt(s.ID, 10)),
		h.Class("border-b border-slate-800/60"),
		h.Td(h.Class("px-3 py-2"), targetNode),
		h.Td(h.Class("px-3 py-2"), g.Text(string(s.Kind))),
		h.Td(h.Class("px-3 py-2 space-x-1"),
			flagToggle(s, "auto_download", "auto", s.AutoDownload, csrf),
			flagToggle(s, "notify_only", "notify", s.NotifyOnly, csrf),
		),
		h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(humanDuration(s.PollInterval()))),
		h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(last)),
		h.Td(h.Class("px-3 py-2 text-right"),
			civButton("subtle", "sm", []g.Node{
				h.Type("button"),
				hx("post", "/subscriptions/"+strconv.FormatInt(s.ID, 10)+"/delete"),
				hx("vals", fmt.Sprintf(`{"csrf_token":"%s"}`, csrf)),
				hx("target", "#sub-"+strconv.FormatInt(s.ID, 10)),
				hx("swap", "outerHTML"),
				hx("confirm", "Unsubscribe from "+target+"?"),
				h.StyleAttr("--civitai-color-primary:var(--civitai-color-error)"),
			}, g.Text("Unsubscribe")),
		),
	)
}

// flagToggle renders a pill that POSTs the flipped flag set and swaps the row.
func flagToggle(s store.Subscription, field, label string, on bool, csrf string) g.Node {
	newAuto := s.AutoDownload
	newNotify := s.NotifyOnly
	switch field {
	case "auto_download":
		newAuto = !s.AutoDownload
	case "notify_only":
		newNotify = !s.NotifyOnly
	}
	// "on" tints the pill with the success token; "off" with the dimmed-text
	// token (the documented per-element --civitai-color-primary override).
	tok := "text-dimmed"
	if on {
		tok = "success"
	}
	vals := fmt.Sprintf(`{"auto_download":"%t","notify_only":"%t","csrf_token":"%s"}`, newAuto, newNotify, csrf)
	return civButton("light", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/subscriptions/"+strconv.FormatInt(s.ID, 10)+"/flags"),
		hx("vals", vals),
		hx("target", "#sub-"+strconv.FormatInt(s.ID, 10)),
		hx("swap", "outerHTML"),
		h.StyleAttr("--civitai-color-primary:var(--civitai-color-" + tok + ")"),
	}, g.Text(label))
}

// eventsFragment renders the recent activity list.
func eventsFragment(events []store.Event) g.Node {
	if len(events) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No activity yet."))
	}
	return h.Ul(
		h.Class("space-y-2 text-sm"),
		g.Map(events, func(ev store.Event) g.Node {
			return h.Li(
				h.Class("flex items-start gap-2"),
				levelBadge(ev.Level),
				h.Div(
					h.Span(h.Class("text-slate-200"), g.Text(ev.Message)),
					h.Div(h.Class("text-xs text-slate-500"), g.Text(humanTime(ev.TS))),
				),
			)
		}),
	)
}

func levelBadge(level string) g.Node {
	switch level {
	case store.LevelError:
		return badge("error", "red")
	case store.LevelWarn:
		return badge("warn", "amber")
	default:
		return badge("info", "blue")
	}
}

// queueFragment renders the download queue rows.
func queueFragment(items []store.QueueItem) g.Node {
	if len(items) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Queue is empty."))
	}
	return h.Ul(
		h.Class("space-y-2 text-sm"),
		g.Map(items, func(it store.QueueItem) g.Node {
			scheduled := isScheduled(it)
			// g.If evaluates its node argument eagerly, so build the NotBefore-
			// dependent note only when NotBefore is non-nil.
			statusNode := queueStatusBadge(it.Status)
			var waitNote g.Node = g.Text("")
			if scheduled {
				statusNode = badge("scheduled", "amber")
				waitNote = h.Div(h.Class("mt-1 text-xs text-slate-500"),
					g.Text("Waiting until "+it.NotBefore.Local().Format("15:04")+" (anti-stampede)"))
			}
			return h.Li(
				h.Class("rounded-md border border-slate-800 p-2"),
				h.Div(
					h.Class("flex items-center justify-between gap-2"),
					h.Span(h.Class("truncate text-slate-200"), g.Text(it.FileName)),
					statusNode,
				),
				progressBar(it),
				waitNote,
				g.If(it.LastError != "" && it.Status == store.StatusFailed,
					h.Div(h.Class("mt-1 text-xs text-rose-400"), g.Text(it.LastError)),
				),
			)
		}),
	)
}

// isScheduled reports whether a queued row is waiting on its anti-stampede
// not_before gate (so the UI shows "scheduled" rather than a stuck "queued").
func isScheduled(it store.QueueItem) bool {
	return it.Status == store.StatusQueued && it.NotBefore != nil && it.NotBefore.After(time.Now())
}

func queueStatusBadge(st store.QueueStatus) g.Node {
	switch st {
	case store.StatusDone:
		return badge("done", "green")
	case store.StatusDownloading:
		return badge("downloading", "blue")
	case store.StatusFailed:
		return badge("failed", "red")
	case store.StatusSkipped:
		return badge("skipped", "amber")
	default:
		return badge("queued", "blue")
	}
}

func progressBar(it store.QueueItem) g.Node {
	pct := 0
	total := int64(it.SizeKB * 1024)
	if total > 0 {
		pct = int(float64(it.BytesDone) / float64(total) * 100)
		if pct > 100 {
			pct = 100
		}
	}
	if it.Status == store.StatusDone {
		pct = 100
	}
	label := fmt.Sprintf("%s / %s", humanBytes(it.BytesDone), humanKB(it.SizeKB))
	return h.Div(
		h.Class("mt-1"),
		h.Div(
			h.Class("h-1.5 w-full overflow-hidden rounded bg-slate-800"),
			h.Div(
				h.Class("h-full bg-indigo-500"),
				h.StyleAttr("width:"+strconv.Itoa(pct)+"%"),
			),
		),
		h.Div(h.Class("mt-0.5 text-xs text-slate-500"), g.Text(label)),
	)
}

// searchPage renders the model search page. results may be nil (initial load).
// mode is the app's NSFW display mode (hide|blur|show), threaded to the showcase
// carousels on each card. heading, when set, labels the result grid (e.g.
// "Popular this month" for the empty-query default feed).
func searchPage(query string, res *civitai.ModelSearchResult, subs map[int]*store.Subscription, csrf, theme, mode, heading, sortSel, periodSel string, rail ...railData) g.Node {
	return page("Models", theme, csrf, mode, railOf(rail),
		card(
			sectionTitle("Search models"),
			h.Form(
				h.Class("flex flex-wrap items-end gap-3"),
				hx("get", "/search"),
				hx("target", "#search-results"),
				hx("swap", "innerHTML"),
				hx("trigger", "submit"),
				h.Div(
					h.Class("min-w-[12rem] flex-1"),
					textInput("text-input", "search-q", "Query",
						h.Type("text"), h.Name("q"), h.Value(query),
						h.Placeholder("Search by name, tag, …")),
				),
				// Sort + period filter dropdowns (GET params threaded into the civitai
				// query). Their values are the exact civitai query strings.
				labeledSelect("search-sort", "sort", "Sort", searchSortOptions, sortSel),
				labeledSelect("search-period", "period", "Period", searchPeriodOptions, periodSel),
				btnPrimary(g.Text("Search")),
			),
		),
		h.Div(h.ID("search-results"), searchResults(res, subs, mode, csrf, heading)),
		// Showcase carousels reuse the shared lightbox + interaction scripts.
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// searchResults renders the result grid fragment (used by htmx swaps too). mode
// is the NSFW display mode; heading optionally labels the grid. Showcase images
// are parsed MANAGER-SIDE from res.Raw (the typed items carry none).
func searchResults(res *civitai.ModelSearchResult, subs map[int]*store.Subscription, mode, csrf, heading string) g.Node {
	if res == nil {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Enter a query to search CivitAI."))
	}
	if len(res.Items) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No results."))
	}
	images := parseSearchImages(res.Raw)
	// Per-model newest version info (from the SAME raw already parsed for images) →
	// each card's "Updated X ago" line + its hover popover (version name/date).
	updated := newestVersionInfoByModel(res.Raw)
	grid := h.Div(
		h.Class("cm-cardgrid"),
		g.Map(res.Items, func(it civitai.ModelListItem) g.Node {
			return modelCard(it, images[it.ID], subs, mode, csrf, updated[it.ID])
		}),
	)
	if heading == "" {
		return grid
	}
	return h.Div(sectionTitle(heading), grid)
}

// modelCard is a search result card: a showcase-image carousel (NSFW-mode
// respecting) above the name/creator/stats, with a per-card subscribe control.
func modelCard(it civitai.ModelListItem, images []galleryImage, subs map[int]*store.Subscription, mode, csrf string, updated modelUpdateInfo) g.Node {
	return modelCardWith(it, images, subs, mode, csrf, updated)
}

// modelCardWith renders the search card with a per-card subscribe control at the
// bottom, so search-page cards and the dashboard subscribe cards share one layout.
// The control reflects real state from the per-render subs map (subs[it.ID] nil →
// collapsed "Subscribe"); the map is built ONCE per render (one ListSubscriptions
// query), never per card.
func modelCardWith(it civitai.ModelListItem, images []galleryImage, subs map[int]*store.Subscription, mode, csrf string, updated modelUpdateInfo) g.Node {
	return modelCardCore(it, images, mode, updated,
		h.Div(h.Class("mt-1"), subscribeControl(it.ID, subs[it.ID], csrf)))
}

// modelCardCore renders the shared result-card body (showcase carousel, name link
// to the in-app model detail page, type/NSFW/creator row, stats, and the "Updated
// X ago" popover) and appends `action` — the trailing per-card control — when it is
// non-nil. The search cards pass the subscribe control; the browse-only Discover
// cards pass nil so no state-changing control appears. Passing nil yields byte-for-
// byte the same body the search cards render minus that final action div, so the
// two surfaces share one card implementation and cannot drift.
func modelCardCore(it civitai.ModelListItem, images []galleryImage, mode string, updated modelUpdateInfo, action g.Node) g.Node {
	creator := ""
	if it.Creator != nil {
		creator = it.Creator.Username
	}
	children := []g.Node{
		h.Class("flex flex-col gap-2"),
		modelCardCarousel(it.ID, images, mode),
		h.A(
			h.Href("/models/"+strconv.Itoa(it.ID)),
			h.Class("font-medium text-indigo-400 hover:underline"),
			g.Text(it.Name),
		),
		h.Div(
			h.Class("flex items-center gap-2 text-xs text-slate-400"),
			// The redundant "Workflows" chip is dropped on discover-workflow cards;
			// the useful Checkpoint/LORA/etc. badge stays on model-search cards.
			g.If(it.Type != "Workflows", badge(it.Type, "indigo")),
			g.If(it.NSFW, badge("NSFW", "red")),
			g.If(creator != "", h.A(h.Href("/creators/"+creator), h.Class("hover:underline"), g.Text("@"+creator))),
		),
		// Stats as inline icon + count (the words "downloads"/"likes" are gone;
		// "likes" == ThumbsUpCount, hence a thumbs-up glyph, not a heart).
		h.Div(
			h.Class("cm-stats text-xs text-slate-500"),
			statWithIcon(downloadIconSVG, compactCount(it.Stats.DownloadCount), "downloads"),
			statWithIcon(thumbsUpIconSVG, compactCount(it.Stats.ThumbsUpCount), "likes"),
		),
	}
	if action != nil {
		children = append(children, action)
	}
	// "Updated X ago" from the newest version's publish date renders LAST so it sits
	// bottom-left of the card, after the primary action. Keeps its hover/focus
	// popover (absolute date + latest version name/date). Omitted when no parseable
	// date is available.
	children = append(children, g.If(!updated.At.IsZero(), updatedCardLine(
		it.ID, updated.VersionID,
		humanSince(updated.At),
		updated.At.Local().Format("2006-01-02 15:04"),
		updated.Name,
		updated.At.Local().Format("2006-01-02"),
	)))
	return card(children...)
}

// statWithIcon renders one card stat as a small inline SVG icon followed by its
// compact count. The SVG markup is OUR OWN static content (g.Raw), but the count
// is untrusted-shaped and is emitted via g.Text. The label ("downloads"/"likes")
// is exposed to assistive tech via aria-label + a title tooltip since the words
// themselves are no longer shown.
func statWithIcon(iconSVG, count, label string) g.Node {
	return h.Span(
		h.Class("cm-stat"),
		g.Attr("role", "img"),
		g.Attr("aria-label", label),
		h.Title(label),
		g.Raw(iconSVG),
		h.Span(g.Text(count)),
	)
}

// downloadIconSVG / thumbsUpIconSVG are inline (feather-style) glyphs shown in
// place of the words "downloads"/"likes" on result cards. Static, self-contained
// markup (no external refs); sized/aligned via .cm-stat-ico in app.css.
const (
	downloadIconSVG = `<svg class="cm-stat-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"/><polyline points="7 10 12 15 17 10"/><line x1="12" y1="15" x2="12" y2="3"/></svg>`
	thumbsUpIconSVG = `<svg class="cm-stat-ico" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><path d="M14 9V5a3 3 0 0 0-3-3l-4 9v11h11.28a2 2 0 0 0 2-1.7l1.38-9a2 2 0 0 0-2-2.3zM7 22H4a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2h3"/></svg>`
)

// creatorPage renders a creator's models with a subscribe-to-creator button. subs
// is the per-render model-subscription map used by each result card's subscribe
// control (built once by the handler, not per card).
func creatorPage(username string, res *civitai.ModelSearchResult, subs map[int]*store.Subscription, csrf, theme, mode string, rail ...railData) g.Node {
	return page("@"+username, theme, csrf, mode, railOf(rail),
		card(
			h.Div(
				h.Class("flex items-center justify-between"),
				h.H1(h.Class("text-xl font-semibold"), g.Text("@"+username)),
				subscribeInline("creator", username, "Subscribe to creator", csrf),
			),
		),
		card(
			sectionTitle("Models"),
			searchResults(res, subs, mode, csrf, ""),
		),
		lightboxOverlay(),
		modelPageScript(),
		libraryCarouselScript(),
	)
}

// subscribeInline renders the subscribe affordance used on search cards,
// suggestions, and the model/creator pages. For MODEL subscribes it renders the
// shared 3-step subscribe control (Subscribe → options → confirm → feedback). For
// CREATOR subscribes it keeps the one-click POST /subscribe form, adding a
// "Subscribed ✓" success note shown after a successful request.
func subscribeInline(kind, value, label, csrf string) g.Node {
	if kind == "creator" {
		return subscribeCreatorInline(value, label, csrf)
	}
	id, _ := strconv.Atoi(value)
	return subscribeControl(id, nil, csrf)
}

// subscribeCreatorInline is the one-click creator subscribe: POST /subscribe with
// auto-download on, plus a success note revealed via htmx's after-request event
// (no extra endpoint, minimal change from the prior one-click behavior).
func subscribeCreatorInline(username, label, csrf string) g.Node {
	return h.Form(
		hx("post", "/subscribe"),
		hx("swap", "none"),
		g.Attr("hx-on::after-request",
			"if(event.detail.successful){this.querySelector('[data-sub-note]').classList.remove('hidden');}"),
		h.Class("flex items-center gap-2"),
		csrfInput(csrf),
		h.Input(h.Type("hidden"), h.Name("creator"), h.Value(username)),
		h.Input(h.Type("hidden"), h.Name("auto_download"), h.Value("true")),
		btnPrimary(g.Text(label)),
		h.Span(g.Attr("data-sub-note", ""), h.Class("hidden text-sm font-medium text-green-500"),
			g.Text("Subscribed ✓")),
	)
}

// --- formatting helpers ---

func humanTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

// humanSince renders a coarse relative age for any past time — "just now",
// "Xm/Xh ago", then "X days/weeks/months/years ago" beyond 24h (unlike humanTime,
// which switches to an absolute timestamp past 24h). Zero → "never"; a future
// time (negative age) reads as "just now". Buckets use fixed day/week/month/year
// spans (24h / 7d / 30d / 365d) — an approximate "X ago", not a calendar diff.
func humanSince(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	const (
		day   = 24 * time.Hour
		week  = 7 * day
		month = 30 * day
		year  = 365 * day
	)
	d := time.Since(t)
	unit := func(n int, name string) string {
		if n == 1 {
			return fmt.Sprintf("1 %s ago", name)
		}
		return fmt.Sprintf("%d %ss ago", n, name)
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < day:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < week:
		return unit(int(d/day), "day")
	case d < month:
		return unit(int(d/week), "week")
	case d < year:
		return unit(int(d/month), "month")
	default:
		return unit(int(d/year), "year")
	}
}

func humanDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	if d >= time.Hour {
		return fmt.Sprintf("%gh", d.Hours())
	}
	return fmt.Sprintf("%gm", d.Minutes())
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func humanKB(kb float64) string {
	return humanBytes(int64(kb * 1024))
}
