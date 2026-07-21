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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/hashutil"
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
	wrk.SetPreviewPolicy(cfg.NoPreview, cfg.MaxPreviewSizeBytes)
	srv := web.NewServer(st, client, pol, web.Config{
		BaseURL:             cfg.BaseURL,
		DefaultPollInterval: cfg.DefaultPollInterval.D(),
		Addr:                cfg.Addr,
		ModelRoot:           cfg.ModelRoot,
		TrashDir:            cfg.TrashDir,
		LibraryPaths:        cfg.LibraryPaths,
		Extensions:          cfg.LibraryExtensions,
		WebScanTimeout:      cfg.WebScanTimeout.D(),
		WebScanMaxFiles:     cfg.WebScanMaxFiles,
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
	var (
		addr           string
		webScanTimeout string
	)
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web UI, subscription poller, and download worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			gf.webScanTimeout = webScanTimeout
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
	cmd.Flags().StringVar(&webScanTimeout, "web-scan-timeout", "", "deadline for a web \"Scan now\" (e.g. 2m; default from config). Bounds the web-triggered directory walk/hash")
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
	// The worker/poller log at WARN by default so their structured INFO lines do
	// not interleave with the friendly progress prints below; -v raises them to
	// DEBUG (see app.cmdLogger).
	log := a.cmdLogger()
	pol := configuredPoller(a.store, a.client, a.cfg, log)

	// The CLI performs the backfill EXPLICITLY (below) rather than letting the
	// seeding poll enqueue it, so it can surface the precise BackfillOutcome. The
	// seed therefore only seeds the ledger.
	seedOpts := opts
	seedOpts.BackfillLatest = false

	var subID int64
	if creator != "" {
		id, err := pol.SubscribeCreator(ctx, creator, seedOpts)
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
		id, err := pol.SubscribeModel(ctx, modelID, seedOpts)
		switch {
		case errors.Is(err, poller.ErrAlreadySubscribed) && opts.BackfillLatest:
			existing, ferr := a.store.FindModelSubscription(modelID)
			if ferr != nil {
				return ferr
			}
			subID = existing.ID
			fmt.Fprintf(out, "Already subscribed to model %d (subscription #%d); re-attempting latest download.\n", modelID, subID)
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
	// the command returns. Enqueue it now (no anti-stampede jitter, so it is
	// immediately claimable) and capture WHY if nothing was enqueued, then drain
	// it to disk.
	//
	// The drain is SCOPED to this subscription: DrainAll would claim every due
	// queued row (including a prior `check`'s backlog or another subscription's
	// jitter-elapsed auto-downloads), so subscribing to model B could
	// synchronously download model A's backlog and misreport the count. A scoped
	// drain also surfaces a failed backfill directly (it returns the error rather
	// than recording it on the row and exiting 0), so no separate failed-row scan
	// is needed.
	fmt.Fprintln(out, "Downloading latest version...")
	bf, err := pol.BackfillLatest(ctx, subID)
	if err != nil {
		return fmt.Errorf("backfill: %w", err)
	}
	completed, err := drainSubscriptionDownloads(ctx, a, subID, log)
	if err != nil {
		return fmt.Errorf("backfill download failed: %w", err)
	}

	if len(completed) == 0 {
		// Nothing was downloaded: report the specific reason (already present on
		// disk, filtered out, no downloadable file, nothing to back-fill) instead
		// of a single overloaded line.
		fmt.Fprintln(out, backfillReasonMessage(bf))
	} else {
		printDownloadVerification(out, completed)
		fmt.Fprintf(out, "Downloaded %d file(s).\n", len(completed))
	}
	return nil
}

// backfillReasonMessage renders the precise user-facing line for a backfill that
// downloaded nothing, distinguishing the cases the old generic "No file
// downloaded (…)" line conflated.
func backfillReasonMessage(bf poller.BackfillOutcome) string {
	switch bf.Reason {
	case poller.BackfillAlreadyPresent:
		return fmt.Sprintf("Already have the latest version (%s) on disk.", backfillVersionLabel(bf))
	case poller.BackfillCompletedMissingOnDisk:
		return fmt.Sprintf("A completed download exists for %s but the file is missing on disk — run 'verify --repair'.", backfillVersionLabel(bf))
	case poller.BackfillFilteredBaseModel:
		return fmt.Sprintf("Latest version skipped: base model does not match filter %q.", bf.Detail)
	case poller.BackfillFilteredSize:
		return fmt.Sprintf("Latest version skipped: %s.", bf.Detail)
	case poller.BackfillNoDownloadableFile:
		return "Latest version has no downloadable file."
	case poller.BackfillNoCandidates:
		return "Nothing to back-fill: the subscription has no versions yet."
	case poller.BackfillTransientError:
		return "Could not resolve the latest version to download (temporary); it will be retried on the next poll."
	default:
		return "No file downloaded (nothing new to back-fill)."
	}
}

// backfillVersionLabel names the version in a friendly way: its name when the
// API supplied one (e.g. "v1.5"), otherwise its numeric id ("version 12345").
func backfillVersionLabel(bf poller.BackfillOutcome) string {
	if bf.VersionName != "" {
		return bf.VersionName
	}
	return fmt.Sprintf("version %d", bf.VersionID)
}

// printDownloadVerification prints one friendly, user-facing line per completed
// download conveying whether its bytes were hash-verified against the API's
// expected SHA256. This surfaces the tool's headline safety guarantee at the
// DEFAULT verbosity: the worker's structured slog lines are suppressed unless -v
// is set (see app.cmdLogger), so without this the user would see only
// "Downloaded N file(s)." with no indication the bytes were verified. A file the
// API gave no hash for is finalized but explicitly flagged UNVERIFIED — it must
// never read as verified.
func printDownloadVerification(out io.Writer, items []store.QueueItem) {
	for _, it := range items {
		name := onDiskName(it)
		if it.SHA256Expected == "" {
			fmt.Fprintf(out, "⚠ %s (unverified — no hash from API)\n", name)
			continue
		}
		sum := it.SHA256Actual
		if sum == "" {
			sum = it.SHA256Expected
		}
		fmt.Fprintf(out, "✓ %s (sha256 %s verified)\n", name, shortHash(sum))
	}
}

// onDiskName returns the actual on-disk file name for a completed row. Files are
// written version-name-cased (e.g. EasyNegative.safetensors) via
// civitai.DestPath, which can differ from the API's file.Name
// (easynegative.safetensors); printing the API name made grepping the printed
// name for the real file fail. It falls back to the API FileName only when the
// destination path is somehow empty.
func onDiskName(it store.QueueItem) string {
	if it.DestPath != "" {
		return filepath.Base(it.DestPath)
	}
	return it.FileName
}

// shortHash truncates a hex digest to a compact display prefix.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// formatCheckSummary renders the `check --download` completion line from the
// real run counts, so a successful download reads as such instead of the old,
// confusing "0 item(s) queued for download."
func formatCheckSummary(newCount, downloaded, remaining int) string {
	return fmt.Sprintf("Poll complete. %d new version(s) found, %d downloaded, %d remaining in queue.",
		newCount, downloaded, remaining)
}

// newWorker builds a download Worker with the app's preview policy
// (--no-preview / --max-preview-size) applied, so every one-shot drain path
// honours the same sidecar configuration as `serve`.
func newWorker(a *app, log *slog.Logger) *queue.Worker {
	wrk := queue.New(a.store, a.client, a.client, log)
	wrk.SetPreviewPolicy(a.cfg.NoPreview, a.cfg.MaxPreviewSizeBytes)
	return wrk
}

// drainDownloads runs the one-shot download worker to completion, returning the
// number of files that finished. Shared by `check --download` and
// `subscribe --backfill-latest` so both use the identical worker path.
func drainDownloads(ctx context.Context, a *app, log *slog.Logger) ([]store.QueueItem, error) {
	return newWorker(a, log).DrainAll(ctx)
}

// drainSubscriptionDownloads runs the one-shot download worker but only over
// rows belonging to subID, returning the number that finished. Used by
// `subscribe --backfill-latest` so the synchronous drain is confined to the
// subscription just created and never touches an unrelated backlog.
func drainSubscriptionDownloads(ctx context.Context, a *app, subID int64, log *slog.Logger) ([]store.QueueItem, error) {
	return newWorker(a, log).DrainSubscription(ctx, subID)
}

// drainItemDownloads runs the one-shot download worker over ONLY the given row
// ids, returning the rows that finished. Used by `verify --repair` so the
// synchronous re-download is confined to the rows the repair just re-enqueued
// and never claims an unrelated queued backlog.
func drainItemDownloads(ctx context.Context, a *app, ids []int64, log *slog.Logger) ([]store.QueueItem, error) {
	return newWorker(a, log).DrainItems(ctx, ids)
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

			// WARN-by-default logger (see app.cmdLogger) so structured INFO lines do
			// not interleave with the friendly summary; -v restores the detail.
			log := a.cmdLogger()
			pol := configuredPoller(a.store, a.client, a.cfg, log)
			newCount, err := pol.PollAll(ctx)
			if err != nil {
				log.Warn("some polls failed", "err", err)
			}

			out := cmd.OutOrStdout()
			if download {
				completed, derr := drainDownloads(ctx, a, log)
				if derr != nil {
					return derr
				}
				printDownloadVerification(out, completed)
				remaining, _ := a.store.ListQueue(store.StatusQueued)
				fmt.Fprintln(out, formatCheckSummary(newCount, len(completed), len(remaining)))
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

func newVerifyCmd(gf *globalFlags) *cobra.Command {
	var (
		repair    bool
		checkHash bool
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Reconcile downloaded files against the ledger; re-download missing/corrupt ones with --repair",
		Long: "verify reconciles the files civitai-manager downloaded (the completed\n" +
			"download-queue rows) against what is actually on disk. By default it does a\n" +
			"cheap existence check — a file the tool downloaded but that has since been\n" +
			"deleted or moved is reported MISSING. With --check-hash it additionally\n" +
			"re-hashes present files and reports any whose contents no longer match the\n" +
			"expected SHA256 as CORRUPT (slower). Plain verify only reports (exit 0);\n" +
			"verify --repair re-downloads the offending files.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := gf.build()
			if err != nil {
				return err
			}
			defer a.close()

			ctx, cancel := signalContext()
			defer cancel()

			return verifyRun(ctx, a, cmd.OutOrStdout(), repair, checkHash)
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "re-download files reported MISSING (and CORRUPT, with --check-hash)")
	cmd.Flags().BoolVar(&checkHash, "check-hash", false, "also re-hash present files and flag content that no longer matches (slower)")
	return cmd
}

// verifyRun reconciles the tool's download-queue rows against disk. The source
// of truth is the set of files civitai-manager downloaded: each row carries a
// dest_path and (usually) a sha256_expected. A missing file — the deleted/moved
// case a normal poll can never recover, because its version is already in
// seen_versions — is detected here and, with --repair, re-downloaded by
// transitioning the row back to queued (see store.RequeueDone /
// store.RequeueFailed) and draining ONLY those re-enqueued rows through the
// shared worker path. Plain verify only reports and exits 0.
//
// Both 'done' AND terminally 'failed' rows are reconciled: a prior
// `verify --repair` whose re-download failed leaves the row in 'failed', so
// including failed rows here (whose file is absent → MISSING/repairable) is what
// makes repair idempotently retryable — a second `verify --repair` re-detects
// and re-attempts it instead of reporting "Nothing to repair".
func verifyRun(ctx context.Context, a *app, out io.Writer, repair, checkHash bool) error {
	log := a.cmdLogger()

	done, err := a.store.ListQueue(store.StatusDone)
	if err != nil {
		return err
	}
	failed, err := a.store.ListQueue(store.StatusFailed)
	if err != nil {
		return err
	}
	// Reconcile completed downloads AND previously-failed repairs against disk.
	rows := append(append([]store.QueueItem{}, done...), failed...)

	var (
		checked int
		okCount int
		missing []store.QueueItem
		corrupt []store.QueueItem
	)
	for _, it := range rows {
		// Abort promptly on Ctrl-C: with --check-hash each iteration may re-hash a
		// multi-GB file, so a cancelled context must not have to wait out the whole
		// loop. Checked between files (the per-file hash itself is not interruptible).
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if it.DestPath == "" {
			// No destination recorded — nothing on disk to reconcile against.
			continue
		}
		checked++
		fi, statErr := os.Stat(it.DestPath)
		if statErr != nil || fi.IsDir() {
			// Not a regular file present at the recorded path → treat as missing.
			missing = append(missing, it)
			continue
		}
		if checkHash && it.SHA256Expected != "" {
			if !hashutil.FileMatches(it.DestPath, it.SHA256Expected) {
				corrupt = append(corrupt, it)
				continue
			}
		}
		okCount++
	}

	fmt.Fprintf(out, "Checked %d downloaded file(s): %d OK, %d missing, %d corrupt.\n",
		checked, okCount, len(missing), len(corrupt))
	for _, it := range missing {
		fmt.Fprintf(out, "  MISSING  %s\n", it.DestPath)
	}
	for _, it := range corrupt {
		fmt.Fprintf(out, "  CORRUPT  %s\n", it.DestPath)
	}

	problems := append(append([]store.QueueItem{}, missing...), corrupt...)
	if !repair {
		if len(problems) > 0 {
			fmt.Fprintln(out, "Run 'verify --repair' to re-download the file(s) listed above.")
		}
		return nil
	}
	if len(problems) == 0 {
		fmt.Fprintln(out, "Nothing to repair.")
		return nil
	}

	// Re-enqueue each problem row back to queued, resetting the download state. A
	// 'done' row uses RequeueDone (done→queued stays in the active status set); a
	// 'failed' row (a prior repair that could not complete) uses RequeueFailed. The
	// ux_dlq_active partial-unique index sees no conflict: at most one active row
	// per version_id/file_id, and a row never conflicts with itself on UPDATE.
	repairIDs := make([]int64, 0, len(problems))
	for _, it := range problems {
		var rerr error
		if it.Status == store.StatusFailed {
			rerr = a.store.RequeueFailed(it.ID)
		} else {
			rerr = a.store.RequeueDone(it.ID)
		}
		if rerr != nil {
			return fmt.Errorf("re-enqueue %s: %w", it.DestPath, rerr)
		}
		repairIDs = append(repairIDs, it.ID)
	}

	fmt.Fprintf(out, "Re-downloading %d file(s)...\n", len(problems))
	// Drain ONLY the rows this repair re-enqueued: an unscoped DrainAll would also
	// claim (and synchronously download) any unrelated queued backlog — the exact
	// scope creep the backfill drain avoids via DrainSubscription.
	completed, err := drainItemDownloads(ctx, a, repairIDs, log)
	if err != nil {
		return fmt.Errorf("repair download failed: %w", err)
	}

	printDownloadVerification(out, completed)
	fmt.Fprintf(out, "Repaired %d of %d file(s).\n", len(completed), len(problems))
	if len(completed) < len(problems) {
		fmt.Fprintf(out, "Warning: %d file(s) could not be repaired (now marked failed); re-run 'verify --repair' to re-attempt, or check the logs with -v.\n",
			len(problems)-len(completed))
	}
	return nil
}
