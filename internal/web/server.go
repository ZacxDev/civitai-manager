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
	// ComfyURL is the local ComfyUI server base URL for workflow runs/preflight. It
	// is config-only (never a per-request parameter). Empty disables local run.
	ComfyURL string
	// ComfyToken is an optional bearer token for a login-fronted ComfyUI. Secret —
	// never rendered/logged.
	ComfyToken string
	// ComfyModelPath is the local ComfyUI models/ directory root used by the
	// "Download & run" action to write a missing model into the correct per-type
	// subfolder. Empty (or a non-loopback ComfyUI) disables the download flow — the
	// missing-models panel then degrades to CivitAI-link-only. Validated at config
	// load (existing writable dir when set).
	ComfyModelPath string
	// ComfyRoot is the local ComfyUI INSTALL root (the folder holding
	// custom_nodes/). It is used ONLY by the explicit, user-triggered install of
	// the civitai-manager ComfyUI helper extension. Empty disables that action.
	// Resolved by config (explicit comfy_root, else the comfy_model_path parent
	// when it looks like a ComfyUI install).
	ComfyRoot string
	// OutputsDir is the civitai-manager-owned directory the output gallery copies
	// successful workflow-run images into (app-owned data next to the DB). Always
	// set by config resolution (defaults to <db-dir>/outputs). Capture is skipped
	// when empty (e.g. an unconfigured test server).
	OutputsDir string
	// OutputsMaxBytes is the TOTAL disk cap of the outputs tree, in bytes. After a
	// successful capture the oldest generations (rows + files) are evicted until the
	// total is back under it. 0 (or negative) means UNLIMITED — no eviction ever.
	// Resolved by config (default 20 GiB); an unset zero value in a test server is
	// therefore "unlimited", matching the pre-cap behaviour.
	OutputsMaxBytes int64
	// MaxFileSizeBytes caps a "Download & run" model download (0 = the built-in
	// safety guard). It reuses the poller's max_file_size setting so a single knob
	// bounds every download the app makes.
	MaxFileSizeBytes int64
	// ComfyCloud enables the "Run on CivitAI Cloud" feature (submit to the CivitAI
	// orchestration API, sending the graph + resource list to civitai.com and
	// spending Buzz). Default false → the cloud UI is shown but disabled with a note.
	ComfyCloud bool
	// Token is the CivitAI API token, reused as the bearer for the cloud
	// orchestration API. Secret — never rendered/logged.
	Token string
	// HFToken is the optional HuggingFace token for the HF fallback resolver. Secret
	// — never rendered/logged; sent only to HuggingFace hosts.
	HFToken string
	// HFFallback enables the HuggingFace fallback (try HF when CivitAI resolution
	// misses). Default resolved from config (on unless explicitly disabled).
	HFFallback bool
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
	// cancel, too-large). onDiscovered mirrors the scanner's OnDiscovered seam: the
	// scan reports the total model-file count found by the walk (the progress
	// denominator) once, before per-file streaming. onHashed mirrors the scanner's
	// OnHashed seam: it fires once per file (increment-style, +1) during the phase-1
	// hash pass so the scanning view can show a moving "Hashing… N / total" line
	// before any card streams.
	scanFn func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(total int), onHashed func(hashed int)) error
	// scanMu guards scanJob. One model-scan job runs at a time (idempotent start,
	// mirroring discovery).
	scanMu sync.Mutex
	// scanJob is the current (or most recent) background model-scan job, or nil
	// before the first scan is triggered.
	scanJob *scanJob

	// workflowScanFn performs the streaming workflow scan. Nil (production)
	// resolves the ComfyUI workflow dirs (discovery + persisted scan_dirs) and runs
	// a library.WorkflowScanner; tests inject a seam to stream WorkflowResults over
	// time without touching the FS. It reports the terminal report + error.
	workflowScanFn func(ctx context.Context, onWorkflow func(library.WorkflowResult)) (*library.WorkflowScanReport, error)
	// workflowScanMu guards workflowScanJob. One workflow-scan job runs at a time.
	workflowScanMu sync.Mutex
	// workflowScanJob is the current (or most recent) background workflow-scan job,
	// or nil before the first workflow scan is triggered.
	workflowScanJob *workflowScanJob

	// runFn performs a workflow run against ComfyUI. Nil (production) uses realRun
	// (load workflow → convert if UI → preflight → submit → poll history/queue).
	// Tests inject a seam to drive job phases deterministically without a ComfyUI.
	runFn func(ctx context.Context, wf *store.Workflow, up runUpdater, opts runOptions) (*runResult, error)
	// comfyClientFn builds the ComfyUI client used by realRun and the /view proxy.
	// Nil (production) builds a comfy.Client from cfg.ComfyURL/ComfyToken; tests
	// inject a fake to exercise the real run orchestration and the view proxy.
	comfyClientFn func() comfyClient
	// captureFn is the output-capture seam invoked after a successful run settles
	// (off runMu, success path only). Nil (production) uses captureGeneration
	// (View → atomic write → InsertGeneration, best-effort); tests inject a seam to
	// assert capture is (or is not) invoked without touching a real ComfyUI/FS.
	captureFn func(wf *store.Workflow, opts runOptions, res *runResult)
	// downloadFn fetches the missing model file for the "Download & run" path. Nil
	// (production) uses downloadModelFile (HTTPS-checked fetch → size-capped atomic
	// write under comfy_model_path); tests inject a seam to drive the
	// download-and-run goroutine without network or disk.
	downloadFn func(ctx context.Context, pd pendingDownload, cb func(string)) error
	// evictMu serializes output-gallery cap enforcement. Captures are NOT mutually
	// exclusive — the run job clears `running` under runMu BEFORE the capture runs
	// off the mutex — so two captures can enforce the cap concurrently. Without this
	// each pass would delete rows under the other's already-measured total and
	// over-evict. It is deliberately SEPARATE from runMu (eviction must never hold
	// the run mutex).
	evictMu sync.Mutex
	// runMu guards runJob. One workflow run is active at a time (global MVP guard).
	runMu sync.Mutex
	// runJob is the current (or most recent) background run, or nil before the first.
	runJob *runJob
	// runSeq is a monotonic per-run counter (guarded by runMu). Every started run is
	// stamped with the next value and it is surfaced as data-run-seq on the run-status
	// fragment, giving each run a stable, strictly-increasing DOM identity. This lets a
	// caller (e.g. the ux-audit harness) tell THIS run's terminal panel apart from a
	// stale panel left in #run-status by a prior run of the same workflow, without
	// having to catch the transient in-flight "Stop" fragment.
	runSeq int64

	// cloudClientFn builds the CivitAI orchestration (cloud) client. Nil
	// (production) builds a comfy.CloudClient from the default base URL + the
	// CivitAI token; tests inject a fake to exercise the whatif/run/poll flow
	// without hitting civitai.com.
	cloudClientFn func() cloudClient
	// cloudMu guards cloudJob. One cloud run is active at a time (same global MVP
	// guard as the local run).
	cloudMu sync.Mutex
	// cloudPollInterval is how often the active cloud run goroutine polls the
	// orchestration API. Set once in NewServer (from defaultCloudPollInterval) and
	// never mutated afterward, so the poll goroutine reads it race-free; tests set
	// it on their own Server instance before starting a run.
	cloudPollInterval time.Duration
	// cloudJob is the current (or most recent) background cloud run, or nil before
	// the first.
	cloudJob *cloudJob

	// downloaderFn builds the CivitAI downloader used by the workflow-discovery
	// import to fetch a Workflows model's zip (with the user's token resolving
	// gated-file auth). Nil (production) builds a live civitai client from
	// cfg.BaseURL/cfg.Token; tests inject a fake that serves a canned zip without
	// touching civitai.com.
	downloaderFn func() civitai.Downloader

	// hfClientFn builds the HuggingFace fallback client (resolver + hardened
	// downloader) used when CivitAI resolution misses. Nil (production) builds a live
	// hf.Client from cfg.HFToken; tests inject a fake that returns a canned Match /
	// serves a canned body without touching huggingface.co.
	hfClientFn func() hfClient

	// appsClientFn builds the CivitAI apps-catalog client used by the /apps/discover
	// browse page. Nil (production) builds a live client from cfg.BaseURL/cfg.Token
	// (the token is optional — browsing is anon-capable; it only widens the visible
	// cohort). Tests inject a fake that serves a synthetic catalog without touching
	// civitai.com.
	appsClientFn func() appsLister

	// popularMu guards the in-process TTL cache of the "recent popular" feed shown
	// as the empty-query search default. The feed is keyed by the NSFW flag
	// (true=include NSFW+images, false=SFW-only) so a mode flip never serves the
	// other flag's cached list; each flag's entry is refreshed on expiry so every
	// dashboard/search load does not hit civitai.com.
	popularMu  sync.Mutex
	popularVal map[bool]*civitai.ModelSearchResult
	popularExp map[bool]time.Time

	// facetFeeds is the TTL cache for the browse-by-facet no-query workflow feeds
	// (ecosystem / use case). Its zero value is ready to use — see facetFeedCache
	// in discover_facets.go — so it needs no constructor line.
	facetFeeds facetFeedCache

	// resolveMu guards the in-process TTL cache of missing-model RESOLUTION searches
	// (the "Find on CivitAI" fragment on a failed-preflight panel). Keyed by
	// (query, types filter, nsfw flag) so repeated opens of the same panel do not
	// re-hit civitai.com; refreshed on expiry via popularTTL.
	resolveMu  sync.Mutex
	resolveVal map[string]*civitai.ModelSearchResult
	resolveExp map[string]time.Time

	// extProbeMu guards the short-lived cache of the civitai-manager ComfyUI helper
	// feature probe. The probe is a network round-trip to ComfyUI, so it is NEVER
	// run on a page render — only on the user action that needs it — and its result
	// (present AND absent alike) is cached for extProbeTTL so a repeated click, or
	// a ComfyUI that is down, does not re-probe every time.
	extProbeMu  sync.Mutex
	extProbeVal *extProbe
	extProbeExp time.Time
}

// extProbe is the cached outcome of a helper feature-detection probe. An absent
// helper is a normal outcome and is cached exactly like a present one.
type extProbe struct {
	// usable is the ONLY field that authorizes the one-click open. It requires
	// BOTH halves of the helper to be live: the ping route answering as ours AND
	// the frontend script actually being served. Ping alone is not enough — see
	// zombie.
	usable bool
	// zombie records the exact live-caught failure: the ping route still answers
	// (ComfyUI registered it at startup and holds the handler in memory) but the
	// frontend script 404s because the directory is gone. Nothing can happen in
	// that state, so the UI must say "restart ComfyUI", not "opened it".
	zombie bool
	// version is what the ping reported (empty unless the ping answered).
	version string
}

// popularTTL bounds how long the cached popular-models feed is served before a
// refresh fetch.
const popularTTL = 10 * time.Minute

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
	// scanned counts files streamed; matched/unmatched/pending partition them by
	// the streamed FileResult status (matched + unmatched + pending == scanned).
	// unmatched is a normal outcome (the file is not on CivitAI, or matching is
	// off), NOT an error; pending is a rate-limited/transient lookup to retry.
	scanned   int
	matched   int
	unmatched int
	pending   int
	// discovered is the TOTAL model-file count the walk found (the progress
	// denominator), set once from the scanner's OnDiscovered seam right after the
	// walk completes — before per-file streaming. 0 until the walk finishes, so the
	// scanning view shows "walking…" until it is known, then "N / discovered".
	discovered int
	// hashed accumulates the phase-1 hashing-progress increments from the scanner's
	// OnHashed seam (bumped +1 per file as it finishes hashing, BEFORE any card
	// streams). It is the numerator for the "Hashing… N / discovered" line the
	// scanning view shows during the otherwise-silent hash+batch phase, so the user
	// sees movement instead of a frozen "0 / total". Read/written only under scanMu.
	hashed int
	// noRemote records whether this scan ran with CivitAI matching DISABLED, so
	// the progress/terminal fragment can tell the user that near-zero matches are
	// expected (matching is off) rather than a broken scan.
	noRemote bool
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
	return &Server{
		store: st, reader: reader, sub: sub, cfg: cfg, log: log, csrf: newCSRFToken(),
		cloudPollInterval: defaultCloudPollInterval,
		popularVal:        map[bool]*civitai.ModelSearchResult{},
		popularExp:        map[bool]time.Time{},
		resolveVal:        map[string]*civitai.ModelSearchResult{},
		resolveExp:        map[string]time.Time{},
	}
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

// downloader returns the CivitAI downloader for the workflow-import fetch: the
// test seam when set, otherwise a live client built from the configured base URL
// and token (the token resolves gated-file auth). It is built per call (cheap);
// callers must have already verified a token is configured.
func (s *Server) downloader() civitai.Downloader {
	if s.downloaderFn != nil {
		return s.downloaderFn()
	}
	return civitai.New(s.cfg.BaseURL, s.cfg.Token)
}

// appsClient returns the CivitAI apps-catalog client: the test seam when set,
// otherwise a live client built from the configured base URL and token. The
// token is optional (browsing is anon-capable); it is sent only to widen the
// visible cohort for an enrolled user.
func (s *Server) appsClient() appsLister {
	if s.appsClientFn != nil {
		return s.appsClientFn()
	}
	return civitai.NewAppsClient(s.cfg.BaseURL, s.cfg.Token)
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
	mux.HandleFunc("GET /models/{id}/title", s.handleModelTitle)
	mux.HandleFunc("GET /models/{id}/version-status", s.handleModelVersionStatus)
	mux.HandleFunc("GET /models/{id}/card-images", s.handleModelCardImages)
	mux.HandleFunc("GET /models/{id}/community", s.handleModelCommunity)
	mux.HandleFunc("GET /models/{id}/subscribe-options", s.handleModelSubscribeOptions)
	mux.HandleFunc("GET /models/{id}/subscribe-control", s.handleModelSubscribeControl)
	mux.HandleFunc("POST /models/{id}/download", s.handleModelDownload)
	mux.HandleFunc("POST /models/{id}/subscribe", s.handleModelSubscribe)
	mux.HandleFunc("POST /models/{id}/unsubscribe", s.handleModelUnsubscribe)
	mux.HandleFunc("GET /creators/{username}", s.handleCreator)

	mux.HandleFunc("POST /settings/nsfw", s.handleSetNSFWDisplay)
	mux.HandleFunc("POST /settings/theme", s.handleSetTheme)
	mux.HandleFunc("POST /settings/outputs-rail", s.handleSetOutputsRail)

	mux.HandleFunc("POST /subscribe", s.handleSubscribe)
	mux.HandleFunc("GET /subscribe/search", s.handleSubscribeSearch)
	mux.HandleFunc("POST /subscriptions/{id}/flags", s.handleFlags)
	mux.HandleFunc("POST /subscriptions/{id}/delete", s.handleDelete)

	mux.HandleFunc("GET /fragments/events", s.handleEventsFragment)
	mux.HandleFunc("GET /fragments/queue", s.handleQueueFragment)

	mux.HandleFunc("GET /library", s.handleLibrary)
	mux.HandleFunc("GET /library/model-card/{id}", s.handleModelCard)
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

	mux.HandleFunc("POST /library/workflow-scan", s.handleWorkflowScan)
	mux.HandleFunc("GET /library/workflow-scan/status", s.handleWorkflowScanStatus)
	mux.HandleFunc("POST /library/workflow-scan/stop", s.handleWorkflowScanStop)

	// Apps discovery (Slice A1): a browse + click-to-play page for CivitAI Apps.
	// GET-only, read-only (no CSRF, not loopback-gated — it is an outbound proxy
	// GET like the model search, not an arbitrary-path primitive). The same
	// handler serves the full page and the HX results fragment.
	mux.HandleFunc("GET /apps/discover", s.handleDiscoverApps)

	// Output gallery — durable capture + browse of ComfyUI run outputs. The image
	// byte route is registered before /outputs/{id} but ServeMux prefers the more
	// specific literal-prefixed pattern regardless of order.
	mux.HandleFunc("GET /outputs", s.handleOutputs)
	mux.HandleFunc("GET /outputs/img/{imageID}", s.handleOutputsImage)
	mux.HandleFunc("GET /outputs/{id}", s.handleGenerationDetail)
	mux.HandleFunc("POST /outputs/{id}/rerun", s.handleGenerationRerun)
	mux.HandleFunc("POST /outputs/{id}/delete", s.handleGenerationDelete)

	mux.HandleFunc("GET /workflows", s.handleWorkflows)
	// Browse-only workflow discovery (Slice D1). Registered before the {id} route;
	// ServeMux prefers this more-specific literal path over /workflows/{id}.
	mux.HandleFunc("GET /workflows/discover", s.handleDiscoverWorkflows)
	// D2 import: download → unzip → store N workflows. A 4-segment literal-prefixed
	// path so it never collides with the /workflows/{id}/… POST controls below.
	mux.HandleFunc("POST /workflows/discover/{modelId}/import", s.handleWorkflowDiscoverImport)
	mux.HandleFunc("GET /workflows/{id}", s.handleWorkflowDetail)
	mux.HandleFunc("POST /workflows/import", s.handleWorkflowImport)
	mux.HandleFunc("POST /workflows/import-png", s.handleWorkflowImportPNG)
	mux.HandleFunc("POST /workflows/{id}/delete", s.handleWorkflowDelete)
	mux.HandleFunc("POST /workflows/{id}/attach", s.handleWorkflowAttach)
	mux.HandleFunc("POST /workflows/{id}/golden", s.handleWorkflowGolden)

	// Save a UI-format workflow into ComfyUI's editor + open it (CSRF + loopback
	// gated; reaches/writes the local ComfyUI, like run).
	mux.HandleFunc("POST /workflows/{id}/open-in-comfyui", s.handleWorkflowOpenInComfyUI)
	// Install/remove the civitai-manager ComfyUI helper extension. These WRITE into
	// the user's ComfyUI install directory (a configured filesystem path), so they
	// carry the same CSRF + loopback gating as every other path-taking endpoint —
	// and they are only ever reached by an explicit click, never on startup.
	mux.HandleFunc("POST /comfy/extension/install", s.handleComfyExtensionInstall)
	mux.HandleFunc("POST /comfy/extension/uninstall", s.handleComfyExtensionUninstall)
	mux.HandleFunc("POST /workflows/{id}/run", s.handleWorkflowRun)
	mux.HandleFunc("POST /workflows/{id}/run-with-params", s.handleWorkflowRunWithParams)
	mux.HandleFunc("POST /workflows/{id}/run-substitute", s.handleWorkflowRunSubstitute)
	mux.HandleFunc("POST /workflows/{id}/run-with-options", s.handleWorkflowRunWithOptions)
	mux.HandleFunc("POST /workflows/{id}/install-option-and-run", s.handleWorkflowInstallOptionAndRun)
	// Download-a-missing-model-into-ComfyUI-then-run (CSRF + loopback gated; reaches
	// civitai.com + the local filesystem). Disabled/degrades to link-only unless
	// comfy_model_path is a writable dir and the ComfyUI is local.
	mux.HandleFunc("POST /workflows/{id}/download-and-run", s.handleWorkflowDownloadAndRun)
	mux.HandleFunc("GET /workflows/{id}/run/comfy-status", s.handleWorkflowRunComfyStatus)
	mux.HandleFunc("GET /workflows/{id}/run/status", s.handleWorkflowRunStatus)
	mux.HandleFunc("GET /workflows/{id}/run/params", s.handleWorkflowRunParams)
	mux.HandleFunc("POST /workflows/run/stop", s.handleWorkflowRunStop)
	mux.HandleFunc("GET /workflows/run/view", s.handleWorkflowRunView)
	// Missing-model resolution fragment (read-only GET, loopback-gated, TTL-cached).
	mux.HandleFunc("GET /workflows/run/resolve-model", s.handleWorkflowResolveModel)

	mux.HandleFunc("GET /workflows/{id}/cloud", s.handleWorkflowCloud)
	mux.HandleFunc("POST /workflows/{id}/cloud/whatif", s.handleWorkflowCloudWhatif)
	mux.HandleFunc("POST /workflows/{id}/cloud/run", s.handleWorkflowCloudRun)
	mux.HandleFunc("GET /workflows/cloud/status", s.handleWorkflowCloudStatus)
	mux.HandleFunc("POST /workflows/cloud/stop", s.handleWorkflowCloudStop)

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
