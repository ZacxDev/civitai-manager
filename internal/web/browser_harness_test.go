package web

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/library"
)

// TestBrowserHarness is NOT a real test: it is an env-gated manual harness that
// serves the web UI on a loopback port with a controllable, deterministic fake
// discovery crawl so a browser (Playwright) can exercise the client-side
// discover→stop→re-discover flow that does NOT reproduce over plain HTTP.
//
// Run: WEB_BROWSER_HARNESS=1 go test ./internal/web/ -run TestBrowserHarness -v -timeout 30m
//
// It writes the served URL to $WEB_HARNESS_URL_FILE (default
// /tmp/web-harness-url) and blocks until $WEB_HARNESS_STOP_FILE appears (default
// /tmp/web-harness-stop) or ~25m elapses.
func TestBrowserHarness(t *testing.T) {
	if os.Getenv("WEB_BROWSER_HARNESS") == "" {
		t.Skip("set WEB_BROWSER_HARNESS=1 to run the manual browser harness")
	}

	root := t.TempDir()
	// Seed a couple of model files so the "Scan for model files" tab yields a
	// non-empty Summary when scanned.
	for _, n := range []string{"model-a.safetensors", "model-b.safetensors"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("weights-"+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	srv := newLibraryTestServer(t, root)

	// Pre-seed one selected scan dir so Tab A shows a selection and Tab B is
	// scannable out of the box.
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "dup.safetensors"), []byte("weights-model-a.safetensors"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AddScanDir(extra); err != nil {
		t.Fatal(err)
	}

	srv.discoverRoots = []string{root}
	// One discovered install points at a REAL temp dir so its Add succeeds (the
	// dir must exist to pass validateScanDir); the others are illustrative fake
	// paths whose Add is a no-op error — the demo adds the real one.
	realInstall := t.TempDir()
	// Deterministic streaming crawl: emit three installs ~1.2s apart, then idle so
	// the "scanning" state persists long enough to click Stop; honor ctx so Stop
	// (context cancel) settles the job promptly.
	installs := []library.Install{
		{Path: realInstall, Kind: library.KindComfyUI, Confidence: library.ConfidenceHigh, ModelDirs: []string{"checkpoints", "loras", "vae"}},
		{Path: "/opt/stable-diffusion-webui", Kind: library.KindA1111, Confidence: library.ConfidenceHigh, ModelDirs: []string{"Stable-diffusion", "Lora"}},
		{Path: "/home/user/AI/ComfyUI-portable", Kind: library.KindComfyUI, Confidence: library.ConfidenceLow, ModelDirs: []string{"checkpoints"}},
	}
	srv.crawlFn = func(ctx context.Context, roots []string, opts library.DiscoverOptions) ([]library.Install, error) {
		var out []library.Install
		for _, in := range installs {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(1200 * time.Millisecond):
			}
			if opts.OnInstall != nil {
				opts.OnInstall(in)
			}
			out = append(out, in)
		}
		// Idle so a human/Playwright has time to observe the streaming/scanning view
		// and click Stop before the crawl would naturally exhaust.
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(20 * time.Second):
		}
		return out, nil
	}

	// Deterministic streaming SCAN seam: emit a handful of scanned-file cards ~1s
	// apart (a mix of matched/unmatched with a preview), then idle so a browser can
	// observe the streaming Model-files view and click Stop before it exhausts.
	// Honors ctx so Stop (context cancel) settles the job promptly.
	scanFiles := []library.FileResult{
		{Path: "/opt/ComfyUI/models/checkpoints/dreamshaper.safetensors", Name: "dreamshaper.safetensors", SizeBytes: 2147483648, SHA256: "aaa", Status: "matched", ModelID: intPtr(4384), VersionID: intPtr(128713), HasPreview: true},
		{Path: "/opt/ComfyUI/models/loras/detail-tweaker.safetensors", Name: "detail-tweaker.safetensors", SizeBytes: 151000000, SHA256: "bbb", Status: "matched", ModelID: intPtr(58390), VersionID: intPtr(62833)},
		{Path: "/opt/ComfyUI/models/checkpoints/mystery-merge.ckpt", Name: "mystery-merge.ckpt", SizeBytes: 5368709120, SHA256: "ccc", Status: "unmatched"},
		{Path: "/opt/ComfyUI/models/vae/broken.safetensors", Name: "broken.safetensors", SizeBytes: 320, SHA256: "ddd", Status: "broken"},
	}
	srv.scanFn = func(ctx context.Context, onFile func(library.FileResult), onDiscovered func(int)) error {
		// Report the discovered total up front (mirrors the real scanner reporting it
		// right after the walk) so the streaming progress line shows "N / total
		// discovered" and the terminal shows "total discovered".
		onDiscovered(len(scanFiles))
		for _, fr := range scanFiles {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1100 * time.Millisecond):
			}
			onFile(fr)
		}
		// Brief idle so a browser can observe the fully-streamed view and Stop before
		// the scan exhausts; short enough that a completed scan is reachable too.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(6 * time.Second):
		}
		return nil
	}

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	urlFile := envOr("WEB_HARNESS_URL_FILE", "/tmp/web-harness-url")
	stopFile := envOr("WEB_HARNESS_STOP_FILE", "/tmp/web-harness-stop")
	_ = os.Remove(stopFile)
	if err := os.WriteFile(urlFile, []byte(ts.URL), 0o644); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("BROWSER_HARNESS_URL=%s\n", ts.URL)
	t.Logf("serving harness at %s (stop by creating %s)", ts.URL, stopFile)

	deadline := time.Now().Add(25 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopFile); err == nil {
			t.Logf("stop file seen; shutting down harness")
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
