package web

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// seedBatch inserts n generations belonging to ONE batch (index 1..n, each with
// one image on disk) and returns their ids in run order. total is batch_total AS
// REQUESTED, which is deliberately allowed to exceed n so the stopped-batch case
// can be seeded.
func seedBatch(t *testing.T, srv *Server, root, batchID, presetName string, n, total int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := 1; i <= n; i++ {
		relPath := fmt.Sprintf("prompt-%s-%d/0-img.png", batchID, i)
		if _, err := writeOutputImage(root, relPath, []byte("IMG"), maxOutputImageBytes); err != nil {
			t.Fatalf("write output file: %v", err)
		}
		genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
			WorkflowName: "batched-wf",
			PromptID:     fmt.Sprintf("prompt-%s-%d", batchID, i),
			BaseModel:    "SDXL 1.0",
			Params:       `{"widget_overrides":[{"node_id":"3","input_name":"seed","value":"42"}]}`,
			PresetName:   presetName,
			BatchID:      batchID,
			BatchIndex:   i,
			BatchTotal:   total,
		}, []store.GenerationImage{
			{Idx: 0, RelPath: relPath, Filename: "img.png", ContentType: "image/png", SizeBytes: 3},
		})
		if err != nil {
			t.Fatalf("insert batched generation %d: %v", i, err)
		}
		ids = append(ids, genID)
	}
	return ids
}

// TestBatchPageRendersEveryRunAndParamsOnce is the point of the surface: N tiles,
// but the parameters the runs SHARE rendered ONCE at the top. Counting the params
// card is load-bearing — if the hoist regressed and the card came back per tile
// the page would still render fine and every other assertion here would pass.
func TestBatchPageRendersEveryRunAndParamsOnce(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	ids := seedBatch(t, srv, root, "batch-abc", "Hi-res 8-step", 4, 4)

	rec := get(t, srv, "/outputs/batch/batch-abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	main := pageMain(rec.Body.String())

	for _, id := range ids {
		if !strings.Contains(main, `href="/outputs/`+strconv.FormatInt(id, 10)+`"`) {
			t.Errorf("batch page missing a tile link to generation %d", id)
		}
	}
	if got := strings.Count(main, "/outputs/img/"); got != len(ids) {
		t.Errorf("batch page shows %d thumbnails, want %d", got, len(ids))
	}
	// The params card exactly once — not once per run.
	if got := strings.Count(main, "Run parameters"); got != 1 {
		t.Errorf("params card rendered %d times, want exactly 1", got)
	}
	if !strings.Contains(main, "seed = 42") {
		t.Error("the hoisted params card is missing the shared run parameters")
	}
	// Header uses the preset name snapshot; back link present.
	if !strings.Contains(main, "Hi-res 8-step") {
		t.Error("batch header should use the snapshotted preset name")
	}
	if !strings.Contains(main, "← All outputs") || !strings.Contains(main, `href="/outputs"`) {
		t.Error("batch page missing the back link to /outputs")
	}
	// Render-plain, like every other outputs surface.
	for _, bad := range []string{"cm-blur", "data-blurred", "cmReveal"} {
		if strings.Contains(main, bad) {
			t.Errorf("batch page must render plain, found %q", bad)
		}
	}
}

// TestBatchPageFallsBackToWorkflowLabel: a batch started outside a preset has no
// preset_name snapshot, so the header must name the workflow instead of rendering
// an empty «».
func TestBatchPageFallsBackToWorkflowLabel(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	seedBatch(t, srv, root, "batch-nopreset", "", 2, 2)
	main := pageMain(get(t, srv, "/outputs/batch/batch-nopreset").Body.String())
	if !strings.Contains(main, "batched-wf") {
		t.Errorf("presetless batch header should fall back to the workflow label:\n%s", main)
	}
	if strings.Contains(main, "«»") {
		t.Error("presetless batch rendered an empty label")
	}
}

// TestBatchPageCountIsHonestWhenStopped is the count-must-not-lie case: batch_total
// is N AS REQUESTED, but Stop cancels the remainder, so a batch of 8 can capture 3.
// Printing "8 runs" over three tiles would be a lie.
func TestBatchPageCountIsHonestWhenStopped(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	seedBatch(t, srv, root, "batch-stopped", "Preset", 3, 8)

	main := pageMain(get(t, srv, "/outputs/batch/batch-stopped").Body.String())
	if !strings.Contains(main, "3 of 8 runs captured") {
		t.Errorf("stopped batch must report captured-of-requested; got:\n%s", main)
	}
	if strings.Contains(main, "8 runs.") {
		t.Error("stopped batch must not claim 8 runs were captured")
	}

	// The complete batch says it plainly instead.
	seedBatch(t, srv, root, "batch-whole", "Preset", 3, 3)
	whole := pageMain(get(t, srv, "/outputs/batch/batch-whole").Body.String())
	if !strings.Contains(whole, "3 runs.") {
		t.Errorf("complete batch should report a bare count; got:\n%s", whole)
	}
	if strings.Contains(whole, "runs captured") {
		t.Error("complete batch must not claim it was stopped")
	}
}

// TestBatchPageUnknownIDIs404: a well-formed but unknown/stale id selects zero
// rows. That is a MISSING page, not an empty one — an empty batch page would tell
// the user a batch exists when it does not.
func TestBatchPageUnknownIDIs404(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	seedBatch(t, srv, root, "batch-real", "P", 2, 2)
	if rec := get(t, srv, "/outputs/batch/batch-does-not-exist"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown batch id status = %d, want 404", rec.Code)
	}
}

// TestBatchPageHostileIDsAre404NotError: {id} is untrusted URL path input. Every
// shape below must 404 — never 500, never reach a query. The traversal id is
// percent-escaped so ServeMux delivers it to the handler as ONE path value instead
// of redirecting it away (which would prove nothing about the handler).
func TestBatchPageHostileIDsAre404NotError(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	seedBatch(t, srv, root, "batch-real", "P", 1, 1)

	for _, tc := range []struct{ name, path string }{
		{"traversal", "/outputs/batch/%2E%2E%2F%2E%2E%2Fetc%2Fpasswd"},
		{"over-64-chars", "/outputs/batch/" + strings.Repeat("a", 65)},
		{"space", "/outputs/batch/batch%20real"},
		{"quote", "/outputs/batch/batch%22real"},
		{"sql-ish", "/outputs/batch/%27%20OR%201%3D1%20--"},
		{"percent-encoded-nul", "/outputs/batch/batch%00real"},
	} {
		rec := get(t, srv, tc.path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body: %q)", tc.name, rec.Code, rec.Body.String())
		}
	}
	// Exactly 64 chars is the boundary and is ACCEPTED as well-formed — it simply
	// matches nothing, which is still a 404 but by the zero-rows path.
	if rec := get(t, srv, "/outputs/batch/"+strings.Repeat("a", 64)); rec.Code != http.StatusNotFound {
		t.Errorf("64-char id status = %d, want 404", rec.Code)
	}
}

// TestGalleryTileLinksToBatch is the caption guard. The exact anchor markup is
// asserted, not just the href, because pointer-events-auto is what makes the link
// clickable at all: the caption bar is pointer-events-none so clicks fall through
// to the tile's full-bleed overlay anchor. Dropping that one class, or emitting
// the segment as plain text, is invisible to any looser assertion.
func TestGalleryTileLinksToBatch(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	// One row, positioned 3 of 8, so the caption must read "Batch 3/8".
	relPath := "prompt-b/0-img.png"
	if _, err := writeOutputImage(root, relPath, []byte("IMG"), maxOutputImageBytes); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	if _, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
		WorkflowName: "wf", PromptID: "prompt-b",
		BatchID: "batch-xyz", BatchIndex: 3, BatchTotal: 8,
	}, []store.GenerationImage{
		{Idx: 0, RelPath: relPath, Filename: "img.png", ContentType: "image/png", SizeBytes: 3},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	main := pageMain(get(t, srv, "/outputs").Body.String())
	want := `<a href="/outputs/batch/batch-xyz" class="pointer-events-auto text-indigo-400 hover:text-indigo-300">Batch 3/8</a>`
	if !strings.Contains(main, want) {
		t.Errorf("tile caption missing the clickable batch link.\nwant substring: %s\ngot:\n%s", want, main)
	}
	// The tile's own detail link is now a full-bleed overlay anchor UNDER the
	// caption, so the batch link is reachable at all.
	if !strings.Contains(main, `class="absolute inset-0 z-10"`) {
		t.Error("tile is missing the full-bleed overlay detail anchor")
	}
	// A nested <a> would be invalid HTML and unnested by the browser — the tile
	// must no longer wrap its children in an anchor.
	if strings.Contains(main, `<a href="/outputs/1" class="cm-masonry-item`) {
		t.Error("the tile still wraps its children in an <a> — the batch link cannot be clicked")
	}
}

// TestGalleryTileWithoutBatchHasNoBatchMarkup: an ordinary single run must not
// grow a batch caption or a /outputs/batch/ href at all.
func TestGalleryTileWithoutBatchHasNoBatchMarkup(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "solo")
	seedGen(t, srv, root, &wf, "solo", []byte("X"))

	body := get(t, srv, "/outputs").Body.String()
	if strings.Contains(body, "/outputs/batch/") {
		t.Error("a non-batch generation must not link to a batch page")
	}
	if strings.Contains(body, "Batch ") {
		t.Error("a non-batch generation must not render a batch caption segment")
	}
}

// TestGalleryTileHalfPopulatedBatchRowRendersNoLink is the defensive case: batch_id,
// batch_index and batch_total are written together, so index 0 should be impossible
// — but a corrupted/legacy row must degrade to plain text rather than advertising a
// "Batch 0/0" link.
func TestGalleryTileHalfPopulatedBatchRowRendersNoLink(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	relPath := "prompt-half/0-img.png"
	if _, err := writeOutputImage(root, relPath, []byte("IMG"), maxOutputImageBytes); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	if _, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
		WorkflowName: "wf", PromptID: "prompt-half",
		BatchID: "batch-half", BatchIndex: 0, BatchTotal: 0,
	}, []store.GenerationImage{
		{Idx: 0, RelPath: relPath, Filename: "img.png", ContentType: "image/png", SizeBytes: 3},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	main := pageMain(get(t, srv, "/outputs").Body.String())
	if strings.Contains(main, "/outputs/batch/") {
		t.Error("a half-populated batch row must not emit a batch link")
	}
	if strings.Contains(main, "Batch 0/0") {
		t.Error("a half-populated batch row must never print Batch 0/0")
	}
}
