// Package cli wires the cobra command tree for civitai-manager: serve (web UI +
// poller + download worker), subscribe, list, unsubscribe, and check.
package cli

import (
	"io"
	"log/slog"
	"os"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/store"
	"github.com/spf13/cobra"
)

// globalFlags collects the flags shared by every command.
type globalFlags struct {
	configPath     string
	token          string
	baseURL        string
	modelRoot      string
	dbPath         string
	maxFileSize    string
	downloadJitter string
	trashDir       string
	webScanTimeout string
	verbose        bool
}

// BuildInfo carries release metadata injected via -ldflags into package main
// and passed through to the root command's version output.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Execute builds and runs the root command with the given build metadata.
func Execute(bi BuildInfo) error {
	return newRootCmd(bi).Execute()
}

func newRootCmd(bi BuildInfo) *cobra.Command {
	gf := &globalFlags{}
	root := &cobra.Command{
		Use:   "civitai-manager",
		Short: "Subscribe to CivitAI models/creators and auto-download new versions",
		Long: "civitai-manager subscribes to CivitAI models and creators, polls for new\n" +
			"versions, and auto-queues downloads. Run `civitai-manager serve` for the\n" +
			"web UI, or use the CLI subcommands directly.",
		Version:       bi.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate(
		"civitai-manager {{.Version}} (commit " + bi.Commit + ", built " + bi.Date + ")\n")

	pf := root.PersistentFlags()
	pf.StringVar(&gf.configPath, "config", "", "config file path (default: XDG config dir)")
	pf.StringVar(&gf.token, "token", "", "CivitAI API token (overrides env/config; never logged)")
	pf.StringVar(&gf.baseURL, "base-url", "", "CivitAI API base URL (default https://civitai.com)")
	pf.StringVar(&gf.modelRoot, "model-root", "", "root directory for downloaded models")
	pf.StringVar(&gf.dbPath, "db", "", "SQLite database path")
	pf.StringVar(&gf.maxFileSize, "max-file-size", "", "skip auto-downloads whose primary file exceeds this size (e.g. 500MB, 2GB; 0/empty = unlimited)")
	pf.StringVar(&gf.downloadJitter, "download-jitter", "", "anti-stampede window: schedule each auto-download at a random point in [0, dur) (e.g. 15m; 0 = start immediately)")
	pf.StringVar(&gf.trashDir, "trash-dir", "", "quarantine trash directory (default <model-root>/.trash)")
	pf.BoolVarP(&gf.verbose, "verbose", "v", false, "verbose logging")

	root.AddCommand(
		newServeCmd(gf),
		newSubscribeCmd(gf),
		newListCmd(gf),
		newUnsubscribeCmd(gf),
		newSearchCmd(gf),
		newCheckCmd(gf),
		newScanCmd(gf),
		newLibraryCmd(gf),
	)
	return root
}

// app bundles the resolved runtime dependencies. client is the narrowed
// civitai.Client interface (not the concrete *sdk.Client) so command logic can
// be exercised with an in-memory fake in tests.
type app struct {
	cfg    *config.Config
	store  *store.Store
	client civitai.Client
	log    *slog.Logger
	// verbose mirrors the global -v flag: it raises the interactive command logger
	// (see cmdLogger) from WARN to DEBUG.
	verbose bool
	// logWriter is where cmdLogger writes (default os.Stderr). Tests point it at a
	// buffer to assert on / suppress the worker+poller log output.
	logWriter io.Writer
}

// close releases the store.
func (a *app) close() {
	if a.store != nil {
		_ = a.store.Close()
	}
}

// cmdLogger builds the logger for interactive one-shot commands (subscribe,
// check) that carry their own friendly progress/summary prints. At the default
// verbosity it logs at WARN so the raw structured INFO lines (`level=INFO
// msg=downloading …`) do not interleave with the friendly output; -v raises it to
// DEBUG so the full structured logs are still available. Errors/warnings always
// surface. serve keeps its own INFO-level a.log (it is a long-running daemon whose
// stderr IS its operational log).
func (a *app) cmdLogger() *slog.Logger {
	w := a.logWriter
	if w == nil {
		w = os.Stderr
	}
	level := slog.LevelWarn
	if a.verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// build resolves configuration, opens the store, and constructs the API client.
func (gf *globalFlags) build() (*app, error) {
	cfg, err := config.Resolve(config.Flags{
		ConfigPath:     gf.configPath,
		Token:          gf.token,
		BaseURL:        gf.baseURL,
		ModelRoot:      gf.modelRoot,
		DBPath:         gf.dbPath,
		MaxFileSize:    gf.maxFileSize,
		DownloadJitter: gf.downloadJitter,
		TrashDir:       gf.trashDir,
		WebScanTimeout: gf.webScanTimeout,
	})
	if err != nil {
		return nil, err
	}

	level := slog.LevelInfo
	if gf.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	client := civitai.New(cfg.BaseURL, cfg.Token)
	return &app{cfg: cfg, store: st, client: client, log: log, verbose: gf.verbose}, nil
}
