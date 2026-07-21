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

	"github.com/civitai/civitai-manager/internal/civitai"
	"github.com/civitai/civitai-manager/internal/hashutil"
	"github.com/civitai/civitai-manager/internal/store"
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
		minInterval:        15 * time.Minute,
	}
}

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
			if p.enqueueCandidate(ctx, sub, candidates[0], &res) {
				res.Enqueued++
			}
		}
		return res, nil
	}

	// Normal poll: newest-first. Record + notify every new version; enqueue when
	// the subscription auto-downloads and is not notify-only.
	for _, c := range newOnes {
		if err := p.store.MarkSeen(sub.ID, c.VersionID, time.Time{}); err != nil {
			return res, err
		}
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelInfo, Kind: "new_version", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("New version %d for %q (%s)", c.VersionID, c.ModelName, c.BaseModel),
		})
		if sub.NotifyOnly || !sub.AutoDownload {
			continue
		}
		if p.enqueueCandidate(ctx, sub, c, &res) {
			res.Enqueued++
		}
	}
	return res, nil
}

// enqueueCandidate resolves a candidate's file and enqueues a download, applying
// the base-model filter and dedup guards. It returns true when a row was
// enqueued. Non-fatal problems are logged as events and return false.
func (p *Poller) enqueueCandidate(ctx context.Context, sub store.Subscription, c Candidate, res *PollResult) bool {
	if sub.BaseModelFilter != "" && !strings.EqualFold(c.BaseModel, sub.BaseModelFilter) {
		res.Skipped++
		return false
	}

	vd, _, err := p.reader.GetModelVersion(ctx, strconv.Itoa(c.VersionID))
	if err != nil {
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelWarn, Kind: "enqueue_error", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("Could not resolve version %d for download: %v", c.VersionID, err),
		})
		return false
	}
	file := civitai.SelectFile(vd.Files, sub.FileTypePref)
	if file == nil {
		_ = p.store.AddEvent(store.Event{
			Level: store.LevelWarn, Kind: "enqueue_error", SubscriptionID: &sub.ID,
			ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
			Message: fmt.Sprintf("Version %d has no downloadable file", c.VersionID),
		})
		return false
	}

	exists, err := p.store.ActiveQueueItemExists(c.VersionID, file.ID)
	if err != nil {
		p.log.Warn("dedup check failed", "err", err)
	}
	if exists {
		res.Skipped++
		return false
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
		return false
	}

	subID := sub.ID
	_, err = p.store.Enqueue(store.QueueItem{
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
	})
	if err != nil {
		p.log.Warn("enqueue failed", "err", err)
		return false
	}
	_ = p.store.AddEvent(store.Event{
		Level: store.LevelInfo, Kind: "enqueued", SubscriptionID: &sub.ID,
		ModelID: intPtr(c.ModelID), VersionID: intPtr(c.VersionID),
		Message: fmt.Sprintf("Queued download: %s", file.Name),
	})
	return true
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
				bo := backoff[sub.ID]
				if bo == 0 {
					bo = 2 * time.Minute
				} else if bo < 30*time.Minute {
					bo *= 2
				}
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

func intPtr(i int) *int { return &i }

func firstModelID(cs []Candidate) *int {
	if len(cs) == 0 {
		return nil
	}
	return intPtr(cs[0].ModelID)
}
