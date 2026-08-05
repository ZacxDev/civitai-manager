package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/hf"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// A PROBE, not a gate: it prints what a REAL workflow's notes actually contain and
// how each link would be classified, using the production functions. It skips
// unless pointed at a database, so it never runs in CI.
//
//	CM_PROBE_DB=/path/to/a/COPY.db CM_PROBE_WORKFLOW=590 \
//	  go test ./internal/web/ -run TestNoteLinkProbe -v
//
// It exists because the fixtures in note_links_web_test.go are MODELLED on wf590's
// note, and a fixture modelled on reality is not reality — this repo has shipped
// green tests over a fixture that could not reach the case. Running it is how you
// find out that a real note's markdown, or a real author's URL shape, defeats the
// extractor.
//
// 🔴 WHAT IT CANNOT PROVE. It reads the graph and nothing else:
//   - it does NOT say which files are MISSING. That needs a preflight against a
//     live /object_info, so a link it reports may be for a file you already have.
//   - it makes NO network call, so "auto-fetchable" means "the URL is shaped like a
//     HuggingFace /resolve/ URL", never that the repo still has the file, that it
//     is un-gated, or that a content hash exists to pin the bytes against. Those
//     are decided at install time and can still decline.
//   - it reads a COPY, so it answers about the state at copy time.
//
// It refuses the canonical database path: this must run against a copy, so a probe
// can never write to (or lock) the database the app is using.
func TestNoteLinkProbe(t *testing.T) {
	dbPath := os.Getenv("CM_PROBE_DB")
	if dbPath == "" {
		t.Skip("set CM_PROBE_DB to a COPY of the database to run the note-link probe")
	}
	if home, err := os.UserHomeDir(); err == nil {
		canonical := filepath.Join(home, ".config", "civitai-manager", "civitai-manager.db")
		if abs, aerr := filepath.Abs(dbPath); aerr == nil && abs == canonical {
			t.Fatalf("refusing to probe the live database at %s — copy it first", canonical)
		}
	}
	wfID := int64(590)
	if v := os.Getenv("CM_PROBE_WORKFLOW"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("CM_PROBE_WORKFLOW=%q: %v", v, err)
		}
		wfID = n
	}

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	t.Cleanup(func() { _ = st.Close() })

	wf, err := st.GetWorkflow(context.Background(), wfID)
	if err != nil {
		t.Fatalf("load workflow %d: %v", wfID, err)
	}
	t.Logf("workflow %d %q format=%s graph=%d bytes", wf.ID, wf.Name, wf.Format, len(wf.Graph))

	links := comfy.ExtractNoteLinks(wf.Format, json.RawMessage(wf.Graph))
	t.Logf("%d note links", len(links))
	for _, l := range links {
		_, _, _, isHF := hf.ParseResolveURL(l.URL)
		t.Logf("  node=%-6s hf=%-5v base=%-45q %s", l.NodeID, isHF, l.Basename, l.URL)
	}

	// Cross-reference against what the graph LOADS, which is the set a missing file
	// can come from. This is the pairing the feature is built on.
	res, _ := comfy.ExtractResourcesAny(wf.Format, json.RawMessage(wf.Graph))
	t.Logf("%d model references in the graph", len(res))
	for _, ref := range res {
		if m := comfy.NoteLinksMatching(links, ref); len(m) > 0 {
			for _, l := range m {
				_, _, _, isHF := hf.ParseResolveURL(l.URL)
				t.Logf("  MATCH %-45q  hf=%-5v  %s", ref, isHF, l.URL)
			}
		}
	}
}
