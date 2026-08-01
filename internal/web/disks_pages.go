package web

import (
	"fmt"
	"strconv"

	"github.com/ZacxDev/civitai-manager/internal/diskusage"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// diskRow is ONE configured directory's capacity line on /disks.
//
// Usage is BEST-EFFORT: a path that could not be stat'd (missing, unmounted,
// unsupported GOOS) arrives with a zero Usage whose Known() is false, and the
// row renders "Capacity unknown" plus the reason. That is a normal state, not an
// error — the whole page must render even when every probe failed.
type diskRow struct {
	// Label names the ROLE the directory plays ("Model root", "Library path",
	// "Scan directory", "Trash"), because the path alone does not say why the app
	// cares about it.
	Label string
	// Path is the directory as configured, shown verbatim: the user has to be able
	// to match it against their own config file.
	Path string
	// Usage is the capacity of the FILESYSTEM the path lives on, not of the
	// directory's contents. Two directories on one filesystem therefore report the
	// same numbers — see disksCapacityCard's footnote, which says so on the page.
	Usage diskusage.Usage
	// Err is why Usage is unknown, rendered as short human text. Empty when Usage
	// is Known.
	Err string
}

// disksPage is the /disks surface: filesystem capacity for every configured
// directory, plus the quarantine batches that used to live at /trash.
//
// WHY THE TWO HALVES SHARE A PAGE. Quarantine is a disk-space feature — it moves
// files aside instead of deleting them, so "what is quarantined" and "how full
// is the disk" are the same question asked twice. /trash answered only half of it
// and was named after an implementation detail. /trash now redirects here
// (handleTrashRedirect), so no bookmark breaks.
//
// gated=true renders the loopback notice INSTEAD of the capacity card: the page
// prints absolute filesystem paths, which is exactly the class of information
// the loopback invariant exists to keep off a LAN-exposed bind.
//
// The quarantine table keeps its "#trash-content" container id and its
// POST /trash/{id}/restore action UNCHANGED — the restore button's htmx target
// is that id, so renaming either would break the swap for no benefit.
func disksPage(rows []diskRow, batches []batchView, gated bool, csrf string, mr maturityRange, rail ...railData) g.Node {
	return page("Disks", csrf, mr, railOf(rail),
		card(
			pageTitle("Disks"), // the page's single <h1>
			disksCapacityCard(rows, gated),
		),
		card(
			sectionTitle("Quarantine"),
			h.P(h.Class("mb-3 text-sm text-slate-400"),
				g.Text("Quarantining a model file MOVES it to the trash directory instead of "+
					"deleting it. Every batch below is restorable to its original locations.")),
			h.Div(h.ID("trash-content"), trashTable(batches, csrf)),
		),
	)
}

// disksCapacityCard renders the per-directory capacity section, the loopback
// notice, or the "nothing configured" empty state.
func disksCapacityCard(rows []diskRow, gated bool) g.Node {
	if gated {
		// The SAME wording every other gated surface uses, so the string a user
		// searches for is one string (and the shared gateMsg test constant matches).
		return alert("warning", "Capacity hidden",
			h.P(g.Text("Disk capacity is disabled when the server is bound to a non-loopback address. "+
				"It reports absolute filesystem paths, which an unauthenticated remote caller must not see.")))
	}
	if len(rows) == 0 {
		return emptyState(
			"No model directories configured",
			"Disk capacity is reported for the directories this app manages: the model root, "+
				"any extra library paths, and the directories selected for scanning. Add one and "+
				"its filesystem shows up here.",
			libraryModelFilesHref, "Set up the library")
	}
	nodes := make([]g.Node, 0, len(rows)+1)
	for _, r := range rows {
		nodes = append(nodes, diskRowNode(r))
	}
	nodes = append(nodes, h.P(h.Class("mt-3 text-xs text-slate-400"),
		g.Text("Figures describe the FILESYSTEM each directory lives on, not the size of its "+
			"contents — two directories on the same disk report the same totals.")))
	// No wrapper class: the rows carry their own (.cm-disk-row), so a class here
	// would have no rule and the class-coverage guard would (correctly) flag it.
	return h.Div(g.Group(nodes))
}

// diskRowNode renders one capacity row. An unknown Usage renders the label, the
// path and why it is unknown — never a 0-byte meter, which reads as a real disk
// that is completely empty (or completely full, depending on which number the
// eye lands on first).
func diskRowNode(r diskRow) g.Node {
	head := h.Div(h.Class("cm-disk-head"),
		h.Span(h.Class("cm-disk-label"), g.Text(r.Label)),
		h.Span(h.Class("cm-disk-path"), g.Text(r.Path)),
	)
	if !r.Usage.Known() {
		reason := r.Err
		if reason == "" {
			reason = "the filesystem did not report a size"
		}
		return h.Div(h.Class("cm-disk-row"),
			head,
			h.Div(h.Class("cm-disk-unknown"),
				h.Span(g.Attr("aria-hidden", "true"), g.Text("⚠ ")),
				g.Text("Capacity unknown — "+reason),
			),
		)
	}
	pct := r.Usage.UsedFraction()
	// The meter is DECORATION: the same numbers are stated as text on the next
	// line, so it carries aria-hidden and colour is never the only signal.
	meter := h.Div(h.Class("cm-meter"), g.Attr("aria-hidden", "true"),
		h.Div(
			h.Class("cm-meter-fill"),
			dataAttr("level", meterLevel(pct)),
			h.StyleAttr("width:"+strconv.FormatFloat(pct*100, 'f', 1, 64)+"%"),
		),
	)
	return h.Div(h.Class("cm-disk-row"),
		head,
		meter,
		// "of the disk" is NOT filler. This percentage is Used/TOTAL, whereas
		// `df`'s Use% column is Used/(Used+Available) — it excludes the root
		// reserve from its denominator. On the machine this was verified against
		// the two read 70% and 75% for the same filesystem, so an unqualified
		// "70%" beside a terminal showing 75% looks like a bug. Naming the
		// denominator makes the two reconcilable instead.
		h.Div(h.Class("cm-disk-figures"), g.Text(fmt.Sprintf(
			"%s free of %s · %s used (%.0f%% of the disk)",
			humanBytes(int64(r.Usage.Free)),
			humanBytes(int64(r.Usage.Total)),
			humanBytes(int64(r.Usage.Used)),
			pct*100,
		))),
	)
}

// meterLevel maps a used-fraction to the meter's intent, so a disk about to fill
// up is visible at a glance. The thresholds are deliberately blunt (90% / 98%):
// this is an at-a-glance tint, not an alerting policy, and the row always states
// the exact figures beside it.
func meterLevel(frac float64) string {
	switch {
	case frac >= 0.98:
		return "full"
	case frac >= 0.90:
		return "warn"
	default:
		return "ok"
	}
}
