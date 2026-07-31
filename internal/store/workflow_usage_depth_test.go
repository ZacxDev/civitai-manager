package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
)

// ===========================================================================
// The SQLITE_MAX_EXPR_DEPTH cliff (audit 🟡-1).
// ===========================================================================
//
// Both usage queries narrow with a chain of OR'd LIKEs, and SQLite parses that
// into a LEFT-DEEP tree. Unchunked, they died with
//
//	SQL logic error: Expression tree is too large (maximum depth 1000)
//
// at MEASURED thresholds of 500 (UnambiguousModelFileBasenames — two OR-operands
// per basename) and 1000 (ListWorkflowsUsingFiles — one). Because
// Server.workflowsUsingModel is fail-soft, the symptom was NOT an error anyone
// would see: a model with that many locally indexed files just lost its
// "Workflows that use this model" section on every render, forever, with a log
// line as the only evidence.
//
// 🔴 THESE TESTS DELIBERATELY ASSERT RESULTS, NOT ABSENCE OF ERROR. "It didn't
// error" is satisfied by a chunked implementation that silently DROPS chunks,
// which would reproduce the original symptom exactly (a quietly empty section)
// while looking fixed. So each case plants known needles spread across chunk
// boundaries — including in the LAST chunk, which is what a drop-the-tail bug
// loses first — and demands every one of them back.

// depthTestN is comfortably past BOTH measured unchunked failure points (500 and
// 1000), so a regression that removes chunking fails here rather than in
// production. It is also several chunks wide at basenameChunk=100, which is what
// makes the cross-chunk assertions below meaningful.
const depthTestN = 1200

// TestUnambiguousBasenamesSurvivesExpressionDepth: >1000 basenames must still
// come back COMPLETE and still honour the ambiguity rule.
func TestUnambiguousBasenamesSurvivesExpressionDepth(t *testing.T) {
	st := usageStore(t)

	// Model 100 owns depthTestN distinct basenames.
	for i := 0; i < depthTestN; i++ {
		seedFile(t, st, fmt.Sprintf("/models/a/f%05d.safetensors", i), 100, 900)
	}
	// A SECOND model claims three of them — one in the first chunk, one in the
	// middle, one in the LAST chunk. All three must be dropped as ambiguous, and
	// the last one is what a chunked loop that stops early would miss.
	// Deliberately NOT multiples of basenameChunk: these indices must be
	// independent of the chunk-start needles asserted below, or the two fixtures
	// collide and the failure is the test's, not the code's.
	ambiguousIdx := []int{7, depthTestN/2 + 3, depthTestN - 1}
	for _, i := range ambiguousIdx {
		seedFile(t, st, fmt.Sprintf("/models/b/f%05d.safetensors", i), 200, 901)
	}

	got, err := st.UnambiguousModelFileBasenames(context.Background(), 100)
	if err != nil {
		t.Fatalf("UnambiguousModelFileBasenames(%d basenames): %v — if this is an "+
			"\"Expression tree is too large\" error, the OR chain is no longer chunked "+
			"(see basenameChunk)", depthTestN, err)
	}

	// COMPLETENESS: exactly the non-ambiguous ones, no more and no fewer. A loop
	// that processed only the first chunk would return far too few here.
	want := depthTestN - len(ambiguousIdx)
	if len(got) != want {
		t.Fatalf("got %d unambiguous basenames, want %d — a chunked implementation "+
			"that drops chunks also 'does not error', which is why this asserts the "+
			"COUNT", len(got), want)
	}
	have := make(map[string]bool, len(got))
	for _, n := range got {
		have[n] = true
	}
	// The ambiguous ones must be ABSENT — including the one in the last chunk.
	for _, i := range ambiguousIdx {
		name := fmt.Sprintf("f%05d.safetensors", i)
		if have[name] {
			t.Errorf("%q is claimed by TWO models and must have been dropped as ambiguous "+
				"(index %d of %d — a chunk boundary bug shows up here first)", name, i, depthTestN)
		}
	}
	// …and a needle from EACH chunk must be present, so a dropped chunk is caught
	// even when the count happens to work out.
	dropped := map[int]bool{}
	for _, i := range ambiguousIdx {
		dropped[i] = true
	}
	for i := 0; i < depthTestN; i += basenameChunk {
		if dropped[i] {
			continue // legitimately absent — asserted above
		}
		name := fmt.Sprintf("f%05d.safetensors", i)
		if !have[name] {
			t.Errorf("basename %q (start of the chunk at offset %d) is missing — that "+
				"chunk was never queried", name, i)
		}
	}
}

// TestListWorkflowsUsingFilesSurvivesExpressionDepth: the same for the workflow
// query, plus the merge/dedupe/order properties chunking introduces.
func TestListWorkflowsUsingFilesSurvivesExpressionDepth(t *testing.T) {
	st := usageStore(t)

	names := make([]string, depthTestN)
	for i := range names {
		names[i] = fmt.Sprintf("f%05d.safetensors", i)
	}

	// One workflow per chunk boundary, each referencing a basename from THAT
	// chunk — so every chunk must be queried for the set to come back whole.
	wantIDs := map[int64]string{}
	for i := 0; i < depthTestN; i += basenameChunk {
		id := seedUsageWF(t, st, fmt.Sprintf("wf-at-%d", i), []string{names[i]}, 0)
		wantIDs[id] = names[i]
	}
	// A workflow spanning TWO different chunks: it must appear ONCE, with BOTH
	// matched resources — the property that breaks if the exact-compare gate is
	// scoped to the chunk instead of the full basename set.
	spanID := seedUsageWF(t, st, "spans-chunks", []string{names[3], names[depthTestN-3]}, 0)
	// A decoy that no basename matches.
	seedUsageWF(t, st, "decoy", []string{"nothing-like-this.safetensors"}, 0)

	got, err := st.ListWorkflowsUsingFiles(context.Background(), names)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingFiles(%d basenames): %v — an \"Expression tree is "+
			"too large\" error here means the OR chain is no longer chunked", depthTestN, err)
	}

	byID := map[int64]WorkflowUsage{}
	for _, u := range got {
		if _, dup := byID[u.ID]; dup {
			t.Errorf("workflow %d returned TWICE — a workflow matched by several chunks "+
				"must be deduped by id", u.ID)
		}
		byID[u.ID] = u
	}

	// Every per-chunk needle came back.
	for id, name := range wantIDs {
		u, ok := byID[id]
		if !ok {
			t.Errorf("workflow %d (references %q) is missing — its chunk was never queried", id, name)
			continue
		}
		if len(u.Matched) != 1 || u.Matched[0] != name {
			t.Errorf("workflow %d Matched = %v, want [%q]", id, u.Matched, name)
		}
	}

	// The cross-chunk workflow: ONE row carrying BOTH of its matched resources, in
	// the graph's own order. This is what fails if a chunk's rows are gated against
	// only that chunk's basenames.
	u, ok := byID[spanID]
	if !ok {
		t.Fatalf("the workflow referencing files from two different chunks is missing entirely")
	}
	if len(u.Matched) != 2 || u.Matched[0] != names[3] || u.Matched[1] != names[depthTestN-3] {
		t.Errorf("cross-chunk workflow Matched = %v, want both %q and %q in resource order — "+
			"the exact-compare gate must use the FULL basename set, not the chunk's",
			u.Matched, names[3], names[depthTestN-3])
	}

	// The decoy is still rejected by the exact compare.
	for _, x := range got {
		if x.Name == "decoy" {
			t.Error("the decoy workflow matched nothing and must not be returned")
		}
	}

	// Newest-first ordering holds ACROSS chunks (a per-chunk sort would interleave).
	ids := make([]int64, len(got))
	for i, x := range got {
		ids[i] = x.ID
	}
	if !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] > ids[j] }) {
		t.Errorf("results are not newest-first across chunks: %v", ids)
	}
}

// TestChunkStringsCoversEveryElement pins the splitter itself: a chunker that
// loses or duplicates an element is the exact silent-drop failure the tests above
// are shaped to catch, so it is worth asserting directly.
func TestChunkStringsCoversEveryElement(t *testing.T) {
	for _, n := range []int{0, 1, 5, 99, 100, 101, 250, 1200} {
		in := make([]string, n)
		for i := range in {
			in[i] = fmt.Sprintf("x%04d", i)
		}
		var flat []string
		for _, c := range chunkStrings(in, basenameChunk) {
			if len(c) > basenameChunk {
				t.Fatalf("n=%d: chunk of %d exceeds basenameChunk=%d", n, len(c), basenameChunk)
			}
			if len(c) == 0 {
				t.Fatalf("n=%d: produced an empty chunk", n)
			}
			flat = append(flat, c...)
		}
		if len(flat) != n {
			t.Fatalf("n=%d: chunks flatten to %d elements", n, len(flat))
		}
		for i := range in {
			if flat[i] != in[i] {
				t.Fatalf("n=%d: element %d = %q, want %q (order must be preserved)", n, i, flat[i], in[i])
			}
		}
	}
	// A non-positive chunk size must not loop forever — it degrades to one chunk.
	if got := chunkStrings([]string{"a", "b"}, 0); len(got) != 1 || len(got[0]) != 2 {
		t.Errorf("chunkStrings(_, 0) = %v, want a single chunk", got)
	}
}

// TestBasenameChunkKeepsHeadroom guards the CONSTANT, not the code. The limit is
// on expression DEPTH, and the worst statement emits TWO OR-operands per
// basename, so a chunk sized near the measured cliff would let one extra clause
// per basename silently re-introduce the bug.
func TestBasenameChunkKeepsHeadroom(t *testing.T) {
	const sqliteMaxExprDepth = 1000
	const operandsPerBasename = 2 // UnambiguousModelFileBasenames, the worse of the two

	worstDepth := basenameChunk * operandsPerBasename
	if worstDepth*2 > sqliteMaxExprDepth {
		t.Errorf("basenameChunk=%d gives a worst-case OR depth of %d against SQLite's "+
			"limit of %d — that is under 2x headroom. The limit is on DEPTH, so adding "+
			"one clause per basename would push it over; keep the chunk small enough "+
			"that a future clause cannot re-break it.",
			basenameChunk, worstDepth, sqliteMaxExprDepth)
	}
}
