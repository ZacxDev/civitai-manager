package web

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE READINESS COPY: PLURALS, THE LEAD, AND WHAT EACH NUMBER COUNTS.
//
// 🔴 THE PR CLAIMED "the singular case is guarded for EVERY pluralised noun".
// It was not: only the model-file noun had both an n=1 and an n>1 fixture.
// "node type"/"node types" had no n≥2 case at all, and "saved option value" plus
// its is/are verb had no n=1 case. The code was correct at all 44 reachable
// combinations when an audit rendered them — this closes the COVERAGE hole so a
// future edit cannot open a real one silently.
// ─────────────────────────────────────────────────────────────────────────────

// readinessTwoNodeTypesGraph needs exactly TWO node types and nothing else — the
// n≥2 case the file had no fixture for. Neither class name is a substring of the
// other, and neither is a substring of the installed ones.
const readinessTwoNodeTypesGraph = `{
  "1":{"class_type":"AbsentClassAlpha","inputs":{}},
  "2":{"class_type":"AbsentClassBeta","inputs":{}}
}`

// readinessOneEachGraph needs exactly ONE model file and carries exactly ONE
// drifted option. It is the n=1 case for the bad-option noun AND for its is/are
// verb, alongside a non-empty `parts` so the two clauses render together.
const readinessOneEachGraph = `{
  "1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"solo_missing.safetensors"}},
  "2":{"class_type":"KSampler","inputs":{"sampler_name":"drifted_solo","scheduler":"normal"}}
}`

// readinessBadOptionsOnlyGraph has NOTHING missing and TWO drifted option values —
// the shape where readinessCountParts is empty. Both loader references resolve
// (the checkpoint is in the installed combo choices), so only KSampler's two
// off-list values remain.
const readinessBadOptionsOnlyGraph = `{
  "1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"comfy_has_this.safetensors"}},
  "2":{"class_type":"KSampler","inputs":{"sampler_name":"drifted_sampler","scheduler":"drifted_scheduler"}}
}`

// readinessOneBadOptionOnlyGraph is the same shape at n=1.
const readinessOneBadOptionOnlyGraph = `{
  "1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"comfy_has_this.safetensors"}},
  "2":{"class_type":"KSampler","inputs":{"sampler_name":"drifted_sampler","scheduler":"normal"}}
}`

// TestReadinessCopyAgreesWithEveryCountItRenders walks every pluralised noun at
// n=1 AND n>1, plus the relative clause and the bad-option verb that have to agree
// with a DIFFERENT total from the one nearest them.
func TestReadinessCopyAgreesWithEveryCountItRenders(t *testing.T) {
	cases := []struct {
		name  string
		graph string
		want  []string
		deny  []string
	}{{
		name:  "one node type, one model file total",
		graph: readinessOneMissingGraph,
		want:  []string{"1 model file that is not installed"},
		deny:  []string{"model files", "that are not installed"},
	}, {
		name:  "two node types",
		graph: readinessTwoNodeTypesGraph,
		want:  []string{"2 node types that are not installed"},
		deny:  []string{"1 node type ", "that is not installed"},
	}, {
		name:  "three model files and two saved option values",
		graph: readinessAPIGraph,
		want: []string{
			"1 node type", "3 model files",
			"that are not installed",
			"2 saved option values are no longer valid",
		},
		deny: []string{"that is not installed", "saved option value is"},
	}, {
		// The discriminating case for the relative clause: ONE model file and ONE
		// drifted option. The clause must agree with the missing TOTAL (1 → "is"),
		// and the bad-option verb with the option count (1 → "is") — two different
		// numbers that happen to match here, which is why the case above (3 and 2)
		// carries the opposite pairing.
		name:  "one of each",
		graph: readinessOneEachGraph,
		want: []string{
			"1 model file that is not installed",
			"1 saved option value is no longer valid",
		},
		deny: []string{"model files", "saved option values", "that are not installed"},
	}, {
		// 🟢-7: with `parts` empty the "A run needs …" lead never rendered, leaving an
		// amber ! beside a bare statement. Reachable and previously untested.
		name:  "bad options only, plural",
		graph: readinessBadOptionsOnlyGraph,
		want:  []string{"A run needs 2 saved option values updated"},
		deny:  []string{"node type", "model file", "that are not installed"},
	}, {
		name:  "bad options only, singular",
		graph: readinessOneBadOptionOnlyGraph,
		want:  []string{"A run needs 1 saved option value updated"},
		deny:  []string{"node type", "model file", "saved option values"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newReadinessServer(t)
			seedObjectInfo(t, srv, readinessInfo)
			id := seedWorkflow(t, srv, store.WorkflowFormatAPI, tc.graph)

			body := readiness(t, srv, id)
			// PRECONDITION: every case here is a "needs" answer. If a fixture drifted into
			// unknown or ready the assertions below would be measuring an absence.
			wantState(t, body, "needs", "")
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q:\n%s", w, body)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(body, d) {
					t.Errorf("must NOT contain %q:\n%s", d, body)
				}
			}
		})
	}
}

// TestReadinessSaysWhatItCounts pins the scope label on both surfaces that carry a
// number about the same workflow.
//
// 🔴 The two figures legitimately DIFFER and neither is being changed: the chips
// card counts everything the saved FILE mentions (bypassed pipelines included,
// widgets_values-wide, snapshotted at insert time), the readiness line counts what
// a RUN would load. That was reported as a contradiction, so each now says what it
// counts. See readinessSentence's header for the four sources of the divergence.
func TestReadinessSaysWhatItCounts(t *testing.T) {
	srv := newReadinessServer(t)
	srv.comfyClientFn = func() comfyClient { return &fakeComfy{} }
	seedObjectInfo(t, srv, readinessInfo)

	// Inserted directly rather than through seedWorkflow: the resources card renders
	// only when wf.Resources is non-empty, so without them this test would assert the
	// card's label against a page that never drew the card.
	wid, err := srv.store.InsertWorkflow(context.Background(), &store.Workflow{
		Name: "wf", Format: store.WorkflowFormatAPI, Graph: readinessReadyGraph,
		Source:    store.WorkflowSourceImported,
		Resources: []string{"comfy_has_this.safetensors", "bypassed_pipeline_only.safetensors"},
	})
	if err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
	id := strconv.FormatInt(wid, 10)

	line := readiness(t, srv, id)
	wantState(t, line, "ready", "")
	if !strings.Contains(line, "a run would load") {
		t.Errorf("the readiness line does not say what it counts:\n%s", line)
	}

	page := get(t, srv, "/workflows/"+id).Body.String()
	if !strings.Contains(page, "Referenced resources") {
		t.Fatalf("precondition: the resources card did not render, so its label cannot be tested")
	}
	if !strings.Contains(page, "including pipelines that are switched off") {
		t.Errorf("the resources card does not say what IT counts:\n%s", page)
	}
}
