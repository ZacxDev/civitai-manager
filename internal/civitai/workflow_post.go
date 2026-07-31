package civitai

import "strings"

// WorkflowsModelType is CivitAI's model-type string for a workflow post. It is
// the ONLY field that distinguishes such a post from a checkpoint: measured
// 2026-07-31 across 1847730 / 1386234 vs 4384 (DreamShaper), the top-level and
// `modelVersions[]` key sets are IDENTICAL, and every workflow version carries a
// populated `baseModel`, a `downloadUrl`, and a `files[]` entry with a real
// SHA256, `primary: true` and `sizeKB`. A workflow post is therefore NOT
// distinguishable by absence — only by this string.
const WorkflowsModelType = "Workflows"

// IsWorkflowPost reports whether a CivitAI model TYPE identifies a workflow post
// (a zip of ComfyUI workflow .json published at a /models/<id> URL).
//
// It takes the type STRING rather than a model struct because the two shapes the
// app renders carry it in different places and the list shape carries nothing
// else: pass ModelDetail.Type on the detail path and ModelListItem.Type on the
// search/discover card path, and both get the identical predicate. That is the
// point — this predicate used to be open-coded at four sites in three spellings
// (two `strings.EqualFold`, one raw `!=`), which is how the download half of the
// bug survived the render-layer checks.
//
// It FAILS OPEN on an empty/whitespace type: an absent type is not asserted to
// be a workflow. Callers that must refuse a NON-workflow (the Discover import
// gate) therefore have to spell the empty case out themselves — see
// IsKnownNonWorkflowPost, and the comment on that function for why the gate must
// stay permissive.
func IsWorkflowPost(modelType string) bool {
	return strings.EqualFold(strings.TrimSpace(modelType), WorkflowsModelType)
}

// IsKnownNonWorkflowPost reports whether a model type is present AND says
// something other than "Workflows". It is the Discover workflow-import gate's
// predicate, and the empty-type carve-out is load-bearing, NOT an oversight: an
// absent type must still be allowed through, the same lesson as the raw-string
// type check that once refused legitimate LoCon installs.
//
// The gate stays this tight (rather than checking "does it ship an Archive zip")
// because 94 of the top 100 `types=Other` models carry an Archive `.zip` — node
// packs, tutorials — and are genuinely not workflows.
func IsKnownNonWorkflowPost(modelType string) bool {
	return strings.TrimSpace(modelType) != "" && !IsWorkflowPost(modelType)
}
