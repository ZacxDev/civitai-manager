package web

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE WARM-CACHE CLASSIFIER, THROUGH THE REAL HANDLER.
// ─────────────────────────────────────────────────────────────────────────────
// 🔴 comfyNodeOrigins had ZERO test callers. Every web test passed nil origins,
// so mutating it to `return nil` left the ENTIRE suite green — the feature would
// have been completely inert in production and CI could not have told anyone.
// That is not hypothetical here: migration 0019 shipped with no non-test writer
// at all, and comfy_model_cache.go's own header says "A CACHE WITH NO WRITER IS
// AN INERT FEATURE". These tests exercise comfyNodeOrigins for real — seeded
// store, real route, real render — and are mutation-verified against exactly
// that `return nil`.

// nodeOriginObjectInfo is a 3-entry /object_info covering all three module roots
// the classifier must separate. Values are the LIVE-measured shapes:
//
//	comfy_extras.nodes_wan               → built-in (501 live types under this root)
//	nodes                                → built-in, and the ONLY dot-free module
//	custom_nodes.comfyui-impact-pack     → custom (1672 live types under this root)
//
// The two built-ins are chosen because they are REAL ComfyUI built-ins that
// coreNodeClasses does NOT contain — WanImageToVideo appeared in 14 of the
// operator's 70 workflows and CLIPVisionLoader in 6, which is what made the
// table-only detector wrong on 62% of them. That is what makes this fixture
// discriminating: under the fallback tier all three are flagged (asserted below
// as the cold-cache control), so anything that fails to consult the payload
// cannot pass.
const nodeOriginObjectInfo = `{
  "WanImageToVideo":         {"python_module":"comfy_extras.nodes_wan","input":{}},
  "CLIPVisionLoader":        {"python_module":"nodes","input":{}},
  "ImpactWildcardProcessor": {"python_module":"custom_nodes.comfyui-impact-pack","input":{}}
}`

// nodeOriginAPIGraph references exactly those three class_types and nothing else.
// API format on purpose: it is the path that NEVER contacts ComfyUI, so the
// cached row is the only origin source and comfyNodeOrigins is the only thing
// that can answer.
const nodeOriginAPIGraph = `{
  "1":{"class_type":"WanImageToVideo","inputs":{}},
  "2":{"class_type":"CLIPVisionLoader","inputs":{}},
  "3":{"class_type":"ImpactWildcardProcessor","inputs":{}}
}`

// nodepackBannerListTitle is the banner's own list heading. Scoping to it matters:
// the resolved-resource TABLE further down the page names every custom-node row
// too, so a bare strings.Contains over the whole body cannot tell the banner's
// claim from the table's.
const nodepackBannerListTitle = "Node types this app did not recognise as built-in"

// cloudBannerNodeNames returns the node names the custom-node banner LISTS, and
// whether the banner rendered at all. present=false with names=nil is the
// legitimate "nothing was flagged" state; the two are returned separately so a
// test can tell "the banner dropped the built-ins" from "the banner vanished".
func cloudBannerNodeNames(t *testing.T, body string) (names []string, present bool) {
	t.Helper()
	ti := strings.Index(body, nodepackBannerListTitle)
	if ti < 0 {
		return nil, false
	}
	rest := body[ti:]
	ul := strings.Index(rest, "<ul")
	end := strings.Index(rest, "</ul>")
	if ul < 0 || end < 0 || end < ul {
		t.Fatalf("banner list markup not found after %q:\n%s", nodepackBannerListTitle, rest)
	}
	for _, li := range strings.Split(rest[ul:end], "<li") {
		// Not named open/close: `close` is a Go builtin and shadowing it here reads
		// as a mistake in a helper other tests will copy.
		tagEnd := strings.Index(li, ">")
		liEnd := strings.Index(li, "</li>")
		if tagEnd < 0 || liEnd < 0 || liEnd < tagEnd {
			continue
		}
		if n := strings.TrimSpace(li[tagEnd+1 : liEnd]); n != "" {
			names = append(names, n)
		}
	}
	return names, true
}

func hasName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestCloudPanelDropsBuiltinsWhenTheObjectInfoCacheIsWarm is the guard the whole
// PR exists for.
//
// It runs the SAME request twice against the SAME server — once with a cold
// cache, once with the row seeded — so the only variable is whether
// comfyNodeOrigins has anything to return.
//
// The cold-cache half is this test's POSITIVE CONTROL, not decoration: it proves
// the fixture reaches the interesting case (all three class_types really are
// outside coreNodeClasses, so all three really are flagged by the fallback tier)
// and that the extraction helper can observe a flagged name at all. Without it, a
// warm-cache result of "only the custom node is listed" is indistinguishable from
// a banner that lists nothing for a boring reason.
func TestCloudPanelDropsBuiltinsWhenTheObjectInfoCacheIsWarm(t *testing.T) {
	srv := newCloudTestServer(t, &fakeCloud{})
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, nodeOriginAPIGraph)

	// ── cold cache: the pre-existing coreNodeClasses tier answers ──────────────
	if ent, err := srv.store.GetComfyObjectInfo(); err != nil || ent != nil {
		t.Fatalf("fixture precondition: the cache must start empty, got ent=%v err=%v", ent, err)
	}
	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud panel (cold) = %d", rec.Code)
	}
	cold, present := cloudBannerNodeNames(t, rec.Body.String())
	if !present {
		t.Fatalf("fixture did not reach the interesting case: with a COLD cache the banner "+
			"must render — all three class_types are outside coreNodeClasses:\n%s", rec.Body.String())
	}
	for _, want := range []string{"WanImageToVideo", "CLIPVisionLoader", "ImpactWildcardProcessor"} {
		if !hasName(cold, want) {
			t.Fatalf("fixture precondition: with a COLD cache %q must be flagged (it is not in "+
				"coreNodeClasses); got %v. If it is not flagged here, the warm-cache assertion "+
				"below proves nothing.", want, cold)
		}
	}

	// ── warm cache: ComfyUI's own module attribution answers ───────────────────
	if err := srv.store.PutComfyObjectInfo([]byte(nodeOriginObjectInfo)); err != nil {
		t.Fatalf("seed object_info cache: %v", err)
	}
	// Assert the intermediate state: the row is readable back and really does
	// classify the three the opposed way. A row that failed to decode would make
	// the warm half silently identical to the cold half.
	ent, err := srv.store.GetComfyObjectInfo()
	if err != nil || ent == nil {
		t.Fatalf("fixture precondition: the seeded row must read back, got ent=%v err=%v", ent, err)
	}
	idx := comfy.NodeOrigins(ent.ObjectInfoJSON)
	if comfy.OriginOf(idx, "WanImageToVideo") != comfy.NodeOriginBuiltin ||
		comfy.OriginOf(idx, "CLIPVisionLoader") != comfy.NodeOriginBuiltin ||
		comfy.OriginOf(idx, "ImpactWildcardProcessor") != comfy.NodeOriginCustom {
		t.Fatalf("fixture precondition: the seeded payload does not oppose the table (idx=%v)", idx)
	}

	rec = get(t, srv, "/workflows/"+id+"/cloud")
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud panel (warm) = %d", rec.Code)
	}
	body := rec.Body.String()
	warm, present := cloudBannerNodeNames(t, body)
	if !present {
		t.Fatalf("the banner vanished entirely on a warm cache. One class_type IS a genuine "+
			"custom node (custom_nodes.comfyui-impact-pack) and must still be warned about — "+
			"suppressing the whole banner is a different bug, not the fix:\n%s", body)
	}
	for _, builtin := range []string{"WanImageToVideo", "CLIPVisionLoader"} {
		if hasName(warm, builtin) {
			t.Errorf("%q is still flagged as an unrecognised node with a WARM cache (banner listed %v). "+
				"ComfyUI reported it under a core python_module, so the cached /object_info must "+
				"override coreNodeClasses. This is the 62%%-false-positive bug the classifier "+
				"exists to fix, and it means comfyNodeOrigins is not being consulted at all.",
				builtin, warm)
		}
	}
	if !hasName(warm, "ImpactWildcardProcessor") {
		t.Errorf("the genuine custom node (custom_nodes.comfyui-impact-pack) was NOT flagged with a "+
			"warm cache (banner listed %v). Dropping real custom nodes steers the user onto a paid "+
			"path that CivitAI's CustomComfy step will reject at submit.", warm)
	}
	if len(warm) != 1 {
		t.Errorf("banner listed %d names (%v) with a warm cache, want exactly 1", len(warm), warm)
	}
}

// TestNodepackBannerStatesNoDetectionMechanism guards the copy fix.
//
// It is a NEGATIVE guard on purpose. Pinning the exact replacement sentence would
// turn a wording change into a build failure; what must not come back is the
// MECHANISM CLAIM — "flagged by a short list of known built-in node types" — which
// is false whenever a payload answered, i.e. on every install that has run ComfyUI
// once. The claim is unfixable in this function by design: cloudNodepackBlocker
// receives only []ResolvedResource and cannot know which tier answered.
//
// ⚠ THE POSITIVE HALF PINS THE HEDGE'S WORDING, AND THIS COMMENT USED TO DENY IT.
// It read "deliberately weak … the hedge's WORDING is expected to move and its
// EXISTENCE is not" — which contradicts the assertion two dozen lines below, a
// literal substring match on "may not recognise every built-in". Rewording the
// hedge breaks the build, so the stated intent was not what shipped.
//
// The pin is KEPT rather than loosened, and here is the honest trade: asserting
// "some hedge exists" without naming its words needs a structural marker (an id or
// data- attribute on that <p>), i.e. changing the served markup purely to make a
// test less brittle — scope this round deliberately did not take. So: **if you
// reword the hedge, update the string below in the same commit.** That is a cheap,
// visible failure. What must NEVER be loosened is the negative half: the whole
// point is that a caveat survives, and a guard that accepts a banner with no caveat
// at all would let the tier-blind assertion come back.
func TestNodepackBannerStatesNoDetectionMechanism(t *testing.T) {
	body := renderString(t, cloudNodepackBlocker([]comfy.ResolvedResource{
		{Filename: "ImpactWildcardProcessor", Status: comfy.ResolveCustomNode},
	}))
	// Fixture reaches the case: a banner rendered at all.
	if !strings.Contains(body, "CivitAI Cloud cannot run this workflow yet") {
		t.Fatalf("fixture precondition: no banner rendered:\n%s", body)
	}
	// ONE banned string, not two. This list used to also carry the full sentence
	// "short list of known built-in node types", which reads as an independent check
	// and is not one: the shorter string is a SUBSTRING of it, so it can never match
	// while this one does not. Keeping the shorter one alone is strictly the broader
	// check — it also catches a reworded tail ("…known built-in nodes", "…built-in
	// classes").
	for _, banned := range []string{
		"short list of known built-in",
	} {
		if strings.Contains(body, banned) {
			t.Errorf("the banner still claims %q. That mechanism is FALSE whenever an "+
				"/object_info payload answered — the classification came from ComfyUI's own "+
				"python_module attribution, not from a table. The caveat must stay "+
				"tier-agnostic, because this function cannot know which tier answered.", banned)
		}
	}
	// It must still hedge — dropping the caveat entirely would turn a conditional
	// warning into an assertion the detector cannot support in either tier. This is
	// the wording pin the header comment describes: reword the banner, update this
	// line in the same commit.
	if !strings.Contains(body, "may not recognise every built-in") {
		t.Errorf("the banner no longer hedges about unrecognised built-ins. Some flagged "+
			"types genuinely may be built-ins — a class absent from the payload still falls "+
			"back to a 47-of-790 table, and even an authoritative answer describes the LOCAL "+
			"install while this warning is about CivitAI's REMOTE runner:\n%s", body)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// THE FRESH-PAYLOAD PATH.
// ─────────────────────────────────────────────────────────────────────────────

// uiNodeOriginGraph / freshNodeOriginInfo model a UI-format workflow — the format
// of ALL 71 workflows on the operator's real database — whose conversion fetches
// /object_info in-request. input_order is load-bearing on every entry: without it
// the converter warns per node and cloudAPIGraph aborts before resolving anything.
const uiNodeOriginGraph = `{"nodes":[
  {"id":1,"type":"WanImageToVideo","widgets_values":[]},
  {"id":2,"type":"ImpactWildcardProcessor","widgets_values":[]}
],"links":[]}`

// 🔴 THE THIRD ENTRY EXISTS SO THE SPLIT COUNTS ARE PAIRWISE DISTINCT, and it is
// the whole reason this fixture can discriminate. With the original two entries the
// split was types=2, builtin=1, custom=1 — so SWAPPING the builtin/custom arguments
// at logNodeOriginSplit's Debug call produced byte-identical attributes and survived
// the entire package. A fixture whose fields collide cannot tell a counter from its
// neighbour, whatever the assertion says. 2 built-in / 1 custom separates them.
//
// CLIPVisionLoader is deliberately NOT in uiNodeOriginGraph: the split is counted
// over everything the PAYLOAD registers, not over the nodes the graph happens to
// reference, so an entry outside the graph is enough to move the counter while
// leaving every banner assertion in this file measuring exactly what it did before.
// It is the same real built-in nodeOriginObjectInfo uses for the dot-free `nodes`
// root (6 of the operator's 70 workflows), and no fixture name here is a substring
// of another.
const freshNodeOriginInfo = `{
  "WanImageToVideo":         {"python_module":"comfy_extras.nodes_wan","input":{"required":{}},"input_order":{"required":[]}},
  "CLIPVisionLoader":        {"python_module":"nodes","input":{"required":{}},"input_order":{"required":[]}},
  "ImpactWildcardProcessor": {"python_module":"custom_nodes.comfyui-impact-pack","input":{"required":{}},"input_order":{"required":[]}}
}`

// TestCloudPanelUsesTheFreshObjectInfoWhenTheCacheCannotAnswer pins that the
// handler classifies from the payload it JUST fetched rather than re-reading it
// back out of SQLite.
//
// 🔴 THE FIXTURE MUST BREAK THE CACHE, and finding that out cost a vacuous first
// draft. The obvious fixture — seed a STALE row that disagrees with the fresh
// payload — CANNOT discriminate, because cloudAPIGraph calls cacheComfyObjectInfo
// with the fresh body BEFORE anything resolves, so the stale row is overwritten
// in-request and both code paths then read the same fresh bytes. That draft passed
// with the pass-through reverted.
//
// So this reproduces the case where the write does NOT land, which is the real
// production scenario the pass-through is about: PutComfyObjectInfo's error is
// swallowed by design (cacheComfyObjectInfo must never fail a run over a display
// cache) and a read-only database never writes at all. Dropping the table makes
// both the write and the read fail deterministically, so the cache tier can
// contribute nothing and only the in-frame payload can answer.
//
// 🔴 THE PRIVATE ON-DISK DB IS LOAD-BEARING — do NOT switch this back to the default
// newCloudTestServer. That helper's ":memory:" is "file::memory:?cache=shared", ONE
// logical database per process, so this DROP TABLE would delete comfy_model_cache
// out from under any server alive at the same moment — and migrate() does not
// restore it, because schema_version already says 0019 ran. Probed: a second server
// opened while the dropping store is alive sees sqlite_master count 0 for that
// table. It happens to be survivable today only because internal/web has no
// t.Parallel() anywhere and this test opens one server; the first person to add
// either would get a silent, baffling failure in an unrelated test. A t.TempDir()
// file costs nothing and confines the DDL to this test. Every OTHER raw-DB write in
// this package is a row-level UPDATE, which is why none of them needs this.
func TestCloudPanelUsesTheFreshObjectInfoWhenTheCacheCannotAnswer(t *testing.T) {
	// Fixture precondition: the fresh payload really does classify the built-in,
	// and coreNodeClasses really does not (proved by the cold-cache control in the
	// test above, which flags WanImageToVideo).
	if o := comfy.OriginOf(comfy.NodeOrigins([]byte(freshNodeOriginInfo)), "WanImageToVideo"); o != comfy.NodeOriginBuiltin {
		t.Fatalf("fixture precondition: the FRESH payload must call WanImageToVideo built-in, got %v", o)
	}

	srv := newCloudTestServerDB(t, &fakeCloud{}, filepath.Join(t.TempDir(), "origins.db"))
	fake := &fakeComfy{
		info:    mustObjectInfo(t, freshNodeOriginInfo),
		infoRaw: []byte(freshNodeOriginInfo),
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	if _, err := srv.store.DB().Exec(`DROP TABLE comfy_model_cache`); err != nil {
		t.Fatalf("break the cache table: %v", err)
	}
	// Assert the intermediate state: the cache genuinely cannot answer. Without
	// this the test could pass because the cache answered correctly all along.
	if ent, err := srv.store.GetComfyObjectInfo(); err == nil {
		t.Fatalf("fixture precondition: the cache must be unable to answer, got ent=%v err=nil", ent)
	}
	id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiNodeOriginGraph)

	rec := get(t, srv, "/workflows/"+id+"/cloud")
	if rec.Code != http.StatusOK {
		t.Fatalf("cloud panel = %d", rec.Code)
	}
	body := rec.Body.String()

	// Fixture reached the interesting case: the panel really did convert, which is
	// what fetches /object_info. Without this, a conversion that aborted early would
	// render no banner at all and the assertions below would read as a pass.
	if fake.objectInfoCalls == 0 {
		t.Fatalf("fixture did not reach the interesting case: the panel never fetched /object_info:\n%s", body)
	}
	names, present := cloudBannerNodeNames(t, body)
	if !present {
		t.Fatalf("no custom-node banner at all — the genuine custom node must still be "+
			"flagged, so this is a broken fixture rather than a pass:\n%s", body)
	}
	if hasName(names, "WanImageToVideo") {
		t.Errorf("WanImageToVideo was flagged as unrecognised (banner listed %v). The handler "+
			"holds a freshly fetched /object_info that classifies it as comfy_extras, so it must "+
			"classify from THAT rather than re-reading the cache — which here cannot answer at "+
			"all, exactly as it cannot when the swallowed PutComfyObjectInfo error means the "+
			"write never landed. Falling back leaves the user the 62%% false-positive banner "+
			"while the authoritative payload sits in the same call frame.", names)
	}
	if !hasName(names, "ImpactWildcardProcessor") {
		t.Errorf("the genuine custom node was not flagged (banner listed %v)", names)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// THE TIER OBSERVABILITY — proving it runs where the TRAFFIC is.
// ─────────────────────────────────────────────────────────────────────────────
// 🔴 THIS EXISTS BECAUSE THE FIRST VERSION LOGGED ON THE PATH NOBODY TAKES. The
// split accounting lived inside comfyNodeOrigins, which resolveResources reaches
// ONLY as a fallback — so on a UI-format workflow with a reachable ComfyUI (all 71
// workflows on the operator's real DB) it never executed at all, while its own
// comment claimed a 0-custom split was "the only externally visible signal". Same
// shape as this repo's recurring defect: not broken code, code that never runs. A
// test that only asserts "some line was logged" would have passed the broken
// version, so every case below pins the TIER.

// capturedRecord is one slog record flattened to what these assertions need.
type capturedRecord struct {
	level slog.Level
	msg   string
	attrs map[string]string
}

// logCapture is a slog.Handler that keeps every record. Enabled always returns
// true: the server's real handler is LevelInfo, which would DROP the Debug split
// line and make these tests vacuous in the most convincing way possible.
type logCapture struct {
	mu   sync.Mutex
	recs []capturedRecord
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{level: r.Level, msg: r.Message, attrs: map[string]string{}}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, rec)
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

func (c *logCapture) withMessage(msg string) []capturedRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []capturedRecord
	for _, r := range c.recs {
		if r.msg == msg {
			out = append(out, r)
		}
	}
	return out
}

func (c *logCapture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.recs))
	for _, r := range c.recs {
		out = append(out, r.level.String()+" "+r.msg)
	}
	return out
}

const (
	nodeOriginSplitMsg = "classified comfy object_info"
	freshDecodeFailMsg = "decode fetched comfy object_info"
)

// captureLogs swaps in a capturing logger. It must be called before the request:
// Server.log is read per record, so a later swap would miss everything.
func captureLogs(srv *Server) *logCapture {
	logs := &logCapture{}
	srv.log = slog.New(logs)
	return logs
}

func TestNodeOriginTierIsObservable(t *testing.T) {
	// ── the DOMINANT path: UI-format workflow, reachable ComfyUI ───────────────
	// This is the case the broken version could not see. Asserting tier=fresh is
	// what makes it discriminating — moving the call back inside the fallback
	// leaves this subtest with zero records.
	t.Run("fresh tier logs the split", func(t *testing.T) {
		srv := newCloudTestServer(t, &fakeCloud{})
		fake := &fakeComfy{
			info:    mustObjectInfo(t, freshNodeOriginInfo),
			infoRaw: []byte(freshNodeOriginInfo),
		}
		srv.comfyClientFn = func() comfyClient { return fake }
		logs := captureLogs(srv)

		id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiNodeOriginGraph)
		if rec := get(t, srv, "/workflows/"+id+"/cloud"); rec.Code != http.StatusOK {
			t.Fatalf("cloud panel = %d", rec.Code)
		}
		// Fixture reached the interesting case: the panel really did fetch, which is
		// what produces a fresh rawInfo at all. A conversion that aborted early would
		// leave no records and read as a different failure.
		if fake.objectInfoCalls == 0 {
			t.Fatalf("fixture did not reach the interesting case: never fetched /object_info")
		}
		got := logs.withMessage(nodeOriginSplitMsg)
		if len(got) != 1 {
			t.Fatalf("want exactly 1 %q record on the fresh path, got %d (all records: %v). "+
				"Zero means the split is logged somewhere the dominant path does not go — "+
				"the exact regression this guards.", nodeOriginSplitMsg, len(got), logs.messages())
		}
		if tier := got[0].attrs["tier"]; tier != "fresh" {
			t.Errorf("tier = %q, want \"fresh\": the freshly fetched payload answered, so a "+
				"\"cache\" tier here means the pass-through is not being used", tier)
		}
		// The counts, not just their presence. freshNodeOriginInfo is deliberately
		// TWO built-ins (comfy_extras.* and the dot-free `nodes`) against ONE
		// custom_nodes.* custom, so all three numbers differ: this pins that the two
		// counters are the right way round, not merely that they did not collapse.
		// The earlier 1-and-1 fixture pinned only the collapse — swapping the
		// builtin/custom arguments passed the whole package.
		for key, want := range map[string]string{"types": "3", "builtin": "2", "custom": "1"} {
			if got := got[0].attrs[key]; got != want {
				t.Errorf("%s = %q, want %q (attrs=%v)", key, got, want, logs.withMessage(nodeOriginSplitMsg)[0].attrs)
			}
		}
	})

	// ── the FALLBACK path: API-format workflow, warm cache ────────────────────
	// 🔴 THE FALLBACK'S OWN LABEL WAS UNASSERTED. Only tier="fresh" was pinned, so
	// mutating the fallback branch's tier = "cache" to "fresh" survived the entire
	// internal/web suite. The field exists for exactly one purpose — telling an
	// operator WHICH tier answered — so a cache tier that reports itself as fresh
	// sends them to debug the half that did not run.
	//
	// An API-format workflow is the deterministic way in: cloudAPIGraph returns
	// before the fetch, so rawInfo is nil, the fresh tier classifies nothing, and the
	// cached row is the only origin source there is.
	t.Run("cache tier logs the split", func(t *testing.T) {
		srv := newCloudTestServer(t, &fakeCloud{})
		// Injected purely so "ComfyUI was never contacted" is a MEASURED precondition
		// rather than an argument about cloudAPIGraph's control flow. Its payload is
		// never used; if it ever were, objectInfoCalls below would say so.
		fake := &fakeComfy{
			info:    mustObjectInfo(t, freshNodeOriginInfo),
			infoRaw: []byte(freshNodeOriginInfo),
		}
		srv.comfyClientFn = func() comfyClient { return fake }
		logs := captureLogs(srv)

		if err := srv.store.PutComfyObjectInfo([]byte(nodeOriginObjectInfo)); err != nil {
			t.Fatalf("seed object_info cache: %v", err)
		}
		// Assert the intermediate state: the row reads back and really does classify
		// two built-ins against one custom. A row that failed to decode would leave
		// the cache tier silent and this subtest asserting nothing.
		ent, err := srv.store.GetComfyObjectInfo()
		if err != nil || ent == nil {
			t.Fatalf("fixture precondition: the seeded row must read back, got ent=%v err=%v", ent, err)
		}
		idx := comfy.NodeOrigins(ent.ObjectInfoJSON)
		if comfy.OriginOf(idx, "WanImageToVideo") != comfy.NodeOriginBuiltin ||
			comfy.OriginOf(idx, "CLIPVisionLoader") != comfy.NodeOriginBuiltin ||
			comfy.OriginOf(idx, "ImpactWildcardProcessor") != comfy.NodeOriginCustom {
			t.Fatalf("fixture precondition: the seeded row does not classify 2 builtin + 1 custom (idx=%v)", idx)
		}

		id := seedWorkflow(t, srv, store.WorkflowFormatAPI, nodeOriginAPIGraph)
		if rec := get(t, srv, "/workflows/"+id+"/cloud"); rec.Code != http.StatusOK {
			t.Fatalf("cloud panel = %d", rec.Code)
		}
		// Fixture reached the interesting case: nothing fresh could possibly have
		// answered, so whatever tier the record names, the CACHE is what classified.
		if fake.objectInfoCalls != 0 {
			t.Fatalf("fixture did not reach the interesting case: the API-format panel fetched "+
				"/object_info %d time(s), so a fresh payload could have answered and the tier "+
				"assertion below would not be about the fallback at all", fake.objectInfoCalls)
		}
		got := logs.withMessage(nodeOriginSplitMsg)
		if len(got) != 1 {
			t.Fatalf("want exactly 1 %q record on the cache path, got %d (all records: %v)",
				nodeOriginSplitMsg, len(got), logs.messages())
		}
		if tier := got[0].attrs["tier"]; tier != "cache" {
			t.Errorf("tier = %q, want \"cache\": this request never contacted ComfyUI (0 "+
				"/object_info fetches), so the cached 0019 row is the only thing that could "+
				"have classified anything. A \"fresh\" label here is a lie that costs an "+
				"operator their whole debugging session.", tier)
		}
		// nodeOriginObjectInfo is 2 built-ins against 1 custom — pairwise distinct for
		// the same reason the fresh fixture is.
		for key, want := range map[string]string{"types": "3", "builtin": "2", "custom": "1"} {
			if got := got[0].attrs[key]; got != want {
				t.Errorf("%s = %q, want %q (attrs=%v)", key, got, want, logs.withMessage(nodeOriginSplitMsg)[0].attrs)
			}
		}
	})

	// ── a fetched body that classifies NOTHING ────────────────────────────────
	// The one case this observability was added for, and the one comfyNodeOrigins
	// structurally cannot report: it never sees rawInfo. Before the fix this fell
	// through to the cache silently, so a corrupt fresh payload was indistinguishable
	// from an API-format workflow that legitimately has none.
	t.Run("a corrupt fresh payload is warned about", func(t *testing.T) {
		// Truncated mid-object: non-empty (so it is NOT the nil/API-format case) and
		// undecodable (so NodeOrigins yields nothing).
		truncated := []byte(`{"WanImageToVideo": {"python_mo`)
		if idx := comfy.NodeOrigins(truncated); len(idx) != 0 {
			t.Fatalf("fixture precondition: the truncated body must classify nothing, got %v", idx)
		}

		srv := newCloudTestServer(t, &fakeCloud{})
		// info decodes (the conversion needs it) while infoRaw does not — the fake
		// carries them independently, which is exactly the production shape of a
		// truncated response body.
		fake := &fakeComfy{
			info:    mustObjectInfo(t, freshNodeOriginInfo),
			infoRaw: truncated,
		}
		srv.comfyClientFn = func() comfyClient { return fake }
		logs := captureLogs(srv)

		id := seedWorkflow(t, srv, store.WorkflowFormatUI, uiNodeOriginGraph)
		if rec := get(t, srv, "/workflows/"+id+"/cloud"); rec.Code != http.StatusOK {
			t.Fatalf("cloud panel = %d", rec.Code)
		}
		if fake.objectInfoCalls == 0 {
			t.Fatalf("fixture did not reach the interesting case: never fetched /object_info")
		}
		got := logs.withMessage(freshDecodeFailMsg)
		if len(got) != 1 {
			t.Fatalf("want exactly 1 %q record, got %d (all records: %v). A fetched body that "+
				"classifies nothing must not fall through to the cache silently.",
				freshDecodeFailMsg, len(got), logs.messages())
		}
		if got[0].level != slog.LevelWarn {
			t.Errorf("level = %v, want WARN: this is a real fault and must reach the default "+
				"log level, unlike the Debug split", got[0].level)
		}
		if b, want := got[0].attrs["bytes"], strconv.Itoa(len(truncated)); b != want {
			t.Errorf("bytes = %q, want %q — the byte count is what separates a truncated body "+
				"from a well-formed empty one", b, want)
		}
	})

	// ── a cold cache stays SILENT ─────────────────────────────────────────────
	// An empty cache is a designed state, not a fault: an API-format workflow never
	// contacts ComfyUI, so rawInfo is nil and there is genuinely nothing to report.
	// Pinned so nobody "improves" the logging into per-render noise on a fresh
	// install, which is the pressure a missing-signal audit creates.
	t.Run("a cold cache logs nothing", func(t *testing.T) {
		srv := newCloudTestServer(t, &fakeCloud{})
		logs := captureLogs(srv)
		if ent, err := srv.store.GetComfyObjectInfo(); err != nil || ent != nil {
			t.Fatalf("fixture precondition: the cache must start empty, got ent=%v err=%v", ent, err)
		}
		id := seedWorkflow(t, srv, store.WorkflowFormatAPI, nodeOriginAPIGraph)
		if rec := get(t, srv, "/workflows/"+id+"/cloud"); rec.Code != http.StatusOK {
			t.Fatalf("cloud panel = %d", rec.Code)
		}
		for _, msg := range []string{nodeOriginSplitMsg, freshDecodeFailMsg} {
			if got := logs.withMessage(msg); len(got) != 0 {
				t.Errorf("a cold cache logged %q %d time(s) (all records: %v). An empty cache is "+
					"the normal fresh-install state; reporting it on every render trains the "+
					"operator to ignore the line that does mean something.", msg, len(got), logs.messages())
			}
		}
	})
}
