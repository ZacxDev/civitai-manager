package store

import (
	"context"
	"testing"
)

func usageStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func iptr(v int) *int { return &v }

// seedFile indexes one local file linked to a civitai model/version.
func seedFile(t *testing.T, st *Store, path string, modelID, versionID int) {
	t.Helper()
	lf := LocalFile{Path: path, SHA256: "sha-" + path, Kind: LocalKindModel, Status: LocalStatusMatched}
	if modelID > 0 {
		lf.ModelID = iptr(modelID)
		lf.VersionID = iptr(versionID)
	} else {
		lf.Status = LocalStatusUnmatched
	}
	if err := st.UpsertLocalFile(lf); err != nil {
		t.Fatalf("upsert local file %q: %v", path, err)
	}
}

// seedUsageWF stores a workflow with the given referenced resources and an
// optional imported-from model linkage.
func seedUsageWF(t *testing.T, st *Store, name string, resources []string, fromModel int) int64 {
	t.Helper()
	wf := &Workflow{
		Name: name, Format: WorkflowFormatAPI, Graph: `{"n":"` + name + `"}`,
		Source: "authored", Resources: resources,
	}
	if fromModel > 0 {
		wf.ModelID = iptr(fromModel)
	}
	id, err := st.InsertWorkflow(context.Background(), wf)
	if err != nil {
		t.Fatalf("insert workflow %q: %v", name, err)
	}
	return id
}

func TestResourceBasename(t *testing.T) {
	cases := map[string]string{
		"waiNSFW_v140.safetensors":              "waiNSFW_v140.safetensors",
		"seg-a/waiNSFW_v140.safetensors":        "waiNSFW_v140.safetensors",
		`seg-a\waiNSFW_v140.safetensors`:        "waiNSFW_v140.safetensors",
		`C:\models\loras\x.safetensors`:         "x.safetensors",
		"  spaced/name.pt  ":                    "name.pt",
		"":                                      "",
		"   ":                                   "",
		"deep/nested/path/to/model.safetensors": "model.safetensors",
	}
	for in, want := range cases {
		if got := ResourceBasename(in); got != want {
			t.Errorf("ResourceBasename(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWorkflowUsesModelFileNotJustName is the core of the matching rule: a
// workflow REFERENCING one of a model's local files is a use; one that merely
// shares a NAME with the model is not.
func TestWorkflowUsesModelFileNotJustName(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/loras/waiNSFW_v140.safetensors", 100, 900)

	uses := seedUsageWF(t, st, "uses it", []string{"seg-a/waiNSFW_v140.safetensors", "vae.pt"}, 0)
	// Named AFTER the model but referencing something else entirely. The model's
	// NAME appears nowhere in the relation — only its files do.
	seedUsageWF(t, st, "waiNSFW_v140 style pack", []string{"someOther.safetensors"}, 0)
	// No resources at all.
	seedUsageWF(t, st, "bare", nil, 0)

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingModel: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d workflows, want exactly the one that references the file: %+v", len(got), got)
	}
	if got[0].ID != uses {
		t.Errorf("matched workflow id = %d, want %d", got[0].ID, uses)
	}
	// The claim names the file it is based on, folder prefix and all.
	if len(got[0].Matched) != 1 || got[0].Matched[0] != "seg-a/waiNSFW_v140.safetensors" {
		t.Errorf("Matched = %v, want the referencing entry verbatim", got[0].Matched)
	}
	if got[0].ModelID != nil {
		t.Errorf("a USES match must not invent an imported-from linkage; ModelID = %v", *got[0].ModelID)
	}
}

// TestWorkflowUsageIsCaseInsensitiveAndStripsFolders documents the two
// normalizations the rule applies.
func TestWorkflowUsageIsCaseInsensitiveAndStripsFolders(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/loras/MyLoRA_V2.safetensors", 100, 900)

	for _, ref := range []string{
		"mylora_v2.safetensors",       // lowercased
		"MYLORA_V2.SAFETENSORS",       // uppercased
		"loras/MyLoRA_V2.safetensors", // forward-slash prefix
		`loras\MyLoRA_V2.safetensors`, // backslash prefix (Windows-authored graph)
	} {
		seedUsageWF(t, st, "wf "+ref, []string{ref}, 0)
	}
	// A LONGER filename that merely CONTAINS the basename must not match — that is
	// what the exact compare after the LIKE narrowing is for.
	seedUsageWF(t, st, "decoy", []string{"notMyLoRA_V2.safetensors"}, 0)

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingModel: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d workflows, want 4 (all four spellings, not the decoy): %+v", len(got), got)
	}
	for _, u := range got {
		if u.Name == "decoy" {
			t.Error("a filename merely CONTAINING the basename must not count as a use")
		}
	}
}

// TestAmbiguousBasenameIsNotClaimed: when two DIFFERENT models both have a local
// file with the same basename, that basename cannot identify either of them and
// must be dropped. A miss is the required outcome, never a guess.
func TestAmbiguousBasenameIsNotClaimed(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/a/vae.safetensors", 100, 900)
	seedFile(t, st, "/models/b/vae.safetensors", 200, 901)
	// …plus one basename unique to model 100, to prove only the ambiguous one is
	// dropped rather than the whole model being skipped.
	seedFile(t, st, "/models/a/unique_100.safetensors", 100, 900)

	seedUsageWF(t, st, "vae only", []string{"vae.safetensors"}, 0)
	unique := seedUsageWF(t, st, "unique", []string{"unique_100.safetensors"}, 0)

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingModel: %v", err)
	}
	if len(got) != 1 || got[0].ID != unique {
		t.Fatalf("an AMBIGUOUS basename must not be claimed for either model; got %+v", got)
	}

	names, err := st.UnambiguousModelFileBasenames(context.Background(), 100)
	if err != nil {
		t.Fatalf("UnambiguousModelFileBasenames: %v", err)
	}
	if len(names) != 1 || names[0] != "unique_100.safetensors" {
		t.Errorf("unambiguous basenames = %v, want only unique_100.safetensors", names)
	}
}

// TestUsageIgnoresUnmatchedLocalFiles: the candidate set is grounded in the
// SHA-256 linkage, so a file the scanner never matched to a civitai model
// contributes nothing.
func TestUsageIgnoresUnmatchedLocalFiles(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/loras/orphan.safetensors", 0, 0) // no model_id
	seedUsageWF(t, st, "uses orphan", []string{"orphan.safetensors"}, 0)

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingModel: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an unmatched local file must contribute no usage; got %+v", got)
	}
}

// TestUsageAndImportedFromAreIndependentRelations pins that a workflow can carry
// one, both or neither, and that the store reports them separately.
func TestUsageAndImportedFromAreIndependentRelations(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/loras/f100.safetensors", 100, 900)

	both := seedUsageWF(t, st, "imported and uses", []string{"f100.safetensors"}, 100)
	usesOnly := seedUsageWF(t, st, "uses only", []string{"f100.safetensors"}, 0)
	// Imported FROM model 100 but referencing nothing of it.
	seedUsageWF(t, st, "imported only", []string{"unrelated.safetensors"}, 100)

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingModel: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("USES should hold exactly the two referencing workflows, got %+v", got)
	}
	byID := map[int64]WorkflowUsage{}
	for _, u := range got {
		byID[u.ID] = u
	}
	if u, ok := byID[both]; !ok || u.ModelID == nil || *u.ModelID != 100 {
		t.Errorf("the both-relations workflow must keep its imported-from linkage; got %+v", u)
	}
	if u, ok := byID[usesOnly]; !ok || u.ModelID != nil {
		t.Errorf("the uses-only workflow must carry no imported-from linkage; got %+v", u)
	}

	// The imported-from relation is still counted by its OWN query, unaffected.
	n, err := st.CountWorkflowsByModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("CountWorkflowsByModel: %v", err)
	}
	if n != 2 {
		t.Errorf("CountWorkflowsByModel = %d, want 2 (the two IMPORTED workflows)", n)
	}
}

// TestUsageQueryDoesNotScanWithoutCandidates: no candidate basenames means no
// workflow query at all — an unbounded "match everything" is exactly the
// full-table scan this shape exists to avoid.
func TestUsageQueryDoesNotScanWithoutCandidates(t *testing.T) {
	st := usageStore(t)
	seedUsageWF(t, st, "a", []string{"x.safetensors"}, 0)

	got, err := st.ListWorkflowsUsingFiles(context.Background(), nil)
	if err != nil || got != nil {
		t.Errorf("an empty basename list must return (nil, nil); got %+v, %v", got, err)
	}
	if got, err := st.ListWorkflowsUsingFiles(context.Background(), []string{"", "  "}); err != nil || got != nil {
		t.Errorf("blank basenames must return (nil, nil); got %+v, %v", got, err)
	}
	// …and a model with no local files at all short-circuits the same way.
	if got, err := st.ListWorkflowsUsingModel(context.Background(), 4242); err != nil || got != nil {
		t.Errorf("a model with no local files must return (nil, nil); got %+v, %v", got, err)
	}
}

// TestUsageSurvivesFilenamesWithLikeWildcards: real filenames contain "_", which
// is a LIKE wildcard. The narrowing may over-match; the exact compare must still
// be the gate.
func TestUsageSurvivesFilenamesWithLikeWildcards(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/loras/a_b.safetensors", 100, 900)

	hit := seedUsageWF(t, st, "exact", []string{"a_b.safetensors"}, 0)
	// "aXb.safetensors" is matched by the LIKE (the "_" wildcard) but must be
	// rejected by the exact compare.
	seedUsageWF(t, st, "wildcard collision", []string{"aXb.safetensors"}, 0)

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("ListWorkflowsUsingModel: %v", err)
	}
	if len(got) != 1 || got[0].ID != hit {
		t.Errorf("LIKE wildcards in a filename must not widen the result; got %+v", got)
	}
}

// TestUsageIgnoresMalformedResources: a bad resources column must not fail the
// read, matching scanWorkflow's stance.
func TestUsageIgnoresMalformedResources(t *testing.T) {
	st := usageStore(t)
	seedFile(t, st, "/models/loras/f.safetensors", 100, 900)
	good := seedUsageWF(t, st, "good", []string{"f.safetensors"}, 0)
	bad := seedUsageWF(t, st, "bad", []string{"f.safetensors"}, 0)
	if _, err := st.db.Exec(`UPDATE workflows SET resources = ? WHERE id = ?`,
		`{not an array: f.safetensors}`, bad); err != nil {
		t.Fatalf("corrupt resources: %v", err)
	}

	got, err := st.ListWorkflowsUsingModel(context.Background(), 100)
	if err != nil {
		t.Fatalf("a malformed resources column must not fail the read: %v", err)
	}
	if len(got) != 1 || got[0].ID != good {
		t.Errorf("got %+v, want only the well-formed row", got)
	}
}
