package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
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
		ModelRoot:           cfg.ModelRoot,
		TrashDir:            cfg.TrashDir,
		LibraryPaths:        cfg.LibraryPaths,
		Extensions:          cfg.LibraryExtensions,
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

			opts := poller.SubscribeOptions{
				AutoDownload:    !noAuto,
				NotifyOnly:      notifyOnly,
				BackfillLatest:  backfillLatest,
				BaseModelFilter: baseModel,
				FileTypePref:    fileType,
				PollInterval:    a.cfg.DefaultPollInterval.D(),
			}
			return subscribeRun(ctx, a, cmd.OutOrStdout(), creator, args, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&creator, "creator", "", "subscribe to a creator username instead of a model")
	f.BoolVar(&notifyOnly, "notify-only", false, "record new versions but do not download")
	f.BoolVar(&noAuto, "no-auto", false, "disable auto-download for this subscription")
	f.BoolVar(&backfillLatest, "backfill-latest", false, "download the current latest version now, before returning")
	f.StringVar(&baseModel, "base-model", "", "only download versions matching this base model")
	f.StringVar(&fileType, "file-type", "", "preferred file type to download (e.g. Model, VAE)")
	return cmd
}

// subscribeRun creates the subscription (seeding its version ledger) and, when
// --backfill-latest was requested, SYNCHRONOUSLY downloads the enqueued latest
// version before returning — draining it through the exact one-shot worker path
// `check --download` uses. A download failure propagates as a non-zero exit.
// Plain subscribe (no --backfill-latest) stays enqueue-only. It is factored out
// of the cobra RunE so it can be exercised with an in-memory app + fake client.
func subscribeRun(ctx context.Context, a *app, out io.Writer, creator string, args []string, opts poller.SubscribeOptions) error {
	pol := configuredPoller(a.store, a.client, a.cfg, a.log)

	var subID int64
	if creator != "" {
		id, err := pol.SubscribeCreator(ctx, creator, opts)
		switch {
		case errors.Is(err, poller.ErrAlreadySubscribed) && opts.BackfillLatest:
			// Recovery path: an existing subscription whose backfill previously
			// failed is otherwise un-retryable (the version is already seen). Re-run
			// the backfill of its current latest instead of erroring.
			existing, ferr := a.store.FindCreatorSubscription(creator)
			if ferr != nil {
				return ferr
			}
			subID = existing.ID
			fmt.Fprintf(out, "Already subscribed to creator @%s (subscription #%d); re-attempting latest download.\n", creator, subID)
			if _, berr := pol.BackfillLatest(ctx, subID); berr != nil {
				return fmt.Errorf("backfill: %w", berr)
			}
		case err != nil:
			return err
		default:
			subID = id
			fmt.Fprintf(out, "Subscribed to creator @%s (subscription #%d)\n", creator, id)
		}
	} else {
		if len(args) == 0 {
			return fmt.Errorf("provide a model id/URL, or use --creator <username>")
		}
		modelID, err := civitai.ParseModelRef(args[0])
		if err != nil {
			return err
		}
		id, err := pol.SubscribeModel(ctx, modelID, opts)
		switch {
		case errors.Is(err, poller.ErrAlreadySubscribed) && opts.BackfillLatest:
			existing, ferr := a.store.FindModelSubscription(modelID)
			if ferr != nil {
				return ferr
			}
			subID = existing.ID
			fmt.Fprintf(out, "Already subscribed to model %d (subscription #%d); re-attempting latest download.\n", modelID, subID)
			if _, berr := pol.BackfillLatest(ctx, subID); berr != nil {
				return fmt.Errorf("backfill: %w", berr)
			}
		case err != nil:
			return err
		default:
			subID = id
			fmt.Fprintf(out, "Subscribed to model %d (subscription #%d)\n", modelID, id)
		}
	}

	if !opts.BackfillLatest {
		return nil
	}

	// --backfill-latest promises the current latest version is downloaded before
	// the command returns. Seeding already enqueued it (with no anti-stampede
	// jitter, so it is immediately claimable); drain it here to disk now.
	//
	// The drain is SCOPED to this subscription: DrainAll would claim every due
	// queued row (including a prior `check`'s backlog or another subscription's
	// jitter-elapsed auto-downloads), so subscribing to model B could
	// synchronously download model A's backlog and misreport the count. A scoped
	// drain also surfaces a failed backfill directly (it returns the error rather
	// than recording it on the row and exiting 0), so no separate failed-row scan
	// is needed.
	fmt.Fprintln(out, "Downloading latest version...")
	downloaded, err := drainSubscriptionDownloads(ctx, a, subID)
	if err != nil {
		return fmt.Errorf("backfill download failed: %w", err)
	}

	if downloaded == 0 {
		fmt.Fprintln(out, "No file downloaded (nothing to backfill, or filtered out).")
	} else {
		fmt.Fprintf(out, "Downloaded %d file(s).\n", downloaded)
	}
	return nil
}

// formatCheckSummary renders the `check --download` completion line from the
// real run counts, so a successful download reads as such instead of the old,
// confusing "0 item(s) queued for download."
func formatCheckSummary(newCount, downloaded, remaining int) string {
	return fmt.Sprintf("Poll complete. %d new version(s) found, %d downloaded, %d remaining in queue.",
		newCount, downloaded, remaining)
}

// drainDownloads runs the one-shot download worker to completion, returning the
// number of files that finished. Shared by `check --download` and
// `subscribe --backfill-latest` so both use the identical worker path.
func drainDownloads(ctx context.Context, a *app) (int, error) {
	wrk := queue.New(a.store, a.client, a.client, a.log)
	return wrk.DrainAll(ctx)
}

// drainSubscriptionDownloads runs the one-shot download worker but only over
// rows belonging to subID, returning the number that finished. Used by
// `subscribe --backfill-latest` so the synchronous drain is confined to the
// subscription just created and never touches an unrelated backlog.
func drainSubscriptionDownloads(ctx context.Context, a *app, subID int64) (int, error) {
	wrk := queue.New(a.store, a.client, a.client, a.log)
	return wrk.DrainSubscription(ctx, subID)
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
			newCount, err := pol.PollAll(ctx)
			if err != nil {
				a.log.Warn("some polls failed", "err", err)
			}

			out := cmd.OutOrStdout()
			if download {
				downloaded, derr := drainDownloads(ctx, a)
				if derr != nil {
					return derr
				}
				remaining, _ := a.store.ListQueue(store.StatusQueued)
				fmt.Fprintln(out, formatCheckSummary(newCount, downloaded, len(remaining)))
				return nil
			}

			queued, _ := a.store.ListQueue(store.StatusQueued)
			fmt.Fprintf(out, "Poll complete. %d new version(s) found, %d queued for download.\n",
				newCount, len(queued))
			return nil
		},
	}
	cmd.Flags().BoolVar(&download, "download", false, "also download queued files now (default: leave queued for serve)")
	return cmd
}
