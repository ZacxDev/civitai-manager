package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"testing"

	"github.com/civitai/civitai-manager/internal/civitai"
	"github.com/civitai/civitai-manager/internal/store"
)

// fakeReader is an in-memory civitai.Reader for driving the poller without a
// network. Fields are keyed by numeric id.
type fakeReader struct {
	models    map[int]*civitai.ModelDetail
	versions  map[int]*civitai.ModelVersionDetail
	searchRaw []byte
	searchErr error
	modelErr  error
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
	v, ok := f.versions[n]
	if !ok {
		return nil, nil, civitai.ErrNotFound
	}
	raw, _ := json.Marshal(v)
	return v, raw, nil
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
