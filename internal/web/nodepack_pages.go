package web

import (
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// The "Missing custom nodes" panel: attribution + a gated install, replacing the
// flat, unactionable bullet list this used to be.
//
// It renders inside the run's TERMINAL (poller-free) fragment, exactly like
// missingModelsPanel and incompatibleOptionsSection, so nothing can swap it away
// mid-interaction. Every string it shows — pack titles, repository URLs, class
// names, ComfyUI-Manager's own error text — is UNTRUSTED third-party data and
// goes through g.Text or attribute escaping.
//
// Four states are all first-class, because all four are common in reality:
//
//	installable   → a gated, per-pack-confirmed Install button;
//	blocked       → no button, the reason in plain words, plus the manual command
//	                (measured: 16% of packs are nightly-only, and the design's own
//	                target workflow needs one of them);
//	unattributed  → its own section naming each class (4 of 7 on that workflow);
//	Manager absent→ attribution and manual commands still render, no install
//	                affordance at all.
//
// A copy-able manual command is shown for EVERY pack, always. It is the answer
// when Manager is absent, when the pack is policy-blocked, and when the user
// simply declines.

// nodepackStatusContainerID is the STABLE container the install/restart controls
// swap their innerHTML into, and the install poller's target. The poller element
// itself is never the swap target (the repo's streaming-job invariant).
const nodepackStatusContainerID = "nodepack-status"

// comfyRootPlaceholder stands in for the ComfyUI install root in the manual
// command when comfy_root is not configured (or is not shell-safe).
const comfyRootPlaceholder = "/path/to/ComfyUI"

// missingNodesPanel renders the whole missing-custom-nodes report: the attributed
// packs (grouped by confidence rung), the classes nothing could place, and the
// stable install-status container.
//
// missing is the preflight's raw class list; it is used ONLY as the degraded
// fallback for a snapshot that carries no attribution at all (an older run, or an
// attribution pass that found nothing) so the panel is never emptier than the old
// flat list was.
func missingNodesPanel(attr nodeAttribution, missing []string, wfID int64, csrf, comfyRoot string) g.Node {
	unattributed := attr.Unattributed
	if len(attr.Packs) == 0 && len(unattributed) == 0 {
		unattributed = missing
	}

	body := []g.Node{
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Missing custom nodes")),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("This workflow uses node types your ComfyUI does not have. "+
				"Each one belongs to a custom-node pack — install the packs below (or by hand), "+
				"then restart ComfyUI and run again.")),
	}
	if note := managerStateNote(attr); note != nil {
		body = append(body, note)
	}
	body = append(body, nodePackEgressNotice(attr.RemoteLookup))

	// Rank BEFORE splitting: contest detection has to see every claimant of a class,
	// and a class can legitimately be claimed by an exact-match pack and a
	// pattern-match pack at once.
	ranked := rankPacks(attr.Packs)
	if note := ambiguityNotice(ranked); note != nil {
		body = append(body, note)
	}

	confident, likely := splitRankedByConfidence(ranked)
	if len(confident) > 0 {
		body = append(body, nodepackGroup(
			"Provided by",
			"",
			confident, attr.ManagerPresent, wfID, csrf, comfyRoot))
	}
	if len(likely) > 0 {
		body = append(body, nodepackGroup(
			"Likely provided by",
			"Matched by a name pattern rather than an exact node list — treat this as a "+
				"strong hint, not a certainty. Check the repository before installing.",
			likely, attr.ManagerPresent, wfID, csrf, comfyRoot))
	}
	if len(unattributed) > 0 {
		body = append(body, unattributedNodesSection(unattributed))
	}
	// Stable container for the install confirmation / progress / result and the
	// restart control. Empty until the user acts.
	body = append(body, h.Div(h.ID(nodepackStatusContainerID)))
	if attr.ManagerPresent {
		body = append(body, h.Div(h.Class("pt-1"), restartComfyButton(csrf)))
	}
	return h.Div(h.Class("mt-2 space-y-2"), g.Group(body))
}

// nodePackEgressNotice renders the data-egress disclosure, in the same spirit as
// the scan's remote-match notice. It has TWO states because the egress has an
// opt-out (`resolve_node_packs`), and a panel that named two third-party hosts
// while the lookups were switched off would be lying about what the app did.
//
// on  → Manager first (loopback), then the two public indexes for what it missed.
// off → Manager only; nothing left the machine, and that is why some classes
//
//	below are unattributed. It names the key so the state is reversible.
func nodePackEgressNotice(remote bool) g.Node {
	if !remote {
		return h.P(h.Class("text-xs text-slate-500"),
			g.Text("Pack names come from ComfyUI-Manager's local index only. The online lookups "+
				"(api.comfy.org, raw.githubusercontent.com) are turned off by "+
				"resolve_node_packs: false, so nothing was sent off this machine — "+
				"node types Manager cannot place stay unmatched."))
	}
	return h.P(h.Class("text-xs text-slate-500"),
		g.Text("Pack names come from ComfyUI-Manager's local index; node types it cannot place "+
			"are looked up against api.comfy.org and raw.githubusercontent.com "+
			"(resolve_node_packs: false turns that off)."))
}

// rankedPack is one attributed pack plus its role in a CONTESTED class — i.e.
// a class more than one pack claims.
//
// The role travels attached to the pack rather than being recomputed per render
// site, because it is derived from the WHOLE candidate set: it cannot be decided
// from one pack in isolation, and re-deriving it inside each group would silently
// give a different answer once the set is split by confidence rung.
type rankedPack struct {
	Pack comfy.Pack
	// Contested is true when at least one of this pack's classes is also claimed
	// by another pack in the same attribution.
	Contested bool
	// Best is true when this pack is the TOP-RANKED claimant of at least one
	// contested class. comfy.sortPacks has already ordered the slice, so "top" is
	// simply "seen first".
	Best bool
}

// rankPacks annotates the (already comfy-ranked) packs with their contest roles.
//
// 🔴 It preserves the incoming ORDER exactly and never removes an entry. The
// ranking is a heuristic over a third-party index, so the user must be able to see
// and choose every candidate; all this adds is a label saying which one the
// evidence favours and why.
func rankPacks(packs []comfy.Pack) []rankedPack {
	claimants := map[string]int{}
	for _, p := range packs {
		for _, c := range p.Classes {
			claimants[c]++
		}
	}

	out := make([]rankedPack, 0, len(packs))
	// taken records the contested classes already assigned a best claimant. Because
	// packs arrives in ranked order, the FIRST pack to reach a contested class is
	// the best-scoped one for it.
	taken := map[string]bool{}
	for _, p := range packs {
		rp := rankedPack{Pack: p}
		for _, c := range p.Classes {
			if claimants[c] < 2 {
				continue
			}
			rp.Contested = true
			if !taken[c] {
				taken[c] = true
				rp.Best = true
			}
		}
		out = append(out, rp)
	}
	return out
}

// contestedClasses lists every class claimed by more than one pack, sorted.
func contestedClasses(ranked []rankedPack) []string {
	claimants := map[string]int{}
	for _, rp := range ranked {
		for _, c := range rp.Pack.Classes {
			claimants[c]++
		}
	}
	var out []string
	for c, n := range claimants {
		if n > 1 {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// ambiguityNotice states, in plain words, that a node type has more than one
// claiming pack — and that the list is ORDERED rather than authoritative.
//
// 🔴 Presenting several claimants as equals is what made this dangerous: installing
// the wrong one writes an unrelated pack (93 node types, in the measured case) into
// the user's ComfyUI. Saying "two packs claim this" costs one line and turns a
// silent coin-flip into an informed choice. It returns nil when nothing is
// contested, so the ordinary single-claimant panel is unchanged.
func ambiguityNotice(ranked []rankedPack) g.Node {
	contested := contestedClasses(ranked)
	if len(contested) == 0 {
		return nil
	}
	msg := "More than one pack claims " + joinAnd(contested) + ". " +
		"They are listed best match first — ranked by how much of each pack is what you " +
		"actually need — but this is a guess from ComfyUI-Manager's index, not a certainty. " +
		"Check the repository before installing."
	return h.P(g.Attr("role", "status"), h.Class("text-xs text-amber-400"), g.Text(msg))
}

// joinAnd renders a list as "a", "a and b", or "a, b and c".
func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// splitRankedByConfidence separates the exact-match rungs (Manager's enumerated
// class list, the Comfy Registry) from the nodename_pattern rung, which is the
// weakest and must never read as certain.
func splitRankedByConfidence(packs []rankedPack) (confident, likely []rankedPack) {
	for _, p := range packs {
		if p.Pack.Source == comfy.SourcePattern {
			likely = append(likely, p)
			continue
		}
		confident = append(confident, p)
	}
	return confident, likely
}

// managerStateNote explains an absent/degraded ComfyUI-Manager. Present and fully
// capable → nil (no note; the Install buttons speak for themselves). Manager's own
// note text is escaped like any other untrusted string.
func managerStateNote(attr nodeAttribution) g.Node {
	if attr.ManagerPresent {
		if attr.ManagerNote == "" {
			return nil
		}
		return h.P(h.Class("text-xs text-slate-500"), g.Text(attr.ManagerNote))
	}
	note := attr.ManagerNote
	if note == "" {
		note = "ComfyUI-Manager's web API did not answer, so nothing can be installed from here. " +
			"Install the packs by hand with the commands below."
	}
	return h.P(g.Attr("role", "status"), h.Class("text-xs text-amber-400"), g.Text(note))
}

// nodepackGroup renders one confidence rung: a heading, an optional caveat, and
// one card per pack.
func nodepackGroup(title, caveat string, packs []rankedPack, managerPresent bool, wfID int64, csrf, comfyRoot string) g.Node {
	body := []g.Node{
		h.Div(h.Class("text-xs font-semibold text-slate-300"), g.Text(title)),
	}
	if caveat != "" {
		body = append(body, h.P(h.Class("text-xs text-slate-500"), g.Text(caveat)))
	}
	for _, p := range packs {
		body = append(body, nodepackCard(p, managerPresent, wfID, csrf, comfyRoot))
	}
	return h.Div(h.Class("mt-2 space-y-2"), g.Group(body))
}

// nodepackCard is one attributed pack: its title, the repository URL as VISIBLE
// text (a scheme-validated external link at most — never a bare untrusted href),
// which missing classes it provides, either an Install button or the plain reason
// it cannot be installed, and always the manual command.
func nodepackCard(rp rankedPack, managerPresent bool, wfID int64, csrf, comfyRoot string) g.Node {
	p := rp.Pack
	head := []g.Node{h.Span(h.Class("font-semibold"), g.Text(packDisplayTitle(p)))}
	if p.Version != "" {
		head = append(head, g.Text(" · "), h.Span(h.Class("font-mono"), g.Text(p.Version)))
	}
	if label := contestLabel(rp); label != "" {
		head = append(head, g.Text(" · "), h.Span(h.Class("font-semibold text-amber-400"), g.Text(label)))
	}

	body := []g.Node{
		h.Class("rounded border border-slate-800 p-2 space-y-1"),
		h.Div(h.Class("text-xs text-slate-300"), g.Group(head)),
		repositoryLine(p.Repository),
		h.Div(h.Class("text-xs text-slate-400"),
			g.Text("Provides: "),
			h.Span(h.Class("font-mono break-all text-slate-300"),
				g.Text(strings.Join(p.Classes, ", "))),
		),
	}
	// The scope line is the EVIDENCE for the ordering. Rendering the ranking without
	// it leaves the user with an unexplained verdict; "1 of 4" versus "1 of 93" lets
	// them check the reasoning and overrule it.
	if line := packScopeLine(p); line != "" {
		body = append(body, h.Div(h.Class("text-xs text-slate-500"), g.Text(line)))
	}

	switch {
	case !managerPresent:
		// No live Manager: no install affordance at all, by design. The manual
		// command below is the whole answer.
	case p.Installable:
		// 🔴 A lower-ranked claimant of a contested class keeps a WORKING Install
		// button, but not an equally loud one. Dropping it would make the ranking
		// authoritative, which it is not; leaving it `filled` beside the best match is
		// what shipped the coin-flip.
		variant := "filled"
		if rp.Contested && !rp.Best {
			variant = "outline"
		}
		body = append(body, h.Div(h.Class("pt-1"), nodepackInstallButton(p, wfID, csrf, variant)))
	default:
		body = append(body, h.P(h.Class("text-xs text-amber-400"), g.Text(packBlockedReason(p))))
	}
	body = append(body, manualInstallBlock(p, comfyRoot))
	return h.Div(body...)
}

// contestLabel is the short badge distinguishing the favoured claimant of a
// contested class from the alternatives. Empty when nothing is contested, so an
// uncontested pack renders exactly as before.
func contestLabel(rp rankedPack) string {
	switch {
	case !rp.Contested:
		return ""
	case rp.Best:
		return "Best match"
	}
	return "Also claims it"
}

// packScopeLine explains, in the pack's own numbers, why it ranked where it did:
// how much of what this pack installs is what the workflow actually asked for.
// Empty when the pack's surface is unknown (see comfy.Pack.ClaimedClasses) — an
// unmeasured pack must not be given a fabricated justification.
func packScopeLine(p comfy.Pack) string {
	if p.ClaimedClasses <= 0 {
		return ""
	}
	return "This pack provides " + strconv.Itoa(p.ClaimedClasses) + " node type" +
		plural(p.ClaimedClasses) + " in total; " + strconv.Itoa(len(p.Classes)) +
		" of them " + isAre(len(p.Classes)) + " what this workflow needs."
}

// packDisplayTitle is the pack's human label, never empty.
func packDisplayTitle(p comfy.Pack) string {
	for _, v := range []string{p.Title, p.ID, p.Repository} {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return "Unknown node pack"
}

// packBlockedReason is the user-facing explanation for a pack that cannot be
// installed automatically. comfy.Pack.Reason is always set when Installable is
// false, but a generic fallback keeps the UI from ever rendering an empty "why".
func packBlockedReason(p comfy.Pack) string {
	if r := strings.TrimSpace(p.Reason); r != "" {
		return r
	}
	return "ComfyUI-Manager cannot install this pack automatically. Install it by hand from the repository URL."
}

// repositoryLine renders the repo URL as VISIBLE text — as an external link when
// the URL is a safe http(s) URL, and as inert text otherwise. A non-http scheme
// (javascript:, data:, …) NEVER becomes an href: the URL comes from a third-party
// index we do not control.
func repositoryLine(repo string) g.Node {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return h.Div(h.Class("text-xs text-slate-500"),
			g.Text("No repository URL is known for this pack."))
	}
	if !isSafeHTTPURL(repo) {
		return h.Div(h.Class("text-xs text-slate-400"),
			g.Text("Repository: "),
			h.Span(h.Class("font-mono break-all text-slate-300"), g.Text(repo)),
		)
	}
	return h.Div(h.Class("text-xs text-slate-400"),
		g.Text("Repository: "),
		h.A(
			h.Href(repo),
			h.Target("_blank"),
			h.Rel("noopener noreferrer"),
			h.Class("font-mono break-all text-indigo-400 hover:underline"),
			g.Text(repo),
		),
	)
}

// manualInstallBlock is the always-present copy-able manual install command. It
// is the answer when ComfyUI-Manager is absent, when the pack is policy-blocked,
// and when the user simply declines the automatic install.
func manualInstallBlock(p comfy.Pack, comfyRoot string) g.Node {
	cmd, ok := manualInstallCommand(p, comfyRoot)
	if !ok {
		return h.P(h.Class("text-xs text-slate-500"),
			g.Text("This pack has no usable repository URL, so there is no command to run. "+
				"Search for it by name in ComfyUI-Manager."))
	}
	return h.Div(h.Class("pt-1"),
		h.Div(h.Class("text-xs text-slate-500"), g.Text("Or install it by hand:")),
		h.Pre(h.Class("mt-1 overflow-x-auto rounded border border-slate-800 p-2"),
			h.Code(h.Class("font-mono text-xs text-slate-300"), g.Text(cmd)),
		),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Then restart ComfyUI. Some packs also need their requirements.txt installed "+
				"into ComfyUI's Python environment.")),
	)
}

// manualInstallCommand builds the git-clone command for a pack, or reports that
// it cannot be built safely.
//
// SECURITY: the repository URL is third-party data and the output is a command
// the user is invited to paste into a shell. A URL carrying a quote, a space or
// any other shell metacharacter would turn a helpful command into an injection,
// so BOTH interpolated values must be shell-safe AND are single-quoted anyway.
// An unsafe comfy_root degrades to the placeholder path rather than to a
// dangerous command.
func manualInstallCommand(p comfy.Pack, comfyRoot string) (string, bool) {
	repo := strings.TrimSpace(p.Repository)
	if repo == "" || !isSafeHTTPURL(repo) || !isShellSafeArg(repo) {
		return "", false
	}
	root := strings.TrimSpace(comfyRoot)
	if root == "" || !isShellSafeArg(root) {
		root = comfyRootPlaceholder
	}
	return "cd '" + root + "/custom_nodes' && git clone '" + repo + "'", true
}

// isShellSafeArg reports whether v may be interpolated into a single-quoted shell
// word. It is deliberately a strict ALLOWLIST of characters that appear in real
// repository URLs and filesystem paths: anything else (a quote, backtick, dollar,
// backslash, whitespace, control byte, or non-ASCII) is refused rather than
// escaped, because the value is untrusted and the output is meant to be pasted
// into a shell.
func isShellSafeArg(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("-._~:/?#[]@!()*+,;=%", r):
		default:
			return false
		}
	}
	return true
}

// nodepackInstallButton is the FIRST click of the two-click install: it asks the
// server for a confirmation naming the pack and its repository URL. Nothing is
// installed by this request.
//
// The pack is identified by (id, repository) rather than by a positional index:
// the server re-derives the pack from its own attribution snapshot and never
// takes a repository URL from the request, so a forged POST cannot drive
// ComfyUI-Manager at an arbitrary repository.
func nodepackInstallButton(p comfy.Pack, wfID int64, csrf, variant string) g.Node {
	return civButton(variant, "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/nodepacks/install"),
		hx("target", "#"+nodepackStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		hx("vals", nodepackVals(csrf, p, false)),
	}, g.Text("Install "+packDisplayTitle(p)))
}

// nodepackVals is the hx-vals payload identifying ONE pack. json.Marshal escapes
// the untrusted id/repository, and the attribute itself is escaped on render.
// confirm=1 is what turns the second click into an actual install.
func nodepackVals(csrf string, p comfy.Pack, confirm bool) string {
	vals := map[string]string{
		"csrf_token": csrf,
		"pack_id":    p.ID,
		"pack_repo":  p.Repository,
	}
	if confirm {
		vals["confirm"] = "1"
	}
	b, _ := json.Marshal(vals)
	return string(b)
}

// nodepackConfirmFragment is what the first click renders: exactly what will run,
// naming the pack and showing its repository URL, with an explicit confirm. There
// is no "install all" and no transitive install — one pack, one confirmation.
func nodepackConfirmFragment(p comfy.Pack, wfID int64, csrf string) g.Node {
	return h.Div(h.Class("mt-2 space-y-1 rounded border border-slate-800 p-2"),
		h.P(g.Attr("role", "status"), h.Class("text-xs font-semibold text-amber-400"),
			g.Text("Install "+packDisplayTitle(p)+"?")),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("ComfyUI-Manager will download and install this pack into your ComfyUI, "+
				"and may run its Python requirements. It comes from:")),
		repositoryLine(p.Repository),
		h.Div(h.Class("pt-1"),
			civButton("filled", "sm", []g.Node{
				h.Type("button"),
				hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/nodepacks/install"),
				hx("target", "#"+nodepackStatusContainerID),
				hx("swap", "innerHTML"),
				hx("disabled-elt", "this"),
				hx("vals", nodepackVals(csrf, p, true)),
			}, g.Text("Yes, install it")),
		),
	)
}

// nodepackPoller is the one-shot re-arming poll element driving the install's
// running view to its terminal state. Like the run poller it NEVER targets
// itself: it swaps the innerHTML of the stable #nodepack-status container. The
// terminal fragment carries no poller, so polling stops.
func nodepackPoller() g.Node {
	return h.Div(
		h.ID("nodepack-poll"),
		hx("get", "/workflows/nodepacks/status"),
		hx("trigger", "load delay:1s"),
		hx("target", "#"+nodepackStatusContainerID),
		hx("swap", "innerHTML"),
	)
}

// nodepackStatusFragment renders the install job's current state into
// #nodepack-status: the in-flight view (spinner + progress lines + poller) while
// running, else the settled result. An absent job renders nothing.
func nodepackStatusFragment(snap nodepackSnapshot, csrf string) g.Node {
	if !snap.Started {
		return h.Div()
	}
	lines := make([]g.Node, 0, len(snap.Lines))
	for _, l := range snap.Lines {
		lines = append(lines, h.Li(h.Class("text-xs text-slate-400 break-all"), g.Text(l)))
	}
	body := []g.Node{
		h.Div(h.Class("text-xs font-semibold text-slate-200"),
			g.Text("Installing "+snap.Title)),
		h.Ul(h.Class("list-disc pl-5 space-y-0.5"), g.Group(lines)),
	}
	if snap.Running {
		body = append(body,
			h.Div(h.Class("flex items-center gap-2 text-sm text-slate-300"),
				spinnerGlyph(), g.Text(snap.Message)),
			nodepackPoller(),
		)
		return h.Div(h.Class("mt-2 space-y-2 rounded border border-slate-800 p-2"), g.Group(body))
	}
	return h.Div(h.Class("mt-2 space-y-2 rounded border border-slate-800 p-2"),
		g.Group(body), nodepackTerminal(snap, csrf))
}

// nodepackTerminal renders the settled install: an honest success (which ALWAYS
// says a restart is required) or an honest failure that falls back to the manual
// command. It never claims an install it did not observe in ComfyUI-Manager's own
// installed-set diff.
func nodepackTerminal(snap nodepackSnapshot, csrf string) g.Node {
	if !snap.Installed {
		body := []g.Node{
			h.P(g.Attr("role", "status"), h.Class("text-xs text-amber-400"), g.Text(snap.Message)),
		}
		if snap.ManualCmd != "" {
			body = append(body,
				h.Div(h.Class("text-xs text-slate-500"), g.Text("Install it by hand instead:")),
				h.Pre(h.Class("mt-1 overflow-x-auto rounded border border-slate-800 p-2"),
					h.Code(h.Class("font-mono text-xs text-slate-300"), g.Text(snap.ManualCmd)),
				),
			)
		}
		return h.Div(h.Class("space-y-1"), g.Group(body))
	}
	return h.Div(h.Class("space-y-1"),
		h.P(g.Attr("role", "status"), h.Class("text-xs text-emerald-400"), g.Text(snap.Message)),
		h.Div(h.Class("pt-1"), restartComfyButton(csrf)),
	)
}

// restartComfyButton is the explicit, never-automatic ComfyUI restart. The server
// refuses it while ComfyUI's queue is busy (Manager's own reboot does os.execv
// with no queue inspection and would destroy a running generation), and that
// refusal surfaces as its own plain message.
func restartComfyButton(csrf string) g.Node {
	return civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/nodepacks/restart"),
		hx("target", "#"+nodepackStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		csrfInline(csrf),
	}, g.Text("Restart ComfyUI"))
}

// nodepackNote renders a one-off message into #nodepack-status (a refusal, a
// restart outcome, an unavailable control). color is a civitai alert intent.
func nodepackNote(color, text string) g.Node {
	return h.Div(h.Class("mt-2"), alert(color, "", g.Text(text)))
}

// unattributedNodesSection names every class nothing could place, with a code
// search link for each.
//
// This is NOT a failure state and must not look like one: on the design's real
// target workflow 4 of 7 missing classes land here. Naming the class and handing
// the user a search that actually finds the repository is the useful answer.
func unattributedNodesSection(classes []string) g.Node {
	rows := make([]g.Node, 0, len(classes))
	for _, c := range classes {
		rows = append(rows, h.Li(
			h.Class("flex flex-wrap items-center gap-2"),
			h.Span(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(c)),
			nodeClassSearchLink(c),
		))
	}
	return h.Div(h.Class("mt-2 space-y-1"),
		h.Div(h.Class("text-xs font-semibold text-slate-300"), g.Text("Not matched to a pack")),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("No index we consulted claims these node types. That is common — many packs "+
				"are not listed. Search for the class name to find the repository that defines it, "+
				"then install that repository by hand.")),
		h.Ul(h.Class("list-disc pl-5 space-y-0.5"), g.Group(rows)),
	)
}

// nodeClassSearchLink is a GitHub code search for the exact class name — the
// practical way to find which repository defines an unlisted ComfyUI node. The
// class name is URL-encoded into the query and escaped in the label.
func nodeClassSearchLink(class string) g.Node {
	q := url.Values{}
	q.Set("q", `"`+class+`"`)
	q.Set("type", "code")
	return h.A(
		h.Href("https://github.com/search?"+q.Encode()),
		h.Target("_blank"),
		h.Rel("noopener noreferrer"),
		h.Class("text-xs text-indigo-400 hover:underline"),
		g.Text("Search GitHub"),
	)
}
