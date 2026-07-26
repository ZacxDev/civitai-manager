package store

import (
	"context"
	"testing"
)

// TestGraphHashCanonicalizes proves the content hash ignores trivial formatting
// and key-order differences (so a re-import of the "same" workflow dedups) while
// still distinguishing genuinely different graphs.
func TestGraphHashCanonicalizes(t *testing.T) {
	a := `{"1":{"class_type":"KSampler","inputs":{"seed":1,"steps":20}}}`
	// Same graph, reordered keys + whitespace.
	b := "{\n  \"1\": {\n    \"inputs\": { \"steps\": 20, \"seed\": 1 },\n    \"class_type\": \"KSampler\"\n  }\n}"
	c := `{"1":{"class_type":"KSampler","inputs":{"seed":2,"steps":20}}}`

	ha, hb, hc := GraphHash(a), GraphHash(b), GraphHash(c)
	if ha == "" {
		t.Fatal("GraphHash returned empty for a valid graph")
	}
	if ha != hb {
		t.Errorf("canonicalization failed: formatting/key-order differences yielded different hashes\n a=%s\n b=%s", ha, hb)
	}
	if ha == hc {
		t.Error("distinct graphs must not share a hash")
	}
	if GraphHash("") != "" || GraphHash("   ") != "" {
		t.Error("empty/blank graph must hash to empty string")
	}
	// A non-JSON graph still hashes deterministically (fallback to raw bytes).
	if GraphHash("not json") == "" || GraphHash("not json") != GraphHash("not json") {
		t.Error("non-JSON graph must hash deterministically to a non-empty value")
	}
}

// TestInsertWorkflowPopulatesGraphHash proves every insert path stamps graph_hash
// from the graph when the caller leaves it blank, and that it round-trips on read.
func TestInsertWorkflowPopulatesGraphHash(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	graph := `{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"m.safetensors"}}}`
	id, err := st.InsertWorkflow(ctx, &Workflow{
		Name: "wf", Format: WorkflowFormatAPI, Graph: graph, Source: WorkflowSourceCivitai,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetWorkflow(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.GraphHash == "" {
		t.Fatal("InsertWorkflow did not populate graph_hash")
	}
	if got.GraphHash != GraphHash(graph) {
		t.Errorf("stored graph_hash %q != GraphHash(graph) %q", got.GraphHash, GraphHash(graph))
	}
}

// TestWorkflowExistsByGraphHash proves the dedup lookup: present hashes report
// true, unknown/blank hashes report false (blank must never match legacy NULLs).
func TestWorkflowExistsByGraphHash(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	graph := `{"1":{"class_type":"KSampler","inputs":{"seed":1}}}`
	hash := GraphHash(graph)

	if ok, err := st.WorkflowExistsByGraphHash(ctx, hash); err != nil || ok {
		t.Fatalf("hash should not exist before insert (ok=%v err=%v)", ok, err)
	}
	if _, err := st.InsertWorkflow(ctx, &Workflow{
		Name: "wf", Format: WorkflowFormatAPI, Graph: graph, Source: WorkflowSourceCivitai,
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.WorkflowExistsByGraphHash(ctx, hash); err != nil || !ok {
		t.Fatalf("hash should exist after insert (ok=%v err=%v)", ok, err)
	}
	if ok, err := st.WorkflowExistsByGraphHash(ctx, "deadbeef"); err != nil || ok {
		t.Fatalf("unknown hash must report false (ok=%v err=%v)", ok, err)
	}
	if ok, err := st.WorkflowExistsByGraphHash(ctx, ""); err != nil || ok {
		t.Fatalf("blank hash must report false (ok=%v err=%v)", ok, err)
	}
}
