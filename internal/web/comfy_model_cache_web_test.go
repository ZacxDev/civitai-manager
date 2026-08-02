package web

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// cmUpdatedTokenRe matches an element carrying `cm-updated` as a whole class
// TOKEN, never as a prefix of cm-updated-title / cm-updated-pop.
var cmUpdatedTokenRe = regexp.MustCompile(`class="(?:[^"]*\s)?cm-updated(?:\s[^"]*)?"`)

// objectInfoFixture is a minimal but REAL-SHAPED /object_info body: one loader
// whose combo lists two model files, plus a sampler whose combos are label enums.
const objectInfoFixture = `{
  "CheckpointLoaderSimple": {"input":{"required":{"ckpt_name":[["cached_a.safetensors","cached_b.safetensors"],{}]}}},
  "KSampler": {"input":{"required":{"sampler_name":[["euler","normal"],{}]}}}
}`

// ─────────────────────────────────────────────────────────────────────────────
// B3 — THE CACHE HAS REAL WRITERS.
// ─────────────────────────────────────────────────────────────────────────────
// The first version of this feature shipped Get/Put/Invalidate with ZERO
// non-test callers, so GetComfyObjectInfo always returned nil, comfyResource
// always returned false, and the third chip state could never render on any page
// — while the migration header documented three population triggers. These two
// tests are the ones that go red if either writer is unwired again.

// TestRunPopulatesTheComfyModelCache drives the REAL run path (realRun, via the
// injected comfy client) and asserts the fetched /object_info landed in the cache.
func TestRunPopulatesTheComfyModelCache(t *testing.T) {
	srv := newFileBackedServer(t)

	// Precondition: nothing cached. Without this the test could pass on a row some
	// other code path left behind.
	if ent, err := srv.store.GetComfyObjectInfo(); err != nil || ent != nil {
		t.Fatalf("fixture precondition: cache must start empty, got ent=%v err=%v", ent, err)
	}

	fake := &fakeComfy{
		info:    mustObjectInfo(t, objectInfoFixture),
		infoRaw: []byte(objectInfoFixture),
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	// The graph references a model NOBODY has, so the run aborts at preflight. That
	// is the point: the cache write sits immediately after the /object_info fetch,
	// so it must land even on a run that goes no further — the payload is already
	// in hand and it is the freshest view of ComfyUI this app ever gets.
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`)

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != http.StatusOK {
		t.Fatalf("run start = %d", rec.Code)
	}
	pollRunUntilDone(t, srv, id)

	if fake.objectInfoCalls == 0 {
		t.Fatal("fixture did not reach the interesting case: the run never fetched /object_info")
	}
	ent, err := srv.store.GetComfyObjectInfo()
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if ent == nil {
		t.Fatal("a run fetched /object_info and cached NOTHING. With no writer the cache is " +
			"permanently empty, comfyResource always returns false, and the ◎ chip state " +
			"can never render — the feature is inert.")
	}
	if string(ent.ObjectInfoJSON) != objectInfoFixture {
		t.Errorf("the cache must hold the body ComfyUI SENT, byte for byte (a re-marshal of "+
			"the decoded ObjectInfo is lossy).\n got: %s\nwant: %s", ent.ObjectInfoJSON, objectInfoFixture)
	}
}

// TestLibraryScanInvalidatesTheComfyModelCache pins the other writer: a finished
// scan drops the row, so the app forgets rather than asserting a stale
// "ComfyUI has this file".
func TestLibraryScanInvalidatesTheComfyModelCache(t *testing.T) {
	srv := newFileBackedServer(t)

	if err := srv.store.PutComfyObjectInfo([]byte(objectInfoFixture)); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	// Assert the intermediate state — a test that "passes" against an
	// already-empty cache proves nothing about invalidation.
	if ent, err := srv.store.GetComfyObjectInfo(); err != nil || ent == nil {
		t.Fatalf("fixture precondition: the cache must be populated before the scan, got ent=%v err=%v", ent, err)
	}

	done := make(chan struct{})
	srv.scanFn = func(_ context.Context, _ func(library.FileResult), _ func(int), _ func(int)) error {
		close(done)
		return nil
	}
	srv.startScan(nil, true)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the scan seam was never invoked")
	}

	// The invalidation runs after the job settles; poll rather than sleep.
	deadline := time.Now().Add(10 * time.Second)
	for {
		ent, err := srv.store.GetComfyObjectInfo()
		if err != nil {
			t.Fatalf("read cache: %v", err)
		}
		if ent == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a finished library scan left the cached ComfyUI /object_info in place. " +
				"The local file set just changed, so the cache is the app's stalest claim " +
				"about what is installed and must be dropped.")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// B4 — ONE DECODE PER REQUEST, NOT ONE PER CHIP.
// ─────────────────────────────────────────────────────────────────────────────
// The first version re-read the blob from SQLite and json.Unmarshal'd the ENTIRE
// ObjectInfo once PER CHIP. Measured against the real live payload (4,661,987
// bytes) that is ~88 ms per call, and the operator's real database holds 305
// resource entries across 52 workflows — roughly 27 s to render one page.

// TestComfyModelIndexDecodesOnce is the unit guard on the memo itself.
func TestComfyModelIndexDecodesOnce(t *testing.T) {
	loads := 0
	idx := &comfyModelIndex{load: func() map[string]struct{} {
		loads++
		return map[string]struct{}{"a.safetensors": {}}
	}}

	// Ten lookups, including misses and repeats — a page's chips.
	for i := 0; i < 5; i++ {
		if !idx.has("a.safetensors") {
			t.Fatalf("lookup %d: a cached file must resolve", i)
		}
		if idx.has("b.safetensors") {
			t.Fatalf("lookup %d: an uncached file must not resolve", i)
		}
	}
	if loads != 1 {
		t.Errorf("the index loaded %d times for 10 lookups, want exactly 1. Every load is a "+
			"SQLite read plus a full json.Unmarshal of a multi-megabyte payload — per chip "+
			"that is tens of seconds of render time.", loads)
	}
}

// TestComfyModelIndexIsLazy: a page whose resources are all present locally must
// never touch the row at all.
func TestComfyModelIndexIsLazy(t *testing.T) {
	loads := 0
	idx := &comfyModelIndex{load: func() map[string]struct{} { loads++; return nil }}
	if loads != 0 {
		t.Errorf("building the index must read nothing; it loaded %d times", loads)
	}
	idx.has("x.safetensors")
	if loads != 1 {
		t.Errorf("the first lookup must trigger exactly one load, got %d", loads)
	}
}

// TestWorkflowResolverReadsTheComfyCacheOnceNotPerChip is the same property
// through the PRODUCTION resolver, and it is observable without a query counter:
// delete the cache row after the first lookup. A resolver that re-reads per chip
// answers false for the second file; one that memoises still answers true.
func TestWorkflowResolverReadsTheComfyCacheOnceNotPerChip(t *testing.T) {
	srv := newFileBackedServer(t)
	if err := srv.store.PutComfyObjectInfo([]byte(objectInfoFixture)); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	res := srv.workflowResolver()
	if !res.comfyHas("cached_a.safetensors") {
		t.Fatal("fixture precondition: the seeded cache must resolve the first file")
	}

	// The row is gone from here on. Any further STORE read returns nothing.
	if err := srv.store.InvalidateComfyModelCache(); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if ent, err := srv.store.GetComfyObjectInfo(); err != nil || ent != nil {
		t.Fatalf("fixture precondition: the row must actually be gone, got ent=%v err=%v", ent, err)
	}

	if !res.comfyHas("cached_b.safetensors") {
		t.Error("the resolver re-read the comfy_model_cache for the SECOND chip. It must read " +
			"and decode ONCE per request — the payload is ~4.7 MB and a real page carries " +
			"hundreds of chips.")
	}
	// And a genuine miss is still a miss — the memo must not answer true for
	// everything once it is warm.
	if res.comfyHas("never_seen.safetensors") {
		t.Error("the memo answered true for a file that is not in the cached payload")
	}
	// A sampler label is NOT a file: this is the false-positive the extension
	// filter in comfy.ModelFileChoices exists to kill, asserted through the
	// production resolver rather than only at the unit level.
	if res.comfyHas("euler") {
		t.Error("a sampler enum label resolved as an installed FILE")
	}
}

// TestResolverWithNoComfyKnowledgeClaimsNothing: the zero resolver (every render
// path built without a store) must collapse to the two-state chip rather than
// panicking or claiming presence.
func TestResolverWithNoComfyKnowledgeClaimsNothing(t *testing.T) {
	if (workflowResolver{}).comfyHas("anything.safetensors") {
		t.Error("a resolver with no ComfyUI knowledge must claim nothing")
	}
	got := renderString(t, workflowResourceChip("anything.safetensors", workflowResolver{}))
	if !strings.Contains(got, `data-have="no"`) {
		t.Errorf("expected the plain missing chip:\n%s", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// B5 — THE NESTED POPOVERS MUST NOT ALL OPEN AT ONCE.
// ─────────────────────────────────────────────────────────────────────────────
// Each chip used to be wrapped in `.cm-updated`, NESTED inside
// workflowResourcesPopover's own `.cm-updated` trigger. app.css opens a popover
// with a DESCENDANT combinator (`.cm-updated:hover .cm-updated-pop`), so opening
// the outer trigger set display:block on every nested pop. Browser-measured on a
// 3-resource fixture: all three panels open, chips 2 and 3 fully covered.

// TestResourceChipPopoverDoesNotReuseTheSharedHoverMechanism is the MARKUP half:
// inside the resources popover there is exactly ONE `.cm-updated` wrapper and ONE
// `.cm-updated-pop` — the outer one — no matter how many chips it holds.
func TestResourceChipPopoverDoesNotReuseTheSharedHoverMechanism(t *testing.T) {
	res := workflowResolver{
		haveFile:      func(string) bool { return true },
		localResource: func(b string) (resourceInfo, bool) { return resourceInfo{Path: "/lib/" + b}, true },
	}
	out := renderString(t, workflowResourcesPopover(
		[]string{"one.safetensors", "two.safetensors", "three.safetensors"}, res))

	// Fixture reaches the interesting case: three chips, each with its own panel.
	if n := strings.Count(out, "cm-res-chip-pop"); n != 3 {
		t.Fatalf("fixture precondition: want 3 chip popovers, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, "cm-updated-pop"); n != 1 {
		t.Errorf("the resources popover subtree must contain EXACTLY ONE .cm-updated-pop (its "+
			"own). Got %d — every extra one is opened by the shared DESCENDANT rule the "+
			"instant the outer trigger opens, stacking panels over the chips it is "+
			"supposed to reveal:\n%s", n, out)
	}
	// ⚠ A bare Count of `class="cm-updated` is WRONG here and was caught writing
	// this test: it also matches cm-updated-title and cm-updated-pop, so it
	// reported 3 on correct markup. Match the class TOKEN.
	if n := len(cmUpdatedTokenRe.FindAllString(out, -1)); n != 1 {
		t.Errorf("the resources popover subtree must contain EXACTLY ONE element carrying the "+
			"`cm-updated` class token (its own trigger), got %d:\n%s", n, out)
	}
}

// TestResourceChipPopoverCSSIsScopedByAChildCombinator is the CSS half, asserted
// against the REAL shipped stylesheet. Distinct classes alone are not enough: the
// rule that opens the chip panel must also use `>` so a wrapper can only ever open
// its OWN panel.
func TestResourceChipPopoverCSSIsScopedByAChildCombinator(t *testing.T) {
	css := readAppCSS(t)

	for _, want := range []string{
		// Hidden by default — without this the panel is a permanently open sheet.
		".cm-res-chip-pop {\n  display: none;",
		// Opened only by its OWN wrapper, via the child combinator.
		".cm-res-chip-wrap:hover > .cm-res-chip-pop",
		".cm-res-chip-wrap:focus-within > .cm-res-chip-pop",
		// Positioning context, or the absolute panel would escape to the page.
		".cm-res-chip-wrap {\n  position: relative;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css is missing %q — the per-chip popover would either never open, "+
				"never close, or open for every sibling at once", want)
		}
	}
	// The DESCENDANT spelling is exactly the bug. It must not appear.
	for _, banned := range []string{
		".cm-res-chip-wrap:hover .cm-res-chip-pop,",
		".cm-res-chip-wrap:focus-within .cm-res-chip-pop,",
	} {
		if strings.Contains(css, banned) {
			t.Errorf("app.css uses the DESCENDANT combinator in %q. Chips nest inside the "+
				"resources popover, so a descendant rule opens every panel below the "+
				"hovered element, not just its own.", banned)
		}
	}
}

// TestCloudPanelPopulatesTheComfyModelCache guards the SECOND cache writer.
//
// 🔴 It exists because an audit neutered the cloud-path write and the ENTIRE
// internal/web suite stayed green — the run-path guard above covers only its own
// call site. An inert cache with zero callers is precisely the regression this
// whole change set exists to undo (the ◎ chip state could never render), so half
// the wiring being removable with nothing going red is that same bug wearing a
// different hat.
//
// The cloud panel fetches /object_info to convert a UI-format graph to API format;
// the payload is already in hand at that moment, so it feeds the display cache for
// the same reason the local run path does.
func TestCloudPanelPopulatesTheComfyModelCache(t *testing.T) {
	srv := newCloudTestServer(t, &fakeCloud{})

	// Precondition: nothing cached, so a pass cannot ride on a row some other path
	// left behind.
	if ent, err := srv.store.GetComfyObjectInfo(); err != nil || ent != nil {
		t.Fatalf("fixture precondition: cache must start empty, got ent=%v err=%v", ent, err)
	}

	fake := &fakeComfy{
		info:    mustObjectInfo(t, objectInfoFixture),
		infoRaw: []byte(objectInfoFixture),
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	// A UI-format graph is what forces the conversion, which is what fetches
	// /object_info. An API-format one would skip the fetch entirely.
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiCheckpointGraph)

	if rec := get(t, srv, "/workflows/"+id+"/cloud"); rec.Code != http.StatusOK {
		t.Fatalf("cloud panel = %d", rec.Code)
	}

	// Fixture reached the interesting case: the panel actually fetched /object_info.
	// Without this the assertion below could fail for the boring reason that the
	// cloud path never ran at all (cloud disabled, no comfy client, API-format
	// graph) — which would read as a missing writer.
	if fake.objectInfoCalls == 0 {
		t.Fatal("fixture did not reach the interesting case: the cloud panel never fetched /object_info")
	}

	ent, err := srv.store.GetComfyObjectInfo()
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if ent == nil {
		t.Fatal("the cloud path fetched /object_info but cached NOTHING — that writer is inert, " +
			"so the ◎ (in ComfyUI) chip state can never appear for anyone who only runs in the cloud")
	}
	if string(ent.ObjectInfoJSON) != objectInfoFixture {
		t.Errorf("cached payload is not the bytes ComfyUI returned:\ngot  %q\nwant %q",
			string(ent.ObjectInfoJSON), objectInfoFixture)
	}
}
