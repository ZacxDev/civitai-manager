// Package queue is the download worker. It claims queued rows, streams each
// file to a temp path while computing its SHA256, verifies the digest against
// the API's expected hash (never keeping a corrupt file), atomically renames
// the verified file into place, and writes sidecar metadata.
package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// Worker drains the download queue.
type Worker struct {
	store       *store.Store
	dl          civitai.Downloader
	reader      civitai.Reader // optional; used for sidecar metadata (may be nil)
	log         *slog.Logger
	idlePoll    time.Duration
	maxAttempts int
}

// New builds a download Worker. reader is optional (sidecar generation); pass
// nil to skip .civitai.info / preview sidecars.
func New(st *store.Store, dl civitai.Downloader, reader civitai.Reader, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &Worker{
		store:       st,
		dl:          dl,
		reader:      reader,
		log:         log,
		idlePoll:    5 * time.Second,
		maxAttempts: 4,
	}
}

// Run drains the queue until ctx is cancelled. On start it requeues any rows
// left mid-download by a previous crash. When the queue is empty it sleeps
// idlePoll before checking again.
func (w *Worker) Run(ctx context.Context) {
	if n, err := w.store.RequeueInterrupted(); err != nil {
		w.log.Error("requeue interrupted downloads", "err", err)
	} else if n > 0 {
		w.log.Info("requeued interrupted downloads", "count", n)
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		item, err := w.store.ClaimNextQueued()
		if err != nil {
			w.log.Error("claim next queued", "err", err)
			timer.Reset(w.idlePoll)
			continue
		}
		if item == nil {
			timer.Reset(w.idlePoll)
			continue
		}
		w.process(ctx, item)
		// Immediately look for the next item.
		timer.Reset(0)
	}
}

// ProcessOne claims and processes a single queued item, returning false when the
// queue was empty. Exposed for the one-shot `check` path and for tests.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	item, err := w.store.ClaimNextQueued()
	if err != nil {
		return false, err
	}
	if item == nil {
		return false, nil
	}
	w.process(ctx, item)
	return true, nil
}

// DrainAll processes queued items until the queue is empty (used by `check`).
func (w *Worker) DrainAll(ctx context.Context) error {
	if _, err := w.store.RequeueInterrupted(); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		item, err := w.store.ClaimNextQueued()
		if err != nil {
			return err
		}
		if item == nil {
			return nil
		}
		w.process(ctx, item)
	}
}

// process downloads, verifies, and finalizes one claimed item. All failure
// paths update the row; a hash mismatch is terminal (the partial file is
// removed), while transient IO/network errors are requeued up to maxAttempts.
func (w *Worker) process(ctx context.Context, item *store.QueueItem) {
	w.log.Info("downloading", "id", item.ID, "file", item.FileName, "dest", item.DestPath)

	if err := os.MkdirAll(filepath.Dir(item.DestPath), 0o755); err != nil {
		w.fail(item, fmt.Sprintf("create dest dir: %v", err), "")
		return
	}

	// If the final file already exists with the expected hash, mark done. Record
	// the file's actual on-disk size (not the possibly-stale bytes_done from a
	// prior partial attempt).
	if item.SHA256Expected != "" && hashutil.FileMatches(item.DestPath, item.SHA256Expected) {
		size := item.BytesDone
		if fi, err := os.Stat(item.DestPath); err == nil {
			size = fi.Size()
		}
		_ = w.store.CompleteDownload(item.ID, item.SHA256Expected, size)
		w.event(store.LevelInfo, "download_done", item, fmt.Sprintf("Already downloaded: %s", item.FileName))
		return
	}

	sum, written, err := w.stream(ctx, item)
	if err != nil {
		w.retryOrFail(ctx, item, err)
		return
	}

	// Verify against the expected hash (when the API provided one).
	if item.SHA256Expected != "" && !hashutil.Equal(sum, item.SHA256Expected) {
		_ = os.Remove(tempPath(item))
		msg := fmt.Sprintf("sha256 mismatch: expected %s got %s", item.SHA256Expected, sum)
		w.fail(item, msg, sum)
		w.event(store.LevelError, "download_failed", item, "Checksum mismatch, discarded: "+item.FileName)
		return
	}

	// Atomically move the verified temp file into place.
	if err := os.Rename(tempPath(item), item.DestPath); err != nil {
		_ = os.Remove(tempPath(item))
		w.retryOrFail(ctx, item, fmt.Errorf("finalize (rename): %w", err))
		return
	}

	_ = w.store.CompleteDownload(item.ID, sum, written)
	_ = w.store.UpsertLocalFile(store.LocalFile{
		Path: item.DestPath, SHA256: sum, ModelID: &item.ModelID,
		VersionID: &item.VersionID, SizeBytes: written,
	})
	// Distinguish a hash-verified download from one the API gave no hash for:
	// the latter is finalized (some legit files lack a hash) but must NEVER be
	// reported as "verified".
	if item.SHA256Expected == "" {
		w.event(store.LevelWarn, "download_unverified", item,
			fmt.Sprintf("Downloaded %s (UNVERIFIED — no hash from API)", item.FileName))
		w.log.Warn("download complete (unverified: no API hash)", "id", item.ID, "sha256", shortHash(sum), "bytes", written)
	} else {
		w.event(store.LevelInfo, "download_done", item,
			fmt.Sprintf("Downloaded %s (%s verified)", item.FileName, shortHash(sum)))
		w.log.Info("download complete", "id", item.ID, "sha256", shortHash(sum), "bytes", written)
	}

	w.writeSidecars(ctx, item)
}

// stream fetches the file, writing it to a temp path while hashing, and returns
// the hex digest and byte count. It restarts the file from zero each attempt:
// the SDK Downloader takes only a URL (no Range header), so byte-range resume
// is not available through it -- an interrupted download is re-fetched whole.
func (w *Worker) stream(ctx context.Context, item *store.QueueItem) (string, int64, error) {
	resp, err := w.dl.DownloadFile(ctx, item.DownloadURL)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		snippet := make([]byte, 256)
		n, _ := io.ReadFull(io.LimitReader(resp.Body, 256), snippet)
		return "", 0, fmt.Errorf("download %s returned HTTP %d: %s", item.DownloadURL, resp.StatusCode, string(snippet[:n]))
	}

	tmp, err := os.Create(tempPath(item))
	if err != nil {
		return "", 0, fmt.Errorf("create temp file: %w", err)
	}
	// Ensure the temp file is closed on every path.
	defer tmp.Close()

	h := sha256.New()
	pw := &progressWriter{
		store: w.store, id: item.ID, flushEvery: 2 * time.Second, lastFlush: time.Now(),
	}
	written, err := io.Copy(io.MultiWriter(tmp, h, pw), resp.Body)
	if err != nil {
		return "", written, fmt.Errorf("stream body: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", written, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", written, fmt.Errorf("close temp file: %w", err)
	}
	_ = w.store.UpdateProgress(item.ID, written)
	return hex.EncodeToString(h.Sum(nil)), written, nil
}

// writeSidecars best-effort writes <base>.civitai.info (raw version JSON) and
// <base>.preview.png (first image). Failures are logged, never fatal -- the
// verified model file is already in place.
func (w *Worker) writeSidecars(ctx context.Context, item *store.QueueItem) {
	if w.reader == nil {
		return
	}
	vd, raw, err := w.reader.GetModelVersion(ctx, strconv.Itoa(item.VersionID))
	if err != nil {
		w.log.Warn("sidecar: resolve version failed", "id", item.ID, "err", err)
		return
	}
	base := civitai.SidecarBase(item.DestPath)
	if len(raw) > 0 {
		if err := os.WriteFile(base+".civitai.info", raw, 0o644); err != nil {
			w.log.Warn("sidecar: write civitai.info failed", "err", err)
		}
	}
	if url := civitai.FirstImageURL(raw); url != "" {
		if err := w.fetchPreview(ctx, url, base+".preview.png"); err != nil {
			w.log.Warn("sidecar: preview failed", "err", err)
		}
	}
	_ = vd
}

// fetchPreview downloads a preview image to path (best-effort).
func (w *Worker) fetchPreview(ctx context.Context, url, path string) error {
	resp, err := w.dl.DownloadFile(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("preview HTTP %d", resp.StatusCode)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(resp.Body, 32<<20)); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// retryOrFail requeues a transient failure up to maxAttempts, else fails it.
func (w *Worker) retryOrFail(ctx context.Context, item *store.QueueItem, cause error) {
	_ = os.Remove(tempPath(item))

	// A cancellation (graceful shutdown via SIGINT/SIGTERM) is NOT a download
	// failure: marking the row failed would strand it (RequeueInterrupted only
	// revives 'downloading' rows, and the version is already in seen_versions,
	// so it would never be re-downloaded). Instead return the row to 'queued'
	// WITHOUT counting the attempt, so it is picked up and completed on restart.
	// (The .part temp was already removed above; there is no byte-range resume,
	// so the next attempt re-fetches whole.)
	if errors.Is(cause, context.Canceled) || ctx.Err() != nil {
		if err := w.store.RequeueCanceled(item.ID); err != nil {
			w.log.Error("requeue canceled download", "id", item.ID, "err", err)
		} else {
			w.log.Info("download interrupted by shutdown; requeued for restart", "id", item.ID, "file", item.FileName)
		}
		return
	}

	if item.Attempts < w.maxAttempts && ctx.Err() == nil {
		backoff := time.Duration(item.Attempts) * 3 * time.Second
		w.log.Warn("download failed; will retry", "id", item.ID, "attempt", item.Attempts, "err", cause, "backoff", backoff)
		_ = w.store.RequeueWithError(item.ID, cause.Error())
		// Bound the retry rate without busy-spinning; respects cancellation.
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		return
	}
	w.fail(item, cause.Error(), "")
	w.event(store.LevelError, "download_failed", item, fmt.Sprintf("Download failed after %d attempts: %v", item.Attempts, cause))
}

func (w *Worker) fail(item *store.QueueItem, message, actualSHA string) {
	if err := w.store.FailDownload(item.ID, message, actualSHA); err != nil {
		w.log.Error("mark failed", "id", item.ID, "err", err)
	}
}

func (w *Worker) event(level, kind string, item *store.QueueItem, msg string) {
	_ = w.store.AddEvent(store.Event{
		Level: level, Kind: kind, SubscriptionID: item.SubscriptionID,
		ModelID: &item.ModelID, VersionID: &item.VersionID, Message: msg,
	})
}

// tempPath returns the in-progress temp path for an item (same directory as the
// destination so the final rename stays on one filesystem and is atomic).
func tempPath(item *store.QueueItem) string {
	return item.DestPath + ".part"
}

func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// progressWriter periodically persists bytes-downloaded to the store as data
// streams, so the UI can show live progress and a restart knows how far it got.
type progressWriter struct {
	store      *store.Store
	id         int64
	total      int64
	flushEvery time.Duration
	lastFlush  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.total += int64(len(b))
	if time.Since(p.lastFlush) >= p.flushEvery {
		p.lastFlush = time.Now()
		_ = p.store.UpdateProgress(p.id, p.total)
	}
	return len(b), nil
}
