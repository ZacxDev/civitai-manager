package library

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// A WINDOWS-AUTHORED REFERENCE MUST STILL AUTO-LINK.
//
// autoLink's candidates are GRAPH-DERIVED — comfy.PrimaryCheckpoint over the
// stored graph, then the extracted resources — so a graph saved on Windows offers
// `zimage\zit_sda_v1.safetensors`. It used to take that basename with
// filepath.Base, which is a NO-OP for backslashes on Linux, and handed the whole
// string to FindVersionByFileName. That store method gates on
// strings.ToLower(filepath.Base(local_files.path)) — a HOST basename, which on
// this platform can never contain a backslash — so the compare could not match and
// the scanned workflow silently failed to link to its CivitAI model/version.
//
// The fake below reproduces the store's real gate rather than approximating it, so
// the test fails for the same reason production did.
// ─────────────────────────────────────────────────────────────────────────────

// fakeAutoLinkStore implements WorkflowScanStore. Only FindVersionByFileName is
// exercised; it records every basename it was asked for so a test can assert the
// QUESTION, not just the answer.
type fakeAutoLinkStore struct {
	// localPaths are rows of local_files.path, as they exist on THIS host.
	localPaths map[string][2]int // host path -> {modelID, versionID}
	asked      []string
}

func (f *fakeAutoLinkStore) GetWorkflowByPath(context.Context, string) (*store.Workflow, error) {
	return nil, nil
}

func (f *fakeAutoLinkStore) UpsertWorkflowByPath(context.Context, *store.Workflow) (int64, bool, error) {
	return 0, false, nil
}

// FindVersionByFileName mirrors store.FindVersionByFileName's real gate: the
// caller-supplied basename is compared, case-insensitively, against the HOST
// basename of each local_files row.
func (f *fakeAutoLinkStore) FindVersionByFileName(_ context.Context, basename string) (*int, *int, bool, error) {
	f.asked = append(f.asked, basename)
	low := strings.ToLower(strings.TrimSpace(basename))
	if low == "" {
		return nil, nil, false, nil
	}
	for path, ids := range f.localPaths {
		if strings.ToLower(filepath.Base(path)) == low {
			m, v := ids[0], ids[1]
			return &m, &v, true, nil
		}
	}
	return nil, nil, false, nil
}

const (
	autoLinkBare      = "zit_sda_v1.safetensors"
	autoLinkLocalPath = "/models/checkpoints/zit_sda_v1.safetensors"
)

// autoLinkRefCases is the three-way table: the bug, then its two positive
// controls.
var autoLinkRefCases = []struct {
	name string
	ref  string
}{
	{"backslash (the bug)", `zimage\` + autoLinkBare},
	{"forward slash (positive control)", "zimage/" + autoLinkBare},
	{"bare basename (positive control)", autoLinkBare},
}

func ckptGraph(t *testing.T, ref string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"4": map[string]any{
			"class_type": "CheckpointLoaderSimple",
			"inputs":     map[string]any{"ckpt_name": ref},
		},
	})
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	return json.RawMessage(b)
}

func newAutoLinkScanner() (*WorkflowScanner, *fakeAutoLinkStore) {
	st := &fakeAutoLinkStore{localPaths: map[string][2]int{autoLinkLocalPath: {10, 20}}}
	return NewWorkflowScanner(st, nil), st
}

// TestAutoLinkResolvesAWindowsAuthoredCheckpoint drives autoLink through the
// PRIMARY-CHECKPOINT candidate.
func TestAutoLinkResolvesAWindowsAuthoredCheckpoint(t *testing.T) {
	// FIXTURE PRECONDITIONS. Without these a green could mean "the fake matches
	// anything" or "the graph yielded no candidate at all".
	{
		_, st := newAutoLinkScanner()
		if _, _, ok, _ := st.FindVersionByFileName(context.Background(), autoLinkBare); !ok {
			t.Fatal("fixture: the fake store does not hold the bare basename")
		}
		if _, _, ok, _ := st.FindVersionByFileName(context.Background(), `zimage\`+autoLinkBare); ok {
			t.Fatal("fixture: the fake store answers to the RAW backslash reference — " +
				"it does not reproduce the store's host-basename gate, so the fold is untested")
		}
	}
	for _, tc := range autoLinkRefCases {
		if got, ok := comfy.PrimaryCheckpoint(comfy.FormatAPI, ckptGraph(t, tc.ref)); !ok || got != tc.ref {
			t.Fatalf("fixture: PrimaryCheckpoint(%q) = (%q,%v); the candidate never reaches autoLink", tc.ref, got, ok)
		}
	}

	for _, tc := range autoLinkRefCases {
		t.Run(tc.name, func(t *testing.T) {
			ws, st := newAutoLinkScanner()
			modelID, versionID, linked := ws.autoLink(context.Background(), comfy.FormatAPI, ckptGraph(t, tc.ref), nil)
			if !linked {
				t.Fatalf("ref %q did NOT auto-link; store was asked %q", tc.ref, st.asked)
			}
			if modelID == nil || *modelID != 10 || versionID == nil || *versionID != 20 {
				t.Fatalf("ref %q linked to the wrong ids: model=%v version=%v", tc.ref, modelID, versionID)
			}
			// The QUESTION, not just the answer: the store is keyed by host basename,
			// so that is what it must be asked for.
			if len(st.asked) == 0 {
				t.Fatal("the store was never consulted")
			}
			if st.asked[0] != autoLinkBare {
				t.Fatalf("ref %q: store was first asked %q, want the basename %q", tc.ref, st.asked[0], autoLinkBare)
			}
		})
	}

	// NEGATIVE CONTROL: a graph referencing a file the library does not hold must
	// still fail to link, backslash and all.
	ws, _ := newAutoLinkScanner()
	if _, _, linked := ws.autoLink(context.Background(), comfy.FormatAPI,
		ckptGraph(t, `zimage\definitely-not-installed.safetensors`), nil); linked {
		t.Fatal("negative control: an absent file auto-linked")
	}
}

// TestAutoLinkResolvesAWindowsAuthoredResource drives the OTHER candidate source —
// the extracted resources list — with a graph that offers no primary checkpoint,
// so the resource branch is the only one that can answer.
func TestAutoLinkResolvesAWindowsAuthoredResource(t *testing.T) {
	// A LoraLoader graph: ExtractResources finds it, PrimaryCheckpoint does not.
	graph := json.RawMessage(`{"7":{"class_type":"LoraLoader","inputs":{"lora_name":"x.safetensors"}}}`)
	if _, ok := comfy.PrimaryCheckpoint(comfy.FormatAPI, graph); ok {
		t.Fatal("fixture: this graph yields a primary checkpoint, so the resource branch is not isolated")
	}

	for _, tc := range autoLinkRefCases {
		t.Run(tc.name, func(t *testing.T) {
			ws, st := newAutoLinkScanner()
			modelID, _, linked := ws.autoLink(context.Background(), comfy.FormatAPI, graph, []string{tc.ref})
			if !linked {
				t.Fatalf("resource %q did NOT auto-link; store was asked %q", tc.ref, st.asked)
			}
			if modelID == nil || *modelID != 10 {
				t.Fatalf("resource %q linked to the wrong model: %v", tc.ref, modelID)
			}
			if len(st.asked) == 0 || st.asked[0] != autoLinkBare {
				t.Fatalf("resource %q: store asked %q, want first %q", tc.ref, st.asked, autoLinkBare)
			}
		})
	}

	ws, _ := newAutoLinkScanner()
	if _, _, linked := ws.autoLink(context.Background(), comfy.FormatAPI, graph,
		[]string{`zimage\definitely-not-installed.safetensors`}); linked {
		t.Fatal("negative control: an absent resource auto-linked")
	}
}

// TestAutoLinkDedupesAcrossSpellings pins that the tried-set is keyed by the FOLDED
// basename: three spellings of one file are ONE question to the store, not three.
// With filepath.Base they were three distinct keys, so a graph referencing the same
// file several ways issued a redundant query per spelling.
func TestAutoLinkDedupesAcrossSpellings(t *testing.T) {
	ws, st := newAutoLinkScanner()
	// Nothing in the library, so every candidate is tried and none short-circuits.
	st.localPaths = map[string][2]int{}
	refs := []string{`a\dup.safetensors`, "b/dup.safetensors", "dup.safetensors"}
	if _, _, linked := ws.autoLink(context.Background(), comfy.FormatAPI,
		json.RawMessage(`{}`), refs); linked {
		t.Fatal("fixture: nothing should link with an empty library")
	}
	if len(st.asked) != 1 {
		t.Fatalf("the store was asked %d times %q for three spellings of ONE file; want 1", len(st.asked), st.asked)
	}
	if st.asked[0] != "dup.safetensors" {
		t.Fatalf("store was asked %q, want %q", st.asked[0], "dup.safetensors")
	}
}
