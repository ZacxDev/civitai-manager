// Package cli wires the cobra command tree for civitai-manager: serve (web UI +
// poller + download worker), subscribe, list, unsubscribe, and check.
package cli

import (
	"log/slog"
	"os"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/store"
	sdk "github.com/civitai/cli/pkg/civitai"
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
	verbose        bool
}

// Execute builds and runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}

func newRootCmd() *cobra.Command {
	gf := &globalFlags{}
	root := &cobra.Command{
		Use:   "civitai-manager",
		Short: "Subscribe to CivitAI models/creators and auto-download new versions",
		Long: "civitai-manager subscribes to CivitAI models and creators, polls for new\n" +
			"versions, and auto-queues downloads. Run `civitai-manager serve` for the\n" +
			"web UI, or use the CLI subcommands directly.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := root.PersistentFlags()
	pf.StringVar(&gf.configPath, "config", "", "config file path (default: XDG config dir)")
	pf.StringVar(&gf.token, "token", "", "CivitAI API token (overrides env/config; never logged)")
	pf.StringVar(&gf.baseURL, "base-url", "", "CivitAI API base URL (default https://civitai.com)")
	pf.StringVar(&gf.modelRoot, "model-root", "", "root directory for downloaded models")
	pf.StringVar(&gf.dbPath, "db", "", "SQLite database path")
	pf.StringVar(&gf.maxFileSize, "max-file-size", "", "skip auto-downloads whose primary file exceeds this size (e.g. 500MB, 2GB; 0/empty = unlimited)")
	pf.StringVar(&gf.downloadJitter, "download-jitter", "", "anti-stampede window: schedule each auto-download at a random point in [0, dur) (e.g. 15m; 0 = start immediately)")
	pf.BoolVarP(&gf.verbose, "verbose", "v", false, "verbose logging")

	root.AddCommand(
		newServeCmd(gf),
		newSubscribeCmd(gf),
		newListCmd(gf),
		newUnsubscribeCmd(gf),
		newCheckCmd(gf),
	)
	return root
}

// app bundles the resolved runtime dependencies.
type app struct {
	cfg    *config.Config
	store  *store.Store
	client *sdk.Client
	log    *slog.Logger
}

// close releases the store.
func (a *app) close() {
	if a.store != nil {
		_ = a.store.Close()
	}
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
	return &app{cfg: cfg, store: st, client: client, log: log}, nil
}
