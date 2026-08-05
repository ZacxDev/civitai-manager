package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/hf"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// resolveTypeWhitelist is the set of CivitAI models-list `types` values the
// resolve endpoint accepts as a filter. Anything else (including a hostile
// arbitrary string) is treated as "no filter" so the `type` query param can
// never smuggle an arbitrary value into the outbound civitai.com request.
var resolveTypeWhitelist = map[string]bool{
	"Checkpoint":       true,
	"LORA":             true,
	"LoCon":            true,
	"VAE":              true,
	"Controlnet":       true,
	"TextualInversion": true,
	"Upscaler":         true,
	"Hypernetwork":     true,
}

// civitaiTypeParam validates an incoming `type` query value against the
// whitelist. An unknown value yields "" (the caller then omits `types=`).
func civitaiTypeParam(v string) string {
	if resolveTypeWhitelist[strings.TrimSpace(v)] {
		return strings.TrimSpace(v)
	}
	return ""
}

// resolveModels runs (or serves from a TTL cache) a bounded CivitAI model search
// for a resolution query + optional type filter. Keyed by (query, type, nsfw
// flag). On any error it returns nil so the fragment degrades to the fallback
// link. The models list API filters by `types` PLURAL — singular `type=` is
// silently ignored — so the type filter is always set as `types`.
func (s *Server) resolveModels(parent context.Context, query, typ string) *civitai.ModelSearchResult {
	nsfw := s.nsfwSearchFlag()
	key := query + "\x00" + typ + "\x00" + strconv.FormatBool(nsfw)

	s.resolveMu.Lock()
	if v, ok := s.resolveVal[key]; ok && time.Now().Before(s.resolveExp[key]) {
		s.resolveMu.Unlock()
		return v
	}
	s.resolveMu.Unlock()

	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", resolveLimit)
	if typ != "" {
		q.Set("types", typ) // PLURAL — see comment above.
	}
	setNSFWParam(q, nsfw)
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.log.Warn("resolve models fetch failed", "query", query, "type", typ, "err", err)
		return nil
	}
	s.resolveMu.Lock()
	s.pruneResolveCacheLocked()
	s.resolveVal[key] = res
	s.resolveExp[key] = time.Now().Add(popularTTL)
	s.resolveMu.Unlock()
	return res
}

// resolveCacheMax caps the number of live entries in the resolution TTL cache.
// Each distinct (query,type,nsfw) key adds an entry; without a bound a long-lived
// server that resolves many distinct filenames would grow the map without limit
// (audit nit). The cap is generous — a session rarely resolves this many models.
const resolveCacheMax = 256

// pruneResolveCacheLocked evicts expired entries and, if still over the cap,
// clears the whole cache (simplest bound — the entries are cheap to refetch and
// re-caching is immediate). The caller MUST hold resolveMu.
func (s *Server) pruneResolveCacheLocked() {
	now := time.Now()
	for k, exp := range s.resolveExp {
		if now.After(exp) {
			delete(s.resolveExp, k)
			delete(s.resolveVal, k)
		}
	}
	if len(s.resolveVal) >= resolveCacheMax {
		s.resolveVal = map[string]*civitai.ModelSearchResult{}
		s.resolveExp = map[string]time.Time{}
	}
}

// resolveLimit bounds a resolution search: the user only needs a few candidates
// to disambiguate which model is theirs.
const resolveLimit = "5"

// resolveMaxCards caps how many model-match cards a resolve fragment renders.
const resolveMaxCards = 5

// resolveModelFragment renders the resolution result for a FIRST display (the GET
// panel) — no action has happened, so there is nothing to explain.
func resolveModelFragment(query string, res *civitai.ModelSearchResult, mr maturityRange) g.Node {
	return resolveModelFragmentWithReason(query, res, mr, "")
}

// resolveModelFragmentWithReason renders the resolution result: an optional
// leading REASON line, a heuristic-match note, up to resolveMaxCards model cards
// (deep-linking to the in-app model page), and an always-present "Search CivitAI
// for '<query>'" fallback link. Zero matches (or an empty query) renders only the
// note + fallback link. Every untrusted string (model names via modelCardCore, the
// query) is escaped.
//
// reason MUST be non-empty whenever this fragment is the ANSWER TO A CLICK. Without
// it the response is byte-identical to the panel the user was already looking at,
// which is indistinguishable from a dead button — exactly the failure the
// filename-resolving primary CTA used to produce. Reasons are the resolveReason*
// constants in run_download.go.
func resolveModelFragmentWithReason(query string, res *civitai.ModelSearchResult, mr maturityRange, reason string) g.Node {
	var body []g.Node
	if strings.TrimSpace(reason) != "" {
		body = append(body, h.P(
			g.Attr("role", "status"),
			h.Class("text-xs font-semibold text-amber-400 mb-2"),
			g.Text(reason),
		))
	}
	body = append(body,
		h.P(h.Class("text-xs text-slate-400 mb-2"),
			g.Text("Matched from the filename — verify this is the model you want.")),
	)

	if res != nil && len(res.Items) > 0 {
		items := res.Items
		if len(items) > resolveMaxCards {
			items = items[:resolveMaxCards]
		}
		images := parseSearchImages(res.Raw)
		updated := newestVersionInfoByModel(res.Raw)
		body = append(body, h.Div(
			h.Class("grid gap-4 sm:grid-cols-2 lg:grid-cols-3"),
			g.Map(items, func(it civitai.ModelListItem) g.Node {
				return modelCardCore(it, images[it.ID], mr, updated[it.ID], nil)
			}),
		))
	} else if query != "" {
		body = append(body, h.P(h.Class("text-xs text-slate-500 mb-2"),
			g.Text("No CivitAI models matched this filename.")))
	}

	body = append(body, resolveFallbackLink(query))
	return h.Div(g.Group(body))
}

// substituteOfferFragment renders the "this is not the file you asked for — install it
// anyway?" confirmation. It is what a first click gets when the chosen model has no
// file matching the workflow's reference: NOTHING has been downloaded at this point.
//
// The confirming button re-posts the SAME target plus confirm_substitute=1, so the
// second click is unambiguous and the server never has to infer intent. The resolve
// cards + search link follow, so choosing a different model stays reachable (the click
// swapped the whole #run-status container, popover included). requested/remote are
// untrusted strings and are escaped via g.Text / json.Marshal.
func substituteOfferFragment(wfID int64, csrf, requested, remote, typ string, modelID int, query string, res *civitai.ModelSearchResult, mr maturityRange) g.Node {
	vals := map[string]string{
		"csrf_token": csrf,
		"filename":   requested,
		"type":       typ,
		// confirm_substitute records THAT a substitution was approved; confirm_file
		// records WHICH ONE. Without the latter the approval is unbound: the second
		// click re-resolves, and if CivitAI promoted a new primary version in between,
		// a different file would install under an approval the user never gave.
		"confirm_substitute": "1",
		"confirm_file":       remote,
	}
	if modelID > 0 {
		vals["model_id"] = strconv.Itoa(modelID)
	}
	b, _ := json.Marshal(vals)
	confirm := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/download-and-run"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		hx("vals", string(b)),
	}, g.Text("Install "+remote+" as "+requested))

	return h.Div(
		h.P(g.Attr("role", "status"), h.Class("text-xs font-semibold text-amber-400 mb-2"),
			g.Text(substituteOfferText(requested, remote))),
		h.Div(h.Class("mb-3"), confirm),
		resolveModelFragment(query, res, mr),
	)
}

// resolveFallbackLink is the always-present deep link into the in-app model
// search for the cleaned query. It is a plain navigation link (the search page
// reads the `q` param).
func resolveFallbackLink(query string) g.Node {
	href := "/search"
	label := "Search CivitAI"
	if query != "" {
		href = "/search?" + url.Values{"q": {query}}.Encode()
		label = "Search CivitAI for \"" + query + "\""
	}
	return h.A(
		h.Href(href),
		h.Class("text-sm text-indigo-400 hover:underline"),
		g.Text(label),
	)
}

// handleWorkflowRunSubstitute starts an EPHEMERAL run of the workflow with ONE
// missing model reference swapped for a chosen installed substitute — the stored
// workflow is never modified. CSRF-protected + loopback-gated (it reaches
// ComfyUI). It respects the one-run-at-a-time invariant (a second call while a
// run is in flight is a no-op via startRunWithMessage).
func (s *Server) handleWorkflowRunSubstitute(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	filename := strings.TrimSpace(r.FormValue("filename"))
	substitute := strings.TrimSpace(r.FormValue("substitute"))
	if filename == "" || substitute == "" {
		http.Error(w, "missing filename or substitute", http.StatusBadRequest)
		return
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	s.renderRunStatus(w, id, s.startRunNotice(wf, runOptions{
		Substitute:    map[string]string{filename: substitute},
		ModeSelection: parseModeChoices(r.Form, wf),
	}))
}

// handleWorkflowRunWithOptions starts an EPHEMERAL run of the workflow with chosen
// incompatible-option fixes applied — each combo's saved (invalid) value swapped for
// a user-picked valid choice — leaving the stored workflow untouched. CSRF-protected
// + loopback-gated (it reaches ComfyUI). It respects the one-run-at-a-time invariant
// (a second call while a run is in flight is a no-op via startRunWithMessage). The chosen values
// are re-validated against the live object_info's real choices inside the run (an
// off-list value is refused there), so this handler only parses the form.
func (s *Server) handleWorkflowRunWithOptions(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	wf, err := s.store.GetWorkflow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.renderError(w, "load workflow", err)
		return
	}
	s.renderRunStatus(w, id, s.startRunNotice(wf, runOptions{
		OptionFixes:   parseOptionFixes(r.Form),
		ModeSelection: parseModeChoices(r.Form, wf),
	}))
}

// parseOptionFixes reads the parallel opt_input / opt_old / opt_new form arrays (one
// entry per Incompatible-options group, index-aligned in DOM order) into a fix map
// keyed by (input name, old value). An empty chosen value (an unmade selection) is
// skipped; server-side ValidateOptionFixes still rejects any off-list value before it
// is injected, so this is a lenient parse.
func parseOptionFixes(form url.Values) map[comfy.OptionFixKey]string {
	inputs, olds, news := form["opt_input"], form["opt_old"], form["opt_new"]
	n := len(inputs)
	if len(olds) < n {
		n = len(olds)
	}
	if len(news) < n {
		n = len(news)
	}
	if n == 0 {
		return nil
	}
	fixes := make(map[comfy.OptionFixKey]string, n)
	for i := 0; i < n; i++ {
		newVal := strings.TrimSpace(news[i])
		if newVal == "" {
			continue
		}
		fixes[comfy.OptionFixKey{InputName: inputs[i], OldValue: olds[i]}] = newVal
	}
	return fixes
}

// missingResolution is the eager, at-settle CivitAI resolution for ONE missing
// model filename. Reached distinguishes "reached CivitAI, no match" (Reached=true,
// Result empty) from "couldn't reach CivitAI" (Reached=false) so the popover renders
// the right degraded state. Computed ONCE at run settle — never per poll.
type missingResolution struct {
	Result  *civitai.ModelSearchResult
	Reached bool
	// HF is the HuggingFace fallback match, resolved ONLY when CivitAI yielded no
	// items (nil otherwise). HFInstallEligible records whether it may be auto-installed
	// here (curated/recognized-org + non-gated + exact + sha + comfy_model_path set +
	// routable subdir) vs shown as an "Open on HuggingFace" link only. Both are
	// computed once at run settle — never per poll.
	HF                *hf.Match
	HFInstallEligible bool
	// NoteLinks are the download URLs THIS WORKFLOW'S OWN Note/MarkdownNote nodes
	// give for this exact filename (exact-basename match). Unlike the two fields
	// above they cost NO network work — extraction and matching are pure — and
	// unlike them they are computed from the UI graph, because conversion drops
	// note nodes. See note_links.go. Nil when the notes name no such file.
	NoteLinks []noteLinkOffer
}

// missingResolveBudget bounds the WHOLE at-settle resolution pass so N missing
// models cannot stack N×(per-call timeout). An unreachable CivitAI degrades to the
// "couldn't reach" state instead of hanging the terminal render.
const missingResolveBudget = 20 * time.Second

// fixAltCap caps how many additional CivitAI matches (beyond the primary) the
// popover offers as pickable alternates.
const fixAltCap = 3

// fixLibHeadCap bounds how many library substitute cards are shown up-front when
// there is no same-base match (the rest collapse into a <details>).
const fixLibHeadCap = 6

// resolveMissingModels eagerly resolves every missing model to CivitAI and enriches
// the installed substitute candidates with local model metadata — ONCE, at run
// settle (while /object_info is in hand). It is bounded by missingResolveBudget so
// an unreachable CivitAI degrades gracefully rather than hanging the render. Returns
// (per-filename resolution, shared basename→local-meta). It must NOT be called from
// a render/poll path (it makes outbound API calls).
func (s *Server) resolveMissingModels(parent context.Context, models []comfy.MissingModel) (map[string]missingResolution, map[string]store.LocalModelMeta) {
	ctx, cancel := context.WithTimeout(parent, missingResolveBudget)
	defer cancel()

	res := make(map[string]missingResolution, len(models))
	for _, mm := range models {
		if _, done := res[mm.Filename]; done {
			continue
		}
		if mm.Query == "" {
			res[mm.Filename] = missingResolution{Reached: true} // nothing searchable
			continue
		}
		r := s.resolveModels(ctx, mm.Query, civitaiTypeParam(mm.CivitaiType))
		// resolveModels returns nil ONLY on an API error/timeout — treat that as
		// "couldn't reach"; a reached-but-empty search returns a non-nil empty result.
		mr := missingResolution{Result: r, Reached: r != nil}
		// HuggingFace fallback: ONLY when CivitAI reached but found no items. A
		// CivitAI hit or an unreachable CivitAI keeps the existing behavior.
		if r != nil && len(r.Items) == 0 {
			if m := s.resolveHF(ctx, mm.Filename); m != nil {
				mr.HF = m
				mr.HFInstallEligible = s.hfInstallEligible(m)
			}
		}
		res[mm.Filename] = mr
	}

	var names []string
	for _, mm := range models {
		names = append(names, mm.SameBase...)
		names = append(names, mm.OtherCandidates...)
	}
	libMeta, err := s.store.LocalModelMetaByBasenames(names)
	if err != nil {
		s.log.Warn("enrich substitute candidates failed", "err", err)
		libMeta = map[string]store.LocalModelMeta{}
	}
	return res, libMeta
}

// missingModelsPanel renders the "Missing model files" panel: one compact row per
// missing file (filename + a "Fix" button opening THAT model's popover), each with
// its own native <dialog>. The popover offers the auto-resolved CivitAI match
// ("Install and run") and the installed-library substitutes ("Use this & run").
// Every untrusted string (filename, model name, choice string) is escaped via
// g.Text / attribute-escaping.
func missingModelsPanel(models []comfy.MissingModel, resolved map[string]missingResolution, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	rows := make([]g.Node, 0, len(models))
	for i, mm := range models {
		rows = append(rows, fixModelRow(i, mm, resolved[mm.Filename], libMeta, wfID, csrf, dlEligible, mr))
	}
	return h.Div(h.Class("mt-2 space-y-2"),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Missing model files")),
		// The per-file path is explicitly the SECONDARY one now: the panel above offers a
		// single action for the whole set, and this line says what these rows are for so
		// the two are not read as competing options.
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("Or handle them one at a time — pick a CivitAI match, or swap in a model you already have.")),
		g.Group(rows),
	)
}

// fixModelDialogID builds the id of the per-missing-model Fix dialog.
func fixModelDialogID(idx int) string { return "fix-model-" + strconv.Itoa(idx) }

// fixModelRow is one missing file: a compact filename + "Fix" row plus that file's
// (hidden) Fix <dialog>. The dialog lives inside the terminal run-status fragment,
// which carries NO poller (the run poll stops on the terminal state), so a later
// swap can never nuke an open popover.
func fixModelRow(idx int, mm comfy.MissingModel, res missingResolution, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	dlgID := fixModelDialogID(idx)
	// The label says what the click DOES. It used to read "Fix", which named neither
	// the outcome nor the difference from the other "Fix" beside it — with two missing
	// files the panel showed two identical buttons and no way to tell them apart or to
	// know that pressing one opens a chooser rather than starting a download.
	fixBtn := civButton("outline", "sm", []g.Node{
		h.Type("button"),
		// Inline open — no external script (offline invariant forbids EXTERNAL
		// scripts/styles only). The dialog id is a constant, not user input.
		g.Attr("onclick", "document.getElementById('"+dlgID+"').showModal()"),
		g.Attr("aria-label", "Choose a model for "+mm.Filename),
	}, g.Text("Choose a model…"))
	row := h.Div(
		h.Class("flex flex-wrap items-center justify-between gap-2 rounded border border-slate-800 p-2"),
		h.Div(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(mm.Filename)),
		fixBtn,
	)
	return h.Div(row, fixModelDialog(dlgID, mm, res, libMeta, wfID, csrf, dlEligible, mr))
}

// fixModelDialog is the chrome-less native <dialog> (transparent shell around a
// theme-aware card) holding the two labeled Fix sections. It mirrors the
// workflowImportPanel dialog pattern.
func fixModelDialog(dlgID string, mm comfy.MissingModel, res missingResolution, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	return h.Dialog(
		h.ID(dlgID),
		h.Class("bg-transparent p-0 border-0 w-full max-w-3xl"),
		card(
			h.Div(h.Class("flex items-center justify-between gap-4 mb-3"),
				h.H2(h.Class("text-lg font-semibold text-slate-100 break-all"),
					g.Text("Choose a model for "+mm.Filename)),
				h.Form(h.Method("dialog"), h.Class("inline"),
					civButton("subtle", "sm", []g.Node{h.Type("submit"),
						g.Attr("aria-label", "Close")}, g.Text("✕"))),
			),
			civitaiMatchSection(mm, res, wfID, csrf, dlEligible, mr),
			// The workflow's own notes sit BETWEEN the two existing sources on
			// purpose: they are more specific than a CivitAI title search (the author
			// named this exact file) and less immediately usable than a model already
			// on disk. noteLinkSection renders nothing at all when the notes name no
			// matching file, so an ordinary dialog is byte-identical to before.
			noteLinkSection(dlgID, mm, res.NoteLinks, wfID, csrf, dlEligible),
			h.Div(h.Class("mt-6"), librarySubstituteSection(mm, libMeta, wfID, csrf, mr)),
		),
	)
}

// civitaiMatchSection is the "Use matched model from CivitAI" section: the best
// auto-resolved match as a primary card (with an Install-and-run CTA), up to
// fixAltCap smaller pickable alternates, a clear zero-match state with a Search
// link, and a "couldn't reach CivitAI" degraded state.
func civitaiMatchSection(mm comfy.MissingModel, res missingResolution, wfID int64, csrf string, dlEligible bool, mr maturityRange) g.Node {
	body := []g.Node{
		h.H3(h.Class("text-sm font-semibold text-slate-200"),
			g.Text("Use matched model from CivitAI")),
	}
	switch {
	case !res.Reached:
		body = append(body,
			h.P(h.Class("text-xs text-slate-500 mb-2"),
				g.Text("Could not reach CivitAI to find a match.")),
			resolveFallbackLink(mm.Query),
		)
	case res.Result != nil && len(res.Result.Items) > 0:
		items := res.Result.Items
		images := parseSearchImages(res.Result.Raw)
		updated := newestVersionInfoByModel(res.Result.Raw)
		primary := items[0]
		// Primary: install THIS card's model (its id rides along) — see
		// installAndRunButton for why a card never resolves by filename alone.
		body = append(body, modelCardCore(primary, images[primary.ID], mr, updated[primary.ID],
			installAndRunButton(mm, primary.ID, dlEligible, wfID, csrf)))
		alts := items[1:]
		if len(alts) > fixAltCap {
			alts = alts[:fixAltCap]
		}
		if len(alts) > 0 {
			cards := make([]g.Node, 0, len(alts))
			for _, it := range alts {
				// Alternate: install passes model_id to disambiguate to THIS model.
				cards = append(cards, modelCardCore(it, images[it.ID], mr, updated[it.ID],
					installAndRunButton(mm, it.ID, dlEligible, wfID, csrf)))
			}
			body = append(body,
				h.P(h.Class("text-xs text-slate-400 mt-4 mb-2"), g.Text("Other possible matches:")),
				h.Div(h.Class("grid gap-4 sm:grid-cols-2 lg:grid-cols-3"), g.Group(cards)),
			)
		}
	case res.HF != nil:
		// CivitAI reached but had no match — a HuggingFace fallback match was found.
		body = append(body,
			h.P(h.Class("text-xs text-slate-500 mb-2"), g.Text("No CivitAI match.")),
			hfMatchSection(mm, res.HF, res.HFInstallEligible, wfID, csrf),
		)
	default:
		body = append(body,
			h.P(h.Class("text-xs text-slate-500 mb-2"), g.Text("No CivitAI match.")),
			resolveFallbackLink(mm.Query),
		)
	}
	return h.Div(g.Group(body))
}

// cardInstallBlockedText is the reason under a blocked per-card "Install and run".
//
// 🔴 It names NO config key, and it carries no `title` tooltip. Both were live
// defects: `title` is unreachable by keyboard and has already raced a CSS popover in
// this repo, and "Set comfy_model_path to install here" tells a reader with no
// config.yaml what is wrong in config-file vocabulary while offering nothing to do
// about it — the same dead end blockedInstallAction removed one level up.
//
// 🔴 IT POINTS AT NO CONTROL ON THE PAGE, and that is the fix for a dangling
// cross-reference this rework introduced. The sentence used to read "use the setup
// step at the top of this report", justified by a comment claiming runFailure "has
// already rendered the batch action, and therefore the setup CTA, above it". That
// WAS true while the blocked batch action always rendered comfySetupDisclosure; it
// stopped being true the moment blockedInstallAction split in two, because the
// !SetupCanHelp (remote comfy_url) branch renders no setup control at all.
// Measured on runStatusFragment with ComfyRemote plus one resolved CivitAI match:
// the card text rendered, and neither the setup CTA nor any /comfy-setup control
// was anywhere on the page. That is the ORDINARY failure page for anyone whose
// comfy_url is not local — resolutions are computed automatically at run settle —
// and it also read as a loop, since the remote branch's own next-step line says
// "use the per-file options below" while the per-file option said "use the setup
// step at the top".
//
// The fix is to point at nothing: this says only what is true in EVERY state it can
// render in and leans on the "View on CivitAI ↗" link rendered beside it.
//
// ⚠ This paragraph used to cite badOptionInstallBlockedText (run_pages.go) as the
// precedent that "solved exactly this problem by pointing at nothing". That symbol no
// longer exists, and the surface it served now DOES point at a control:
// failureSetupOwner guarantees the incompatible-options section carries the panel's
// single setup CTA whenever the missing-models section did not, so
// badOptionNeedsSetupText can honestly say "use the setup step above".
//
// 🔴 THAT DOES NOT TRANSFER HERE, and the reason is structural rather than a matter
// of taste: this text renders inside a native <dialog> opened with showModal(), so
// anything "above" is behind the modal and invisible while the reader is looking at
// this sentence. Do not "bring it into line" with the bad-option copy. The remote
// comfy_url state remains a second, independent reason — there is no control to name
// there at all.
//
// Pinned by TestBlockedCardReasonHoldsInBothBlockedStates, which renders the whole
// failure panel in both blocked states and fails if this copy points at a control
// the page does not contain. (The old comment cited
// TestBlockedCardPointsAtTheSetupStepThatIsAlwaysAboveIt — a test that has never
// existed in this repo. Grep before citing.)
const cardInstallBlockedText = "civitai-manager is not set up to install this file for you, " +
	"so download it from CivitAI yourself."

// installAndRunButton renders the "Install and run" CTA for a resolved CivitAI
// model card. When the download-and-run flow is eligible (comfy_model_path +
// local ComfyUI + a routable type) it POSTs the (CSRF-carrying) download-and-run
// request and swaps the stable #run-status container. When NOT eligible it renders
// a DISABLED CTA + a one-line reason + a "View on CivitAI ↗" link (never hidden).
//
// EVERY card — primary and alternate alike — carries its own model_id. A card is a
// SPECIFIC, named, pictured model, so the click must install THAT model; there is
// no reason to make the endpoint re-guess from the filename. The primary used to
// omit model_id and resolve by filename instead, which dead-ended whenever no
// CivitAI file's basename equalled the workflow's reference (the common case —
// checkpoints get renamed across versions): the endpoint resolved nothing, wrote
// nothing, and answered with the SAME panel, so the most prominent button in the
// resolver looked like a no-op while the alternates beside it worked. Do not
// reintroduce a model-id-less card CTA (TestInstallAndRunCTAAlwaysCarriesModelID
// pins this).
func installAndRunButton(mm comfy.MissingModel, modelID int, dlEligible bool, wfID int64, csrf string) g.Node {
	if !(dlEligible && comfyTypeRoutable(mm.CivitaiType)) {
		return h.Div(h.Class("mt-1 space-y-1"),
			h.Div(h.Class("flex flex-wrap items-center gap-2"),
				civButton("filled", "sm", []g.Node{
					h.Type("button"), h.Disabled(),
				}, g.Text("Install and run")),
				viewOnCivitaiLink(civitaiModelURL(modelID)),
			),
			h.P(h.Class("text-xs text-slate-500"), g.Text(cardInstallBlockedText)),
		)
	}
	vals := map[string]string{"csrf_token": csrf, "filename": mm.Filename, "type": mm.CivitaiType}
	if modelID > 0 {
		vals["model_id"] = strconv.Itoa(modelID)
	}
	b, _ := json.Marshal(vals)
	return h.Div(h.Class("mt-1"),
		civButton("filled", "sm", []g.Node{
			h.Type("button"),
			hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/download-and-run"),
			hx("target", "#"+runStatusContainerID),
			hx("swap", "innerHTML"),
			hx("disabled-elt", "this"),
			hx("vals", string(b)),
		}, g.Text("Install and run")),
	)
}

// civitaiModelURL builds the external civitai.com page URL for a model id (bare
// /models when unknown). Used for the "View on CivitAI ↗" fallback link.
func civitaiModelURL(modelID int) string {
	if modelID > 0 {
		return "https://civitai.com/models/" + strconv.Itoa(modelID)
	}
	return "https://civitai.com/models"
}

// librarySubstituteSection is the "Replace with a model from my library" section:
// the installed substitute candidates as model cards (same-base first; the long
// tail collapsed in a <details>), each with a "Use this & run" CTA that swaps the
// missing filename for that installed file via the run-substitute endpoint.
func librarySubstituteSection(mm comfy.MissingModel, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, mr maturityRange) g.Node {
	body := []g.Node{
		h.H3(h.Class("text-sm font-semibold text-slate-200"),
			g.Text("Replace with a model from my library")),
	}
	if len(mm.SameBase) == 0 && len(mm.OtherCandidates) == 0 {
		body = append(body, h.P(h.Class("text-xs text-slate-500 mt-1"),
			g.Text("No installed models available to substitute for this input.")))
		return h.Div(g.Group(body))
	}
	body = append(body, h.P(h.Class("text-xs text-slate-400 mb-2"),
		g.Text("These are already installed in ComfyUI and safe to run.")))

	visible := mm.SameBase
	collapsed := mm.OtherCandidates
	if len(visible) == 0 {
		// No same-base match — surface the first few directly so it isn't all collapsed.
		if len(collapsed) > fixLibHeadCap {
			visible, collapsed = collapsed[:fixLibHeadCap], collapsed[fixLibHeadCap:]
		} else {
			visible, collapsed = collapsed, nil
		}
	}
	body = append(body, libraryCardGrid(visible, mm.Filename, libMeta, wfID, csrf, mr))
	if len(collapsed) > 0 {
		body = append(body, h.Details(h.Class("mt-2"),
			h.Summary(h.Class("cursor-pointer text-xs text-slate-400"),
				g.Text(strconv.Itoa(len(collapsed))+" more installed models")),
			h.Div(h.Class("mt-2"), libraryCardGrid(collapsed, mm.Filename, libMeta, wfID, csrf, mr)),
		))
	}
	return h.Div(g.Group(body))
}

// libraryCardGrid renders a responsive grid of installed-model substitute cards.
func libraryCardGrid(candidates []string, filename string, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, mr maturityRange) g.Node {
	cards := make([]g.Node, 0, len(candidates))
	for _, c := range candidates {
		cards = append(cards, libraryCard(c, filename, libMeta, wfID, csrf, mr))
	}
	return h.Div(h.Class("grid gap-4 sm:grid-cols-2 lg:grid-cols-3"), g.Group(cards))
}

// libraryCard renders ONE installed substitute candidate. When the candidate's
// basename resolves to a CivitAI-linked, cached model it is a rich card (preview
// image + name link + base-model badge); otherwise it is a minimal card (the
// choice string + a base-model guess). Both carry the "Use this & run" CTA.
func libraryCard(candidate, filename string, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, mr maturityRange) g.Node {
	meta, ok := libMeta[strings.ToLower(path.Base(strings.ReplaceAll(candidate, "\\", "/")))]
	useBtn := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/run-substitute"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		substituteVals(csrf, filename, candidate),
	}, g.Text("Use this & run"))

	children := []g.Node{h.Class("flex flex-col gap-2")}
	if ok {
		if img := libraryPreviewImg(meta, mr); img != nil {
			children = append(children, img)
		}
		name := meta.Name
		if strings.TrimSpace(name) == "" {
			name = candidate
		}
		children = append(children, h.A(
			h.Href("/models/"+strconv.Itoa(meta.ModelID)),
			h.Class("font-medium text-indigo-400 hover:underline break-all"),
			g.Text(name),
		))
		if meta.BaseModel != "" {
			children = append(children, h.Div(h.Class("flex"), badge(meta.BaseModel, "slate")))
		}
	} else {
		children = append(children,
			h.Div(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(candidate)))
		if guess := guessBaseModel(candidate); guess != "" {
			children = append(children, h.Div(h.Class("flex"), badge(guess, "slate")))
		}
	}
	children = append(children, h.Div(h.Class("mt-1"), useBtn))
	return card(children...)
}

// libraryPreviewImg renders an installed model's preview thumbnail honoring the
// maturity range: a preview whose level falls outside the band is OMITTED — the
// URL never reaches the DOM — and everything inside renders plain. A preview with
// no parseable level counts as UNKNOWN and is omitted too (fail closed: this is
// CivitAI-sourced metadata whose rating we expected and did not get, unlike the
// user's own outputs, which carry no level by nature and are never filtered).
// Returns nil when there is no image (or it is omitted), so the card gracefully
// has no image slot.
func libraryPreviewImg(meta store.LocalModelMeta, mr maturityRange) g.Node {
	if strings.TrimSpace(meta.ImageURL) == "" {
		return nil
	}
	level := meta.NSFWLevel
	if !meta.NSFWLevelKnown {
		level = browsingLevelUnknown // fail-closed
	}
	if !mr.containsBrowsingLevel(level) {
		return nil // outside the range: OMIT server-side (not just CSS-hidden)
	}
	return h.Img(
		h.Src(civitaiThumbURL(meta.ImageURL, 300)),
		h.Alt("installed model preview"),
		h.Loading("lazy"),
		h.Class("w-full h-32 object-cover rounded border border-slate-800 bg-slate-900"),
	)
}

// guessBaseModel infers a base-model label from a filename when no cached metadata
// is available (a best-effort chip for the minimal library card). "" when unknown.
func guessBaseModel(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	n := b.String()
	switch {
	case strings.Contains(n, "illustrious"), strings.Contains(n, "ilxl"):
		return "Illustrious"
	case strings.Contains(n, "pony"):
		return "Pony"
	case strings.Contains(n, "flux"):
		return "Flux"
	case strings.Contains(n, "sdxl"), strings.Contains(n, "xl"):
		return "SDXL"
	case strings.Contains(n, "sd35"), strings.Contains(n, "sd3"):
		return "SD 3"
	case strings.Contains(n, "sd15"), strings.Contains(n, "sd1"):
		return "SD 1.5"
	}
	return ""
}

// substituteVals builds the hx-vals JSON carrying the CSRF token, the missing
// filename, and the chosen substitute. Using json.Marshal guarantees any quote/
// backslash in an untrusted choice string is escaped; gomponents then HTML-escapes
// the attribute value (matching the repo's csrfInline posture).
func substituteVals(csrf, filename, substitute string) g.Node {
	b, _ := json.Marshal(map[string]string{
		"csrf_token": csrf,
		"filename":   filename,
		"substitute": substitute,
	})
	return hx("vals", string(b))
}
