package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE PNG IMPORT PATH MUST CLASSIFY, NOT TRUST THE CHUNK KEYWORD.
//
// handleWorkflowImportPNG stored the `prompt` tEXt chunk as format=api without
// calling comfy.DetectFormat; the only check was comfy's looksLikeJSON, which
// inspects ONE byte. So a truncated graph, a {"prompt": <graph>} wrapper, or a
// UI-shaped graph under that keyword all became rows this app labelled api.
//
// 🔴 EVERY ASSERTION BELOW READS THE STORED ROW — the `format` column and the
// `graph` column — never a flash string, a log line or a rendered word. The
// invariant is a RELATIONSHIP between two stored values, and pngImportCases
// enforces it on every case at once via storedFormatMatchesStoredGraph: whatever
// row an import writes, DetectFormat over that row's own graph must return that
// row's own format. A future case added to the table inherits the check.
//
// Regression baseline: red at 52cb872 (the commit before the fix), green at HEAD,
// with `go build ./...` passing under the revert — so the red is an assertion
// failure and not a compiler-caught false red. The two "unchanged" cases are the
// POSITIVE CONTROL: they are green on both sides, which is what proves the harness
// can observe a successful import at all rather than merely counting zeros.
// ─────────────────────────────────────────────────────────────────────────────

// pngUIGraph is a UI-format (editor "Save") graph: a top-level `nodes` array. Its
// widget value is a model filename so the resource assertion can tell "extracted as
// a ui graph" from "extracted as an api graph" (the api extractor looks at
// `inputs`, finds no loader node here, and would yield nothing).
const pngUIGraph = `{"nodes":[{"id":1,"type":"CheckpointLoaderSimple",` +
	`"widgets_values":["ui_only.safetensors"]}],"links":[]}`

// pngTruncatedGraph is cut off mid-object. It starts with `{`, so looksLikeJSON
// passes it through as a candidate — which is exactly the pre-filter's limit and
// the reason a real parse has to happen downstream.
const pngTruncatedGraph = `{"1":{"class_type":"KSampler","inputs":{`

// pngWrappedGraph is the realistic corruption: a real api graph nested under a
// "prompt" key. It is valid JSON and decodes into a one-entry node map, so nothing
// short of DetectFormat's class_type rule rejects it.
const pngWrappedGraph = `{"prompt":{"4":{"class_type":"CheckpointLoaderSimple",` +
	`"inputs":{"ckpt_name":"present.safetensors"}}}}`

// buildPNGWithTexts assembles a minimal valid PNG carrying the given tEXt chunks in
// order. Separate from buildTestPNG (one chunk only) because the fall-through case
// needs a PNG with BOTH a `prompt` and a `workflow` chunk.
func buildPNGWithTexts(pairs ...[2]string) []byte {
	chunk := func(typ string, data []byte) []byte {
		var b bytes.Buffer
		_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		_ = binary.Write(&b, binary.BigEndian, crc.Sum32())
		return b.Bytes()
	}
	var b bytes.Buffer
	b.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:4], 1)
	binary.BigEndian.PutUint32(ihdr[4:8], 1)
	ihdr[8], ihdr[9] = 8, 6
	b.Write(chunk("IHDR", ihdr))
	for _, kv := range pairs {
		text := append([]byte(kv[0]), 0)
		text = append(text, []byte(kv[1])...)
		b.Write(chunk("tEXt", text))
	}
	b.Write(chunk("IEND", nil))
	return b.Bytes()
}

// postImportPNG uploads png to /workflows/import-png with a valid CSRF token.
func postImportPNG(srv *Server, filename string, png []byte) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf_token", srv.csrf)
	fw, _ := mw.CreateFormFile("png", filename)
	_, _ = fw.Write(png)
	_ = mw.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/workflows/import-png", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// pngImportCase pins ONE upload against the row it may (or may not) create.
//
// wantFormat == "" means NO row at all is acceptable — the fail-closed refusal.
// wantGraph names WHICH chunk had to win, so a case cannot pass by storing the
// other one.
type pngImportCase struct {
	name  string
	texts [][2]string

	// Preconditions, asserted before the outcome so a fixture that cannot reach the
	// branch under test fails LOUDLY instead of passing quietly. wantExtractedAPI /
	// wantExtractedUI are what comfy.ExtractFromPNG must hand the handler — i.e.
	// which candidate slots are populated — and wantDetected is what DetectFormat
	// says about the chunk that is supposed to win ("" = it must reject it).
	wantExtractedAPI bool
	wantExtractedUI  bool
	probeGraph       string
	wantDetected     string

	wantFormat    string
	wantGraph     string
	wantResources []string
	regression    bool // false = unchanged-behaviour positive control
}

var pngImportCases = []pngImportCase{
	// 🔴 THE DEFECT. A UI graph under the `prompt` keyword: ExtractFromPNG puts it in
	// the API slot (keyword-driven), the old handler stored it as api on sight.
	{
		name:             "ui graph under the prompt keyword",
		texts:            [][2]string{{"prompt", pngUIGraph}},
		wantExtractedAPI: true,
		probeGraph:       pngUIGraph,
		wantDetected:     comfy.FormatUI,
		wantFormat:       store.WorkflowFormatUI,
		wantGraph:        pngUIGraph,
		// Keyed off the DETECTED format: the ui extractor reads widgets_values. The
		// api extractor would return nothing here, so a non-empty list is positive
		// evidence the ui path ran.
		wantResources: []string{"ui_only.safetensors"},
		regression:    true,
	},
	// 🔴 Truncated: valid first byte, unparseable body. No row may exist afterwards.
	{
		name:             "truncated graph under the prompt keyword",
		texts:            [][2]string{{"prompt", pngTruncatedGraph}},
		wantExtractedAPI: true,
		probeGraph:       pngTruncatedGraph,
		wantDetected:     "",
		wantFormat:       "",
		regression:       true,
	},
	// 🔴 Parses, decodes to a one-entry map, and is still not a graph — the wrapper
	// case the run-gate table already documents as reaching production this way.
	{
		name:             "api graph under a prompt wrapper",
		texts:            [][2]string{{"prompt", pngWrappedGraph}},
		wantExtractedAPI: true,
		probeGraph:       pngWrappedGraph,
		wantDetected:     "",
		wantFormat:       "",
		regression:       true,
	},
	// The fall-through the fix adds: the good `workflow` chunk is no longer dropped
	// on the floor because the `prompt` chunk beside it is corrupt.
	{
		name:             "truncated prompt beside an intact workflow chunk",
		texts:            [][2]string{{"prompt", pngTruncatedGraph}, {"workflow", pngUIGraph}},
		wantExtractedAPI: true,
		wantExtractedUI:  true,
		probeGraph:       pngUIGraph,
		wantDetected:     comfy.FormatUI,
		wantFormat:       store.WorkflowFormatUI,
		wantGraph:        pngUIGraph,
		wantResources:    []string{"ui_only.safetensors"},
		regression:       true,
	},
	// The mirror of the defect on the other keyword: an api graph saved under
	// `workflow` used to be stored as ui.
	{
		name:            "api graph under the workflow keyword",
		texts:           [][2]string{{"workflow", testAPIGraph}},
		wantExtractedUI: true,
		probeGraph:      testAPIGraph,
		wantDetected:    comfy.FormatAPI,
		wantFormat:      store.WorkflowFormatAPI,
		wantGraph:       testAPIGraph,
		wantResources:   []string{"sdxl.safetensors"},
		regression:      true,
	},
	// POSITIVE CONTROL. A genuine api graph under `prompt` must import EXACTLY as it
	// did before — same format, same graph, same resources. Green on both sides of
	// the revert: without it, every assertion above could be satisfied by an import
	// path that had simply stopped working.
	{
		name:             "api graph under the prompt keyword (unchanged)",
		texts:            [][2]string{{"prompt", testAPIGraph}},
		wantExtractedAPI: true,
		probeGraph:       testAPIGraph,
		wantDetected:     comfy.FormatAPI,
		wantFormat:       store.WorkflowFormatAPI,
		wantGraph:        testAPIGraph,
		wantResources:    []string{"sdxl.safetensors"},
	},
	// POSITIVE CONTROL, ui half. A genuine ui graph under `workflow` — the branch the
	// old code got right by coincidence of the keyword matching the shape.
	{
		name:            "ui graph under the workflow keyword (unchanged)",
		texts:           [][2]string{{"workflow", pngUIGraph}},
		wantExtractedUI: true,
		probeGraph:      pngUIGraph,
		wantDetected:    comfy.FormatUI,
		wantFormat:      store.WorkflowFormatUI,
		wantGraph:       pngUIGraph,
		wantResources:   []string{"ui_only.safetensors"},
	},
}

// TestWorkflowImportPNGStoresTheDetectedFormat runs the table. Preconditions first,
// then the stored row.
func TestWorkflowImportPNGStoresTheDetectedFormat(t *testing.T) {
	for _, tc := range pngImportCases {
		t.Run(tc.name, func(t *testing.T) {
			png := buildPNGWithTexts(tc.texts...)

			// ── PRECONDITION 1: the extractor really hands the handler the candidate
			// slots this case is about. A fixture whose `prompt` chunk never reaches
			// the API slot could not exercise the mislabelling at all.
			ex, err := comfy.ExtractFromPNG(bytes.NewReader(png))
			if err != nil {
				t.Fatalf("precondition: ExtractFromPNG = %v, want no error", err)
			}
			if got := ex.APIGraph != nil; got != tc.wantExtractedAPI {
				t.Fatalf("precondition: APIGraph populated = %v, want %v", got, tc.wantExtractedAPI)
			}
			if got := ex.UIGraph != nil; got != tc.wantExtractedUI {
				t.Fatalf("precondition: UIGraph populated = %v, want %v", got, tc.wantExtractedUI)
			}

			// ── PRECONDITION 2: DetectFormat genuinely disagrees with the keyword (or
			// genuinely rejects). If the fixture's graph classified the way the chunk
			// name implies, the case would prove nothing about classification.
			gotFmt, derr := comfy.DetectFormat([]byte(tc.probeGraph))
			switch tc.wantDetected {
			case "":
				if !errors.Is(derr, comfy.ErrUnknownFormat) {
					t.Fatalf("precondition: DetectFormat(probe) = (%q, %v), want ErrUnknownFormat",
						gotFmt, derr)
				}
			default:
				if derr != nil || gotFmt != tc.wantDetected {
					t.Fatalf("precondition: DetectFormat(probe) = (%q, %v), want %q",
						gotFmt, derr, tc.wantDetected)
				}
			}

			srv := newWorkflowServer(t)
			rec := postImportPNG(srv, "shot.png", png)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
			}

			wfs, lerr := srv.store.ListWorkflows(context.Background())
			if lerr != nil {
				t.Fatalf("list workflows: %v", lerr)
			}

			// ── The refusal case: NO row, of ANY format. Asserting "not api" would let
			// a row stored under some third label pass.
			if tc.wantFormat == "" {
				if len(wfs) != 0 {
					t.Fatalf("unclassifiable PNG stored %d row(s): %+v", len(wfs), wfs)
				}
				if loc := rec.Header().Get("Location"); !strings.Contains(loc, "level=error") {
					t.Errorf("refusal should carry an error flash, got %q", loc)
				}
				return
			}

			if len(wfs) != 1 {
				t.Fatalf("expected exactly 1 workflow, got %d", len(wfs))
			}
			got := wfs[0]
			if got.Format != tc.wantFormat {
				t.Errorf("stored format = %q, want %q", got.Format, tc.wantFormat)
			}
			if got.Graph != tc.wantGraph {
				t.Errorf("stored graph = %q, want the %q chunk's graph %q",
					got.Graph, tc.wantFormat, tc.wantGraph)
			}
			if got.Source != store.WorkflowSourceExtractedPNG {
				t.Errorf("stored source = %q, want %q", got.Source, store.WorkflowSourceExtractedPNG)
			}
			if !equalStrings(got.Resources, tc.wantResources) {
				t.Errorf("stored resources = %v, want %v", got.Resources, tc.wantResources)
			}
			storedFormatMatchesStoredGraph(t, got)
		})
	}
}

// storedFormatMatchesStoredGraph is the invariant the whole table exists to pin,
// stated as a RELATIONSHIP between two stored columns rather than as a list of
// expected spellings: whatever a PNG import writes, re-classifying that row's own
// graph must return that row's own format. Any future mislabelling — a new keyword,
// a new chunk source, a new default — fails here without anyone remembering to add
// a case for it.
func storedFormatMatchesStoredGraph(t *testing.T, wf store.Workflow) {
	t.Helper()
	f, err := comfy.DetectFormat([]byte(wf.Graph))
	if err != nil {
		t.Fatalf("stored row %d claims format %q but DetectFormat rejects its own graph: %v",
			wf.ID, wf.Format, err)
	}
	if f != wf.Format {
		t.Fatalf("stored row %d: format column %q disagrees with DetectFormat(graph) = %q",
			wf.ID, wf.Format, f)
	}
}

// TestWorkflowImportPNGRefusesAnUnclassifiableGraphOutright is the non-table half of
// the fail-closed decision: refusing must not be a silent 500 or a swallowed error,
// and it must leave the library exactly as it found it. Uses a PNG whose ONLY comfy
// chunk is garbage — no `parameters` chunk, so this cannot be satisfied by the
// pre-existing A1111 branch (which would be a different code path returning the
// same "no row" observable).
func TestWorkflowImportPNGRefusesAnUnclassifiableGraphOutright(t *testing.T) {
	srv := newWorkflowServer(t)

	// Seed a good workflow first: the refusal must leave EXISTING rows untouched, and
	// a bare "count == 0" cannot tell "refused" from "wiped".
	if _, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "pre-existing", Format: store.WorkflowFormatAPI, Graph: testAPIGraph,
		Source: store.WorkflowSourceImported,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	png := buildPNGWithTexts([2]string{"prompt", pngTruncatedGraph})
	ex, err := comfy.ExtractFromPNG(bytes.NewReader(png))
	if err != nil || ex.APIGraph == nil {
		t.Fatalf("precondition: ExtractFromPNG = (%+v, %v), want a populated APIGraph", ex, err)
	}
	if ex.IsA1111 {
		t.Fatalf("precondition: fixture must not take the A1111 branch")
	}

	rec := postImportPNG(srv, "broken.png", png)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "level=error") {
		t.Errorf("refusal should carry an error flash, got %q", loc)
	}

	wfs, lerr := srv.store.ListWorkflows(context.Background())
	if lerr != nil {
		t.Fatalf("list workflows: %v", lerr)
	}
	if len(wfs) != 1 {
		t.Fatalf("expected only the pre-existing row, got %d: %+v", len(wfs), wfs)
	}
	if wfs[0].Name != "pre-existing" {
		t.Fatalf("refused import replaced the library: %+v", wfs[0])
	}
}

// equalStrings compares two string slices, treating nil and empty as equal.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
