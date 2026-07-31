package web

import (
	"strconv"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ── ONE RUN ZONE ─────────────────────────────────────────────────────────────
//
// The detail page used to answer "how do I run this?" with THREE same-weight
// controls stacked vertically, and two of them did DIFFERENT things:
//
//  1. a "Generate" button living inside the lazily-loaded reachability fragment.
//     It hx-included ONLY #run-modes, so it submitted the workflow's own saved
//     values and SILENTLY DISCARDED every edit typed into the Parameters panel
//     directly below it.
//  2. a "Run with these parameters" submit inside that panel, which did include
//     them.
//  3. a "Queue ×N" block with its own heading, its own explanatory paragraph, its
//     own four quick-pick buttons, its own number input and its own "Queue"
//     button — a second control for "the same thing, N times".
//
// (1) vs (2) is the worst shape available: two primary-looking buttons for one
// intent where the more prominent one throws your work away. (3) doubled the
// surface again, and — because it was rendered INSIDE the preset form — it did
// not exist at all for a UI workflow that exposes no editable parameters, so
// those workflows could not be batched.
//
// There is now ONE run zone: a count segment (1 / ×2 / ×4 / ×8 / ×16 / custom)
// and ONE primary button. Both hx-include the parameters form, so the single
// primary action always runs exactly what is on screen, and the count is a
// PARAMETER of that action rather than a rival to it.
//
// STRUCTURE — three things are load-bearing:
//
//   - The zone sits OUTSIDE #run-params and OUTSIDE #run-status, so neither the
//     preset-tab swap (which replaces #run-params' innerHTML) nor the 1 s run
//     poller (which replaces #run-status') can destroy it, and #run-params and
//     #run-status remain non-nested exactly as before.
//   - The primary button stays INSIDE the stable #run-comfy-status container, so
//     the reachability probe can still render it DISABLED with a reason when
//     ComfyUI is unreachable. Only its hx-include changed.
//   - The count inputs live in their own stable #cm-run-count container, named in
//     runZoneInclude, because the button is not inside a <form> of its own.

// runZoneID is the id of the ONE run zone. It is also the anchor the run hint's
// aria-describedby points at.
const runZoneID = "cm-run-zone"

// runCountGroupID is the STABLE container holding the count segment's inputs. The
// primary button is not inside a form, so hx-include has to name it explicitly.
const runCountGroupID = "cm-run-count"

// runZoneInclude is the hx-include selector the ONE primary run control uses: the
// parameters form and the mode picker (runPresetInclude, unchanged) PLUS the count
// segment. Adding the count here is what merged "Run" and "Queue ×N" into one
// request shape — the endpoint reads a count that is simply absent (⇒ 1) for a
// workflow with no count control.
const runZoneInclude = runPresetInclude + ", #" + runCountGroupID

// runCountRadioID is the DOM id of one count radio. It is derived, not free-form,
// because the CSS that suffixes the button label (" ×4") selects on these ids.
func runCountRadioID(v string) string { return "cm-count-" + v }

// runZone renders the whole run zone: the count segment (UI-format only), the
// reachability container that carries the ONE primary button, the secondary
// "Open in ComfyUI" form, and the hint that explains what a batch costs.
//
// 🔴 openInComfyForm is a real <form method="post" target="_blank">, NOT an htmx
// button, and it is rendered HERE rather than inside the lazily-loaded fragment so
// it exists from the first paint. Do not convert it — the browser has to open the
// tab synchronously from the click for the handler's 303 into
// <comfy_url>/?cm_open=<path> to land.
func runZone(wfID int64, csrf string, canQueue, editable bool) g.Node {
	id := strconv.FormatInt(wfID, 10)

	row := []g.Node{
		h.Div(h.ID(runComfyStatusID), h.Class("cm-gen-row"),
			hx("get", "/workflows/"+id+"/run/comfy-status"),
			hx("trigger", "load"),
			hx("swap", "innerHTML"),
			h.Span(h.Class("text-sm text-slate-400"), g.Text("Checking ComfyUI…")),
		),
	}
	if editable {
		row = append(row, openInComfyForm(id, csrf, "outline", "md"))
	}

	body := []g.Node{h.ID(runZoneID), h.Class("cm-runbar")}
	if canQueue {
		body = append(body, runCountSegment())
	}
	body = append(body, h.Div(h.Class("cm-gen-row"), g.Group(row)))
	body = append(body, h.P(h.ID(runZoneHintID), h.Class("cm-run-hint"), g.Text(runZoneHint(canQueue))))
	return h.Div(body...)
}

// runZoneHintID is the hint paragraph the count segment is aria-describedby.
const runZoneHintID = "cm-run-hint"

// runZoneHint is the ONE sentence that replaced the Queue block's own paragraph.
// It names the two consequences a batch has and the plain "Queue ×N" paragraph
// used to bury: every item re-rolls the seed, and every captured item adds
// eviction pressure on the user's older outputs.
func runZoneHint(canQueue bool) string {
	if !canQueue {
		return "Runs once on your local ComfyUI with the values above."
	}
	return "Runs on your local ComfyUI with the values above. A count above 1 queues that " +
		"many runs, one at a time, each with a new random seed — Stop cancels the rest, and " +
		"anything already captured is kept. Maximum " + strconv.Itoa(maxBatchCount) + " per batch."
}

// runCountSegment is the count control: five radio pills plus a "Custom" pill that
// reveals a number input.
//
// It is RADIOS, not buttons, for three reasons that each cost something in the old
// design:
//
//   - A radio is a VALUE, so the count rides along on the ONE primary button's
//     request instead of needing four buttons that each fire their own.
//   - The chosen count stays visible after the click. The quick picks were fire-
//     and-forget: after clicking ×8 nothing on the page said 8.
//   - `1` is a real, selectable option. The old block had no way to say "just once"
//     other than clearing a number field, which is exactly how it ended up shipping
//     a control labelled "Queue ×N" that quietly did a single run.
//
// The custom input is a SEPARATE field name (count_custom). Two inputs both named
// `count` would submit twice and r.FormValue would silently return the radio's
// value, so the "Custom" radio submits a sentinel and the server reads the other
// field — see batchCountFromForm.
func runCountSegment() g.Node {
	opts := make([]g.Node, 0, len(batchQuickPicks)+2)
	opts = append(opts, runCountOption("1", "1", "Run once", true))
	for _, n := range batchQuickPicks {
		s := strconv.Itoa(n)
		opts = append(opts, runCountOption(s, "×"+s, "Queue "+s+" runs, each with a new random seed", false))
	}
	opts = append(opts, runCountOption(batchCountCustom, "Custom", "Queue a custom number of runs", false))

	custom := h.Div(h.Class("cm-count-custom"),
		h.Div(dataAttr("civitai-ui", "text-input"),
			h.Label(dataFlag("civitai-ui-label"), h.For("cm-count-custom-input"), g.Text("How many")),
			h.Input(dataFlag("civitai-ui-control"), h.ID("cm-count-custom-input"),
				h.Type("number"), h.Name(batchCountCustomField),
				g.Attr("min", "1"), g.Attr("max", strconv.Itoa(maxBatchCount)),
				g.Attr("step", "1"), h.Value(strconv.Itoa(defaultBatchCount))),
		),
	)

	return h.Div(h.ID(runCountGroupID), h.Class("cm-count"),
		g.Attr("role", "radiogroup"),
		g.Attr("aria-label", "How many runs"),
		g.Attr("aria-describedby", runZoneHintID),
		h.Div(h.Class("cm-count-opts"), g.Group(opts)),
		custom,
	)
}

// runCountOption renders one count pill: a visually-hidden-but-FOCUSABLE radio
// plus its label. The radio is only clipped (never display:none and never
// hidden), so it keeps native radiogroup keyboard behaviour — arrow keys move
// between counts — and `:focus-visible + label` paints the focus ring.
func runCountOption(value, label, title string, checked bool) g.Node {
	attrs := []g.Node{
		h.Class("cm-count-radio"),
		h.ID(runCountRadioID(value)),
		h.Type("radio"),
		h.Name(batchCountField),
		h.Value(value),
	}
	if checked {
		attrs = append(attrs, g.Attr("checked"))
	}
	return g.Group([]g.Node{
		h.Input(attrs...),
		h.Label(h.Class("cm-count-pill"), h.For(runCountRadioID(value)),
			g.Attr("title", title), g.Text(label)),
	})
}
