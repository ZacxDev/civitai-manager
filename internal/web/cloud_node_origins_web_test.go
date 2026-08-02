package web

import (
	"net/http"
	"strings"
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
// The positive half is deliberately weak (the banner still hedges at all), because
// the hedge's WORDING is expected to move and its EXISTENCE is not.
func TestNodepackBannerStatesNoDetectionMechanism(t *testing.T) {
	body := renderString(t, cloudNodepackBlocker([]comfy.ResolvedResource{
		{Filename: "ImpactWildcardProcessor", Status: comfy.ResolveCustomNode},
	}))
	// Fixture reaches the case: a banner rendered at all.
	if !strings.Contains(body, "CivitAI Cloud cannot run this workflow yet") {
		t.Fatalf("fixture precondition: no banner rendered:\n%s", body)
	}
	for _, banned := range []string{
		"short list of known built-in node types",
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
	// warning into an assertion the detector cannot support in either tier.
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

const freshNodeOriginInfo = `{
  "WanImageToVideo":         {"python_module":"comfy_extras.nodes_wan","input":{"required":{}},"input_order":{"required":[]}},
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
func TestCloudPanelUsesTheFreshObjectInfoWhenTheCacheCannotAnswer(t *testing.T) {
	// Fixture precondition: the fresh payload really does classify the built-in,
	// and coreNodeClasses really does not (proved by the cold-cache control in the
	// test above, which flags WanImageToVideo).
	if o := comfy.OriginOf(comfy.NodeOrigins([]byte(freshNodeOriginInfo)), "WanImageToVideo"); o != comfy.NodeOriginBuiltin {
		t.Fatalf("fixture precondition: the FRESH payload must call WanImageToVideo built-in, got %v", o)
	}

	srv := newCloudTestServer(t, &fakeCloud{})
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
