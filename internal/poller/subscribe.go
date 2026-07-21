package poller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ErrAlreadySubscribed is returned when a subscription for the same target
// already exists.
var ErrAlreadySubscribed = errors.New("already subscribed")

// SubscribeOptions configures a new subscription.
type SubscribeOptions struct {
	NotifyOnly      bool
	AutoDownload    bool
	BackfillLatest  bool
	BaseModelFilter string
	FileTypePref    string
	PollInterval    time.Duration
	Layout          string
}

// defaultsApplied returns the options with an effective poll interval floored at
// the poller minimum.
func (p *Poller) effectivePollSecs(iv time.Duration) int {
	if iv < p.minInterval {
		iv = p.minInterval
	}
	return int(iv / time.Second)
}

// SubscribeModel creates a model subscription and performs the initial seeding
// poll. It verifies the model exists first (so a bad id fails fast with a clear
// error rather than creating a dead subscription). Returns the new id.
func (p *Poller) SubscribeModel(ctx context.Context, modelID int, opts SubscribeOptions) (int64, error) {
	if _, err := p.store.FindModelSubscription(modelID); err == nil {
		return 0, ErrAlreadySubscribed
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}

	// Verify the model resolves.
	if _, _, err := p.reader.GetModel(ctx, strconv.Itoa(modelID)); err != nil {
		return 0, fmt.Errorf("resolve model %d: %w", modelID, err)
	}

	id, err := p.store.CreateSubscription(store.Subscription{
		Kind:             store.KindModel,
		ModelID:          &modelID,
		AutoDownload:     opts.AutoDownload,
		NotifyOnly:       opts.NotifyOnly,
		Layout:           orDefault(opts.Layout),
		BaseModelFilter:  opts.BaseModelFilter,
		FileTypePref:     opts.FileTypePref,
		PollIntervalSecs: p.effectivePollSecs(opts.PollInterval),
	})
	if err != nil {
		return 0, err
	}
	return id, p.seedNew(ctx, id, opts.BackfillLatest)
}

// SubscribeCreator creates a creator subscription and performs the initial
// seeding poll. It verifies the creator has at least one model.
func (p *Poller) SubscribeCreator(ctx context.Context, username string, opts SubscribeOptions) (int64, error) {
	if username == "" {
		return 0, errors.New("empty creator username")
	}
	if _, err := p.store.FindCreatorSubscription(username); err == nil {
		return 0, ErrAlreadySubscribed
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}

	id, err := p.store.CreateSubscription(store.Subscription{
		Kind:             store.KindCreator,
		Username:         username,
		AutoDownload:     opts.AutoDownload,
		NotifyOnly:       opts.NotifyOnly,
		Layout:           orDefault(opts.Layout),
		BaseModelFilter:  opts.BaseModelFilter,
		FileTypePref:     opts.FileTypePref,
		PollIntervalSecs: p.effectivePollSecs(opts.PollInterval),
	})
	if err != nil {
		return 0, err
	}
	return id, p.seedNew(ctx, id, opts.BackfillLatest)
}

// seedNew runs the first poll of a freshly-created subscription (seeding the
// ledger, optionally enqueuing the latest version).
func (p *Poller) seedNew(ctx context.Context, id int64, backfillLatest bool) error {
	sub, err := p.store.GetSubscription(id)
	if err != nil {
		return err
	}
	_, err = p.PollOnce(ctx, *sub, backfillLatest)
	_ = p.store.TouchPolled(id, time.Now().UTC())
	return err
}

// PollAll polls every subscription once (the `check` command's core). It does
// not stop on the first error: each subscription's failure is recorded and
// polling continues. A small jittered delay is inserted between subscriptions,
// and an ErrRateLimited response triggers the same escalating backoff the
// scheduler (Run) uses, so a cron `check` over many subs does not burst the
// API. Returns the first error encountered, if any.
func (p *Poller) PollAll(ctx context.Context) error {
	subs, err := p.store.ListSubscriptions()
	if err != nil {
		return err
	}
	var (
		firstErr error
		backoff  time.Duration
	)
	for i, sub := range subs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Space out requests so a cron check over many subs does not burst
		// api.civitai.com.
		if i > 0 {
			p.waitFn(ctx, p.checkDelay+p.randJitter(p.checkDelay/2))
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}

		_, err := p.PollOnce(ctx, sub, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if errors.Is(err, civitai.ErrRateLimited) {
				backoff = nextBackoff(backoff)
				p.log.Warn("rate limited during check; backing off", "sub", sub.ID, "backoff", backoff)
				_ = p.store.AddEvent(store.Event{
					Level: store.LevelWarn, Kind: "poll_error", SubscriptionID: &sub.ID,
					Message: fmt.Sprintf("Rate limited; backing off %s", backoff),
				})
				p.waitFn(ctx, backoff)
				continue
			}
			_ = p.store.AddEvent(store.Event{
				Level: store.LevelError, Kind: "poll_error", SubscriptionID: &sub.ID,
				Message: fmt.Sprintf("Poll failed: %v", err),
			})
		} else {
			backoff = 0
		}
		_ = p.store.TouchPolled(sub.ID, time.Now().UTC())
	}
	return firstErr
}

// nextBackoff escalates a rate-limit backoff: 0 -> 2m, then doubling up to a
// 30m ceiling. It matches the scheduler's (Run) backoff policy.
func nextBackoff(cur time.Duration) time.Duration {
	if cur == 0 {
		return 2 * time.Minute
	}
	if cur < 30*time.Minute {
		return cur * 2
	}
	return cur
}

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}
