package web

import (
	"fmt"
	"strconv"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// dashboardPage is the full dashboard: subscriptions, activity feed, and queue.
func dashboardPage(subs []store.Subscription) g.Node {
	return page("Dashboard",
		card(
			sectionTitle("Add a subscription"),
			subscribeForm(),
		),
		card(
			sectionTitle("Subscriptions"),
			subscriptionsTable(subs, ""),
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
	)
}

func subscribeForm() g.Node {
	return h.Form(
		hx("post", "/subscribe"),
		hx("target", "#subscriptions-table"),
		hx("swap", "outerHTML"),
		h.Class("flex flex-wrap items-end gap-3"),
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

func labeledInput(name, label, placeholder string, required bool) g.Node {
	inputAttrs := []g.Node{
		h.Type("text"),
		h.Name(name),
		h.Placeholder(placeholder),
		h.Class("rounded-md border border-slate-700 bg-slate-800 px-3 py-1.5 text-sm text-slate-100 placeholder-slate-500 focus:border-indigo-500 focus:outline-none w-80"),
	}
	if required {
		inputAttrs = append(inputAttrs, g.Attr("required"))
	}
	return h.Div(
		h.Class("flex flex-col gap-1"),
		h.Label(h.Class("text-xs text-slate-400"), g.Text(label)),
		h.Input(inputAttrs...),
	)
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

func subscriptionsTable(subs []store.Subscription, errMsg string) g.Node {
	var rows []g.Node
	if len(subs) == 0 {
		rows = append(rows, h.Tr(
			h.Td(h.ColSpan("6"), h.Class("px-3 py-4 text-center text-slate-500"),
				g.Text("No subscriptions yet. Add one above or from the Search page.")),
		))
	} else {
		for _, s := range subs {
			rows = append(rows, subscriptionRow(s))
		}
	}
	return h.Div(
		h.ID("subscriptions-table"),
		h.Class("overflow-x-auto"),
		g.If(errMsg != "",
			h.Div(h.Class("mb-3 rounded-md border border-rose-800 bg-rose-950 px-3 py-2 text-sm text-rose-200"),
				g.Text(errMsg)),
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

func subscriptionRow(s store.Subscription) g.Node {
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
			flagToggle(s, "auto_download", "auto", s.AutoDownload),
			flagToggle(s, "notify_only", "notify", s.NotifyOnly),
		),
		h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(humanDuration(s.PollInterval()))),
		h.Td(h.Class("px-3 py-2 text-slate-400"), g.Text(last)),
		h.Td(h.Class("px-3 py-2 text-right"),
			h.Button(
				hx("post", "/subscriptions/"+strconv.FormatInt(s.ID, 10)+"/delete"),
				hx("target", "#sub-"+strconv.FormatInt(s.ID, 10)),
				hx("swap", "outerHTML"),
				hx("confirm", "Unsubscribe from "+target+"?"),
				h.Class("text-xs text-rose-400 hover:text-rose-300"),
				g.Text("Unsubscribe"),
			),
		),
	)
}

// flagToggle renders a pill that POSTs the flipped flag set and swaps the row.
func flagToggle(s store.Subscription, field, label string, on bool) g.Node {
	newAuto := s.AutoDownload
	newNotify := s.NotifyOnly
	switch field {
	case "auto_download":
		newAuto = !s.AutoDownload
	case "notify_only":
		newNotify = !s.NotifyOnly
	}
	variant := "off"
	cls := "cursor-pointer rounded-full px-2 py-0.5 text-xs font-medium bg-slate-700 text-slate-400 hover:bg-slate-600"
	if on {
		variant = "on"
		cls = "cursor-pointer rounded-full px-2 py-0.5 text-xs font-medium bg-emerald-800 text-emerald-200 hover:bg-emerald-700"
	}
	_ = variant
	vals := fmt.Sprintf(`{"auto_download":"%t","notify_only":"%t"}`, newAuto, newNotify)
	return h.Button(
		hx("post", "/subscriptions/"+strconv.FormatInt(s.ID, 10)+"/flags"),
		hx("vals", vals),
		hx("target", "#sub-"+strconv.FormatInt(s.ID, 10)),
		hx("swap", "outerHTML"),
		h.Class(cls),
		g.Text(label),
	)
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
			return h.Li(
				h.Class("rounded-md border border-slate-800 p-2"),
				h.Div(
					h.Class("flex items-center justify-between gap-2"),
					h.Span(h.Class("truncate text-slate-200"), g.Text(it.FileName)),
					queueStatusBadge(it.Status),
				),
				progressBar(it),
				g.If(it.LastError != "" && it.Status == store.StatusFailed,
					h.Div(h.Class("mt-1 text-xs text-rose-400"), g.Text(it.LastError)),
				),
			)
		}),
	)
}

func queueStatusBadge(st store.QueueStatus) g.Node {
	switch st {
	case store.StatusDone:
		return badge("done", "green")
	case store.StatusDownloading:
		return badge("downloading", "indigo")
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
func searchPage(query string, res *civitai.ModelSearchResult, baseURL string) g.Node {
	return page("Search",
		card(
			sectionTitle("Search models"),
			h.Form(
				h.Class("flex items-end gap-3"),
				hx("get", "/search"),
				hx("target", "#search-results"),
				hx("swap", "innerHTML"),
				hx("trigger", "submit"),
				h.Div(
					h.Class("flex flex-col gap-1 flex-1"),
					h.Input(
						h.Type("text"), h.Name("q"), h.Value(query),
						h.Placeholder("Search by name, tag, …"),
						h.Class("w-full rounded-md border border-slate-700 bg-slate-800 px-3 py-1.5 text-sm text-slate-100 placeholder-slate-500 focus:border-indigo-500 focus:outline-none"),
					),
				),
				btnPrimary(g.Text("Search")),
			),
		),
		h.Div(h.ID("search-results"), searchResults(res, baseURL)),
	)
}

// searchResults renders the result grid fragment (used by htmx swaps too).
func searchResults(res *civitai.ModelSearchResult, baseURL string) g.Node {
	if res == nil {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("Enter a query to search CivitAI."))
	}
	if len(res.Items) == 0 {
		return h.P(h.Class("text-sm text-slate-500"), g.Text("No results."))
	}
	return h.Div(
		h.Class("grid gap-4 sm:grid-cols-2 lg:grid-cols-3"),
		g.Map(res.Items, func(it civitai.ModelListItem) g.Node {
			return modelCard(it)
		}),
	)
}

func modelCard(it civitai.ModelListItem) g.Node {
	creator := ""
	if it.Creator != nil {
		creator = it.Creator.Username
	}
	return h.Div(
		h.Class("rounded-lg border border-slate-800 bg-slate-900 p-4 flex flex-col gap-2"),
		h.A(
			h.Href("/models/"+strconv.Itoa(it.ID)),
			h.Class("font-medium text-indigo-400 hover:underline"),
			g.Text(it.Name),
		),
		h.Div(
			h.Class("flex items-center gap-2 text-xs text-slate-400"),
			badge(it.Type, "indigo"),
			g.If(it.NSFW, badge("NSFW", "red")),
			g.If(creator != "", h.A(h.Href("/creators/"+creator), h.Class("hover:underline"), g.Text("@"+creator))),
		),
		h.Div(
			h.Class("text-xs text-slate-500"),
			g.Text(fmt.Sprintf("%d downloads · %d likes", it.Stats.DownloadCount, it.Stats.ThumbsUpCount)),
		),
	)
}

// modelDetailPage renders a model's versions with a subscribe button.
func modelDetailPage(m *civitai.ModelDetail) g.Node {
	creator := ""
	if m.Creator != nil {
		creator = m.Creator.Username
	}
	return page(m.Name,
		card(
			h.Div(
				h.Class("flex items-start justify-between gap-4"),
				h.Div(
					h.H1(h.Class("text-xl font-semibold"), g.Text(m.Name)),
					h.Div(
						h.Class("mt-1 flex items-center gap-2 text-sm text-slate-400"),
						badge(m.Type, "indigo"),
						g.If(m.NSFW, badge("NSFW", "red")),
						g.If(creator != "", h.A(h.Href("/creators/"+creator), h.Class("hover:underline"), g.Text("@"+creator))),
					),
				),
				subscribeInline("model", strconv.Itoa(m.ID), "Subscribe"),
			),
		),
		card(
			sectionTitle("Versions"),
			h.Ul(
				h.Class("divide-y divide-slate-800 text-sm"),
				g.Map(m.ModelVersions, func(v civitai.ModelVersionSummary) g.Node {
					return h.Li(
						h.Class("flex items-center justify-between py-2"),
						h.Span(h.Class("text-slate-200"), g.Text(v.Name)),
						g.If(v.BaseModel != "", badge(v.BaseModel, "blue")),
					)
				}),
			),
		),
	)
}

// creatorPage renders a creator's models with a subscribe-to-creator button.
func creatorPage(username string, res *civitai.ModelSearchResult) g.Node {
	return page("@"+username,
		card(
			h.Div(
				h.Class("flex items-center justify-between"),
				h.H1(h.Class("text-xl font-semibold"), g.Text("@"+username)),
				subscribeInline("creator", username, "Subscribe to creator"),
			),
		),
		card(
			sectionTitle("Models"),
			searchResults(res, ""),
		),
	)
}

// subscribeInline is a small POST-to-/subscribe form button used on detail pages.
func subscribeInline(kind, value, label string) g.Node {
	field := "model"
	if kind == "creator" {
		field = "creator"
	}
	return h.Form(
		hx("post", "/subscribe"),
		hx("swap", "none"),
		h.Input(h.Type("hidden"), h.Name(field), h.Value(value)),
		h.Input(h.Type("hidden"), h.Name("auto_download"), h.Value("true")),
		btnPrimary(g.Text(label)),
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
