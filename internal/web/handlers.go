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
	h "maragu.dev/gomponents/html"
)

const searchLimit = "24"

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	subs, err := s.store.ListSubscriptions()
	if err != nil {
		s.renderError(w, "load subscriptions", err)
		return
	}
	s.render(w, http.StatusOK, dashboardPage(subs, s.csrf))
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	isHX := r.Header.Get("HX-Request") == "true"

	if query == "" {
		if isHX {
			s.render(w, http.StatusOK, searchResults(nil, s.cfg.BaseURL))
			return
		}
		s.render(w, http.StatusOK, searchPage("", nil, s.cfg.BaseURL))
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
		s.render(w, http.StatusOK, searchPage(query, nil, s.cfg.BaseURL))
		return
	}
	if isHX {
		s.render(w, http.StatusOK, searchResults(res, s.cfg.BaseURL))
		return
	}
	s.render(w, http.StatusOK, searchPage(query, res, s.cfg.BaseURL))
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	m, _, err := s.reader.GetModel(ctx, id)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, civitai.ErrNotFound) {
			status = http.StatusNotFound
		}
		s.render(w, status, page("Not found", errorNote("Could not load model "+id+": "+err.Error())))
		return
	}
	s.render(w, http.StatusOK, modelDetailPage(m, s.csrf))
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
		s.render(w, http.StatusBadGateway, page("@"+username, errorNote("Could not load creator: "+err.Error())))
		return
	}
	s.render(w, http.StatusOK, creatorPage(username, res, s.csrf))
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
	return h.Div(
		h.Class("rounded-md border border-rose-800 bg-rose-950 px-3 py-2 text-sm text-rose-200"),
		g.Text(msg),
	)
}

func checkboxVal(r *http.Request, name string) bool {
	return r.FormValue(name) == "true"
}
