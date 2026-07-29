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

// handleWorkflowResolveModel returns the "resolve this missing model to CivitAI"
// fragment for one missing filename: up to a handful of heuristic model-match
// cards plus an always-present "Search CivitAI for …" fallback link. GET (no
// state change, no CSRF); loopback-gated like the other comfy-adjacent controls.
// The search query is derived SERVER-SIDE from the filename (the `type` param is
// whitelist-validated), and the result is TTL-cached so re-opening a panel does
// not re-hit civitai.com.
func (s *Server) handleWorkflowResolveModel(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	filename := strings.TrimSpace(r.URL.Query().Get("filename"))
	typ := civitaiTypeParam(r.URL.Query().Get("type"))
	query := comfy.CleanModelQuery(filename)
	if query == "" {
		// Nothing searchable — still offer the (empty) fallback link so the panel is
		// never a dead end.
		s.render(w, http.StatusOK, resolveModelFragment("", nil, s.nsfwMode()))
		return
	}
	res := s.resolveModels(r.Context(), query, typ)
	s.render(w, http.StatusOK, resolveModelFragment(query, res, s.nsfwMode()))
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
func resolveModelFragment(query string, res *civitai.ModelSearchResult, mode string) g.Node {
	return resolveModelFragmentWithReason(query, res, mode, "")
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
func resolveModelFragmentWithReason(query string, res *civitai.ModelSearchResult, mode, reason string) g.Node {
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
				return modelCardCore(it, images[it.ID], mode, updated[it.ID], nil)
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
func substituteOfferFragment(wfID int64, csrf, requested, remote, typ string, modelID int, query string, res *civitai.ModelSearchResult, mode string) g.Node {
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
		resolveModelFragment(query, res, mode),
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
// run is in flight is a no-op via startRun).
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
	s.startRun(wf, runOptions{Substitute: map[string]string{filename: substitute}})
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
}

// handleWorkflowRunWithOptions starts an EPHEMERAL run of the workflow with chosen
// incompatible-option fixes applied — each combo's saved (invalid) value swapped for
// a user-picked valid choice — leaving the stored workflow untouched. CSRF-protected
// + loopback-gated (it reaches ComfyUI). It respects the one-run-at-a-time invariant
// (a second call while a run is in flight is a no-op via startRun). The chosen values
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
	s.startRun(wf, runOptions{OptionFixes: parseOptionFixes(r.Form)})
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.nsfwMode()))
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
func missingModelsPanel(models []comfy.MissingModel, resolved map[string]missingResolution, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	rows := make([]g.Node, 0, len(models))
	for i, mm := range models {
		rows = append(rows, fixModelRow(i, mm, resolved[mm.Filename], libMeta, wfID, csrf, dlEligible, mode))
	}
	return h.Div(h.Class("mt-2 space-y-2"),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Missing model files")),
		g.Group(rows),
	)
}

// fixModelDialogID builds the id of the per-missing-model Fix dialog.
func fixModelDialogID(idx int) string { return "fix-model-" + strconv.Itoa(idx) }

// fixModelRow is one missing file: a compact filename + "Fix" row plus that file's
// (hidden) Fix <dialog>. The dialog lives inside the terminal run-status fragment,
// which carries NO poller (the run poll stops on the terminal state), so a later
// swap can never nuke an open popover.
func fixModelRow(idx int, mm comfy.MissingModel, res missingResolution, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	dlgID := fixModelDialogID(idx)
	fixBtn := civButton("filled", "sm", []g.Node{
		h.Type("button"),
		// Inline open — no external script (offline invariant forbids EXTERNAL
		// scripts/styles only). The dialog id is a constant, not user input.
		g.Attr("onclick", "document.getElementById('"+dlgID+"').showModal()"),
		g.Attr("aria-label", "Fix "+mm.Filename),
	}, g.Text("Fix"))
	row := h.Div(
		h.Class("flex flex-wrap items-center justify-between gap-2 rounded border border-slate-800 p-2"),
		h.Div(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(mm.Filename)),
		fixBtn,
	)
	return h.Div(row, fixModelDialog(dlgID, mm, res, libMeta, wfID, csrf, dlEligible, mode))
}

// fixModelDialog is the chrome-less native <dialog> (transparent shell around a
// theme-aware card) holding the two labeled Fix sections. It mirrors the
// workflowImportPanel dialog pattern.
func fixModelDialog(dlgID string, mm comfy.MissingModel, res missingResolution, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	return h.Dialog(
		h.ID(dlgID),
		h.Class("bg-transparent p-0 border-0 w-full max-w-3xl"),
		card(
			h.Div(h.Class("flex items-center justify-between gap-4 mb-3"),
				h.H2(h.Class("text-lg font-semibold text-slate-100 break-all"),
					g.Text("Fix "+mm.Filename)),
				h.Form(h.Method("dialog"), h.Class("inline"),
					civButton("subtle", "sm", []g.Node{h.Type("submit"),
						g.Attr("aria-label", "Close")}, g.Text("✕"))),
			),
			civitaiMatchSection(mm, res, wfID, csrf, dlEligible, mode),
			h.Div(h.Class("mt-6"), librarySubstituteSection(mm, libMeta, wfID, csrf, mode)),
		),
	)
}

// civitaiMatchSection is the "Use matched model from CivitAI" section: the best
// auto-resolved match as a primary card (with an Install-and-run CTA), up to
// fixAltCap smaller pickable alternates, a clear zero-match state with a Search
// link, and a "couldn't reach CivitAI" degraded state.
func civitaiMatchSection(mm comfy.MissingModel, res missingResolution, wfID int64, csrf string, dlEligible bool, mode string) g.Node {
	body := []g.Node{
		h.H3(h.Class("text-sm font-semibold text-slate-200"),
			g.Text("Use matched model from CivitAI")),
		h.P(h.Class("text-xs text-slate-400 mb-3"),
			g.Text("Matched from filename — verify this is the model you want.")),
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
		body = append(body, modelCardCore(primary, images[primary.ID], mode, updated[primary.ID],
			installAndRunButton(mm, primary.ID, dlEligible, wfID, csrf)))
		alts := items[1:]
		if len(alts) > fixAltCap {
			alts = alts[:fixAltCap]
		}
		if len(alts) > 0 {
			cards := make([]g.Node, 0, len(alts))
			for _, it := range alts {
				// Alternate: install passes model_id to disambiguate to THIS model.
				cards = append(cards, modelCardCore(it, images[it.ID], mode, updated[it.ID],
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
					g.Attr("title", "Set comfy_model_path to install here."),
				}, g.Text("Install and run")),
				viewOnCivitaiLink(civitaiModelURL(modelID)),
			),
			h.P(h.Class("text-xs text-slate-500"),
				g.Text("Set comfy_model_path to install here.")),
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
func librarySubstituteSection(mm comfy.MissingModel, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, mode string) g.Node {
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
	body = append(body, libraryCardGrid(visible, mm.Filename, libMeta, wfID, csrf, mode))
	if len(collapsed) > 0 {
		body = append(body, h.Details(h.Class("mt-2"),
			h.Summary(h.Class("cursor-pointer text-xs text-slate-400"),
				g.Text(strconv.Itoa(len(collapsed))+" more installed models")),
			h.Div(h.Class("mt-2"), libraryCardGrid(collapsed, mm.Filename, libMeta, wfID, csrf, mode)),
		))
	}
	return h.Div(g.Group(body))
}

// libraryCardGrid renders a responsive grid of installed-model substitute cards.
func libraryCardGrid(candidates []string, filename string, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, mode string) g.Node {
	cards := make([]g.Node, 0, len(candidates))
	for _, c := range candidates {
		cards = append(cards, libraryCard(c, filename, libMeta, wfID, csrf, mode))
	}
	return h.Div(h.Class("grid gap-4 sm:grid-cols-2 lg:grid-cols-3"), g.Group(cards))
}

// libraryCard renders ONE installed substitute candidate. When the candidate's
// basename resolves to a CivitAI-linked, cached model it is a rich card (preview
// image + name link + base-model badge); otherwise it is a minimal card (the
// choice string + a base-model guess). Both carry the "Use this & run" CTA.
func libraryCard(candidate, filename string, libMeta map[string]store.LocalModelMeta, wfID int64, csrf string, mode string) g.Node {
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
		if img := libraryPreviewImg(meta, mode); img != nil {
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
// NSFW mode: an NSFW preview is OMITTED under "hide", blurred under "blur", shown
// under "show" (a safe preview always renders). Returns nil when there is no image
// (or it is omitted), so the card gracefully has no image slot.
func libraryPreviewImg(meta store.LocalModelMeta, mode string) g.Node {
	if strings.TrimSpace(meta.ImageURL) == "" {
		return nil
	}
	level := meta.NSFWLevel
	if !meta.NSFWLevelKnown {
		level = nsfwLevelUnknown // fail-closed
	}
	nsfw := isNSFWLevel(level)
	if nsfw && mode == NSFWHide {
		return nil // hide: OMIT server-side (not just CSS-hidden)
	}
	cls := "w-full h-32 object-cover rounded border border-slate-800 bg-slate-900"
	// Blur any NSFW preview unless the mode is an explicit "show" (safe default).
	if nsfw && mode != NSFWShow {
		cls += " cm-blur"
	}
	return h.Img(
		h.Src(civitaiThumbURL(meta.ImageURL, 300)),
		h.Alt("installed model preview"),
		h.Loading("lazy"),
		h.Class(cls),
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
