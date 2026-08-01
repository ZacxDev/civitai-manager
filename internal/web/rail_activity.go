package web

import (
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ---------------------------------------------------------------------------
// The rail's "Recent activity" widget.
//
// It answers one question — "what has this app been doing?" — across the four
// things it does on the user's behalf: downloading files, watching subscriptions,
// importing workflows, and running them.
//
// 🔴 IT IS BUILT FROM TWO BOUNDED READS AND NO OTHERS. The rail is app chrome
// rendered on EVERY page, so every query behind it must be a fixed, bounded read
// — the same constraint that shaped railFetchLimit. The two sources are:
//
//   - the generation rows the OUTPUTS widget already fetched (zero extra cost),
//     which is where workflow runs come from;
//   - store.RecentEvents(railActivityFetchLimit), which is LIMIT-ed in SQL.
//
// store.ListQueue was deliberately NOT used even though "downloads" is one of the
// categories: it takes no limit and returns the WHOLE download_queue table, so
// putting it here would add an unbounded per-render scan to every page in the app.
// Downloads reach this feed through the events table instead — the queue worker
// writes an event per transition (internal/queue/queue.go), so the information is
// already there, already bounded.
//
// HONEST LIMIT ON COVERAGE: this widget reports what the events table actually
// contains. Today that is the poller's and the download worker's kinds (seed,
// new_version, enqueued, enqueue_error, poll_error, skip_existing, size_skip,
// type_skip) plus locally-captured runs. Workflow IMPORT does not currently write
// an event, so imports appear only once some code path emits one — at which point
// they show up here with no change to this file, because the default arm renders
// any unknown kind from its own stored message. That is a gap in the event
// producers, not in this widget, and it is recorded rather than papered over.
// ---------------------------------------------------------------------------

// railActivityLimit is how many ENTRIES the activity widget shows after
// coalescing. Small on purpose: this is a glanceable column, not the full log
// (that is the Activity card on /subscriptions).
const railActivityLimit = 8

// railActivityFetchLimit is how many event ROWS are read before coalescing.
//
// It is deliberately larger than railActivityLimit for exactly the reason
// railFetchLimit is larger than outputsRailLimit: coalescing over only
// railActivityLimit rows lets a burst of same-subject events UNDER-fill the
// widget. Twenty new_version rows for one model collapse to a single entry, and
// with a 1:1 window that would leave seven slots empty while older, different
// activity sat just outside the window.
//
// It stays a constant, and stays within store.RecentEvents' own default cap.
const railActivityFetchLimit = 40

// activityEntry is ONE row of the widget, after coalescing.
type activityEntry struct {
	// Kind is the coarse category, used for the icon/label shape and as half the
	// coalescing key. It is NOT the raw store.Event.Kind — several event kinds map
	// onto one category.
	Kind string
	// Subject is the thing the entry is about (a workflow label, a model id, a
	// filename). It is the OTHER half of the coalescing key, and it is what makes
	// "8 runs of X" possible without merging eight runs of DIFFERENT workflows.
	Subject string
	// Text is the display wording for a SINGLE occurrence. The coalesced wording is
	// derived from it plus Count at render time (see activityLine), so the two can
	// never disagree.
	Text string
	// Href is an optional destination. Empty renders as plain text rather than a
	// dead link.
	Href string
	// TS is the entry's time — the NEWEST member's, for a coalesced entry.
	TS time.Time
	// Count is how many occurrences this entry collapsed. 1 renders with no badge:
	// a "×1" would be pure noise.
	Count int
}

// activity category constants. These are OURS, not the store's event kinds.
const (
	activityRun       = "run"
	activityDownload  = "download"
	activityVersion   = "version"
	activitySubscribe = "subscribe"
	activityProblem   = "problem"
	activityNote      = "note"
)

// buildRailActivity merges the two bounded sources into one time-ordered,
// coalesced feed, newest first, clamped to limit ENTRIES.
//
// ORDERING follows the same rule as groupRailGenerations, and for the same
// reason: a coalesced entry takes the position of its NEWEST member, and that
// member is its representative — so the relative time shown describes the row
// whose slot the entry occupies. Taking the oldest would caption a top-of-feed
// entry with a stale age.
func buildRailActivity(groups []railGroup, events []store.Event, limit int) []activityEntry {
	entries := make([]activityEntry, 0, len(groups)+len(events))
	for _, gr := range groups {
		entries = append(entries, runActivity(gr))
	}
	for _, ev := range events {
		entries = append(entries, eventActivity(ev))
	}
	// Newest first. A stable sort keeps same-timestamp entries in source order,
	// which keeps the output deterministic for a fixture whose rows share a time.
	sortActivityDesc(entries)
	return coalesceActivity(entries, limit)
}

// runActivity turns one rail group (a single run, or a whole batch) into an
// activity entry. A batch arrives ALREADY coalesced by groupRailGenerations, so
// its Count comes in >1 and the widget reports "N runs of X" without having to
// re-derive it.
func runActivity(gr railGroup) activityEntry {
	label := generationLabel(gr.Rep)
	href := "/outputs/" + strconv.FormatInt(gr.Rep.ID, 10)
	if gr.Count > 1 {
		if bh := batchHref(gr.Rep.BatchID); bh != "" {
			href = bh
		}
	}
	return activityEntry{
		Kind:    activityRun,
		Subject: label,
		Text:    label,
		Href:    href,
		TS:      gr.Rep.CreatedAt,
		Count:   max(gr.Count, 1),
	}
}

// eventActivity maps ONE store event onto a category and a subject.
//
// The default arm is deliberately permissive: an unrecognised kind renders with
// its own stored message rather than being dropped. A dropped event is invisible
// twice over — the user never learns the thing happened, and nobody notices the
// widget is incomplete.
func eventActivity(ev store.Event) activityEntry {
	e := activityEntry{Text: ev.Message, TS: ev.TS, Count: 1}
	// Subject defaults to the model the event is about, so N events for one model
	// coalesce. Falling back to the message keeps unrelated events apart.
	if ev.ModelID != nil {
		e.Subject = "model:" + strconv.Itoa(*ev.ModelID)
		e.Href = "/models/" + strconv.Itoa(*ev.ModelID)
	} else {
		e.Subject = ev.Message
	}
	switch ev.Kind {
	case "enqueued":
		e.Kind = activityDownload
	case "new_version":
		e.Kind = activityVersion
	case "seed":
		e.Kind = activitySubscribe
	case "poll_error", "enqueue_error":
		e.Kind = activityProblem
		// A failure must never merge with a success for the same model — the count
		// would read as "3 downloads" while one of them was an error.
		e.Subject = "err:" + e.Subject
	default:
		e.Kind = activityNote
		// Skips and other notes keep their own message as the subject: they are
		// per-file, and merging them under a model id would claim a count the
		// message text does not support.
		e.Subject = ev.Kind + ":" + ev.Message
	}
	if ev.Level == store.LevelError || ev.Level == store.LevelWarn {
		if e.Kind != activityProblem {
			e.Kind = activityProblem
			e.Subject = "err:" + e.Subject
		}
	}
	return e
}

// sortActivityDesc orders newest-first with a stable insertion sort. The slice is
// bounded by railActivityFetchLimit + outputsRailLimit (< 60), so an O(n²) sort
// is cheaper than the machinery to avoid it, and stability is the property that
// matters here.
func sortActivityDesc(es []activityEntry) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && es[j].TS.After(es[j-1].TS); j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}

// coalesceActivity merges entries sharing (Kind, Subject) into one, summing their
// counts, and clamps to limit ENTRIES.
//
// 🔴 IT MERGES ACROSS THE WHOLE WINDOW, NOT JUST ADJACENT ROWS. That is the point
// of the widget: eight runs of a workflow interleaved with other activity still
// read as "8 runs of X" rather than eight lines pushing everything else out of a
// column that only holds railActivityLimit rows.
//
// HONEST LIMIT, the same one railGroup carries: Count counts only occurrences
// INSIDE the caller's fetch window. A subject straddling the window's oldest edge
// undercounts, because its older members were never read. railActivityFetchLimit
// is sized to make that unlikely rather than impossible; the alternative — a
// GROUP BY over the whole events table on every page render — is exactly the
// unbounded per-render scan this widget must not have.
func coalesceActivity(entries []activityEntry, limit int) []activityEntry {
	out := make([]activityEntry, 0, len(entries))
	seen := make(map[string]int, len(entries))
	for _, e := range entries {
		key := e.Kind + "\x00" + e.Subject
		if i, ok := seen[key]; ok {
			out[i].Count += max(e.Count, 1)
			continue
		}
		if e.Count < 1 {
			e.Count = 1
		}
		seen[key] = len(out)
		out = append(out, e)
	}
	if limit > 0 && len(out) > limit {
		// Clamp to ENTRIES, not source rows — the whole point of over-fetching.
		out = out[:limit]
	}
	return out
}

// activityLine is the display wording for one entry: the singular Text, or a
// coalesced form derived from Text + Count.
//
// The coalesced and singular wordings come from the SAME Text field, so they can
// never describe different things — the bug shape where a "×8" badge sits beside
// a label naming only one of the eight.
func activityLine(e activityEntry) string {
	if e.Count <= 1 {
		return e.Text
	}
	n := strconv.Itoa(e.Count)
	switch e.Kind {
	case activityRun:
		return n + " runs of " + e.Text
	case activityDownload:
		return n + " downloads · " + e.Text
	case activityVersion:
		return n + " new versions · " + e.Text
	default:
		return n + " × " + e.Text
	}
}

// activityGlyph is the entry's category mark. A GLYPH, not a colour, is what
// distinguishes the categories: colour alone fails for a reader with a
// colour-vision deficiency and in a forced-colours rendering — the same reasoning
// as alertIcon. aria-hidden, because the line's text already says what happened.
func activityGlyph(kind string) string {
	switch kind {
	case activityRun:
		return "▶"
	case activityDownload:
		return "↓"
	case activityVersion:
		return "✦"
	case activitySubscribe:
		return "★"
	case activityProblem:
		return "!"
	default:
		return "·"
	}
}

// railActivityBodyID is the STABLE container the activity poller swaps INTO.
//
// 🔴 THE POLLER MUST NEVER REPLACE THE POLLING NODE ITSELF. This id belongs to a
// container whose hx-get/hx-trigger live on it and whose INNER HTML is swapped
// (hx-swap="innerHTML"); an outerHTML swap would delete the very element carrying
// the trigger and the poll loop would stop after exactly one tick — silently,
// looking like "the feed just never updates". Same invariant as the scan and
// discovery streaming jobs.
const railActivityBodyID = "cm-rail-activity-body"

// railActivityWidget renders the activity widget: a stable, self-polling
// container whose contents are replaced every 10s.
//
// The heading is NOT a link. Every other entry point in the rail goes somewhere
// specific; "activity" as a whole has no single destination — the individual
// lines link to the model or the run they are about, which is the useful
// granularity.
func railActivityWidget(entries []activityEntry) g.Node {
	return railWidget(
		h.Div(h.Class("cm-rail-whead"),
			h.Span(h.Class("cm-rail-wtitle"), g.Text("Recent activity")),
		),
		h.Div(
			h.ID(railActivityBodyID),
			h.Class("cm-rail-activity"),
			hx("get", "/fragments/rail-activity"),
			hx("trigger", "every 10s"),
			// 🔴 innerHTML, never outerHTML — see railActivityBodyID.
			hx("swap", "innerHTML"),
			railActivityList(entries),
		),
	)
}

// railActivityList is the widget's INNER content — the exact fragment
// /fragments/rail-activity returns, so the first paint and every poll tick render
// through one function and cannot drift.
func railActivityList(entries []activityEntry) g.Node {
	if len(entries) == 0 {
		return h.P(h.Class("cm-rail-empty"), g.Text("Nothing yet."))
	}
	items := make([]g.Node, 0, len(entries))
	for _, e := range entries {
		line := activityLine(e)
		inner := []g.Node{
			h.Span(h.Class("cm-rail-act-glyph"), g.Attr("aria-hidden", "true"),
				g.Text(activityGlyph(e.Kind))),
			// g.Text escapes: every one of these strings is untrusted-ish (a workflow
			// name, a civitai filename, a stored message).
			h.Span(h.Class("cm-rail-act-text"), g.Text(line)),
		}
		var body g.Node
		if e.Href != "" && safeRailHref(e.Href) {
			body = h.A(h.Href(e.Href), h.Class("cm-rail-act-link"), g.Group(inner))
		} else {
			body = h.Div(h.Class("cm-rail-act-link"), g.Group(inner))
		}
		items = append(items, h.Li(
			h.Class("cm-rail-act"),
			h.Title(line+" · "+humanSince(e.TS)),
			body,
			h.Span(h.Class("cm-rail-act-when"), g.Text(humanSince(e.TS))),
		))
	}
	return h.Ul(h.Class("cm-rail-acts"), g.Group(items))
}

// safeRailHref keeps the feed's links to same-origin app paths.
//
// Every href here is built from our own constants plus an integer id, so this is
// belt-and-braces — but the feed's TEXT comes from stored, externally-influenced
// messages, and a future arm that derived an href from one would otherwise be one
// edit away from putting `javascript:` (or an off-site URL) behind a link the user
// has every reason to trust. Requiring a leading single slash rejects `javascript:`,
// `data:`, a scheme-relative `//evil.example`, and a bare relative path.
func safeRailHref(href string) bool {
	return strings.HasPrefix(href, "/") && !strings.HasPrefix(href, "//")
}
