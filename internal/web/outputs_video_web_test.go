package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ---------------------------------------------------------------------------
// Capture: a gifs-only run must produce a generation row
// ---------------------------------------------------------------------------

// realVHSGifsOutputs is the `outputs` block of a REAL /history entry from the
// user's live ComfyUI 0.27.1 (VHS_VideoCombine, node 70), captured 2026-07-31. It
// is decoded through the production comfy types rather than hand-built as
// []ImageRef, so the test exercises the actual JSON path — a hand-built ref would
// have passed even while the `gifs` key was being dropped on decode, which is
// precisely the bug.
const realVHSGifsOutputs = `{
  "outputs": {
    "70": {
      "gifs": [{
        "filename":   "base_00245.mp4",
        "subfolder":  "video/wan22/base",
        "type":       "output",
        "format":     "video/h264-mp4",
        "frame_rate": 30.0,
        "workflow":   "base_00245.png",
        "fullpath":   "/home/zach/workspace/fast/comfyui/ComfyUI/output/video/wan22/base/base_00245.mp4"
      }]
    }
  },
  "status": {"completed": true, "status_str": "success"}
}`

// decodeHistory decodes a raw /history entry body through the production types.
func decodeHistory(t *testing.T, body string) *comfy.HistoryEntry {
	t.Helper()
	var e comfy.HistoryEntry
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	return &e
}

// TestCaptureStoresVideoFromGifsOnlyRun is THE bug: before the fix, a run whose
// only output was a VHS video produced no generation row at all, so /outputs, the
// batch gallery, the rail and provenance were all empty for a library whose
// workflows save via VHS_VideoCombine.
func TestCaptureStoresVideoFromGifsOnlyRun(t *testing.T) {
	// The /view response carries the type ComfyUI really returns for the real file
	// (verified live: `Content-Type: video/mp4`).
	fake := &fakeComfy{viewData: []byte("MP4BYTES"), viewCT: "video/mp4"}
	srv, root, wf := newCaptureServer(t, fake)

	entry := decodeHistory(t, realVHSGifsOutputs)
	refs := entry.AllImages()
	if len(refs) != 1 {
		t.Fatalf("fixture must yield exactly 1 ref, got %d — the test would not "+
			"reach the capture path it is meant to cover", len(refs))
	}

	srv.captureGeneration(wf, runOptions{}, &runResult{PromptID: "vidprompt", Images: refs})

	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("a gifs-only run must produce 1 generation, got %d — this is the "+
			"reported bug (no row was written at all)", len(gens))
	}
	gen := gens[0]
	if gen.Status != store.GenerationStatusReady {
		t.Errorf("status = %q, want ready", gen.Status)
	}
	if gen.ImageCount != 1 {
		t.Errorf("image_count = %d, want 1", gen.ImageCount)
	}
	if gen.FirstImageContentType != "video/mp4" {
		t.Errorf("FirstImageContentType = %q, want video/mp4 — the list query must "+
			"surface the media kind or no tile can choose <video>", gen.FirstImageContentType)
	}

	_, imgs, err := srv.store.GetGeneration(context.Background(), gen.ID)
	if err != nil {
		t.Fatalf("get generation: %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("images = %d, want 1", len(imgs))
	}
	if imgs[0].ContentType != "video/mp4" {
		t.Errorf("stored content_type = %q, want video/mp4 (NOT the upstream "+
			"video/h264-mp4, and NOT application/octet-stream)", imgs[0].ContentType)
	}
	if imgs[0].Filename != "base_00245.mp4" {
		t.Errorf("filename = %q", imgs[0].Filename)
	}

	// The bytes really landed on disk under the sanitized rel path.
	p := filepath.Join(root, "vidprompt", "0-base_00245.mp4")
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("expected the video at %s: %v", p, err)
	}
	if string(got) != "MP4BYTES" {
		t.Errorf("stored bytes = %q", got)
	}
}

// TestCaptureNeverReadsFullpath makes the "we do not touch the untrusted absolute
// path" claim BITE.
//
// The payload's `fullpath` names a real file that exists on disk and whose contents
// differ from what /view serves. If any code path preferred fullpath, the stored
// bytes would be the decoy's. The fetch is additionally asserted to have gone out
// as filename+subfolder+type.
func TestCaptureNeverReadsFullpath(t *testing.T) {
	// A decoy the capture must NOT read, at an absolute path we hand it.
	decoyDir := t.TempDir()
	decoy := filepath.Join(decoyDir, "decoy.mp4")
	if err := os.WriteFile(decoy, []byte("DECOY-FROM-FULLPATH"), 0o600); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	body := `{"outputs":{"70":{"gifs":[{
		"filename":"real.mp4","subfolder":"video/sub","type":"output",
		"format":"video/h264-mp4",
		"fullpath":` + strconv.Quote(decoy) + `}]}}}`
	entry := decodeHistory(t, body)
	refs := entry.AllImages()
	if len(refs) != 1 {
		t.Fatalf("fixture yielded %d refs, want 1", len(refs))
	}

	var seen []comfy.ImageRef
	fake := &fakeComfy{viewFunc: func(ref comfy.ImageRef) ([]byte, string, error) {
		seen = append(seen, ref)
		return []byte("VIA-VIEW"), "video/mp4", nil
	}}
	srv, root, wf := newCaptureServer(t, fake)
	srv.captureGeneration(wf, runOptions{}, &runResult{PromptID: "fp", Images: refs})

	if len(seen) != 1 {
		t.Fatalf("View calls = %d, want 1", len(seen))
	}
	// The fetch is addressed by filename/subfolder/type — the same shape images use.
	if seen[0].Filename != "real.mp4" || seen[0].Subfolder != "video/sub" || seen[0].Type != "output" {
		t.Errorf("View ref = %+v, want {real.mp4 video/sub output} — the fetch must "+
			"go through /view by filename+subfolder+type, never by fullpath", seen[0])
	}
	if strings.Contains(seen[0].Filename, decoyDir) {
		t.Errorf("the untrusted absolute path leaked into the fetch: %+v", seen[0])
	}

	// The stored bytes came from /view, not from the decoy on disk.
	stored, err := os.ReadFile(filepath.Join(root, "fp", "0-real.mp4"))
	if err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if string(stored) != "VIA-VIEW" {
		t.Fatalf("stored bytes = %q, want VIA-VIEW — capture read the upstream's "+
			"`fullpath` instead of fetching through /view", stored)
	}
	// And the decoy is untouched where it sits.
	if b, err := os.ReadFile(decoy); err != nil || string(b) != "DECOY-FROM-FULLPATH" {
		t.Errorf("decoy changed: %q %v", b, err)
	}
}

// TestCaptureImagesOnlyIsUnchanged is the regression guard for the only thing that
// worked before. An images-only capture must store exactly what it always did.
func TestCaptureImagesOnlyIsUnchanged(t *testing.T) {
	fake := &fakeComfy{viewData: []byte("PNGBYTES"), viewCT: "image/png"}
	srv, root, wf := newCaptureServer(t, fake)

	entry := decodeHistory(t, `{"outputs":{"9":{"images":[
		{"filename":"a.png","subfolder":"","type":"output"},
		{"filename":"b.png","subfolder":"","type":"output"}]}}}`)
	srv.captureGeneration(wf, runOptions{}, &runResult{PromptID: "imgonly", Images: entry.AllImages()})

	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(gens) != 1 || gens[0].ImageCount != 2 {
		t.Fatalf("generations = %+v, want 1 row with 2 images", gens)
	}
	if gens[0].FirstImageContentType != "image/png" {
		t.Errorf("FirstImageContentType = %q, want image/png", gens[0].FirstImageContentType)
	}
	_, imgs, err := srv.store.GetGeneration(context.Background(), gens[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for i, img := range imgs {
		if img.ContentType != "image/png" {
			t.Errorf("image[%d] content_type = %q, want image/png", i, img.ContentType)
		}
	}
	for _, name := range []string{"0-a.png", "1-b.png"} {
		if _, err := os.Stat(filepath.Join(root, "imgonly", name)); err != nil {
			t.Errorf("expected %s: %v", name, err)
		}
	}
}

// TestCaptureNodeEmittingBothKinds covers the mixed node: a PreviewImage beside a
// VHS_VideoCombine (the shape of the user's workflow 570). Both must be captured,
// in order, each with its own type.
func TestCaptureNodeEmittingBothKinds(t *testing.T) {
	fake := &fakeComfy{viewFunc: func(ref comfy.ImageRef) ([]byte, string, error) {
		if strings.HasSuffix(ref.Filename, ".mp4") {
			return []byte("MP4"), "video/mp4", nil
		}
		return []byte("PNG"), "image/png", nil
	}}
	srv, _, wf := newCaptureServer(t, fake)

	entry := decodeHistory(t, `{"outputs":{"70":{
		"images":[{"filename":"preview.png","type":"output"}],
		"gifs":[{"filename":"clip.mp4","type":"output","format":"video/h264-mp4"}]}}}`)
	refs := entry.AllImages()
	if len(refs) != 2 {
		t.Fatalf("fixture yielded %d refs, want 2 — the test would not reach the "+
			"mixed-node path", len(refs))
	}
	srv.captureGeneration(wf, runOptions{}, &runResult{PromptID: "both", Images: refs})

	gens, err := srv.store.ListGenerations(context.Background(), store.ListGenerationsOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(gens) != 1 {
		t.Fatalf("generations = %d, want 1", len(gens))
	}
	_, imgs, err := srv.store.GetGeneration(context.Background(), gens[0].ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(imgs) != 2 {
		t.Fatalf("captured %d outputs, want 2 (both kinds from one node)", len(imgs))
	}
	if imgs[0].Filename != "preview.png" || imgs[0].ContentType != "image/png" {
		t.Errorf("output[0] = %s/%s, want preview.png/image/png", imgs[0].Filename, imgs[0].ContentType)
	}
	if imgs[1].Filename != "clip.mp4" || imgs[1].ContentType != "video/mp4" {
		t.Errorf("output[1] = %s/%s, want clip.mp4/video/mp4", imgs[1].Filename, imgs[1].ContentType)
	}
	if imgs[0].Idx != 0 || imgs[1].Idx != 1 {
		t.Errorf("idx = %d,%d — order must be preserved", imgs[0].Idx, imgs[1].Idx)
	}
}

// ---------------------------------------------------------------------------
// MIME resolution
// ---------------------------------------------------------------------------

// TestOutputMediaTypeRefusesHostileAndBogusFormats pins the whole resolution
// table, including the two things that must NEVER be echoed: the real-but-bogus
// VHS `format` and an attacker-chosen one.
func TestOutputMediaTypeRefusesHostileAndBogusFormats(t *testing.T) {
	cases := []struct {
		name           string
		filename       string
		format         string
		upstreamCT     string
		want           string
		wantIsVideo    bool
		mustNotContain string
	}{
		{
			name: "the real VHS payload resolves to a playable type",
			// This is the live case: format is bogus, the extension is the truth.
			filename: "base_00245.mp4", format: "video/h264-mp4", upstreamCT: "video/mp4",
			want: "video/mp4", wantIsVideo: true, mustNotContain: "h264",
		},
		{
			name:     "a bogus format with NO usable extension still maps, never echoes",
			filename: "clip", format: "video/h264-mp4", upstreamCT: "",
			want: "video/mp4", wantIsVideo: true, mustNotContain: "h264",
		},
		{
			name:     "h265 maps to mp4 too",
			filename: "clip", format: "video/h265-mp4", upstreamCT: "",
			want: "video/mp4", wantIsVideo: true, mustNotContain: "h265",
		},
		{
			name:     "av1-webm maps to webm",
			filename: "clip", format: "video/av1-webm", upstreamCT: "",
			want: "video/webm", wantIsVideo: true, mustNotContain: "av1",
		},
		{
			name: "an UNKNOWN format is refused, not echoed",
			// Nothing identifies this. It must fall all the way through.
			filename: "clip", format: "video/totally-made-up", upstreamCT: "",
			want: outputMediaTypeRefused, mustNotContain: "made-up",
		},
		{
			name:     "a HOSTILE format is refused, not echoed",
			filename: "clip", format: "text/html", upstreamCT: "",
			want: outputMediaTypeRefused, mustNotContain: "html",
		},
		{
			name:     "a header-injecting format is refused",
			filename: "clip", format: "video/mp4\r\nX-Evil: 1", upstreamCT: "",
			want: outputMediaTypeRefused, mustNotContain: "Evil",
		},
		{
			name:     "a hostile UPSTREAM content-type is refused",
			filename: "clip", format: "", upstreamCT: "text/html; charset=utf-8",
			want: outputMediaTypeRefused, mustNotContain: "html",
		},
		{
			name:     "a script upstream type on a .png is overridden by our extension",
			filename: "a.png", format: "", upstreamCT: "application/javascript",
			want: "image/png", mustNotContain: "javascript",
		},
		{
			name:     "a whitelisted upstream type with parameters is normalized",
			filename: "noext", format: "", upstreamCT: "image/png; charset=utf-8",
			want: "image/png", mustNotContain: "charset",
		},
		{
			name:     "a traversal filename is basenamed before its extension is read",
			filename: "../../../etc/passwd.mp4", format: "", upstreamCT: "",
			want: "video/mp4", wantIsVideo: true,
		},
		{
			name:     "webm by extension",
			filename: "a.webm", format: "", upstreamCT: "", want: "video/webm", wantIsVideo: true,
		},
		{
			name: "a .mov is deliberately refused (ProRes cannot play in a browser)",
			// Better an honest download link than a black <video> that never works.
			filename: "a.mov", format: "", upstreamCT: "video/quicktime",
			want: outputMediaTypeRefused,
		},
		{
			name:     "an uppercase extension still resolves",
			filename: "A.MP4", format: "", upstreamCT: "", want: "video/mp4", wantIsVideo: true,
		},
		{name: "png", filename: "a.png", want: "image/png"},
		{name: "jpeg", filename: "a.jpeg", want: "image/jpeg"},
		{name: "gif from a gifs entry really is a gif", filename: "a.gif", want: "image/gif"},
		{name: "nothing at all is refused", filename: "", want: outputMediaTypeRefused},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := outputMediaType(tc.filename, tc.format, tc.upstreamCT)
			if got != tc.want {
				t.Errorf("outputMediaType(%q, %q, %q) = %q, want %q",
					tc.filename, tc.format, tc.upstreamCT, got, tc.want)
			}
			if tc.mustNotContain != "" && strings.Contains(got, tc.mustNotContain) {
				t.Errorf("the resolved type %q echoes untrusted upstream text (%q)",
					got, tc.mustNotContain)
			}
			if isVideoOutput(got) != tc.wantIsVideo {
				t.Errorf("isVideoOutput(%q) = %v, want %v", got, isVideoOutput(got), tc.wantIsVideo)
			}
		})
	}
}

// TestServableOutputContentTypeIgnoresTheStoredRow asserts the SERVING side
// re-derives its header from the whitelist instead of trusting the DB. A row
// written by an older build, or a corrupted one, must not widen what this origin
// serves.
func TestServableOutputContentTypeIgnoresTheStoredRow(t *testing.T) {
	for _, tc := range []struct{ stored, want string }{
		{"video/mp4", "video/mp4"},
		{"image/png", "image/png"},
		{"VIDEO/MP4", "video/mp4"},
		{"image/png; charset=utf-8", "image/png"},
		// The trap: the bogus upstream format, had it ever been persisted.
		{"video/h264-mp4", outputMediaTypeRefused},
		{"text/html", outputMediaTypeRefused},
		{"application/javascript", outputMediaTypeRefused},
		{"image/svg+xml", outputMediaTypeRefused}, // scriptable — deliberately absent
		{"", outputMediaTypeRefused},
		{"video/mp4\r\nX-Evil: 1", outputMediaTypeRefused},
	} {
		if got := servableOutputContentType(tc.stored); got != tc.want {
			t.Errorf("servableOutputContentType(%q) = %q, want %q", tc.stored, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Serving
// ---------------------------------------------------------------------------

// seedVideoGen inserts a generation whose single output is a video and writes its
// bytes to disk. Returns (generationID, imageID).
func seedVideoGen(t *testing.T, srv *Server, root, contentType string, data []byte) (int64, int64) {
	t.Helper()
	relPath := "vid-prompt/0-clip.mp4"
	if _, err := writeOutputImage(root, relPath, data, maxOutputImageBytes); err != nil {
		t.Fatalf("write output file: %v", err)
	}
	genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
		WorkflowName: "vid-wf", PromptID: "vid-prompt",
	}, []store.GenerationImage{
		{Idx: 0, RelPath: relPath, Filename: "clip.mp4",
			ContentType: contentType, SizeBytes: int64(len(data))},
	})
	if err != nil {
		t.Fatalf("insert generation: %v", err)
	}
	_, imgs, err := srv.store.GetGeneration(context.Background(), genID)
	if err != nil {
		t.Fatalf("get generation: %v", err)
	}
	return genID, imgs[0].ID
}

// TestServeVideoKeepsTheSecurityHeaders covers the serving contract for video: a
// real playable type, nosniff kept, immutable cache kept, and Range supported (a
// non-faststart mp4 cannot be previewed without it).
func TestServeVideoKeepsTheSecurityHeaders(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:0")
	body := []byte("0123456789ABCDEF")
	_, imgID := seedVideoGen(t, srv, root, "video/mp4", body)
	mux := srv.Handler()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/outputs/img/"+strconv.FormatInt(imgID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("Content-Type = %q, want video/mp4 — a video served as "+
			"application/octet-stream cannot play", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Errorf("Cache-Control = %q, want the immutable cache header", got)
	}
	if rec.Body.String() != string(body) {
		t.Errorf("body = %q", rec.Body.String())
	}

	// Range: a VHS mp4 is not faststart, so preload="metadata" issues a TAIL range
	// request to reach the moov atom. Without 206 support the browser would have to
	// download the whole file to paint a poster frame, and seeking would not work.
	rangeReq := httptest.NewRequest(http.MethodGet, "/outputs/img/"+strconv.FormatInt(imgID, 10), nil)
	rangeReq.Header.Set("Range", "bytes=-4")
	rrec := httptest.NewRecorder()
	mux.ServeHTTP(rrec, rangeReq)
	if rrec.Code != http.StatusPartialContent {
		t.Fatalf("Range request status = %d, want 206 — video preview/seek needs "+
			"Range support (keep http.ServeContent)", rrec.Code)
	}
	if got := rrec.Body.String(); got != "CDEF" {
		t.Errorf("tail range body = %q, want CDEF", got)
	}
}

// TestServeRefusesNonWhitelistedStoredType asserts a row claiming a bogus or
// hostile type is served inert, NOT echoed.
func TestServeRefusesNonWhitelistedStoredType(t *testing.T) {
	for _, stored := range []string{"video/h264-mp4", "text/html", "image/svg+xml"} {
		t.Run(stored, func(t *testing.T) {
			srv, root := newOutputsServer(t, "127.0.0.1:0")
			_, imgID := seedVideoGen(t, srv, root, stored, []byte("BYTES"))
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/outputs/img/"+strconv.FormatInt(imgID, 10), nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			ct := rec.Header().Get("Content-Type")
			if ct != outputMediaTypeRefused {
				t.Errorf("Content-Type = %q, want %q — a stored type outside the "+
					"whitelist must never reach the header", ct, outputMediaTypeRefused)
			}
			if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Error("nosniff must be kept for a refused type — it is what stops the "+
					"browser sniffing the bytes back into something renderable")
			}
		})
	}
}

// TestServeVideoStillRejectsTraversal asserts the containment check is unchanged
// by the video work: a corrupted rel_path must 404, not read outside the root.
func TestServeVideoStillRejectsTraversal(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:0")

	// A file the traversal would reach if containment were broken.
	outside := filepath.Join(filepath.Dir(root), "outside-secret.mp4")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })

	for _, rel := range []string{
		"../outside-secret.mp4",
		"../../outside-secret.mp4",
		"..\\outside-secret.mp4",
		"/etc/passwd",
	} {
		t.Run(rel, func(t *testing.T) {
			genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
				WorkflowName: "evil", PromptID: "evil" + rel,
			}, []store.GenerationImage{
				{Idx: 0, RelPath: rel, Filename: "clip.mp4", ContentType: "video/mp4", SizeBytes: 6},
			})
			if err != nil {
				t.Fatalf("insert: %v", err)
			}
			_, imgs, err := srv.store.GetGeneration(context.Background(), genID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
				"/outputs/img/"+strconv.FormatInt(imgs[0].ID, 10), nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 — rel_path %q escaped the outputs root",
					rec.Code, rel)
			}
			if strings.Contains(rec.Body.String(), "SECRET") {
				t.Fatalf("the traversal READ a file outside the outputs root")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

// videoGen builds a Generation as the LIST queries would return one for a video.
func videoGen(id int64, imgID int64) store.Generation {
	wf := int64(7)
	return store.Generation{
		ID: id, WorkflowID: &wf, WorkflowName: "wan22-base", PromptID: "p",
		Status: store.GenerationStatusReady, ImageCount: 1,
		FirstImageID: imgID, FirstImageContentType: "video/mp4",
	}
}

// TestVideoTileRendersVideoNotBrokenImg covers the gallery/batch tile and the rail
// together: a video entry must render a <video>, must NOT be handed to an <img>
// (which cannot decode an mp4 and shows a broken-image glyph), and must carry the
// ▶ badge.
func TestVideoTileRendersVideoNotBrokenImg(t *testing.T) {
	gen := videoGen(1, 42)
	url := generationImgURL(42)

	surfaces := map[string]string{
		"gallery/batch tile": renderString(t, generationTile(gen)),
		"rail tile":          renderString(t, railTile(railGroup{Rep: gen, Count: 1})),
	}
	for name, out := range surfaces {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(out, "<video") {
				t.Fatalf("a video output must render a <video> element:\n%s", out)
			}
			// The decisive assertion: no <img> pointed at the video bytes.
			if strings.Contains(out, `<img src="`+url) {
				t.Errorf("the video is rendered as an <img>, which cannot decode it "+
					"(broken-image glyph):\n%s", out)
			}
			if !strings.Contains(out, "cm-video-badge") {
				t.Errorf("a video tile must carry the ▶ badge so it reads as video:\n%s", out)
			}
			if !strings.Contains(out, url) {
				t.Errorf("the tile must point at %s:\n%s", url, out)
			}
		})
	}
}

// TestVideoGridDoesNotEagerlyLoadVideos is the bandwidth guard. A masonry page of
// N video tiles must not autoplay or preload the clips.
//
// It renders a FULL page of tiles (not one) because the failure being guarded is
// per-page cost, and it asserts on counts so a single missed attribute is visible.
func TestVideoGridDoesNotEagerlyLoadVideos(t *testing.T) {
	const n = 24
	gens := make([]store.Generation, 0, n)
	for i := 0; i < n; i++ {
		gens = append(gens, videoGen(int64(i+1), int64(100+i)))
	}
	out := renderString(t, generationGrid(gens, nil))

	if got := strings.Count(out, "<video"); got != n {
		t.Fatalf("rendered %d <video> elements, want %d — the fixture does not reach "+
			"the case this test guards", got, n)
	}
	if got := strings.Count(out, `preload="metadata"`); got != n {
		t.Errorf(`preload="metadata" on %d of %d tiles — every grid video must bound `+
			`its fetch to container metadata`, got, n)
	}
	for _, banned := range []string{"autoplay", `preload="auto"`, "<source"} {
		if strings.Contains(out, banned) {
			t.Errorf("a grid of %d videos must not use %q — that streams the whole "+
				"page's clips on load", n, banned)
		}
	}
	// No controls in the grid: the tile is a poster, and its click must reach the
	// detail-page overlay anchor rather than a play button.
	if strings.Contains(out, "controls") {
		t.Errorf("grid tiles must not carry controls (they would swallow the tile click):\n%s", out)
	}
}

// TestGenerationTileMarkupContractSurvivesForImages pins that the image path is
// untouched — batch_gallery_web_test.go depends on this exact string.
func TestGenerationTileMarkupContractSurvivesForImages(t *testing.T) {
	wf := int64(7)
	gen := store.Generation{
		ID: 1, WorkflowID: &wf, WorkflowName: "img-wf", ImageCount: 1,
		FirstImageID: 42, FirstImageContentType: "image/png",
	}
	out := renderString(t, generationTile(gen))
	if !strings.Contains(out, `class="cm-tile-link absolute inset-0 z-10"`) {
		t.Errorf("the pinned overlay-anchor contract changed:\n%s", out)
	}
	if !strings.Contains(out, "<img") || strings.Contains(out, "<video") {
		t.Errorf("an image generation must still render an <img> and no <video>:\n%s", out)
	}
	if strings.Contains(out, "cm-video-badge") {
		t.Errorf("an image tile must not carry the video badge:\n%s", out)
	}
}

// TestGenerationDetailPageRendersVideoAndImage covers the detail surface: a video
// plays in place with controls, an image keeps its lightbox hook, and an
// unrenderable type degrades to an honest download instead of a black <video>.
func TestGenerationDetailPageRendersVideoAndImage(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:0")
	relPath := "mixed/0-clip.mp4"
	if _, err := writeOutputImage(root, relPath, []byte("MP4"), maxOutputImageBytes); err != nil {
		t.Fatalf("write: %v", err)
	}
	genID, err := srv.store.InsertGeneration(context.Background(), &store.Generation{
		WorkflowName: "mixed-wf", PromptID: "mixed",
	}, []store.GenerationImage{
		{Idx: 0, RelPath: relPath, Filename: "clip.mp4", ContentType: "video/mp4", SizeBytes: 3},
		{Idx: 1, RelPath: "mixed/1-a.png", Filename: "a.png", ContentType: "image/png", SizeBytes: 3},
		{Idx: 2, RelPath: "mixed/2-x.mov", Filename: "x.mov", ContentType: outputMediaTypeRefused, SizeBytes: 3},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/outputs/"+strconv.FormatInt(genID, 10), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "<video") || !strings.Contains(body, "controls") {
		t.Errorf("the detail page must PLAY a video (a <video controls>):\n%s", body)
	}
	if !strings.Contains(body, `preload="metadata"`) {
		t.Error(`the detail page's video must still preload="metadata", not the whole clip`)
	}
	if !strings.Contains(body, "cmOpenLightbox") {
		t.Error("an image on the detail page must keep its lightbox hook")
	}
	// The unrenderable third output degrades honestly.
	if !strings.Contains(body, "cannot be previewed") {
		t.Errorf("an output outside the whitelist must say so rather than render a "+
			"black <video>:\n%s", body)
	}
	if !strings.Contains(body, "Download") {
		t.Error("an unrenderable output must still offer its bytes")
	}
}

// TestRunCompleteMessageDoesNotClaimImages pins the terminal message. A successful
// video-only run used to report "no images returned" while an mp4 sat on disk.
func TestRunCompleteMessageDoesNotClaimImages(t *testing.T) {
	srv := &Server{}

	t.Run("a video-only run reports success, not 'no images'", func(t *testing.T) {
		job := &runJob{}
		entry := decodeHistory(t, realVHSGifsOutputs)
		refs := entry.AllImages()
		if len(refs) == 0 {
			t.Fatal("fixture yielded no refs — the test cannot reach the success branch")
		}
		srv.applyItemOutcomeLocked(job, &runResult{PromptID: "p", Images: refs}, nil)
		if job.phase != runPhaseDone {
			t.Fatalf("phase = %q, want done", job.phase)
		}
		if strings.Contains(job.message, "no images") || strings.Contains(job.message, "no outputs") {
			t.Errorf("a run that produced a video must not report an empty result: %q", job.message)
		}
		if job.message != "Run complete." {
			t.Errorf("message = %q, want %q", job.message, "Run complete.")
		}
	})

	t.Run("a genuinely empty run does not name one media kind", func(t *testing.T) {
		job := &runJob{}
		srv.applyItemOutcomeLocked(job, &runResult{PromptID: "p"}, nil)
		if strings.Contains(job.message, "image") {
			t.Errorf("the empty-result message must not claim 'images' — a video run "+
				"lands here too: %q", job.message)
		}
		if job.message != "Run complete (no outputs returned)." {
			t.Errorf("message = %q", job.message)
		}
	})
}
