// Package poller diffs each subscription's current versions against the
// seen-versions ledger and enqueues downloads for genuinely new versions. The
// diff logic (diff.go) is pure and unit-tested; this file adds fetching,
// the seed/enqueue decisions, and the per-subscription scheduler.
package poller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/config"
	"github.com/ZacxDev/civitai-manager/internal/hashutil"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// enqueueOutcome is the definitiveness classification of an enqueue attempt. It
// drives whether the poller marks a version seen: only a DEFINITIVE outcome
// (enqueued, or a permanent skip) records it; a transient error leaves it
// unseen so the next poll retries.
type enqueueOutcome int

const (
	// outcomeEnqueued: a download row was created (definitive).
	outcomeEnqueued enqueueOutcome = iota
	// outcomePermanentSkip: a settled decision not to download this version
	// (filter mismatch, no downloadable file, over the size cap, already
	// present/queued). Definitive — mark seen, do not re-evaluate every poll.
	outcomePermanentSkip
	// outcomeTransientError: a recoverable failure (API hiccup resolving the
	// version, a DB error enqueuing). NOT definitive — do NOT mark seen.
	outcomeTransientError
)

// Poller runs subscription polls against a civitai.Reader and records results
// in the store.
type Poller struct {
	store  *store.Store
	reader civitai.Reader
	root   string
	log    *slog.Logger

	// creatorSearchLimit bounds how many of a creator's newest models a poll
	// inspects. The API caps offset paging, and new versions surface on the
	// newest models, so a modest window suffices.
	creatorSearchLimit int
	// minInterval is the hard floor on any subscription's effective interval.
	minInterval time.Duration
	// maxFileSizeBytes, when > 0, caps the primary-file size the poller will
	// enqueue; a larger file is skipped with a size_skip event. 0 = unlimited.
	maxFileSizeBytes int64
	// downloadJitter is the window for the per-instance random "not before"
	// offset applied to AUTO-detected downloads (fleet anti-stampede). 0 =
	// downloads start immediately. Manual/backfill downloads never jitter.
	downloadJitter time.Duration
	// randJitter returns a random duration in [0, d); injectable for tests.
	randJitter func(time.Duration) time.Duration
	// checkDelay is the base inter-subscription spacing PollAll (the `check`
	// path) waits between subscriptions so a cron run does not burst the API.
	checkDelay time.Duration
	// waitFn sleeps for d respecting ctx cancellation; injectable for tests.
	waitFn func(ctx context.Context, d time.Duration)
}

// New builds a Poller. A nil logger discards output.
func New(st *store.Store, reader civitai.Reader, modelRoot string, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	return &Poller{
		store:              st,
		reader:             reader,
		root:               modelRoot,
		log:                log,
		creatorSearchLimit: 20,
		minInterval:        config.MinPollInterval,
		randJitter:         jitter,
		checkDelay:         2 * time.Second,
		waitFn:             sleepCtx,
	}
}

// SetMaxFileSize configures the primary-file size cap in bytes (0 = unlimited).
func (p *Poller) SetMaxFileSize(bytes int64) { p.maxFileSizeBytes = bytes }

// SetDownloadJitter configures the anti-stampede jitter window for auto-detected
// downloads (0 = start immediately).
func (p *Poller) SetDownloadJitter(d time.Duration) { p.downloadJitter = d }

// PollResult summarizes one poll.
type PollResult struct {
	Seeded    bool
	NewCount  int
	Enqueued  int
	Skipped   int
	Candidate int
}

// fetchCandidates returns the current version candidates for a subscription in
// newest-first order.
func (p *Poller) fetchCandidates(ctx context.Context, sub store.Subscription) ([]Candidate, error) {
	switch sub.Kind {
	case store.KindModel:
		if sub.ModelID == nil {
			return nil, errors.New("model subscription missing model_id")
		}
		m, _, err := p.reader.GetModel(ctx, strconv.Itoa(*sub.ModelID))
		if err != nil {
			return nil, err
		}
		return candidatesFromModel(m), nil
	case store.KindCreator:
		q := url.Values{}
		q.Set("username", sub.Username)
		q.Set("sort", "Newest")
		q.Set("limit", strconv.Itoa(p.creatorSearchLimit))
		res, err := p.reader.SearchModels(ctx, q)
		if err != nil {
			return nil, err
		}
		return candidatesFromCreatorSearch(res.Raw, sub.Username)
	default:
		return nil, fmt.Errorf("unknown subscription kind %q", sub.Kind)
	}
}

// PollOnce performs a single poll of a subscription: fetch, diff, seed or
// enqueue. On the FIRST poll (no seen versions yet) it seeds the ledger WITHOUT
// enqueuing anything, unless backfillLatest is set, in which case it also
// enqueues the single newest version. This prevents a new subscription from
// retro-downloading the entire back-catalog.
func (p *Poller) PollOnce(ctx context.Context, sub store.Subscription, backfillLatest bool) (PollResult, error) {
	var res PollResult
	candidates, err := p.fetchCandidates(ctx, sub)
	if err != nil {
		return res, err
	}
	res.Candidate = len(candidates)

	seenCount, err := p.store.CountSeen(sub.ID)
	if err != nil {
		return res, err
	}
	seen, err := p.store.SeenVersionIDs(sub.ID)
	if err != nil {
		return res, err
	}
	firstPoll := seenCount == 0
	newOnes := Diff(seen, candidates)
	res.NewCount = len(newOnes)

	if firstPoll {
		res.Seeded = true
		for _, c := range candidates {
			if err := p.store.MarkSeen(sub.ID, c.VersionID, time.Time{}); err != nil {
				return res, err
			}
		}
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelInfo, Kind: "seed", SubscriptionID: &sub.ID,
			ModelID: firstModelID(candidates),
			Message: fmt.Sprintf("Subscribed to %s: seeded %d existing version(s) without downloading", sub.Label(), len(candidates)),
		})
		if backfillLatest && len(candidates) > 0 {
			// Backfill is a user-initiated download of the current latest: it
			// starts immediately (no anti-stampede jitter).
			if p.enqueueCandidate(ctx, sub, candidates[0], &res, false) == outcomeEnqueued {
				res.Enqueued++
			}
		}
		return res, nil
	}

	// Normal poll: newest-first. Enqueue when the subscription auto-downloads
	// and is not notify-only. A version is marked seen (and notified on) ONLY
	// after a definitive outcome — a transient enqueue error leaves it unseen so
	// the next poll retries rather than silently dropping the new version.
	for _, c := range newOnes {
		notifyOnly := sub.NotifyOnly || !sub.AutoDownload

		outcome := outcomePermanentSkip
		if !notifyOnly {
			// Auto-detected downloads get the anti-stampede start jitter.
			outcome = p.enqueueCandidate(ctx, sub, c, &res, true)
		}
		if outcome == outcomeTransientError {
			// Do not mark seen or notify; the next poll retries this version.
			continue
		}

		if err := p.store.MarkSeen(sub.ID, c.VersionID, time.Time{}); err != nil {
			return res, err
		}
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelInfo, Kind: "new_version", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("New version %d for %q (%s)", c.VersionID, c.ModelName, c.BaseModel),
		})
		if outcome == outcomeEnqueued {
			res.Enqueued++
		}
	}
	return res, nil
}

// enqueueCandidate resolves a candidate's file and enqueues a download, applying
// the base-model filter, size cap, and dedup guards. jitterStart requests the
// anti-stampede random start offset (auto-detected downloads only). It returns
// an enqueueOutcome the caller uses to decide whether to mark the version seen.
func (p *Poller) enqueueCandidate(ctx context.Context, sub store.Subscription, c Candidate, res *PollResult, jitterStart bool) enqueueOutcome {
	if sub.BaseModelFilter != "" && !strings.EqualFold(c.BaseModel, sub.BaseModelFilter) {
		res.Skipped++
		return outcomePermanentSkip
	}

	vd, _, err := p.reader.GetModelVersion(ctx, strconv.Itoa(c.VersionID))
	if err != nil {
		// Transient: an API hiccup resolving the version. Do NOT mark seen —
		// retried next poll.
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelWarn, Kind: "enqueue_error", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("Could not resolve version %d for download: %v", c.VersionID, err),
		})
		return outcomeTransientError
	}
	file := civitai.SelectFile(vd.Files, sub.FileTypePref)
	if file == nil {
		// Permanent: this version has no file we can download.
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelWarn, Kind: "enqueue_error", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("Version %d has no downloadable file", c.VersionID),
		})
		return outcomePermanentSkip
	}

	// Size cap: skip a version whose primary file exceeds the configured
	// maximum (permanent — a bigger file will not shrink on the next poll).
	if p.maxFileSizeBytes > 0 {
		if fileBytes := int64(file.SizeKB * 1024); fileBytes > p.maxFileSizeBytes {
			_ = p.store.AddEvent(store.Event{
				Level: store.LevelInfo, Kind: "size_skip", SubscriptionID: &sub.ID,
				ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
				Message: fmt.Sprintf("Skipping %s: %s exceeds max file size %s",
					file.Name, humanSize(fileBytes), humanSize(p.maxFileSizeBytes)),
			})
			res.Skipped++
			return outcomePermanentSkip
		}
	}

	exists, err := p.store.ActiveQueueItemExists(c.VersionID, file.ID)
	if err != nil {
		p.log.Warn("dedup check failed", "err", err)
	}
	if exists {
		res.Skipped++
		return outcomePermanentSkip
	}

	downloadURL := file.DownloadURL
	if downloadURL == "" {
		downloadURL = vd.DownloadURL
	}
	dest := civitai.DestPath(p.root, c.ModelType, c.CreatorUsername, c.ModelName, c.VersionName, file.Name)

	// Dedup: destination already present with the expected hash.
	if file.Hashes.SHA256 != "" && hashutil.FileMatches(dest, file.Hashes.SHA256) {
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelInfo, Kind: "skip_existing", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("Skipping %s: already present with matching hash", file.Name),
		})
		res.Skipped++
		return outcomePermanentSkip
	}

	// Anti-stampede: give auto-detected downloads a per-instance random start
	// offset so a fleet of installs does not begin the same download in unison.
	var notBefore *time.Time
	if jitterStart && p.downloadJitter > 0 {
		nb := time.Now().UTC().Add(p.randJitter(p.downloadJitter))
		notBefore = &nb
	}

	subID := sub.ID
	_, inserted, err := p.store.Enqueue(store.QueueItem{
		SubscriptionID: &subID,
		ModelID:        c.ModelID,
		VersionID:      c.VersionID,
		FileID:         file.ID,
		FileName:       file.Name,
		DownloadURL:    downloadURL,
		DestPath:       dest,
		Status:         store.StatusQueued,
		SizeKB:         file.SizeKB,
		SHA256Expected: file.Hashes.SHA256,
		NotBefore:      notBefore,
	})
	if err != nil {
		// Transient: a DB error enqueuing. Do NOT mark seen — retried next poll.
		p.log.Warn("enqueue failed", "err", err)
		return outcomeTransientError
	}
	if !inserted {
		// The partial-unique index rejected the insert: an active row for this
		// (version_id, file_id) already exists (e.g. a concurrent enqueue won the
		// race, or the ActiveQueueItemExists pre-check missed it). Settled decision
		// — mark seen, but count it as a skip, not a fresh enqueue.
		res.Skipped++
		return outcomePermanentSkip
	}
	_ = p.store.AddEvent(store.Event{
		Level: store.LevelInfo, Kind: "enqueued", SubscriptionID: &sub.ID,
		ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
		Message: fmt.Sprintf("Queued download: %s", file.Name),
	})
	return outcomeEnqueued
}

// EffectiveInterval returns a subscription's poll interval, floored at the
// poller's minimum.
func (p *Poller) EffectiveInterval(sub store.Subscription) time.Duration {
	iv := sub.PollInterval()
	if iv < p.minInterval {
		return p.minInterval
	}
	return iv
}

// Run starts the scheduler loop: each subscription is polled on its own
// interval (plus jitter) until ctx is cancelled. It reloads the subscription
// list each tick so new subscriptions are picked up without a restart. A poll
// that hits a rate limit backs that subscription off.
func (p *Poller) Run(ctx context.Context) {
	// per-subscription next-due times.
	next := map[int64]time.Time{}
	backoff := map[int64]time.Duration{}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	p.tick(ctx, next, backoff) // poll due subscriptions immediately on start
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.tick(ctx, next, backoff)
		}
	}
}

func (p *Poller) tick(ctx context.Context, next map[int64]time.Time, backoff map[int64]time.Duration) {
	subs, err := p.store.ListSubscriptions()
	if err != nil {
		p.log.Error("list subscriptions", "err", err)
		return
	}
	now := time.Now()
	for _, sub := range subs {
		due, ok := next[sub.ID]
		if !ok {
			// First time we see this subscription: schedule with a small jitter
			// so many subs don't stampede the API at once.
			next[sub.ID] = now.Add(jitter(30 * time.Second))
			continue
		}
		if now.Before(due) {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		res, err := p.PollOnce(ctx, sub, false)
		_ = p.store.TouchPolled(sub.ID, time.Now().UTC())
		interval := p.EffectiveInterval(sub)
		if err != nil {
			if errors.Is(err, civitai.ErrRateLimited) {
				bo := nextBackoff(backoff[sub.ID])
				backoff[sub.ID] = bo
				next[sub.ID] = now.Add(bo)
				p.log.Warn("rate limited; backing off", "sub", sub.ID, "backoff", bo)
				continue
			}
			p.log.Error("poll failed", "sub", sub.ID, "err", err)
			_ = p.store.AddEvent(store.Event{
				Level: store.LevelError, Kind: "poll_error", SubscriptionID: &sub.ID,
				Message: fmt.Sprintf("Poll failed: %v", err),
			})
		} else {
			backoff[sub.ID] = 0
			if res.NewCount > 0 || res.Seeded {
				p.log.Info("polled", "sub", sub.ID, "new", res.NewCount,
					"enqueued", res.Enqueued, "seeded", res.Seeded)
			}
		}
		next[sub.ID] = now.Add(interval + jitter(interval/10))
	}
}

// --- small helpers ---

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)))
}

// sleepCtx sleeps for d, returning early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// humanSize renders a byte count as a compact human string (e.g. "1.5 GB").
func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func intPtr(i int) *int { return &i }

func firstModelID(cs []Candidate) *int {
	if len(cs) == 0 {
		return nil
	}
	return intPtr(cs[0].ModelID)
}
