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

// staleNodeOriginInfo is the SAME app one ComfyUI upgrade ago: WanImageToVideo did
// not exist yet, so the cached row cannot classify it and it falls through to
// coreNodeClasses — which does not contain it either, so the stale tier flags a
// real built-in. That disagreement is the whole point of the fixture.
const staleNodeOriginInfo = `{
  "ImpactWildcardProcessor": {"python_module":"custom_nodes.comfyui-impact-pack","input":{"required":{}},"input_order":{"required":[]}}
}`

// TestCloudPanelPrefersTheFreshlyFetchedObjectInfoOverTheCachedRow pins that the
// handler classifies from the payload it JUST fetched rather than re-reading a
// stale ~4.66 MB row back out of SQLite.
//
// The two sources are made to DISAGREE on purpose. If they agreed, the assertion
// would be over-determined and would pass with the pass-through deleted.
func TestCloudPanelPrefersTheFreshlyFetchedObjectInfoOverTheCachedRow(t *testing.T) {
	// Fixture preconditions: the two payloads must genuinely disagree about
	// WanImageToVideo, or this test cannot discriminate.
	if o := comfy.OriginOf(comfy.NodeOrigins([]byte(freshNodeOriginInfo)), "WanImageToVideo"); o != comfy.NodeOriginBuiltin {
		t.Fatalf("fixture precondition: the FRESH payload must call WanImageToVideo built-in, got %v", o)
	}
	if o := comfy.OriginOf(comfy.NodeOrigins([]byte(staleNodeOriginInfo)), "WanImageToVideo"); o != comfy.NodeOriginUnknown {
		t.Fatalf("fixture precondition: the STALE payload must NOT know WanImageToVideo, got %v", o)
	}

	srv := newCloudTestServer(t, &fakeCloud{})
	fake := &fakeComfy{
		info:    mustObjectInfo(t, freshNodeOriginInfo),
		infoRaw: []byte(freshNodeOriginInfo),
	}
	srv.comfyClientFn = func() comfyClient { return fake }

	if err := srv.store.PutComfyObjectInfo([]byte(staleNodeOriginInfo)); err != nil {
		t.Fatalf("seed stale cache: %v", err)
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
			"holds a freshly fetched /object_info that classifies it as comfy_extras — it must "+
			"use that rather than re-reading a stale cached row, which is both a wasted 4.66 MB "+
			"read and a silent downgrade whenever the cache write did not land.", names)
	}
	if !hasName(names, "ImpactWildcardProcessor") {
		t.Errorf("the genuine custom node was not flagged (banner listed %v)", names)
	}
}
