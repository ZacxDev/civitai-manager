package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
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

			pol := poller.New(a.store, a.client, a.cfg.ModelRoot, a.log)
			wrk := queue.New(a.store, a.client, a.client, a.log)
			srv := web.NewServer(a.store, a.client, pol, web.Config{
				BaseURL:             a.cfg.BaseURL,
				DefaultPollInterval: a.cfg.DefaultPollInterval.D(),
			}, a.log)

			go pol.Run(ctx)
			go wrk.Run(ctx)

			httpSrv := &http.Server{
				Addr:              a.cfg.Addr,
				Handler:           srv.Handler(),
				ReadHeaderTimeout: 10 * time.Second,
			}

			a.log.Info("starting", "addr", a.cfg.Addr, "config", a.cfg.String())
			fmt.Printf("civitai-manager serving on http://localhost%s\n", a.cfg.Addr)

			errCh := make(chan error, 1)
			go func() {
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					errCh <- err
				}
			}()

			select {
			case <-ctx.Done():
				a.log.Info("shutting down")
			case err := <-errCh:
				return err
			}
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutCancel()
			return httpSrv.Shutdown(shutCtx)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "listen address (default from config, :8787)")
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

			pol := poller.New(a.store, a.client, a.cfg.ModelRoot, a.log)
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

			pol := poller.New(a.store, a.client, a.cfg.ModelRoot, a.log)
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
