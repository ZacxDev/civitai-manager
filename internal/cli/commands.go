package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/poller"
	"github.com/ZacxDev/civitai-manager/internal/queue"
	"github.com/ZacxDev/civitai-manager/internal/store"
	"github.com/ZacxDev/civitai-manager/internal/web"
	"github.com/spf13/cobra"
)

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// configuredPoller builds a poller with the runtime size cap and anti-stampede
// download jitter applied from config.
func configuredPoller(st *store.Store, client civitai.Client, cfg *config.Config, log *slog.Logger) *poller.Poller {
	p := poller.New(st, client, cfg.ModelRoot, log)
	p.SetMaxFileSize(cfg.MaxFileSizeBytes)
	p.SetDownloadJitter(cfg.DownloadJitter.D())
	return p
}

// displayAddr renders a listen address as a browsable URL host. A bare ":8787"
// or a 0.0.0.0-bound address is shown as localhost (the process still binds the
// configured interface); any other host is shown as-is so the printed link is
// truthful rather than always claiming "localhost".
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	if host, port, ok := strings.Cut(addr, ":"); ok {
		if host == "" || host == "0.0.0.0" || host == "::" {
			return "localhost:" + port
		}
	}
	return addr
}

// serveRun starts the web UI, poller, and download worker, and blocks until ctx
// is cancelled (SIGINT/SIGTERM) or the HTTP server fails. On shutdown it stops
// accepting connections, cancels the background goroutines, and WAITS for the
// poller and worker to return before returning — so their final status writes
// land before the caller closes the store. It does not close the store.
func serveRun(ctx context.Context, st *store.Store, client civitai.Client, cfg *config.Config, log *slog.Logger) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pol := configuredPoller(st, client, cfg, log)
	wrk := queue.New(st, client, client, log)
	srv := web.NewServer(st, client, pol, web.Config{
		BaseURL:             cfg.BaseURL,
		DefaultPollInterval: cfg.DefaultPollInterval.D(),
	}, log)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pol.Run(ctx) }()
	go func() { defer wg.Done(); wrk.Run(ctx) }()

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Info("starting", "addr", cfg.Addr, "config", cfg.String())
	fmt.Printf("civitai-manager serving on http://%s\n", displayAddr(cfg.Addr))

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errCh:
		runErr = err
	}

	// Stop accepting HTTP, then stop the background goroutines and WAIT for them
	// so the final download/status write lands before the store is closed.
	cancel()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil && runErr == nil {
		runErr = err
	}
	wg.Wait()
	return runErr
}

func newServeCmd(gf *globalFlags) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web UI, subscription poller, and download worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()
			if addr != "" {
				a.cfg.Addr = addr
			}

			ctx, cancel := signalContext()
			defer cancel()

			return serveRun(ctx, a.store, a.client, a.cfg, a.log)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default from config, 127.0.0.1:8787); use a non-loopback host to expose the UI on your LAN")
	return cmd
}

func newSubscribeCmd(gf *globalFlags) *cobra.Command {
	var (
		creator        string
		notifyOnly     bool
		noAuto         bool
		backfillLatest bool
		baseModel      string
		fileType       string
	)
	cmd := &cobra.Command{
		Use:   "subscribe [model-id | model-url]",
		Short: "Subscribe to a model or creator and seed its version ledger",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			pol := configuredPoller(a.store, a.client, a.cfg, a.log)
			opts := poller.SubscribeOptions{
				AutoDownload:    !noAuto,
				NotifyOnly:      notifyOnly,
				BackfillLatest:  backfillLatest,
				BaseModelFilter: baseModel,
				FileTypePref:    fileType,
				PollInterval:    a.cfg.DefaultPollInterval.D(),
			}

			if creator != "" {
				id, err := pol.SubscribeCreator(ctx, creator, opts)
				if err != nil {
					return err
				}
				fmt.Printf("Subscribed to creator @%s (subscription #%d)\n", creator, id)
				return nil
			}
			if len(args) == 0 {
				return fmt.Errorf("provide a model id/URL, or use --creator <username>")
			}
			modelID, err := civitai.ParseModelRef(args[0])
			if err != nil {
				return err
			}
			id, err := pol.SubscribeModel(ctx, modelID, opts)
			if err != nil {
				return err
			}
			fmt.Printf("Subscribed to model %d (subscription #%d)\n", modelID, id)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&creator, "creator", "", "subscribe to a creator username instead of a model")
	f.BoolVar(&notifyOnly, "notify-only", false, "record new versions but do not download")
	f.BoolVar(&noAuto, "no-auto", false, "disable auto-download for this subscription")
	f.BoolVar(&backfillLatest, "backfill-latest", false, "download the current latest version on subscribe")
	f.StringVar(&baseModel, "base-model", "", "only download versions matching this base model")
	f.StringVar(&fileType, "file-type", "", "preferred file type to download (e.g. Model, VAE)")
	return cmd
}

func newListCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List subscriptions",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			subs, err := a.store.ListSubscriptions()
			if err != nil {
				return err
			}
			if len(subs) == 0 {
				fmt.Println("No subscriptions.")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tKIND\tTARGET\tAUTO\tNOTIFY\tINTERVAL\tLAST POLLED")
			for _, s := range subs {
				last := "never"
				if s.LastPolledAt != nil && !s.LastPolledAt.IsZero() {
					last = s.LastPolledAt.Local().Format("2006-01-02 15:04")
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%t\t%t\t%s\t%s\n",
					s.ID, s.Kind, s.Label(), s.AutoDownload, s.NotifyOnly,
					s.PollInterval(), last)
			}
			return tw.Flush()
		},
	}
}

func newUnsubscribeCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "unsubscribe <id>",
		Short: "Remove a subscription",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid subscription id %q", args[0])
			}
			if err := a.store.DeleteSubscription(id); err != nil {
				if err == store.ErrNotFound {
					return fmt.Errorf("no subscription with id %d", id)
				}
				return err
			}
			fmt.Printf("Removed subscription #%d\n", id)
			return nil
		},
	}
}

func newCheckCmd(gf *globalFlags) *cobra.Command {
	var download bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Poll all subscriptions once (for cron), then optionally drain the download queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			pol := configuredPoller(a.store, a.client, a.cfg, a.log)
			if err := pol.PollAll(ctx); err != nil {
				a.log.Warn("some polls failed", "err", err)
			}

			if download {
				wrk := queue.New(a.store, a.client, a.client, a.log)
				if err := wrk.DrainAll(ctx); err != nil {
					return err
				}
			}

			queued, _ := a.store.ListQueue(store.StatusQueued)
			fmt.Printf("Poll complete. %d item(s) queued for download.\n", len(queued))
			return nil
		},
	}
	cmd.Flags().BoolVar(&download, "download", false, "also download queued files now (default: leave queued for serve)")
	return cmd
}
