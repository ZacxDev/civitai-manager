package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
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
	s.resolveVal[key] = res
	s.resolveExp[key] = time.Now().Add(popularTTL)
	s.resolveMu.Unlock()
	return res
}

// resolveLimit bounds a resolution search: the user only needs a few candidates
// to disambiguate which model is theirs.
const resolveLimit = "5"

// resolveMaxCards caps how many model-match cards a resolve fragment renders.
const resolveMaxCards = 5

// resolveModelFragment renders the resolution result: a heuristic-match note,
// up to resolveMaxCards model cards (deep-linking to the in-app model page), and
// an always-present "Search CivitAI for '<query>'" fallback link. Zero matches
// (or an empty query) renders only the note + fallback link. Every untrusted
// string (model names via modelCardCore, the query) is escaped.
func resolveModelFragment(query string, res *civitai.ModelSearchResult, mode string) g.Node {
	body := []g.Node{
		h.P(h.Class("text-xs text-slate-400 mb-2"),
			g.Text("Matched from the filename — verify this is the model you want.")),
	}

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
	s.render(w, http.StatusOK, runStatusFragment(s.runJobState(), id, s.csrf))
}

// missingModelsPanel renders the actionable "Missing model files" panel: for
// each missing model, a "Find on CivitAI" resolve control (lazy htmx fragment)
// and a list of installed substitute candidates each offering a one-click "Run
// with substitute". Every untrusted string (filenames, choice strings) is
// escaped via g.Text.
func missingModelsPanel(models []comfy.MissingModel, wfID int64, csrf string) g.Node {
	rows := make([]g.Node, 0, len(models))
	for i, mm := range models {
		rows = append(rows, missingModelRow(i, mm, wfID, csrf))
	}
	return h.Div(h.Class("mt-2 space-y-4"),
		h.Div(h.Class("text-xs font-semibold text-slate-200"), g.Text("Missing model files")),
		g.Group(rows),
	)
}

// missingModelRow renders one missing model: its filename, the resolve control +
// its (stable) result container, and the substitute candidate controls.
func missingModelRow(idx int, mm comfy.MissingModel, wfID int64, csrf string) g.Node {
	resolveID := "resolve-model-" + strconv.Itoa(idx)

	// Resolve control: lazy GET into the stable #resolve-model-i container.
	rq := url.Values{"filename": {mm.Filename}}
	if mm.CivitaiType != "" {
		rq.Set("type", mm.CivitaiType)
	}
	findBtn := civButton("outline", "sm", []g.Node{
		h.Type("button"),
		hx("get", "/workflows/run/resolve-model?"+rq.Encode()),
		hx("target", "#"+resolveID),
		hx("swap", "innerHTML"),
	}, g.Text("Find on CivitAI"))

	children := []g.Node{
		h.Div(h.Class("font-mono text-xs text-slate-300 break-all"), g.Text(mm.Filename)),
		h.Div(h.Class("mt-1 flex flex-wrap items-center gap-2"), findBtn),
		h.Div(h.ID(resolveID)), // stable resolve-result container
	}

	if sub := substituteCandidates(mm, wfID, csrf); sub != nil {
		children = append(children, sub)
	}
	return h.Div(h.Class("rounded border border-slate-800 p-3"), g.Group(children))
}

// substituteCandidates renders the "run with an installed model instead" block:
// same-base candidates as visible buttons, the rest collapsed in a <details>.
// Returns nil when there are no installed candidates to offer.
func substituteCandidates(mm comfy.MissingModel, wfID int64, csrf string) g.Node {
	if len(mm.SameBase) == 0 && len(mm.OtherCandidates) == 0 {
		return h.Div(h.Class("mt-2 text-xs text-slate-500"),
			g.Text("No installed models available to substitute for this input."))
	}
	blocks := []g.Node{
		h.Div(h.Class("mt-2 text-xs text-slate-400"),
			g.Text("Run with an installed model instead:")),
	}
	if len(mm.SameBase) > 0 {
		blocks = append(blocks, substituteButtonRow(mm.SameBase, mm.Filename, wfID, csrf))
	} else if len(mm.OtherCandidates) > 0 {
		// No same-base match — surface the first few directly so the row is not an
		// all-collapsed dead end.
		head := mm.OtherCandidates
		var tail []string
		if len(head) > 6 {
			head, tail = head[:6], head[6:]
		}
		blocks = append(blocks, substituteButtonRow(head, mm.Filename, wfID, csrf))
		if len(tail) > 0 {
			blocks = append(blocks, substituteDetails(tail, mm.Filename, wfID, csrf))
		}
		return h.Div(g.Group(blocks))
	}
	if len(mm.OtherCandidates) > 0 {
		blocks = append(blocks, substituteDetails(mm.OtherCandidates, mm.Filename, wfID, csrf))
	}
	return h.Div(g.Group(blocks))
}

// substituteButtonRow renders one "Run with substitute" button per candidate.
func substituteButtonRow(candidates []string, filename string, wfID int64, csrf string) g.Node {
	btns := make([]g.Node, 0, len(candidates))
	for _, c := range candidates {
		btns = append(btns, substituteButton(c, filename, wfID, csrf))
	}
	return h.Div(h.Class("mt-1 flex flex-wrap gap-2"), g.Group(btns))
}

// substituteDetails collapses the remaining candidates behind a disclosure.
func substituteDetails(candidates []string, filename string, wfID int64, csrf string) g.Node {
	return h.Details(h.Class("mt-1"),
		h.Summary(h.Class("cursor-pointer text-xs text-slate-400"),
			g.Text(strconv.Itoa(len(candidates))+" more installed models")),
		substituteButtonRow(candidates, filename, wfID, csrf),
	)
}

// substituteButton is one candidate's "Run with substitute" control: it POSTs the
// substitution to the ephemeral run endpoint and swaps the run-status container.
// The button label shows the candidate choice string (escaped); the CSRF token +
// missing filename + chosen substitute travel in hx-vals as properly JSON-escaped
// values.
func substituteButton(candidate, filename string, wfID int64, csrf string) g.Node {
	id := strconv.FormatInt(wfID, 10)
	return civButton("filled", "sm", []g.Node{
		h.Type("button"),
		hx("post", "/workflows/"+id+"/run-substitute"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "this"),
		substituteVals(csrf, filename, candidate),
	}, g.Text(candidate))
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
