// Package web serves the local management UI (gomponents templates + htmx +
// embedded Tailwind) and its JSON-free HTML fragment endpoints. All static
// assets are embedded, so the server is fully self-contained and offline.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/store"
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
	// Addr is the server's listen address (host:port). It decides whether the
	// arbitrary extra-scan-path capability is exposed: only a loopback bind is
	// treated as single-user-local (see extraPathsAllowed).
	Addr string
	// Library-management config (used by the library page + quarantine).
	ModelRoot    string
	TrashDir     string
	LibraryPaths []string
	Extensions   []string
	// WebScanTimeout bounds a web-triggered "Scan now"; WebScanMaxFiles caps how
	// many model files that scan will walk. Both bound the arbitrary-path walk the
	// endpoint exposes. Zero falls back to the config package defaults.
	WebScanTimeout  time.Duration
	WebScanMaxFiles int
}

// Server wires the store, the CivitAI reader, and the subscriber into an
// http.Handler.
type Server struct {
	store  *store.Store
	reader civitai.Reader
	sub    Subscriber
	cfg    Config
	log    *slog.Logger
	// csrf is a per-process random token embedded in every state-changing form
	// and verified on each POST. It defends the local, single-user UI against
	// cross-site request forgery (a malicious page in the user's browser cannot
	// read it, so it cannot forge a valid POST) without any login system.
	csrf string
	// discoverRoots overrides the auto-discovery crawl roots. Nil (production)
	// uses the built-in default locations ($HOME + common install dirs); tests
	// point it at a fixture tree for a deterministic, hermetic crawl.
	discoverRoots []string

	// baseCtx is the server's long-lived base context, from which a background
	// discovery crawl derives its own timeout context. It is tied to serveRun's
	// context (via SetBaseContext) so server shutdown cancels an in-flight crawl
	// instead of leaking its goroutine. Nil is treated as context.Background().
	baseCtx context.Context
	// crawlFn performs the discovery crawl. Nil (production) uses
	// library.DiscoverInstalls; tests inject a seam to count/gate crawls and to
	// drive job-state transitions deterministically without touching the real FS.
	crawlFn func(ctx context.Context, roots []string, opts library.DiscoverOptions) ([]library.Install, error)
	// discoverMu guards discoverJob. One discovery job runs at a time.
	discoverMu sync.Mutex
	// discoverJob is the current (or most recent) background discovery job, or nil
	// before the first crawl is triggered.
	discoverJob *discoveryJob

	// scanFn performs the streaming model-file scan. Nil (production) builds a
	// library.Scanner from the resolved dirs and runs Scan with the OnFile stream;
	// tests inject a seam to emit FileResults over time (deterministic streaming)
	// without hashing a real tree. It reports the terminal error (nil, deadline,
	// cancel, too-large).
	scanFn func(ctx context.Context, onFile func(library.FileResult)) error
	// scanMu guards scanJob. One model-scan job runs at a time (idempotent start,
	// mirroring discovery).
	scanMu sync.Mutex
	// scanJob is the current (or most recent) background model-scan job, or nil
	// before the first scan is triggered.
	scanJob *scanJob
}

// scanJob is the in-memory state of a single background streaming model-file
// scan. All fields are read/written only under Server.scanMu.
//
// The scan STREAMS results: it appends to results incrementally (under the
// mutex) as the walker hashes/matches each file, so a /library/scan/status poll
// shows the growing list. A reader MUST snapshot-copy the slice under the lock
// before rendering — never hand the live, still-appended slice header across the
// lock boundary (the same torn-slice guard the discovery job uses).
type scanJob struct {
	// running is true from job start until the scan goroutine settles.
	running bool
	// results are the per-file cards streamed so far by the (possibly still
	// running) scan. APPENDED incrementally under Server.scanMu, so any reader
	// must snapshot-copy it under the lock.
	results []library.FileResult
	// scanned counts files streamed; matched counts those with a CivitAI match.
	scanned int
	matched int
	// stopped is true when the user explicitly stopped the scan (POST
	// /library/scan/stop) so the terminal fragment reads "Scan stopped".
	stopped    bool
	err        error
	startedAt  time.Time
	finishedAt time.Time
	// cancel cancels the scan's context; invoked when the scan finishes (to
	// release the timeout context), on server shutdown, and on an explicit Stop.
	cancel context.CancelFunc
}

// discoveryJob is the in-memory state of a single background discovery crawl.
// All fields are read/written only under Server.discoverMu.
//
// The crawl STREAMS results: it appends to installs incrementally (under the
// mutex) as the walker finds them, so a /status poll shows the growing list. A
// reader MUST snapshot-copy the slice under the lock before rendering — never
// hand the live, still-appended slice header across the lock boundary.
type discoveryJob struct {
	// running is true from job start until the crawl goroutine settles.
	running bool
	// installs are the candidates found so far by the (possibly still-running)
	// crawl. It is APPENDED incrementally under Server.discoverMu as installs
	// stream in, so any reader must snapshot-copy it under the lock.
	installs []library.Install
	// stopped is true when the user explicitly stopped the crawl (POST
	// /library/discover/stop), so the terminal fragment can say "Scan stopped"
	// rather than "Scan complete".
	stopped    bool
	err        error
	startedAt  time.Time
	finishedAt time.Time
	// cancel cancels the crawl's context; invoked when the crawl finishes (to
	// release the timeout context), on server shutdown, and on an explicit Stop.
	cancel context.CancelFunc
}

// SetBaseContext sets the server's base context, from which background discovery
// crawls derive. Cancelling ctx (server shutdown) cancels any in-flight crawl.
// Call before Handler is served.
func (s *Server) SetBaseContext(ctx context.Context) { s.baseCtx = ctx }

// NewServer builds a Server.
func NewServer(st *store.Store, reader civitai.Reader, sub Subscriber, cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &Server{store: st, reader: reader, sub: sub, cfg: cfg, log: log, csrf: newCSRFToken()}
}

// extraPathsAllowed reports whether the arbitrary extra-scan-path capability is
// safe to expose: only when the server is bound to a loopback address (a
// single-user-local surface). On any non-loopback bind the "Scan now" form may
// scan only model_root + configured library_paths, never a client-submitted
// host path — the endpoint is unauthenticated, so a non-loopback bind would make
// it a remote arbitrary-path walk primitive.
func (s *Server) extraPathsAllowed() bool {
	return config.IsLoopbackAddr(s.cfg.Addr)
}

// webScanTimeout returns the deadline for a web-triggered scan, falling back to
// the config default when unset.
func (s *Server) webScanTimeout() time.Duration {
	if s.cfg.WebScanTimeout > 0 {
		return s.cfg.WebScanTimeout
	}
	return config.DefaultWebScanTimeout
}

// webScanMaxFiles returns the model-file budget for a web-triggered scan,
// falling back to the config default when unset.
func (s *Server) webScanMaxFiles() int {
	if s.cfg.WebScanMaxFiles > 0 {
		return s.cfg.WebScanMaxFiles
	}
	return config.DefaultWebScanMaxFiles
}

// newCSRFToken returns a fresh random hex token.
func newCSRFToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is fatal for a security token; fall back to a
		// process-unique-ish value rather than an empty (guessable) token.
		return hex.EncodeToString([]byte(os.Args[0] + time.Now().String()))
	}
	return hex.EncodeToString(b)
}

// verifyCSRF checks the request's CSRF token (from the X-CSRF-Token header or a
// csrf_token form field) against the server token in constant time. On failure
// it writes 403 and returns false; the handler must stop.
func (s *Server) verifyCSRF(w http.ResponseWriter, r *http.Request) bool {
	tok := r.Header.Get("X-CSRF-Token")
	if tok == "" {
		tok = r.FormValue("csrf_token")
	}
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.csrf)) != 1 {
		http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
		return false
	}
	return true
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

	mux.HandleFunc("POST /settings/nsfw", s.handleSetNSFWDisplay)
	mux.HandleFunc("POST /settings/theme", s.handleSetTheme)

	mux.HandleFunc("POST /subscribe", s.handleSubscribe)
	mux.HandleFunc("POST /subscriptions/{id}/flags", s.handleFlags)
	mux.HandleFunc("POST /subscriptions/{id}/delete", s.handleDelete)

	mux.HandleFunc("GET /fragments/events", s.handleEventsFragment)
	mux.HandleFunc("GET /fragments/queue", s.handleQueueFragment)

	mux.HandleFunc("GET /library", s.handleLibrary)
	mux.HandleFunc("POST /library/scan", s.handleLibraryScan)
	mux.HandleFunc("GET /library/scan/status", s.handleScanStatus)
	mux.HandleFunc("POST /library/scan/stop", s.handleScanStop)
	mux.HandleFunc("POST /settings/match-remote", s.handleSetMatchRemote)
	mux.HandleFunc("POST /library/discover", s.handleLibraryDiscover)
	mux.HandleFunc("GET /library/discover/status", s.handleDiscoverStatus)
	mux.HandleFunc("POST /library/discover/stop", s.handleDiscoverStop)
	mux.HandleFunc("POST /library/browse", s.handleLibraryBrowse)
	mux.HandleFunc("POST /library/scan-dirs/add", s.handleScanDirAdd)
	mux.HandleFunc("POST /library/scan-dirs/remove", s.handleScanDirRemove)
	mux.HandleFunc("POST /library/quarantine", s.handleQuarantine)
	mux.HandleFunc("GET /trash", s.handleTrash)
	mux.HandleFunc("POST /trash/{id}/restore", s.handleRestore)

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
