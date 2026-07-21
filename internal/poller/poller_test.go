package poller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// fakeReader is an in-memory civitai.Reader for driving the poller without a
// network. Fields are keyed by numeric id.
type fakeReader struct {
	models    map[int]*civitai.ModelDetail
	versions  map[int]*civitai.ModelVersionDetail
	searchRaw []byte
	searchErr error
	modelErr  error
	// failVersionOnce[versionID]=true makes the NEXT GetModelVersion for that id
	// return a transient error, then clears itself (simulates an API hiccup).
	failVersionOnce map[int]bool
}

func (f *fakeReader) GetModel(_ context.Context, id string) (*civitai.ModelDetail, []byte, error) {
	if f.modelErr != nil {
		return nil, nil, f.modelErr
	}
	n, _ := strconv.Atoi(id)
	m, ok := f.models[n]
	if !ok {
		return nil, nil, civitai.ErrNotFound
	}
	raw, _ := json.Marshal(m)
	return m, raw, nil
}

func (f *fakeReader) GetModelVersion(_ context.Context, id string) (*civitai.ModelVersionDetail, []byte, error) {
	n, _ := strconv.Atoi(id)
	if f.failVersionOnce != nil && f.failVersionOnce[n] {
		f.failVersionOnce[n] = false
		return nil, nil, civitai.ErrNetwork // transient
	}
	v, ok := f.versions[n]
	if !ok {
		return nil, nil, civitai.ErrNotFound
	}
	raw, _ := json.Marshal(v)
	return v, raw, nil
}

func (f *fakeReader) GetModelVersionByHash(_ context.Context, _ string) (*civitai.ModelVersionDetail, []byte, error) {
	return nil, nil, civitai.ErrNotFound
}

func (f *fakeReader) SearchModels(_ context.Context, _ url.Values) (*civitai.ModelSearchResult, error) {
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return &civitai.ModelSearchResult{Raw: f.searchRaw}, nil
}

func (f *fakeReader) SearchCreators(_ context.Context, _ url.Values) (*civitai.CreatorSearchResult, error) {
	return &civitai.CreatorSearchResult{}, nil
}

func (f *fakeReader) SearchImages(_ context.Context, _ url.Values) (*civitai.ImageSearchResult, error) {
	return &civitai.ImageSearchResult{}, nil
}

// helpers to build fixtures.
func modelWithVersions(id int, versionIDs ...int) *civitai.ModelDetail {
	m := &civitai.ModelDetail{
		ID: id, Name: fmt.Sprintf("Model %d", id), Type: "LORA",
		Creator: &civitai.Creator{Username: "alice"},
	}
	for _, vid := range versionIDs {
		m.ModelVersions = append(m.ModelVersions, civitai.ModelVersionSummary{
			ID: vid, Name: fmt.Sprintf("v%d", vid), BaseModel: "SDXL",
		})
	}
	return m
}

func versionDetail(vid, mid int, baseModel string) *civitai.ModelVersionDetail {
	return &civitai.ModelVersionDetail{
		ID: vid, ModelID: mid, Name: fmt.Sprintf("v%d", vid), BaseModel: baseModel,
		Files: []civitai.ModelVersionFile{{
			ID: vid * 10, Name: fmt.Sprintf("file%d.safetensors", vid), Type: "Model",
			Primary: true, DownloadURL: fmt.Sprintf("https://civitai.com/api/download/%d", vid),
			SizeKB: 1024, Hashes: civitai.FileHashes{SHA256: "HASH"},
		}},
	}
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func queuedCount(t *testing.T, st *store.Store) int {
	t.Helper()
	items, err := st.ListQueue(store.StatusQueued)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

func TestPollFirstPollSeedsWithoutEnqueue(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 300, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{},
	}
	p := New(st, fr, t.TempDir(), nil)

	subID, _ := st.CreateSubscription(store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	res, err := p.PollOnce(context.Background(), *sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Seeded {
		t.Error("first poll should be a seed")
	}
	if res.Enqueued != 0 {
		t.Errorf("first poll must not enqueue, got %d", res.Enqueued)
	}
	if got := queuedCount(t, st); got != 0 {
		t.Errorf("queue should be empty after seed, got %d", got)
	}
	seen, _ := st.SeenVersionIDs(subID)
	if len(seen) != 3 {
		t.Errorf("seed should record all 3 versions, got %d", len(seen))
	}
}

func TestPollDetectsAndEnqueuesNewVersion(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")},
	}
	p := New(st, fr, t.TempDir(), nil)

	subID, _ := st.CreateSubscription(store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	// Seed.
	if _, err := p.PollOnce(context.Background(), *sub, false); err != nil {
		t.Fatal(err)
	}
	// New version 300 appears (newest first).
	fr.models[mid] = modelWithVersions(mid, 300, 200, 100)

	res, err := p.PollOnce(context.Background(), *sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewCount != 1 {
		t.Fatalf("expected 1 new version, got %d", res.NewCount)
	}
	if res.Enqueued != 1 {
		t.Fatalf("expected 1 enqueued, got %d", res.Enqueued)
	}
	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 || items[0].VersionID != 300 || items[0].FileID != 3000 {
		t.Fatalf("queue item wrong: %+v", items)
	}
	if items[0].SHA256Expected != "HASH" {
		t.Errorf("expected hash carried into queue, got %q", items[0].SHA256Expected)
	}

	// Polling again with no change enqueues nothing (dedup + seen).
	res, _ = p.PollOnce(context.Background(), *sub, false)
	if res.NewCount != 0 || res.Enqueued != 0 {
		t.Errorf("idempotent re-poll should do nothing: %+v", res)
	}
}

func TestPollNotifyOnlyDoesNotEnqueue(t *testing.T) {
	st := newTestStore(t)
	mid := 7
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 100)},
		versions: map[int]*civitai.ModelVersionDetail{200: versionDetail(200, mid, "SDXL")},
	}
	p := New(st, fr, t.TempDir(), nil)
	subID, _ := st.CreateSubscription(store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true, NotifyOnly: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	_, _ = p.PollOnce(context.Background(), *sub, false)
	fr.models[mid] = modelWithVersions(mid, 200, 100)

	res, err := p.PollOnce(context.Background(), *sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewCount != 1 {
		t.Fatalf("expected new version detected, got %d", res.NewCount)
	}
	if res.Enqueued != 0 {
		t.Errorf("notify-only must not enqueue, got %d", res.Enqueued)
	}
}

func TestPollBackfillLatestOnSeed(t *testing.T) {
	st := newTestStore(t)
	mid := 9
	fr := &fakeReader{
		models: map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 300, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{
			300: versionDetail(300, mid, "SDXL"),
		},
	}
	p := New(st, fr, t.TempDir(), nil)
	subID, _ := st.CreateSubscription(store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	res, err := p.PollOnce(context.Background(), *sub, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Seeded || res.Enqueued != 1 {
		t.Fatalf("backfill seed should enqueue exactly the latest: %+v", res)
	}
	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 || items[0].VersionID != 300 {
		t.Fatalf("backfill should enqueue latest (300): %+v", items)
	}
}

func TestPollBaseModelFilter(t *testing.T) {
	st := newTestStore(t)
	mid := 11
	fr := &fakeReader{
		models: map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 100)},
		versions: map[int]*civitai.ModelVersionDetail{
			200: versionDetail(200, mid, "SD 1.5"), // filtered out
			201: versionDetail(201, mid, "SDXL"),   // kept
		},
	}
	p := New(st, fr, t.TempDir(), nil)
	subID, _ := st.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &mid, AutoDownload: true,
		BaseModelFilter: "SDXL", PollIntervalSecs: 3600,
	})
	sub, _ := st.GetSubscription(subID)
	_, _ = p.PollOnce(context.Background(), *sub, false)

	// Two new versions: one SD 1.5 (filtered), one SDXL (kept).
	m := modelWithVersions(mid, 100)
	m.ModelVersions = append([]civitai.ModelVersionSummary{
		{ID: 201, Name: "v201", BaseModel: "SDXL"},
		{ID: 200, Name: "v200", BaseModel: "SD 1.5"},
	}, m.ModelVersions...)
	fr.models[mid] = m

	res, err := p.PollOnce(context.Background(), *sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.NewCount != 2 {
		t.Fatalf("expected 2 new versions detected, got %d", res.NewCount)
	}
	if res.Enqueued != 1 {
		t.Fatalf("base-model filter should enqueue only 1, got %d", res.Enqueued)
	}
	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 || items[0].VersionID != 201 {
		t.Fatalf("only SDXL version should be queued: %+v", items)
	}
}

func TestPollCreatorSeeds(t *testing.T) {
	st := newTestStore(t)
	raw := []byte(`{"items":[{"id":1,"name":"A","type":"LORA","creator":{"username":"bob"},
		"modelVersions":[{"id":11,"name":"v1","baseModel":"SDXL"}]}]}`)
	fr := &fakeReader{searchRaw: raw, versions: map[int]*civitai.ModelVersionDetail{}}
	p := New(st, fr, t.TempDir(), nil)
	subID, _ := st.CreateSubscription(store.Subscription{Kind: store.KindCreator, Username: "bob", AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	res, err := p.PollOnce(context.Background(), *sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Seeded || res.Candidate != 1 {
		t.Fatalf("creator seed wrong: %+v", res)
	}
	seen, _ := st.SeenVersionIDs(subID)
	if !seen[11] {
		t.Errorf("creator version 11 should be seeded, seen=%+v", seen)
	}
}

// modelSub creates an auto-download model subscription and returns it seeded.
func seededModelSub(t *testing.T, st *store.Store, p *Poller, mid int, opts store.Subscription) store.Subscription {
	t.Helper()
	opts.Kind = store.KindModel
	opts.ModelID = &mid
	opts.AutoDownload = true
	if opts.PollIntervalSecs == 0 {
		opts.PollIntervalSecs = 3600
	}
	subID, err := st.CreateSubscription(opts)
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	sub, _ := st.GetSubscription(subID)
	if _, err := p.PollOnce(context.Background(), *sub, false); err != nil {
		t.Fatalf("seed poll: %v", err)
	}
	return *sub
}

// TestPollTransientEnqueueErrorRetriedNextPoll proves finding #2: a transient
// error resolving/enqueuing a new version must NOT mark it seen — the next poll
// retries it rather than silently dropping the version forever.
func TestPollTransientEnqueueErrorRetriedNextPoll(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:          map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 200, 100)},
		versions:        map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")},
		failVersionOnce: map[int]bool{300: true}, // first resolve of v300 fails transiently
	}
	p := New(st, fr, t.TempDir(), nil)
	sub := seededModelSub(t, st, p, mid, store.Subscription{})

	// New version 300 appears.
	fr.models[mid] = modelWithVersions(mid, 300, 200, 100)

	// First real poll: resolving v300 fails transiently.
	res, err := p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 0 {
		t.Fatalf("transient error should enqueue nothing, got %d", res.Enqueued)
	}
	if got := queuedCount(t, st); got != 0 {
		t.Fatalf("queue should be empty after a transient failure, got %d", got)
	}
	if seen, _ := st.SeenVersionIDs(sub.ID); seen[300] {
		t.Fatal("version must NOT be marked seen after a transient enqueue error")
	}

	// Second poll: the hiccup cleared, so the version is enqueued and now seen.
	res, err = p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 1 {
		t.Fatalf("retry poll should enqueue the version, got %d", res.Enqueued)
	}
	if seen, _ := st.SeenVersionIDs(sub.ID); !seen[300] {
		t.Fatal("version should be marked seen after a successful enqueue")
	}
}

// TestPollFilterMismatchMarkedSeen proves the other half of #2: a PERMANENT skip
// (base-model filter mismatch) is definitive — it is marked seen and not
// re-evaluated every poll.
func TestPollFilterMismatchMarkedSeen(t *testing.T) {
	st := newTestStore(t)
	mid := 11
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 100)},
		versions: map[int]*civitai.ModelVersionDetail{},
	}
	p := New(st, fr, t.TempDir(), nil)
	sub := seededModelSub(t, st, p, mid, store.Subscription{BaseModelFilter: "SDXL"})

	// A new SD 1.5 version appears (mismatches the SDXL filter).
	m := modelWithVersions(mid, 100)
	m.ModelVersions = append([]civitai.ModelVersionSummary{
		{ID: 200, Name: "v200", BaseModel: "SD 1.5"},
	}, m.ModelVersions...)
	fr.models[mid] = m

	res, err := p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 0 {
		t.Fatalf("filter mismatch must not enqueue, got %d", res.Enqueued)
	}
	if seen, _ := st.SeenVersionIDs(sub.ID); !seen[200] {
		t.Fatal("a permanent filter-mismatch skip must be marked seen (not retried each poll)")
	}
}

// TestPollSizeCapSkipsLargeFile proves finding #5(a): a version whose primary
// file exceeds the configured cap is skipped (not enqueued) and marked seen.
func TestPollSizeCapSkipsLargeFile(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")}, // file is 1024 KB = 1 MB
	}
	p := New(st, fr, t.TempDir(), nil)
	p.SetMaxFileSize(500 * 1024) // 500 KB cap
	sub := seededModelSub(t, st, p, mid, store.Subscription{})

	fr.models[mid] = modelWithVersions(mid, 300, 200, 100)
	res, err := p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 0 {
		t.Fatalf("file over the size cap must not be enqueued, got %d", res.Enqueued)
	}
	if got := queuedCount(t, st); got != 0 {
		t.Fatalf("queue should be empty, got %d", got)
	}
	if seen, _ := st.SeenVersionIDs(sub.ID); !seen[300] {
		t.Fatal("an over-cap version should be marked seen (permanent skip)")
	}
	// A size_skip event should be recorded.
	evs, _ := st.RecentEvents(10)
	var sawSizeSkip bool
	for _, e := range evs {
		if e.Kind == "size_skip" {
			sawSizeSkip = true
		}
	}
	if !sawSizeSkip {
		t.Error("expected a size_skip event")
	}
	// (The no-cap case — the same 1 MB file enqueuing — is covered by
	// TestPollDetectsAndEnqueuesNewVersion, which runs with the default cap of 0.)
}

// TestPollAllBacksOffOnRateLimit proves finding #5(b): PollAll applies the
// escalating rate-limit backoff (like the scheduler) instead of hammering the
// API.
func TestPollAllBacksOffOnRateLimit(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 100)},
		modelErr: civitai.ErrRateLimited,
	}
	p := New(st, fr, t.TempDir(), nil)
	var waits []time.Duration
	p.waitFn = func(_ context.Context, d time.Duration) { waits = append(waits, d) }

	if _, err := st.CreateSubscription(store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600}); err != nil {
		t.Fatal(err)
	}

	err := p.PollAll(context.Background())
	if !errors.Is(err, civitai.ErrRateLimited) {
		t.Fatalf("PollAll should surface the rate-limit error, got %v", err)
	}
	var sawBackoff bool
	for _, d := range waits {
		if d >= 2*time.Minute {
			sawBackoff = true
		}
	}
	if !sawBackoff {
		t.Fatalf("expected a rate-limit backoff wait >= 2m, recorded waits=%v", waits)
	}
}

// TestPollAutoDownloadJitteredThenClaimable proves the anti-stampede feature: an
// auto-detected download gets a not_before offset within [now, now+window) and
// is not claimable until due.
func TestPollAutoDownloadJitteredThenClaimable(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")},
	}
	p := New(st, fr, t.TempDir(), nil)
	window := 30 * time.Minute
	p.SetDownloadJitter(window)
	p.randJitter = func(d time.Duration) time.Duration { return d / 2 } // deterministic: 15m

	sub := seededModelSub(t, st, p, mid, store.Subscription{})
	fr.models[mid] = modelWithVersions(mid, 300, 200, 100)

	before := time.Now().UTC()
	res, err := p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()
	if res.Enqueued != 1 {
		t.Fatalf("expected 1 enqueued, got %d", res.Enqueued)
	}

	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 {
		t.Fatalf("expected 1 queued item, got %d", len(items))
	}
	nb := items[0].NotBefore
	if nb == nil {
		t.Fatal("auto-detected download must carry a not_before offset")
	}
	lo := before.Add(window / 2).Add(-2 * time.Second)
	hi := after.Add(window / 2).Add(2 * time.Second)
	if nb.Before(lo) || nb.After(hi) {
		t.Fatalf("not_before %v outside expected window [%v, %v]", nb, lo, hi)
	}

	// Gated: the worker cannot claim it yet.
	if claimed, _ := st.ClaimNextQueued(); claimed != nil {
		t.Fatalf("jittered download must not be claimable before not_before, got %+v", claimed)
	}
}

// TestBackfillDownloadNotJittered proves the manual/backfill path is exempt from
// the anti-stampede jitter (user-initiated downloads start immediately).
func TestBackfillDownloadNotJittered(t *testing.T) {
	st := newTestStore(t)
	mid := 9
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 300, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")},
	}
	p := New(st, fr, t.TempDir(), nil)
	p.SetDownloadJitter(30 * time.Minute)
	p.randJitter = func(d time.Duration) time.Duration { return d / 2 }

	subID, _ := st.CreateSubscription(store.Subscription{Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	res, err := p.PollOnce(context.Background(), *sub, true) // backfillLatest
	if err != nil {
		t.Fatal(err)
	}
	if res.Enqueued != 1 {
		t.Fatalf("backfill should enqueue the latest, got %d", res.Enqueued)
	}
	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 || items[0].NotBefore != nil {
		t.Fatalf("manual/backfill download must NOT be jittered: %+v", items)
	}
	if claimed, _ := st.ClaimNextQueued(); claimed == nil {
		t.Fatal("backfill download should be immediately claimable")
	}
}

// TestPollZeroJitterImmediate proves download_jitter=0 disables the offset.
func TestPollZeroJitterImmediate(t *testing.T) {
	st := newTestStore(t)
	mid := 42
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: modelWithVersions(mid, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")},
	}
	p := New(st, fr, t.TempDir(), nil)
	p.SetDownloadJitter(0) // disabled
	sub := seededModelSub(t, st, p, mid, store.Subscription{})
	fr.models[mid] = modelWithVersions(mid, 300, 200, 100)

	if _, err := p.PollOnce(context.Background(), sub, false); err != nil {
		t.Fatal(err)
	}
	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 || items[0].NotBefore != nil {
		t.Fatalf("with download_jitter=0 the row must be immediately claimable: %+v", items)
	}
}
