package poller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// workflowPostModel builds a model detail shaped like a REAL CivitAI Workflows
// post. The point of the fixture is that nothing here is missing: measured
// 2026-07-31 on 1847730 / 1386234, a workflow post's versions carry a populated
// baseModel exactly like a checkpoint's, and (see workflowVersionDetail) a
// primary file with a real SHA256, a downloadUrl and a sizeKB. A fixture that
// omitted any of those would prove the skip for the WRONG reason — the version
// would fall out at "no downloadable file" instead of at the type guard.
func workflowPostModel(id int, versionIDs ...int) *civitai.ModelDetail {
	m := &civitai.ModelDetail{
		ID: id, Name: fmt.Sprintf("Workflow pack %d", id), Type: "Workflows",
		Creator: &civitai.Creator{Username: "alice"},
	}
	for _, vid := range versionIDs {
		m.ModelVersions = append(m.ModelVersions, civitai.ModelVersionSummary{
			ID: vid, Name: fmt.Sprintf("v%d", vid), BaseModel: "Wan Video 14B t2v",
		})
	}
	return m
}

// workflowVersionDetail is versionDetail's Workflows twin: an Archive .zip with a
// real primary file, which SelectFile happily returns.
func workflowVersionDetail(vid, mid int) *civitai.ModelVersionDetail {
	return &civitai.ModelVersionDetail{
		ID: vid, ModelID: mid, Name: fmt.Sprintf("v%d", vid), BaseModel: "Wan Video 14B t2v",
		Files: []civitai.ModelVersionFile{{
			ID: vid * 10, Name: fmt.Sprintf("WAN22_AIO_%d.zip", vid), Type: "Archive",
			Primary: true, DownloadURL: fmt.Sprintf("https://civitai.com/api/download/%d", vid),
			SizeKB: 812.5, Hashes: civitai.FileHashes{SHA256: "ZIPHASH"},
		}},
	}
}

// assertFixtureIsDownloadableWhole is the "does the fixture reach the interesting
// case" check. Without it, a workflow fixture that had (say) no files would make
// every assertion below pass for a reason that has nothing to do with the type
// guard. This asserts the INTERMEDIATE state: civitai.SelectFile — the exact call
// enqueueCandidate makes after the guard — DOES return a file for this version.
func assertFixtureIsDownloadableWhole(t *testing.T, vd *civitai.ModelVersionDetail) {
	t.Helper()
	f := civitai.SelectFile(vd.Files, "")
	if f == nil {
		t.Fatalf("fixture does not reach the interesting case: SelectFile returned nil, so "+
			"this version would be skipped as 'no downloadable file' whatever the type guard does (%+v)", vd)
	}
	if f.DownloadURL == "" || f.Hashes.SHA256 == "" || f.SizeKB == 0 || !f.Primary {
		t.Fatalf("fixture does not reach the interesting case: a real workflow post's file "+
			"carries downloadUrl/SHA256/sizeKB/primary like a checkpoint's, got %+v", f)
	}
	if vd.BaseModel == "" {
		t.Fatal("fixture does not reach the interesting case: a real workflow post carries a populated baseModel")
	}
}

func typeSkipEvents(t *testing.T, st *store.Store) []store.Event {
	t.Helper()
	evs, err := st.RecentEvents(100)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Event
	for _, e := range evs {
		if e.Kind == "type_skip" {
			out = append(out, e)
		}
	}
	return out
}

// TestEnqueueCandidatePermanentlySkipsAWorkflowPost is 🔴-1(a): the load-bearing
// guard. A Workflows-type candidate must never reach the download queue, and the
// skip must be PERMANENT (the version is marked seen, so the next poll does not
// re-spend an API call re-deciding a type that cannot change).
func TestEnqueueCandidatePermanentlySkipsAWorkflowPost(t *testing.T) {
	st := newTestStore(t)
	mid := 1847730
	vd := workflowVersionDetail(300, mid)
	assertFixtureIsDownloadableWhole(t, vd)

	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: workflowPostModel(mid, 200, 100)},
		versions: map[int]*civitai.ModelVersionDetail{300: vd},
	}
	p := New(st, fr, t.TempDir(), nil)
	sub := seededModelSub(t, st, p, mid, store.Subscription{})

	// A new version of the workflow pack appears.
	fr.models[mid] = workflowPostModel(mid, 300, 200, 100)

	res, err := p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	// The version IS detected and notified on — a subscription to a workflow post
	// is still a legitimate "tell me when it updates".
	if res.NewCount != 1 {
		t.Fatalf("expected the new workflow version to be DETECTED, got NewCount=%d", res.NewCount)
	}
	if res.Enqueued != 0 {
		t.Fatalf("a Workflows post must never be enqueued, got Enqueued=%d", res.Enqueued)
	}
	if res.Skipped != 1 {
		t.Fatalf("expected the skip to be counted, got Skipped=%d", res.Skipped)
	}
	if n := queuedCount(t, st); n != 0 {
		items, _ := st.ListQueue(store.StatusQueued)
		t.Fatalf("download queue must be empty for a workflow post, got %d row(s): %+v", n, items)
	}

	// Permanent, not transient: the version is marked seen.
	seen, _ := st.SeenVersionIDs(sub.ID)
	if !seen[300] {
		t.Error("a permanent skip must mark the version seen, or every poll re-decides it forever")
	}

	// …and it says WHY, naming the action that does work.
	evs := typeSkipEvents(t, st)
	if len(evs) != 1 {
		t.Fatalf("expected exactly 1 type_skip event, got %d", len(evs))
	}
	msg := evs[0].Message
	if !strings.Contains(msg, "imported, not downloaded") || !strings.Contains(msg, "Import") {
		t.Errorf("type_skip event must explain that workflows are imported, got %q", msg)
	}
	if evs[0].VersionID == nil || *evs[0].VersionID != 300 {
		t.Errorf("type_skip event should carry the version id, got %+v", evs[0].VersionID)
	}

	// Re-polling changes nothing (seen + the guard both hold).
	res2, err := p.PollOnce(context.Background(), sub, false)
	if err != nil {
		t.Fatal(err)
	}
	if res2.NewCount != 0 || res2.Enqueued != 0 {
		t.Errorf("re-poll should be a no-op, got %+v", res2)
	}
}

// TestEnqueueCandidateStillEnqueuesRealModelTypes is the OTHER DIRECTION, and it
// is the half that catches an over-broad guard: subscribing to a Checkpoint /
// LORA / LoCon / LyCORIS / VAE must be completely unaffected. That regression —
// silently refusing to download real models — would be worse than the bug.
func TestEnqueueCandidateStillEnqueuesRealModelTypes(t *testing.T) {
	for _, typ := range []string{"Checkpoint", "LORA", "LoCon", "LyCORIS", "TextualInversion", "VAE", "Other"} {
		t.Run(typ, func(t *testing.T) {
			st := newTestStore(t)
			mid := 42
			m := modelWithVersions(mid, 200, 100)
			m.Type = typ
			fr := &fakeReader{
				models:   map[int]*civitai.ModelDetail{mid: m},
				versions: map[int]*civitai.ModelVersionDetail{300: versionDetail(300, mid, "SDXL")},
			}
			p := New(st, fr, t.TempDir(), nil)
			sub := seededModelSub(t, st, p, mid, store.Subscription{})

			newer := modelWithVersions(mid, 300, 200, 100)
			newer.Type = typ
			fr.models[mid] = newer

			res, err := p.PollOnce(context.Background(), sub, false)
			if err != nil {
				t.Fatal(err)
			}
			if res.Enqueued != 1 {
				t.Fatalf("%s must still be enqueued, got Enqueued=%d (Skipped=%d)", typ, res.Enqueued, res.Skipped)
			}
			if n := queuedCount(t, st); n != 1 {
				t.Fatalf("%s should produce exactly 1 queue row, got %d", typ, n)
			}
			if evs := typeSkipEvents(t, st); len(evs) != 0 {
				t.Fatalf("%s must not emit a type_skip event, got %+v", typ, evs)
			}
		})
	}
}

// TestCreatorPollDownloadsNoWorkflowPost is 🔴-2: the creator search sets no
// `types` param, so candidatesFromCreatorSearch flattens EVERY model of every
// type a creator published. Workflow-only creators are common. This proves the
// enqueueCandidate guard really does cover that path — the render layer cannot
// reach it at all — while the creator's real LORA in the SAME response is still
// downloaded.
func TestCreatorPollDownloadsNoWorkflowPost(t *testing.T) {
	st := newTestStore(t)
	// One creator, two models: a Workflows post (id 1, version 11) and a LORA
	// (id 2, version 22). Both versions carry a populated baseModel, exactly as
	// the live payload does.
	seed := []byte(`{"items":[
		{"id":1,"name":"WAN 2.2 AIO","type":"Workflows","creator":{"username":"bob"},
		 "modelVersions":[{"id":11,"name":"v1","baseModel":"Wan Video 14B t2v"}]},
		{"id":2,"name":"Real LORA","type":"LORA","creator":{"username":"bob"},
		 "modelVersions":[{"id":22,"name":"v1","baseModel":"SDXL"}]}]}`)
	wfVer := workflowVersionDetail(12, 1)
	assertFixtureIsDownloadableWhole(t, wfVer)

	fr := &fakeReader{
		searchRaw: seed,
		versions: map[int]*civitai.ModelVersionDetail{
			12: wfVer,
			23: versionDetail(23, 2, "SDXL"),
		},
	}
	p := New(st, fr, t.TempDir(), nil)
	subID, _ := st.CreateSubscription(store.Subscription{
		Kind: store.KindCreator, Username: "bob", AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	if _, err := p.PollOnce(context.Background(), *sub, false); err != nil {
		t.Fatal(err)
	}

	// Both models publish a new version.
	fr.searchRaw = []byte(`{"items":[
		{"id":1,"name":"WAN 2.2 AIO","type":"Workflows","creator":{"username":"bob"},
		 "modelVersions":[{"id":12,"name":"v2","baseModel":"Wan Video 14B t2v"},
		                  {"id":11,"name":"v1","baseModel":"Wan Video 14B t2v"}]},
		{"id":2,"name":"Real LORA","type":"LORA","creator":{"username":"bob"},
		 "modelVersions":[{"id":23,"name":"v2","baseModel":"SDXL"},
		                  {"id":22,"name":"v1","baseModel":"SDXL"}]}]}`)

	res, err := p.PollOnce(context.Background(), *sub, false)
	if err != nil {
		t.Fatal(err)
	}
	// Intermediate state: the workflow version really did arrive as a candidate.
	// Without this the test would pass just as happily if the creator fixture had
	// silently stopped producing it.
	if res.NewCount != 2 {
		t.Fatalf("expected BOTH new versions to be detected (the workflow one must reach "+
			"enqueueCandidate to be skipped there), got NewCount=%d", res.NewCount)
	}
	if res.Enqueued != 1 {
		t.Fatalf("expected exactly the LORA to be enqueued, got Enqueued=%d", res.Enqueued)
	}
	items, _ := st.ListQueue(store.StatusQueued)
	if len(items) != 1 || items[0].VersionID != 23 {
		t.Fatalf("only the LORA version 23 may be queued, got %+v", items)
	}
	for _, it := range items {
		if strings.HasSuffix(it.FileName, ".zip") {
			t.Fatalf("a workflow .zip reached the download queue: %+v", it)
		}
	}
	if evs := typeSkipEvents(t, st); len(evs) != 1 {
		t.Fatalf("expected 1 type_skip event on the creator path, got %d", len(evs))
	}
}

// TestBackfillLatestReportsTheWorkflowPostSkip covers the CLI's other entry
// point: `subscribe --backfill-latest` calls enqueueCandidate exactly once, and
// the reason it reports must name the workflow case rather than falling through
// to the generic "nothing to back-fill" line.
func TestBackfillLatestReportsTheWorkflowPostSkip(t *testing.T) {
	st := newTestStore(t)
	mid := 1847730
	vd := workflowVersionDetail(300, mid)
	assertFixtureIsDownloadableWhole(t, vd)
	fr := &fakeReader{
		models:   map[int]*civitai.ModelDetail{mid: workflowPostModel(mid, 300, 200)},
		versions: map[int]*civitai.ModelVersionDetail{300: vd},
	}
	p := New(st, fr, t.TempDir(), nil)
	subID, _ := st.CreateSubscription(store.Subscription{
		Kind: store.KindModel, ModelID: &mid, AutoDownload: true, PollIntervalSecs: 3600})
	sub, _ := st.GetSubscription(subID)

	// First poll WITH backfill: this is the path that would have created the
	// real download_queue row for `…_AIO.zip`.
	res, err := p.PollOnce(context.Background(), *sub, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Seeded {
		t.Fatalf("expected a seeding first poll, got %+v", res)
	}
	if res.Enqueued != 0 || queuedCount(t, st) != 0 {
		t.Fatalf("backfill must not queue a workflow post: Enqueued=%d queued=%d", res.Enqueued, queuedCount(t, st))
	}
	if res.backfillReason != BackfillFilteredWorkflowPost {
		t.Fatalf("backfill reason = %v, want BackfillFilteredWorkflowPost", res.backfillReason)
	}
	if res.backfillDetail != workflowPostSkipDetail {
		t.Errorf("backfill detail = %q, want %q", res.backfillDetail, workflowPostSkipDetail)
	}
}
