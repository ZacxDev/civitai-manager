package web

import (
	"fmt"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// scanForModelsCTA is the prominent primary "Scan for models" call-to-action
// rendered in Tab A once the persisted selection has ≥1 directory (see
// selectedDirsList). Clicking it POSTs /library/scan, which stops any running
// discovery crawl and starts the streaming model scan, then HX-Redirects the
// user to the Model files tab (whose #scan-results bootstraps the live scanning
// view). hx-target is a fallback for the rare synchronous error (gating); on
// success the redirect supersedes the swap.
func scanForModelsCTA(csrf string) g.Node {
	return h.Div(
		h.Class("mt-3 border-t border-slate-800 pt-3"),
		civButton("filled", "lg", []g.Node{
			h.Type("button"),
			hx("post", "/library/scan"),
			hx("target", "#tab-panel"),
			hx("swap", "innerHTML"),
			hx("disabled-elt", "this"),
			csrfInline(csrf),
			h.Class("w-full sm:w-auto"),
		}, g.Text("Scan for models")),
		h.P(h.Class("mt-1 text-xs text-slate-400"),
			g.Text("Scan the selected directories for model files (opens the Model files tab).")),
		h.P(h.Class("mt-1 text-xs text-slate-500"),
			g.Text("Matches your files against CivitAI by hash (sends file hashes to civitai.com). Turn off “Match against CivitAI” on the Model files tab to scan offline.")),
	)
}

// scanPoller is the one-shot, re-arming poll element that drives the scanning
// view to its terminal state — the model-scan twin of discoverPoller and the
// same re-arming pattern that avoids an orphaned poller.
//
// It never targets itself. It fires ONCE (hx-trigger="load delay:1s"), GETs
// /library/scan/status, and swaps the innerHTML of the STABLE #scan-results
// container. While the scan runs, each status response carries a FRESH
// scanPoller (re-arming the next one-shot); the terminal fragment carries none,
// so polling stops. Every swap fully replaces the stable container's children,
// so there is never a duplicate or detached #scan-poll.
func scanPoller() g.Node {
	return h.Div(
		h.ID("scan-poll"),
		hx("get", "/library/scan/status"),
		hx("trigger", "load delay:1s"),
		hx("target", "#scan-results"),
		hx("swap", "innerHTML"),
	)
}

// scanStopButton renders the large PRIMARY Stop CTA shown while a scan runs. It
// POSTs /library/scan/stop (CSRF via hx-vals) and swaps the current status
// fragment into the STABLE #scan-results container (innerHTML).
func scanStopButton(csrf string) g.Node {
	return civButton("filled", "lg", []g.Node{
		h.Type("button"),
		hx("post", "/library/scan/stop"),
		hx("target", "#scan-results"),
		hx("swap", "innerHTML"),
		csrfInline(csrf),
		h.Class("w-full sm:w-auto"),
	}, g.Text("Stop scanning"))
}

// scanProgressText renders the muted running/terminal count line
// "scanned N · matched M · unmatched U" (with "· pending P" only when any file is
// pending). unmatched is a normal outcome, so it is a plain count, not alarming.
func scanProgressText(scanned, matched, unmatched, pending int) string {
	s := fmt.Sprintf("scanned %d · matched %d · unmatched %d", scanned, matched, unmatched)
	if pending > 0 {
		s += fmt.Sprintf(" · pending %d", pending)
	}
	return s
}

// matchingOffNote is the muted advisory shown while (and after) a scan that ran
// with CivitAI matching DISABLED, so a user who sees near-zero matches knows WHY
// (matching is off) and how to fix it — rather than assuming the scan is broken.
func matchingOffNote() g.Node {
	return h.P(h.Class("text-xs text-amber-400"),
		g.Text("CivitAI matching is OFF — enable “Match against CivitAI” to identify models."))
}

// scanScanning renders the in-progress fragment swapped into the STABLE
// #scan-results container (innerHTML): a card with a large PRIMARY "Stop
// scanning" CTA, a spinner, live progress
// ("scanned N · matched M · unmatched U · pending P"), a matching-off note when
// matching is disabled, the result cards streamed so far, and the one-shot
// re-arming poller.
func scanScanning(snap scanSnapshot, csrf string) g.Node {
	header := h.Div(
		h.Class("flex items-center gap-2 text-sm text-slate-300"),
		spinnerGlyph(),
		g.Text("Scanning selected directories for model files… "+
			scanProgressText(snap.Scanned, snap.Matched, snap.Unmatched, snap.Pending)+
			" (Stop any time)"),
	)
	cardChildren := []g.Node{
		h.Class("space-y-3 border-indigo-500/50"),
		header,
	}
	if snap.NoRemote {
		cardChildren = append(cardChildren, matchingOffNote())
	}
	cardChildren = append(cardChildren, h.Div(h.Class("flex"), scanStopButton(csrf)))
	if len(snap.Results) > 0 {
		var cards []g.Node
		for _, fr := range snap.Results {
			cards = append(cards, scanResultCard(fr))
		}
		cardChildren = append(cardChildren, h.Div(h.Class("space-y-2"), g.Group(cards)))
	}
	return h.Div(
		h.Class("mt-2 space-y-2"),
		card(cardChildren...),
		scanPoller(),
	)
}

// scanResults renders the TERMINAL model-scan view (no poller): a "Scan complete
// / stopped" status line (with the scanned/matched/unmatched breakdown) followed
// by the authoritative Summary / Files / Deletion-candidate view rebuilt from the
// completed local_files. snap.Started=false (no scan ever ran) renders just the
// plain library content so any stray poller halts. It distinguishes an exhausted
// scan ("Scan complete — N files · M matched · U unmatched") from a user-stopped
// or errored one ("Scan stopped — …" / a friendly budget/deadline message), and
// surfaces the matching-off note when the scan ran with matching disabled.
func scanResults(v libraryView, snap scanSnapshot, csrf string) g.Node {
	if !snap.Started {
		return libraryContent(v, csrf)
	}
	var status g.Node
	switch {
	case snap.Err != nil && !snap.Stopped:
		// A non-user error (too-large / deadline / shutdown): friendly message.
		status = h.P(h.Class("text-xs text-amber-400"), g.Text(scanErrorMessage(snap.Err)))
	case snap.Stopped || snap.Err != nil:
		status = h.P(h.Class("text-xs text-amber-400"),
			g.Text("Scan stopped — "+scanProgressText(snap.Scanned, snap.Matched, snap.Unmatched, snap.Pending)))
	default:
		status = h.P(h.Class("text-xs text-emerald-400"),
			g.Text(fmt.Sprintf("Scan complete — %d files · %d matched · %d unmatched",
				snap.Scanned, snap.Matched, snap.Unmatched)+pendingSuffix(snap.Pending)))
	}
	cardChildren := []g.Node{
		h.Class("space-y-1"),
		sectionTitle("Scan result"),
		status,
	}
	if snap.NoRemote {
		cardChildren = append(cardChildren, matchingOffNote())
	}
	return h.Div(
		h.Class("space-y-4"),
		card(cardChildren...),
		libraryContent(v, csrf),
	)
}

// pendingSuffix appends "· P pending" only when any file is pending, so the
// common (no-pending) terminal line stays clean.
func pendingSuffix(pending int) string {
	if pending > 0 {
		return fmt.Sprintf(" · %d pending", pending)
	}
	return ""
}

// scanResultCard renders one streamed scanned file as a themed card: the matched
// model/version (linked to its model page) or an unmatched note, the on-disk
// filename, the size, a status badge (matched/unmatched/pending/broken via the
// civitai Badge data-color intent), and a small preview marker when a sibling
// ".preview.png" exists.
func scanResultCard(fr library.FileResult) g.Node {
	var title g.Node
	if fr.ModelID != nil {
		label := "Model #" + strconv.Itoa(*fr.ModelID)
		if fr.VersionID != nil {
			label += " · v" + strconv.Itoa(*fr.VersionID)
		}
		title = h.A(
			h.Href("/models/"+strconv.Itoa(*fr.ModelID)),
			h.Class("text-sm font-medium text-indigo-300 hover:text-indigo-200"),
			g.Text(label),
		)
	} else {
		title = h.Span(h.Class("text-sm text-slate-300"), g.Text("Unmatched"))
	}

	meta := []g.Node{scanStatusBadge(fr.Status)}
	if fr.HasPreview {
		meta = append(meta, badge("preview", "slate"))
	}

	return h.Div(
		h.Class("flex items-center justify-between gap-3 rounded-md border border-slate-800 bg-slate-900 p-2"),
		h.Div(
			h.Class("min-w-0 space-y-1"),
			title,
			h.Div(h.Class("truncate text-xs text-slate-400"), g.Text(fr.Name)),
			h.Div(h.Class("flex flex-wrap items-center gap-1"), g.Group(meta)),
		),
		h.Span(h.Class("shrink-0 text-xs text-slate-400"), g.Text(humanBytes(fr.SizeBytes))),
	)
}

// scanStatusBadge maps a streamed file's match status to a semantic badge.
func scanStatusBadge(status string) g.Node {
	switch status {
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
