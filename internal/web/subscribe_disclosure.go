package web

import (
	"fmt"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// ===========================================================================
// "What will subscribing actually download?" — said BEFORE the Confirm click.
// ===========================================================================
//
// The subscribe options panel offers Auto-download as the PRE-CHECKED default,
// and the word "subscribe" reads like a notification opt-in. This block is the
// disclosure that closes that gap. Every sentence it can emit is derived from
// what the POLLER really does, not from what the word suggests — the four facts
// below each cost a real read of internal/poller before they were written down,
// and getting any one of them wrong produces a confident, specific, WRONG number.
//
//  1. 🔴 SUBSCRIBING DOWNLOADS NOTHING AT THE MOMENT YOU SUBSCRIBE.
//     handleModelSubscribe calls SubscribeModel with BackfillLatest unset, so
//     seedNew → PollOnce(…, backfillLatest=false) takes the first-poll branch:
//     it MARKS EVERY EXISTING VERSION SEEN and enqueues nothing ("seeded %d
//     existing version(s) without downloading"). Only versions published AFTER
//     this moment are ever fetched. A panel that said "this will download 6.5 GB"
//     would be wrong in the direction that scares the user off a free action.
//
//  2. 🔴 IT IS ONE FILE PER VERSION, NOT THE VERSION'S WHOLE FILE LIST.
//     enqueueCandidate resolves exactly one file — civitai.SelectFile(vd.Files,
//     sub.FileTypePref), which with the web path's empty FileTypePref is the
//     SDK's PrimaryFile — and enqueues that single row. So the honest unit is
//     "one file", and the honest size is THAT file's size.
//     ⚠ This is why the dashboard's "4 file(s) · 54.2 GB" line must NOT be
//     reused here even though it is right there and formats beautifully: that
//     number is the LOCAL footprint of files the user already has on disk, which
//     is a different quantity that happens to be about the same subject.
//
//  3. 🔴 A SUBSCRIPTION IS ONGOING, so the number is a RATE, not a total. It is
//     phrased as the size of one version, explicitly per future version, never
//     as a sum and never as a one-off cost.
//
//  4. 🔴 A WORKFLOWS POST NEVER DOWNLOADS ANYTHING, at any size, ever — the
//     poller's type guard in enqueueCandidate is a PERMANENT skip. Its payload
//     is shaped exactly like a checkpoint's (a real downloadUrl, a primary file
//     with a real SHA256 and a real sizeKB), so the arithmetic below would
//     happily produce a confident size for bytes that will never be fetched.
//     modelSubscribeDownload therefore refuses at the top on m.Type, before it
//     looks at a single file — and the panel does not render a mode chooser for
//     one at all (see subscribeOptionsPanel).
//
// And the fifth, which is the same class of lie one layer down: a configured
// max_file_size CAP makes the poller permanently skip an over-cap file
// (BackfillFilteredSize), so a size printed as "this is what you'll get" would
// describe a download the user's own config forbids. That is surfaced instead of
// suppressed — the user can change the setting, but only if they are told.

// subscribeDownload is the resolved answer to "what would an auto-download
// subscription to this model fetch, and how big is it".
//
// The ZERO VALUE IS THE HONEST UNKNOWN, and it is reached by every path that
// cannot produce a number: no cached/fetchable detail, a workflow post, a model
// with no versions, a version with no downloadable file, or a file whose sizeKB
// is missing/zero. Nothing here ever guesses — an unknown renders as "size
// unknown", never as a plausible-looking figure.
type subscribeDownload struct {
	// Known is true only when VersionName/FileName/Bytes all describe a real file.
	Known bool
	// VersionName is the version the size was measured on — named in the copy so
	// the number is attributable rather than floating.
	VersionName string
	// FileName is the file SelectFile would pick for that version.
	FileName string
	// Bytes is that file's size.
	Bytes int64
	// OverCap reports that Bytes exceeds the configured max_file_size, i.e. the
	// poller would PERMANENTLY SKIP this download. CapBytes is that limit.
	OverCap  bool
	CapBytes int64
}

// modelSubscribeDownload derives the disclosure facts from a model detail and
// the RAW body it was decoded from, with NO extra network call: the inline
// modelVersions[].files[] on /api/v1/models carries name, sizeKB and primary,
// which is everything needed.
//
// ⚠ HONEST LIMIT: the poller re-fetches the version (GET /model-versions/{id})
// and runs SelectFile over THAT file list. This runs the same selector over the
// inline copy of the same list from the model payload. They are the same data on
// every model measured, but they are two endpoints, so a CivitAI inconsistency
// would show up here as a slightly wrong size rather than as an error. The copy
// is worded as an expectation ("about that much per version"), not a contract.
//
// capBytes is cfg.MaxFileSizeBytes (0 = unlimited).
func modelSubscribeDownload(m *civitai.ModelDetail, raw []byte, capBytes int64) subscribeDownload {
	// Fact 4: refuse before touching any file. A workflow post's files look
	// exactly like a checkpoint's, so this cannot be a later check.
	if m == nil || civitai.IsWorkflowPost(m.Type) || len(m.ModelVersions) == 0 {
		return subscribeDownload{}
	}
	v := newestVersionSummary(m, raw)
	// The EXACT selector enqueueCandidate uses. The empty preference is not a
	// simplification: the web subscribe path builds poller.SubscribeOptions with no
	// FileTypePref, so SelectFile falls through to PrimaryFile there too.
	f := civitai.SelectFile(v.Files, "")
	if f == nil {
		return subscribeDownload{}
	}
	b := int64(f.SizeKB * 1024)
	if b <= 0 {
		// A missing sizeKB is an unknown, not a zero-byte download.
		return subscribeDownload{}
	}
	d := subscribeDownload{Known: true, VersionName: v.Name, FileName: f.Name, Bytes: b}
	if capBytes > 0 && b > capBytes {
		d.OverCap, d.CapBytes = true, capBytes
	}
	return d
}

// newestVersionSummary picks the version whose size the disclosure quotes: the
// most recently PUBLISHED one.
//
// 🔴 NOT ModelVersions[0]. modelVersions[] is ordered by the creator's `index`
// (primary/featured first), NOT by publish date — the documented data gotcha that
// already caused one ship-then-revert in this codebase. The disclosure is about
// what the NEXT version will look like, so the most recent one is the honest
// reference; the featured one can be years older.
//
// The publish times come from the raw body (versionPublishedTimes) because the
// typed ModelVersionSummary does not carry publishedAt. When raw is absent or
// carries no usable times — a fixture, a trimmed cache row — it falls back to
// [0], the primary version, which is also what the model detail page shows by
// default. That fallback is a worse reference, not a wrong one.
func newestVersionSummary(m *civitai.ModelDetail, raw []byte) civitai.ModelVersionSummary {
	best := m.ModelVersions[0]
	times := versionPublishedTimes(raw)
	bestAt, ok := times[best.ID]
	for _, v := range m.ModelVersions[1:] {
		at, has := times[v.ID]
		if !has {
			continue
		}
		if !ok || at.After(bestAt) {
			best, bestAt, ok = v, at, true
		}
	}
	return best
}

// subscribeDownloadDisclosure renders the per-mode consequences under the
// Auto-download / Notify only radios.
//
// BOTH MODES ARE STATED AT ONCE, ALWAYS — there is no toggling. The alternative
// (show only the checked mode's line) needs either JS, which this app does not
// ship for a disclosure, or a CSS :has() rule whose unsupported fallback shows
// both anyway. Showing both is what a chooser should do regardless: the point of
// the panel is to compare two options before picking one, and a line that
// appears only after you have already chosen has arrived too late to inform the
// choice.
//
// The lead sentence is the one most users get wrong, so it goes FIRST and
// applies to both modes.
func subscribeDownloadDisclosure(d subscribeDownload) g.Node {
	return h.Div(
		h.Class("mt-2 space-y-1 text-xs text-slate-400"),
		// Fact 1 — true in BOTH modes, so it is not attached to either.
		h.P(g.Text("Nothing is downloaded now: the versions that already exist are "+
			"marked as seen, not fetched.")),
		h.P(
			h.Span(h.Class("font-medium text-slate-300"), g.Text("Auto-download")),
			g.Text(" — "+autoDownloadSentence(d)),
		),
		h.P(
			h.Span(h.Class("font-medium text-slate-300"), g.Text("Notify only")),
			// Fact: notify-only fetches nothing, so it gets NO size, ever. The poller
			// takes the outcomePermanentSkip branch without resolving a file at all.
			g.Text(" — nothing is ever downloaded. A new version just appears in Activity."),
		),
	)
}

// autoDownloadSentence is the Auto-download arm's consequence, in the one shape
// that fits the facts: a RATE ("each new version … one file"), then the size of a
// real named version as a scale reference, never a total.
func autoDownloadSentence(d subscribeDownload) string {
	const lead = "each new version published from now on is fetched automatically — " +
		"one file per version, into your model folder"
	if !d.Known {
		// No invented number. Naming where the real figure lives is more useful than
		// a hedge, and it is the same page the Subscribe button sits on.
		return lead + ". Its file size could not be read just now — the model page lists " +
			"each version's files and sizes."
	}
	if d.OverCap {
		// The user's own max_file_size would make the poller skip it permanently
		// (BackfillFilteredSize). Saying "6.5 GB per version" here would describe a
		// download that is never going to happen.
		return fmt.Sprintf("but nothing would actually download: its newest version %q is %s, "+
			"over your max file size of %s, so the poller skips it. Raise max_file_size to change that.",
			d.VersionName, humanBytes(d.Bytes), humanBytes(d.CapBytes))
	}
	return fmt.Sprintf("%s. Its newest version %q is %s (%s), so expect roughly that much each time.",
		lead, d.VersionName, humanBytes(d.Bytes), d.FileName)
}
