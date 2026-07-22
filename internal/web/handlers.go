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

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		s.renderError(w, "load subscriptions", err)
		return
	}
	s.render(w, http.StatusOK, dashboardPage(subs, s.csrf, s.currentTheme()))
}

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

	if query == "" {
		if isHX {
			s.render(w, http.StatusOK, searchResults(nil, s.cfg.BaseURL))
			return
		}
		s.render(w, http.StatusOK, searchPage("", nil, s.cfg.BaseURL, s.csrf, s.currentTheme()))
		return
	}

	q := url.Values{}
	q.Set("query", query)
	q.Set("limit", searchLimit)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res, err := s.reader.SearchModels(ctx, q)
	if err != nil {
		if isHX {
			s.render(w, http.StatusOK, errorNote("Search failed: "+err.Error()))
			return
		}
		s.render(w, http.StatusOK, searchPage(query, nil, s.cfg.BaseURL, s.csrf, s.currentTheme()))
		return
	}
	if isHX {
		s.render(w, http.StatusOK, searchResults(res, s.cfg.BaseURL))
		return
	}
	s.render(w, http.StatusOK, searchPage(query, res, s.cfg.BaseURL, s.csrf, s.currentTheme()))
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	view, errNode := s.loadModelView(r.Context(), id, r.URL.Query().Get("version"))
	if errNode != nil {
		status := http.StatusBadGateway
		if view.Model == nil && errors.Is(view.loadErr, civitai.ErrNotFound) {
			status = http.StatusNotFound
		}
		s.render(w, status, page("Not found", s.currentTheme(), s.csrf, errNode))
		return
	}
	s.render(w, http.StatusOK, modelDetailPage(view, s.csrf, s.currentTheme()))
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
// (default: the latest), and the showcase image gallery. The version and image
// calls degrade gracefully — a failure there still renders the page. It returns a
// non-nil error node only when the model itself cannot be loaded.
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
	if selVID > 0 {
		if vd, vraw, verr := s.reader.GetModelVersion(ctx, strconv.Itoa(selVID)); verr == nil {
			view.Version = vd
			view.PublishedAt = parsePublishedAt(vraw)
		}
	}

	// Showcase images: request generation metadata + all NSFW levels; the render
	// mode decides what is shown/blurred/omitted.
	q := url.Values{}
	q.Set("modelId", strconv.Itoa(m.ID))
	q.Set("limit", "30")
	q.Set("nsfw", "X")
	q.Set("sort", "Most Reactions")
	q.Set("withMeta", "true")
	q.Set("flatMeta", "true")
	if res, ierr := s.reader.SearchImages(ctx, q); ierr == nil && res != nil {
		view.Images = res.Items
	}
	return view, nil
}

// handleSetNSFWDisplay persists the global NSFW display mode and re-renders the
// model page (target body) so the gallery reflects the new mode immediately.
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
	modelID := strings.TrimSpace(r.FormValue("model_id"))
	if modelID == "" {
		// No model context: just acknowledge (the setting is persisted).
		w.WriteHeader(http.StatusOK)
		return
	}
	view, errNode := s.loadModelView(r.Context(), modelID, r.FormValue("version"))
	if errNode != nil {
		s.render(w, http.StatusOK, page("Not found", s.currentTheme(), s.csrf, errNode))
		return
	}
	s.render(w, http.StatusOK, modelDetailPage(view, s.csrf, s.currentTheme()))
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
		s.render(w, http.StatusBadGateway, page("@"+username, s.currentTheme(), s.csrf, errorNote("Could not load creator: "+err.Error())))
		return
	}
	s.render(w, http.StatusOK, creatorPage(username, res, s.csrf, s.currentTheme()))
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
