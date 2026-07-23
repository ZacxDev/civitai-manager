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

// scanScanning renders the in-progress fragment swapped into the STABLE
// #scan-results container (innerHTML): a card with a large PRIMARY "Stop
// scanning" CTA, a spinner, live progress ("scanned N files, matched M"), the
// result cards streamed so far, and the one-shot re-arming poller.
func scanScanning(results []library.FileResult, scanned, matched int, csrf string) g.Node {
	header := h.Div(
		h.Class("flex items-center gap-2 text-sm text-slate-300"),
		spinnerGlyph(),
		g.Text(fmt.Sprintf("Scanning selected directories for model files… scanned %d, matched %d (Stop any time)", scanned, matched)),
	)
	cardChildren := []g.Node{
		h.Class("space-y-3 border-indigo-500/50"),
		header,
		h.Div(h.Class("flex"), scanStopButton(csrf)),
	}
	if len(results) > 0 {
		var cards []g.Node
		for _, fr := range results {
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
// / stopped" status line followed by the authoritative Summary / Files /
// Deletion-candidate view rebuilt from the completed local_files. started=false
// (no scan ever ran) renders just the plain library content so any stray poller
// halts. It distinguishes an exhausted scan ("Scan complete — N files, M
// matched") from a user-stopped or errored one ("Scan stopped — …" / a friendly
// budget/deadline message).
func scanResults(v libraryView, scanned, matched int, started, stopped bool, err error, csrf string) g.Node {
	if !started {
		return libraryContent(v, csrf)
	}
	var status g.Node
	switch {
	case err != nil && !stopped:
		// A non-user error (too-large / deadline / shutdown): friendly message.
		status = h.P(h.Class("text-xs text-amber-400"), g.Text(scanErrorMessage(err)))
	case stopped || err != nil:
		status = h.P(h.Class("text-xs text-amber-400"),
			g.Text(fmt.Sprintf("Scan stopped — scanned %d files, matched %d", scanned, matched)))
	default:
		status = h.P(h.Class("text-xs text-emerald-400"),
			g.Text(fmt.Sprintf("Scan complete — %d files, %d matched", scanned, matched)))
	}
	return h.Div(
		h.Class("space-y-4"),
		card(h.Class("space-y-1"),
			sectionTitle("Scan result"),
			status,
		),
		libraryContent(v, csrf),
	)
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
