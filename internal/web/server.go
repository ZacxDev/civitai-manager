// Package web serves the local management UI (gomponents templates + htmx +
// embedded Tailwind) and its JSON-free HTML fragment endpoints. All static
// assets are embedded, so the server is fully self-contained and offline.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/civitai/civitai-manager/internal/civitai"
	"github.com/civitai/civitai-manager/internal/poller"
	"github.com/civitai/civitai-manager/internal/store"
	g "maragu.dev/gomponents"
)

// Subscriber is the subscription-management surface the UI needs (satisfied by
// *poller.Poller).
type Subscriber interface {
	SubscribeModel(ctx context.Context, modelID int, opts poller.SubscribeOptions) (int64, error)
	SubscribeCreator(ctx context.Context, username string, opts poller.SubscribeOptions) (int64, error)
}

// Config holds the web server's view of app configuration.
type Config struct {
	BaseURL             string
	DefaultPollInterval time.Duration
}

// Server wires the store, the CivitAI reader, and the subscriber into an
// http.Handler.
type Server struct {
	store  *store.Store
	reader civitai.Reader
	sub    Subscriber
	cfg    Config
	log    *slog.Logger
}

// NewServer builds a Server.
func NewServer(st *store.Store, reader civitai.Reader, sub Subscriber, cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &Server{store: st, reader: reader, sub: sub, cfg: cfg, log: log}
}

// Handler builds the routed http.Handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Embedded static assets under /assets/.
	mux.Handle("GET /assets/", http.FileServer(http.FS(assetsFS)))

	mux.HandleFunc("GET /{$}", s.handleDashboard)
	mux.HandleFunc("GET /search", s.handleSearch)
	mux.HandleFunc("GET /models/{id}", s.handleModel)
	mux.HandleFunc("GET /creators/{username}", s.handleCreator)

	mux.HandleFunc("POST /subscribe", s.handleSubscribe)
	mux.HandleFunc("POST /subscriptions/{id}/flags", s.handleFlags)
	mux.HandleFunc("POST /subscriptions/{id}/delete", s.handleDelete)

	mux.HandleFunc("GET /fragments/events", s.handleEventsFragment)
	mux.HandleFunc("GET /fragments/queue", s.handleQueueFragment)

	return logRequests(s.log, mux)
}

// render writes a gomponents node as an HTML response.
func (s *Server) render(w http.ResponseWriter, status int, node g.Node) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := node.Render(w); err != nil {
		s.log.Error("render", "err", err)
	}
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Debug("http", "method", r.Method, "path", r.URL.Path, "dur", time.Since(start))
	})
}
