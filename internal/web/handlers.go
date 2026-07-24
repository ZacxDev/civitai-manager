package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

const searchLimit = "24"

// nsfwSearchFlag maps the persisted NSFW display mode to the civitai
// `/api/v1/models` `nsfw` boolean query param: blur/show want the NSFW models
// AND their showcase images to come through (the client carousel then blurs or
// shows per mode), while hide wants SFW-only results server-side. Returns true
// for blur/show, false for hide.
func (s *Server) nsfwSearchFlag() bool { return s.nsfwMode() != NSFWHide }

// setNSFWParam sets the `nsfw` query param civitai's model search honors:
// nsfw=true includes NSFW models with their images, nsfw=false restricts to SFW.
// Setting it explicitly (rather than omitting) keeps the popular default and the
// keyword search consistent and makes the data-egress behavior obvious.
func setNSFWParam(q url.Values, nsfw bool) {
	if nsfw {
		q.Set("nsfw", "true")
	} else {
		q.Set("nsfw", "false")
	}
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		s.renderError(w, "load subscriptions", err)
		return
	}
	files, err := s.store.ListLocalFiles()
	if err != nil {
		s.renderError(w, "load local files", err)
		return
	}
	suggestions := librarySubscribeSuggestions(files, subs, subscribeSuggestionLimit)
	// Resolve suggestion titles from the local model_cache only (zero civitai
	// calls); a cache miss leaves Name empty so the card fetches it lazily.
	for i := range suggestions {
		if ent, _ := s.store.GetModelCache(suggestions[i].ModelID); ent != nil && ent.Name != "" {
			suggestions[i].Name = ent.Name
		}
	}
	s.render(w, http.StatusOK, dashboardPage(subs, suggestions, s.csrf, s.currentTheme(), s.nsfwMode()))
}

// subscribeSuggestionLimit caps how many library-derived subscribe suggestions
// the dashboard shows.
const subscribeSuggestionLimit = 12

// themeSettingKey persists the UI light/dark choice.
const themeSettingKey = "theme"

// currentTheme returns the persisted UI theme ("light"|"dark"), defaulting to
// dark (civitai-manager's established look). Reflected onto <html data-theme>.
func (s *Server) currentTheme() string {
	v, _ := s.store.GetSettingDefault(themeSettingKey, "dark")
	if v != "light" {
		v = "dark"
	}
	return v
}

// handleSetTheme persists the light/dark choice and asks htmx to refresh so the
// page re-renders under the new <html data-theme> (from which every --civitai-*
// token re-resolves). CSRF-protected like every other state-changing POST.
func (s *Server) handleSetTheme(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	theme := "dark"
	if strings.EqualFold(strings.TrimSpace(r.FormValue("theme")), "light") {
		theme = "light"
	}
	if err := s.store.SetSetting(themeSettingKey, theme); err != nil {
		s.renderError(w, "save theme setting", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	isHX := r.Header.Get("HX-Request") == "true"
	mode := s.nsfwMode()
	nsfw := s.nsfwSearchFlag()

	if query == "" {
		// Empty query → the recent-popular default feed (cached per NSFW flag),
		// with a heading. If that fetch fails, res stays nil and searchResults
		// falls back to the "Enter a query…" hint.
		res, heading := s.popularModels(r.Context(), nsfw)
		if isHX {
			s.render(w, http.StatusOK, searchResults(res, mode, heading))
			return
		}
		s.render(w, http.StatusOK, searchPage("", res, s.csrf, s.currentTheme(), mode, heading))
		return
	}

	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", searchLimit)
	// Keyword search returns the most popular matches first (the empty-query
	// "popular this month" default already sorts Most Downloaded).
	q.Set("sort", "Most Downloaded")
	// Tie the nsfw param to the display mode so NSFW models return WITH their
	// showcase images (blur/show) or are excluded (hide) — see setNSFWParam.
	setNSFWParam(q, nsfw)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		if isHX {
			s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
			return
		}
		s.render(w, http.StatusOK, searchPage(query, nil, s.csrf, s.currentTheme(), mode, ""))
		return
	}
	if isHX {
		s.render(w, http.StatusOK, searchResults(res, mode, ""))
		return
	}
	s.render(w, http.StatusOK, searchPage(query, res, s.csrf, s.currentTheme(), mode, ""))
}

// handleSubscribeSearch backs the dashboard's integrated civitai search: it
// searches models and renders subscribe-enabled result cards (each with a
// one-click, auto-download Subscribe button) into the dashboard results
// container. GET-only; the Subscribe action itself is a CSRF-protected POST.
func (s *Server) handleSubscribeSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	mode := s.nsfwMode()
	if query == "" {
		s.render(w, http.StatusOK, subscribeSearchResults(nil, mode, s.csrf))
		return
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", searchLimit)
	// Same NSFW-image behavior as the main search (see setNSFWParam).
	setNSFWParam(q, s.nsfwSearchFlag())
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
		return
	}
	s.render(w, http.StatusOK, subscribeSearchResults(res, mode, s.csrf))
}

// popularModels returns the "recent popular" model feed (Most Downloaded, this
// Month) used as the empty-query search default, served from a ~10 min in-process
// TTL cache so repeated dashboard/search loads do not hit civitai.com. On success
// it returns the feed plus a heading; on any fetch error it returns (nil, "") so
// the caller falls back to the "Enter a query…" hint.
func (s *Server) popularModels(parent context.Context, nsfw bool) (*civitai.ModelSearchResult, string) {
	s.popularMu.Lock()
	if v := s.popularVal[nsfw]; v != nil && time.Now().Before(s.popularExp[nsfw]) {
		s.popularMu.Unlock()
		return v, "Popular this month"
	}
	s.popularMu.Unlock()

	q := url.Values{}
	q.Set("sort", "Most Downloaded")
	q.Set("period", "Month")
	q.Set("limit", "24")
	// Include NSFW models + their showcase images unless the user is in hide mode
	// (see setNSFWParam) — without this, NSFW models return with images withheld
	// and their cards show "No showcase images".
	setNSFWParam(q, nsfw)
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.log.Warn("popular models fetch failed", "err", err)
		return nil, ""
	}
	s.popularMu.Lock()
	s.popularVal[nsfw] = res
	s.popularExp[nsfw] = time.Now().Add(popularTTL)
	s.popularMu.Unlock()
	return res, "Popular this month"
}

// handleModelTitle returns just the model's display name as an escaped text
// span, used by the lazy suggestion-title container when the dashboard render
// found no cached name. It resolves cache-first via cachedModelDetail (GetModel
// only on a cache miss/stale), and falls back to "Model #id" on any error so the
// card degrades gracefully rather than showing an error. GET-only; read-only.
func (s *Server) handleModelTitle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	name := "Model #" + strconv.Itoa(id)
	m, _, mErr := s.cachedModelDetail(r.Context(), id)
	if mErr == nil && m != nil && m.Name != "" {
		name = m.Name
	}
	s.render(w, http.StatusOK, g.Text(name))
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, errNode := s.loadModelView(r.Context(), id, r.URL.Query().Get("version"))
	if errNode != nil {
		status := http.StatusBadGateway
		if view.Model == nil && errors.Is(view.loadErr, civitai.ErrNotFound) {
			status = http.StatusNotFound
		}
		s.render(w, status, page("Not found", s.currentTheme(), s.csrf, s.nsfwMode(), errNode))
		return
	}
	// Mark which of this model's versions the user already has locally, so the
	// version list can badge them (mirrors handleModelCard's local-file gather).
	if mid, cerr := strconv.Atoi(id); cerr == nil {
		view.LocalVersionIDs = s.localVersionIDs(mid)
	}
	s.render(w, http.StatusOK, modelDetailPage(view, s.csrf, s.currentTheme()))
}

// handleModelCommunity backs the LAZY-loaded community feed at the bottom of the
// model page: recent-popular civitai images that use the selected model version.
// It is a GET fragment (no state change, no CSRF) that makes ONE bounded outbound
// SearchImages proxy call — the same egress posture as /models — and NEVER breaks
// the page: on error or empty results it renders a small muted note. It is fetched
// out-of-band (not inline during page render) because that SearchImages call is
// slow (20s+, frequently timing out); see loadModelView.
func (s *Server) handleModelCommunity(w http.ResponseWriter, r *http.Request) {
	versionID := strings.TrimSpace(r.URL.Query().Get("versionId"))
	mode := s.nsfwMode()
	// Validate versionId is a positive integer before spending an upstream round
	// trip on it (a malformed value would only earn a rejection from civitai).
	if vid, err := strconv.Atoi(versionID); err != nil || vid <= 0 {
		s.render(w, http.StatusOK, communityFeedNote("No community images yet."))
		return
	}

	q := url.Values{}
	q.Set("modelVersionId", versionID)
	q.Set("sort", "Most Reactions")
	q.Set("period", "Month")
	q.Set("limit", "12")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	res, err := s.reader.SearchImages(ctx, q)
	if err != nil {
		s.log.Warn("community feed fetch failed", "versionId", versionID, "err", err)
		s.render(w, http.StatusOK, communityFeedNote("Couldn't load community images."))
		return
	}
	if res == nil || len(res.Items) == 0 {
		s.render(w, http.StatusOK, communityFeedNote("No community images yet."))
		return
	}
	s.render(w, http.StatusOK, s.communityFeedFragment(res.Items, mode))
}

// nsfwMode returns the persisted global NSFW display mode (default blur).
func (s *Server) nsfwMode() string {
	v, err := s.store.GetSettingDefault(nsfwSettingKey, NSFWBlur)
	if err != nil {
		return NSFWBlur
	}
	return normalizeNSFWMode(v)
}

// loadModelView fetches and assembles the rich model-detail view: model detail
// (with a description parsed from the raw body), the selected version's detail
// (default: the latest), and the showcase image gallery — the latter sourced
// entirely from the version's INLINE images[] in the model/version JSON, with no
// separate /api/v1/images call in the page path. The version call degrades
// gracefully — a failure there still renders the page. It returns a non-nil error
// node only when the model itself cannot be loaded.
func (s *Server) loadModelView(parent context.Context, id, versionParam string) (modelDetailView, g.Node) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()

	m, raw, err := s.reader.GetModel(ctx, id)
	if err != nil {
		return modelDetailView{loadErr: err},
			errorNote("Could not load model " + id + ": " + err.Error())
	}

	view := modelDetailView{
		Model:       m,
		Description: parseModelDescription(raw),
		NSFWMode:    s.nsfwMode(),
	}

	// Selected version: the ?version= override, else the latest (first listed).
	selVID := 0
	if versionParam != "" {
		selVID, _ = strconv.Atoi(versionParam)
	}
	if selVID == 0 && len(m.ModelVersions) > 0 {
		selVID = m.ModelVersions[0].ID
	}
	view.SelectedVersionID = selVID
	var versionRaw []byte
	if selVID > 0 {
		if vd, vraw, verr := s.reader.GetModelVersion(ctx, strconv.Itoa(selVID)); verr == nil {
			view.Version = vd
			view.PublishedAt = parsePublishedAt(vraw)
			versionRaw = vraw
		}
	}

	// Showcase gallery: sourced from the version's INLINE images[] — already
	// present in the model/version JSON fetched above. This deliberately does NOT
	// make a separate GET /api/v1/images (SearchImages) call: that call was slow
	// (20s+, frequently timing out) and its error was silently swallowed into an
	// empty gallery. The render mode decides what is shown/blurred/omitted.
	view.Images = parseVersionImages(versionRaw, raw, selVID)
	return view, nil
}

// handleSetNSFWDisplay persists the global NSFW display mode (set from the
// navbar's 3-state cycling toggle) and asks htmx to refresh so the CURRENT page
// re-renders under the new mode — whichever page it is (its galleries then
// hide/blur/show accordingly). This mirrors the theme toggle's HX-Refresh
// pattern, so the one control works everywhere rather than only on the model
// page. CSRF-protected like every other state-changing POST.
func (s *Server) handleSetNSFWDisplay(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	mode := normalizeNSFWMode(r.FormValue("mode"))
	if err := s.store.SetSetting(nsfwSettingKey, mode); err != nil {
		s.renderError(w, "save nsfw setting", err)
		return
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreator(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	q := url.Values{}
	q.Set("username", username)
	q.Set("sort", "Newest")
	q.Set("limit", searchLimit)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		s.render(w, http.StatusBadGateway, page("@"+username, s.currentTheme(), s.csrf, s.nsfwMode(), errorNote("Could not load creator: "+err.Error())))
		return
	}
	s.render(w, http.StatusOK, creatorPage(username, res, s.csrf, s.currentTheme(), s.nsfwMode()))
}

func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSubsWithError(w, "invalid form: "+err.Error())
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	opts := poller.SubscribeOptions{
		AutoDownload:   checkboxVal(r, "auto_download"),
		NotifyOnly:     checkboxVal(r, "notify_only"),
		BackfillLatest: checkboxVal(r, "backfill_latest"),
		PollInterval:   s.cfg.DefaultPollInterval,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	creator := strings.TrimSpace(r.FormValue("creator"))
	modelRef := strings.TrimSpace(r.FormValue("model"))

	var subErr error
	switch {
	case creator != "":
		_, subErr = s.sub.SubscribeCreator(ctx, creator, opts)
	case modelRef != "":
		modelID, perr := civitai.ParseModelRef(modelRef)
		if perr != nil {
			s.renderSubsWithError(w, perr.Error())
			return
		}
		_, subErr = s.sub.SubscribeModel(ctx, modelID, opts)
	default:
		s.renderSubsWithError(w, "provide a model id/URL or a creator username")
		return
	}

	if subErr != nil {
		if errors.Is(subErr, poller.ErrAlreadySubscribed) {
			s.renderSubsWithError(w, "already subscribed to that target")
			return
		}
		s.renderSubsWithError(w, "subscribe failed: "+subErr.Error())
		return
	}
	s.renderSubsWithError(w, "")
}

// handleModelSubscribeOptions (GET) renders the subscribe options panel (state 2
// of the shared control): the auto-download vs notify-only choice + Confirm/Cancel.
// The heading resolves the model name from the local model_cache only (zero
// civitai calls), falling back to "this model" on a cache miss. Read-only; no CSRF.
func (s *Server) handleModelSubscribeOptions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	name := "this model"
	if ent, _ := s.store.GetModelCache(id); ent != nil && ent.Name != "" {
		name = ent.Name
	}
	s.render(w, http.StatusOK, subscribeOptionsPanel(id, name, s.csrf))
}

// handleModelSubscribeControl (GET) re-renders the shared subscribe control in its
// current persisted state (collapsed when not subscribed, subscribed feedback when
// subscribed). It backs the options panel's Cancel action. Read-only; no CSRF.
func (s *Server) handleModelSubscribeControl(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, subscribeControl(id, s.modelSubscription(id), s.csrf))
}

// handleModelSubscribe creates a MODEL subscription honoring the options panel's
// choice (auto-download by default, or notify-only when mode=notify_only /
// notify_only=true) and returns the shared control's subscribed-feedback fragment.
// It is SEPARATE from the dashboard's POST /subscribe → subscriptionsTable flow.
// CSRF-protected. An already-subscribed model is idempotent (still returns the
// subscribed feedback).
func (s *Server) handleModelSubscribe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	// The options panel sends the choice as a radio (mode=auto_download|notify_only);
	// an explicit notify_only=true is also honored.
	notifyOnly := checkboxVal(r, "notify_only") || r.FormValue("mode") == "notify_only"
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	_, subErr := s.sub.SubscribeModel(ctx, id, poller.SubscribeOptions{
		AutoDownload: !notifyOnly,
		NotifyOnly:   notifyOnly,
		PollInterval: s.cfg.DefaultPollInterval,
	})
	if subErr != nil && !errors.Is(subErr, poller.ErrAlreadySubscribed) {
		// A genuine failure (e.g. the model didn't resolve): surface it in the
		// control instead of silently collapsing to a bare "Subscribe" button.
		s.log.Warn("model subscribe", "model", id, "err", subErr)
		s.render(w, http.StatusOK, subscribeControlCollapsed(id, s.csrf, "Subscribe failed — please try again."))
		return
	}
	// Re-render from the persisted state so the control reflects reality
	// (subscribed on success / already-subscribed).
	s.render(w, http.StatusOK, subscribeControl(id, s.modelSubscription(id), s.csrf))
}

// handleModelUnsubscribe removes the model subscription and returns the collapsed
// subscribe control with an "Unsubscribed" note. It looks up the model's
// subscription id and deletes it. CSRF-protected.
func (s *Server) handleModelUnsubscribe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if sub := s.modelSubscription(id); sub != nil {
		if derr := s.store.DeleteSubscription(sub.ID); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			s.log.Warn("model unsubscribe", "model", id, "err", derr)
		}
	}
	s.render(w, http.StatusOK, subscribeControlCollapsed(id, s.csrf, "Unsubscribed"))
}

func (s *Server) handleFlags(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	auto := r.FormValue("auto_download") == "true"
	notify := r.FormValue("notify_only") == "true"
	if err := s.store.SetSubscriptionFlags(id, auto, notify); err != nil {
		s.renderError(w, "update flags", err)
		return
	}
	sub, err := s.store.GetSubscription(id)
	if err != nil {
		s.renderError(w, "reload subscription", err)
		return
	}
	s.render(w, http.StatusOK, subscriptionRow(*sub, s.csrf))
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if err := s.store.DeleteSubscription(id); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.renderError(w, "delete subscription", err)
		return
	}
	// Empty body: the htmx outerHTML swap removes the row.
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleEventsFragment(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(40)
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Could not load activity: "+err.Error()))
		return
	}
	s.render(w, http.StatusOK, eventsFragment(events))
}

func (s *Server) handleQueueFragment(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListQueue()
	if err != nil {
		s.render(w, http.StatusOK, errorNote("Could not load queue: "+err.Error()))
		return
	}
	if len(items) > 25 {
		items = items[:25]
	}
	s.render(w, http.StatusOK, queueFragment(items))
}

// --- helpers ---

func (s *Server) renderSubsWithError(w http.ResponseWriter, errMsg string) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		s.renderError(w, "load subscriptions", err)
		return
	}
	s.render(w, http.StatusOK, subscriptionsTable(subs, errMsg, s.csrf))
}

func (s *Server) renderError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	s.render(w, http.StatusInternalServerError, errorNote(what+": "+err.Error()))
}

func errorNote(msg string) g.Node {
	return alert("error", "", g.Text(msg))
}

func checkboxVal(r *http.Request, name string) bool {
	return r.FormValue(name) == "true"
}
