package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/civitai"
	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// newFixServer wires a server for the missing-model Fix popover flow: the given
// reader, a fake ComfyUI whose loader's only installed choice is
// installed.safetensors (so a graph referencing absent.safetensors fails preflight),
// a loopback comfy_url, and an optional comfy_model_path (eligible when non-empty).
func newFixServer(t *testing.T, reader civitai.Reader, comfyModelPath string) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := NewServer(st, reader, stubSubscriber{}, Config{
		BaseURL: "https://civitai.com", DefaultPollInterval: time.Hour,
		Addr: "127.0.0.1:8787", ComfyURL: "http://127.0.0.1:8188",
		ComfyModelPath: comfyModelPath,
	}, nil)
	base, cancel := context.WithCancel(context.Background())
	srv.SetBaseContext(base)
	t.Cleanup(cancel)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{info: mustObjectInfo(t, substituteObjectInfo)} }
	return srv
}

const missingGraph = `{"4":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"absent.safetensors"}}}`

// erroringSearchReader (SearchModels always errors) is defined in
// discover_workflows_web_test.go and reused here to model a CivitAI outage.

// TestFixResolutionResolvedOnceNotPerRender proves the eager CivitAI resolution
// runs ONCE at run settle and is NOT re-fired by the ~1-2s run-status poll (the
// terminal popover renders from the cached snapshot).
func TestFixResolutionResolvedOnceNotPerRender(t *testing.T) {
	reader := &recordingSearchReader{result: resolveResult("Fabricated XL")}
	srv := newFixServer(t, reader, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, missingGraph)

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != 200 {
		t.Fatalf("run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)
	if !strings.Contains(body, "Fabricated XL") {
		t.Fatalf("terminal popover missing the resolved match:\n%s", body)
	}
	// Exactly one search for the single missing model — computed at settle.
	if n := reader.callCount(); n != 1 {
		t.Fatalf("SearchModels called %d times at settle, want 1", n)
	}
	// Re-render the terminal fragment several more times: NO new API calls.
	for i := 0; i < 5; i++ {
		if rec := get(t, srv, "/workflows/"+id+"/run/status"); rec.Code != 200 {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if n := reader.callCount(); n != 1 {
		t.Errorf("SearchModels called %d times after 5 extra renders, want 1 (resolution is at settle, not per render)", n)
	}
}

// TestFixResolutionCivitaiFailureDegrades asserts a CivitAI outage at settle
// degrades to the "couldn't reach CivitAI" state (no panic, run still terminates).
func TestFixResolutionCivitaiFailureDegrades(t *testing.T) {
	srv := newFixServer(t, erroringSearchReader{}, t.TempDir())
	id := seedWorkflow(t, srv, store.WorkflowFormatAPI, missingGraph)

	if rec := post(t, srv, "/workflows/"+id+"/run", nil, true); rec.Code != 200 {
		t.Fatalf("run = %d", rec.Code)
	}
	body := pollRunUntilDone(t, srv, id)
	if !strings.Contains(body, "Could not reach CivitAI") {
		t.Errorf("expected the couldn't-reach degraded state:\n%s", body)
	}
	// The fallback search link is still offered so the popover is never a dead end.
	if !strings.Contains(body, "/search?q=absent") {
		t.Errorf("degraded state must still offer the Search CivitAI link:\n%s", body)
	}
}

// fixTestModel is a missing model with a same-base rich candidate + a long tail of
// other candidates (to exercise ordering + collapse).
func fixTestModel() comfy.MissingModel {
	return comfy.MissingModel{
		Filename:        "fabricatedXL_v70.safetensors",
		Query:           "fabricated XL",
		CivitaiType:     "Checkpoint",
		SameBase:        []string{"richbase.safetensors"},
		OtherCandidates: []string{"o1.safetensors", "o2.safetensors", "o3.safetensors", "o4.safetensors", "o5.safetensors", "o6.safetensors", "o7.safetensors"},
	}
}

// TestFixPopoverRenderSectionsAndWiring renders the popover directly and asserts
// both sections, the primary + capped alternates, install/substitute CTA wiring
// (CSRF + model_id semantics), enriched vs minimal library cards, same-base
// ordering + collapsed tail, and the poller-free / no-external-asset invariants.
func TestFixPopoverRenderSectionsAndWiring(t *testing.T) {
	mm := fixTestModel()
	res := missingResolution{Reached: true, Result: resolveResult("Primary Match", "Alt1", "Alt2", "Alt3", "Alt4")}
	libMeta := map[string]store.LocalModelMeta{
		"richbase.safetensors": {
			Basename: "richbase.safetensors", ModelID: 5, Name: "Rich Base Model",
			BaseModel: "SDXL 1.0", ImageURL: "https://image.civitai.com/rb.jpeg",
			NSFWLevel: 1, NSFWLevelKnown: true,
		},
	}
	body := renderString(t, missingModelsPanel(
		[]comfy.MissingModel{mm},
		map[string]missingResolution{mm.Filename: res},
		libMeta, 7, "tok-csrf", true, NSFWBlur,
	))

	// Both labeled sections.
	for _, want := range []string{"Use matched model from CivitAI", "Replace with a model from my library"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing section header %q", want)
		}
	}
	// Primary + capped-at-3 alternates ("Alt4" dropped).
	for _, want := range []string{"Primary Match", "Alt1", "Alt2", "Alt3"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing card %q", want)
		}
	}
	if strings.Contains(body, "Alt4") {
		t.Errorf("alternates should be capped at %d; Alt4 leaked:\n%s", fixAltCap, body)
	}
	// model_id is passed for EVERY card — the primary (id 1) and the 3 alternates.
	// The primary used to omit it and dead-end; see TestInstallAndRunCTAAlwaysCarriesModelID.
	if n := strings.Count(body, "model_id"); n != fixAltCap+1 {
		t.Errorf("model_id appears %d times, want %d (primary + %d alternates)", n, fixAltCap+1, fixAltCap)
	}
	for _, id := range []string{"1", "2", "3", "4"} {
		if !strings.Contains(body, "model_id&#34;:&#34;"+id+"&#34;") {
			t.Errorf("card missing model_id=%s:\n%s", id, body)
		}
	}
	// Install CTA wiring: download-and-run + CSRF + filename + type.
	for _, want := range []string{"Install and run", "/workflows/7/download-and-run", "tok-csrf",
		"filename&#34;:&#34;fabricatedXL_v70.safetensors", "type&#34;:&#34;Checkpoint"} {
		if !strings.Contains(body, want) {
			t.Errorf("install CTA missing %q", want)
		}
	}
	// Library: rich card (name + base + image + in-app link) for the same-base match.
	for _, want := range []string{"Rich Base Model", "SDXL 1.0", "image.civitai.com/rb.jpeg", `href="/models/5"`} {
		if !strings.Contains(body, want) {
			t.Errorf("enriched library card missing %q", want)
		}
	}
	// Minimal cards for the unmatched tail + Use-this-&-run substitute wiring.
	for _, want := range []string{"o1.safetensors", "Use this &amp; run", "/workflows/7/run-substitute",
		"substitute&#34;:&#34;o1.safetensors"} {
		if !strings.Contains(body, want) {
			t.Errorf("library substitute missing %q", want)
		}
	}
	// Same-base first, tail collapsed in a <details>.
	if !strings.Contains(body, "7 more installed models") {
		t.Errorf("expected the collapsed tail disclosure:\n%s", body)
	}
	if i, j := strings.Index(body, "Rich Base Model"), strings.Index(body, "7 more installed models"); i < 0 || j < 0 || i > j {
		t.Errorf("same-base rich card should render BEFORE the collapsed tail (i=%d j=%d)", i, j)
	}
	// Poller-free (the dialog lives in the terminal fragment; nothing re-swaps it).
	if strings.Contains(body, `id="run-poll"`) || strings.Contains(body, "hx-get") {
		t.Errorf("Fix popover must carry no poll hx-get:\n%s", body)
	}
	// No external CDN asset (offline invariant): no external scripts/stylesheets.
	if strings.Contains(body, "<script src") || strings.Contains(body, `rel="stylesheet"`) {
		t.Errorf("Fix popover must not reference external assets:\n%s", body)
	}
}

// TestFixPopoverZeroMatchAndUnreachable asserts the CivitAI-section degraded states:
// a reached-but-empty search → "No CivitAI match" + Search link; an unreachable
// resolution → "Could not reach CivitAI" + Search link.
func TestFixPopoverZeroMatchAndUnreachable(t *testing.T) {
	mm := comfy.MissingModel{Filename: "absent.safetensors", Query: "absent", CivitaiType: "LORA"}
	libMeta := map[string]store.LocalModelMeta{}

	zero := renderString(t, missingModelsPanel([]comfy.MissingModel{mm},
		map[string]missingResolution{mm.Filename: {Reached: true, Result: resolveResult()}},
		libMeta, 7, "tok", true, NSFWShow))
	if !strings.Contains(zero, "No CivitAI match") || !strings.Contains(zero, "/search?q=absent") {
		t.Errorf("zero-match state wrong:\n%s", zero)
	}

	unreach := renderString(t, missingModelsPanel([]comfy.MissingModel{mm},
		map[string]missingResolution{mm.Filename: {Reached: false}},
		libMeta, 7, "tok", true, NSFWShow))
	if !strings.Contains(unreach, "Could not reach CivitAI") || !strings.Contains(unreach, "/search?q=absent") {
		t.Errorf("unreachable state wrong:\n%s", unreach)
	}
}

// TestFixPopoverIneligibleInstallDisabled asserts that when download is NOT
// eligible the CivitAI card still renders with a DISABLED Install-and-run + reason
// + a View-on-CivitAI link, and issues NO download-and-run POST.
func TestFixPopoverIneligibleInstallDisabled(t *testing.T) {
	mm := comfy.MissingModel{Filename: "absent.safetensors", Query: "absent", CivitaiType: "Checkpoint"}
	body := renderString(t, missingModelsPanel([]comfy.MissingModel{mm},
		map[string]missingResolution{mm.Filename: {Reached: true, Result: resolveResult("A Match")}},
		map[string]store.LocalModelMeta{}, 7, "tok", false /* dlEligible */, NSFWShow))

	if strings.Contains(body, "/download-and-run") {
		t.Errorf("ineligible popover must NOT POST download-and-run:\n%s", body)
	}
	if !strings.Contains(body, "Install and run") || !strings.Contains(body, "disabled") {
		t.Errorf("ineligible popover should render a disabled Install-and-run:\n%s", body)
	}
	if !strings.Contains(body, "Set comfy_model_path to install here") {
		t.Errorf("ineligible popover missing the reason line:\n%s", body)
	}
	if !strings.Contains(body, "View on CivitAI") || !strings.Contains(body, "https://civitai.com/models/1") {
		t.Errorf("ineligible popover missing the View-on-CivitAI link:\n%s", body)
	}
}

// TestFixPopoverEscapesUntrustedName asserts a hostile CivitAI model name is
// escaped in the popover (no raw <script>).
func TestFixPopoverEscapesUntrustedName(t *testing.T) {
	mm := comfy.MissingModel{Filename: "x.safetensors", Query: "x", CivitaiType: "LORA"}
	body := renderString(t, missingModelsPanel([]comfy.MissingModel{mm},
		map[string]missingResolution{mm.Filename: {Reached: true, Result: resolveResult(`<script>alert(1)</script>`)}},
		map[string]store.LocalModelMeta{}, 7, "tok", true, NSFWShow))
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("untrusted model name was not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Errorf("expected an escaped model name:\n%s", body)
	}
}

// TestFixPopoverNSFWHideOmitsPreview asserts an NSFW library preview is OMITTED
// server-side (not just CSS-hidden) under nsfw=hide, blurred under nsfw=blur.
func TestFixPopoverNSFWHideOmitsPreview(t *testing.T) {
	mm := comfy.MissingModel{Filename: "a.safetensors", Query: "a",
		SameBase: []string{"nsfwpick.safetensors"}}
	libMeta := map[string]store.LocalModelMeta{
		"nsfwpick.safetensors": {ModelID: 3, Name: "Spicy", ImageURL: "https://image.civitai.com/x.jpeg",
			NSFWLevel: 8, NSFWLevelKnown: true},
	}
	panel := func(mode string) string {
		return renderString(t, missingModelsPanel([]comfy.MissingModel{mm},
			map[string]missingResolution{mm.Filename: {Reached: true}}, libMeta, 7, "tok", true, mode))
	}
	if hide := panel(NSFWHide); strings.Contains(hide, "image.civitai.com/x.jpeg") {
		t.Errorf("nsfw=hide must OMIT the preview image server-side:\n%s", hide)
	}
	if blur := panel(NSFWBlur); !strings.Contains(blur, "cm-blur") || !strings.Contains(blur, "image.civitai.com/x.jpeg") {
		t.Errorf("nsfw=blur must render a blurred preview:\n%s", blur)
	}
}

// TestFixPopoverTerminalHasNoPoller asserts the terminal (failed) run-status
// fragment carries the popover but NO active poll — so a later swap can never nuke
// an open dialog (the poll loop stops on the terminal state).
func TestFixPopoverTerminalHasNoPoller(t *testing.T) {
	snap := runSnapshot{
		Started: true, Running: false, WorkflowID: 7, Phase: runPhaseFailed,
		Message:         "Preflight failed",
		Preflight:       &comfy.PreflightReport{MissingModels: []string{"a.safetensors"}},
		MissingModels:   []comfy.MissingModel{{Filename: "a.safetensors", Query: "a", CivitaiType: "LORA"}},
		MissingResolved: map[string]missingResolution{"a.safetensors": {Reached: true}},
		LibMeta:         map[string]store.LocalModelMeta{},
	}
	body := renderString(t, runStatusFragment(snap, 7, "tok", false, NSFWBlur))
	if hasRunPoller(body) {
		t.Errorf("terminal fragment must not carry a poller (would nuke the popover):\n%s", body)
	}
	if !strings.Contains(body, "<dialog") {
		t.Errorf("terminal fragment should contain the Fix dialog:\n%s", body)
	}
}
