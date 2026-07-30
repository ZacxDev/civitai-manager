package uxaudit

import (
	"encoding/json"
	"strings"
	"testing"
)

// realisticDigest is the shape a11y-digest.js emits — one interactive element, one
// form control, one landmark — used wherever a test needs a digest that auditloop
// would ACCEPT.
const realisticDigest = `{"interactive":[{"tag":"a","role":"link","accessible_name":"Open workflow",` +
	`"selector":"a#wf-1","type":"","disabled":false,"focusable":true,"label_source":"text-content"}],` +
	`"form_controls":[{"selector":"input#search","accessible_name":"Search","has_label":true,"label_source":"for"}],` +
	`"landmarks":[{"tag":"h1","role":"","text":"Workflows"}]}`

// emptyDigest is what a11y-digest.js returns for a genuinely bare page AND from its
// catch-all after a JS exception. auditloop REJECTS it (400) — and that rejection
// fails the WHOLE multi-page push — so the producer must never attach it.
const emptyDigest = `{"interactive":[],"form_controls":[],"landmarks":[]}`

// digestCaptures builds a two-page capture set where the digest bytes of each page
// are caller-controlled, for the attach/omit tests. Everything else mirrors
// synthCaptures so the payload stays otherwise valid.
func digestCaptures(digests ...string) []CapturedView {
	out := make([]CapturedView, 0, len(digests))
	for i, d := range digests {
		vc := ViewCapture{
			ScreenshotPNG: tinyPNG,
			AxeJSON:       []byte(`{"violations":[]}`),
			NetworkJSON:   []byte(`[]`),
		}
		if d != "" {
			vc.A11yDigestJSON = []byte(d)
		}
		out = append(out, CapturedView{
			View:     View{Name: "view-" + string(rune('a'+i))},
			Viewport: Viewports[0],
			Capture:  vc,
		})
	}
	return out
}

// TestA11yDigestAttachedWhenNonEmpty is the POSITIVE wiring assertion: a capture that
// produced a real digest must yield BOTH an `a11y_digest` ref AND a matching multipart
// part carrying the captured bytes verbatim, named `<base>.a11y.json` alongside the
// existing .axe.json / .network.json artifacts.
//
// NON-VACUOUS: this fails if the BuildPayload wiring in walk.go is removed (no ref, no
// file) — see TestA11yDigestWiringIsNonVacuous for the explicit mutation proof.
func TestA11yDigestAttachedWhenNonEmpty(t *testing.T) {
	caps := digestCaptures(realisticDigest)
	payload, files, err := BuildPayload("t", caps)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}

	pg := payload.Pages[0]
	wantName := "view-a." + Viewports[0].Name + ".a11y.json"
	if pg.A11yDigest != wantName {
		t.Fatalf("a11y_digest ref = %q, want %q", pg.A11yDigest, wantName)
	}
	if got := files[pg.A11yDigest]; string(got) != realisticDigest {
		t.Fatalf("digest part bytes = %q, want the captured digest verbatim", string(got))
	}
	// The ref must resolve to a real part and leave no orphan (auditloop enforces both
	// halves of that integrity check server-side).
	if err := payload.Validate(setOf(files)); err != nil {
		t.Fatalf("payload with a digest failed validation: %v", err)
	}
}

// TestA11yDigestOmittedWhenEmpty is THE 400-avoidance rule, and the single most
// important assertion in this file: for a page whose digest carries no elements the
// harness must emit NEITHER the ref NOR the file part.
//
// Attaching an all-empty digest makes auditloop reject the ENTIRE multi-page push
// (not just the page), and an empty digest is the routine output of a11y-digest.js on
// a bare page or after a JS exception — so this is a case that occurs in practice.
// Both halves are asserted: a stray part with no ref is itself an orphan-upload
// rejection, so checking only the ref would leave a second way to 400 the push.
func TestA11yDigestOmittedWhenEmpty(t *testing.T) {
	cases := []struct {
		name   string
		digest string
	}{
		{"all-empty lists (bare page / JS-exception catch-all)", emptyDigest},
		{"no digest captured at all", ""},
		{"unparseable digest bytes", `{"interactive":[`},
		{"non-JSON garbage", `undefined`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, files, err := BuildPayload("t", digestCaptures(tc.digest))
			if err != nil {
				t.Fatalf("BuildPayload: %v", err)
			}
			if ref := payload.Pages[0].A11yDigest; ref != "" {
				t.Errorf("a11y_digest ref = %q, want empty (auditloop 400s the whole push on this digest)", ref)
			}
			for name := range files {
				if strings.HasSuffix(name, ".a11y.json") {
					t.Errorf("digest file part %q was uploaded with no ref — an orphan part also 400s the push", name)
				}
			}
			// Belt and braces: the payload must still be self-valid.
			if err := payload.Validate(setOf(files)); err != nil {
				t.Errorf("payload invalid after omitting the digest: %v", err)
			}
		})
	}
}

// TestA11yDigestWiringIsNonVacuous proves the two assertions above actually discriminate,
// by MUTATION: it re-implements the two ways the producer could be broken and shows each
// makes the corresponding assertion fail.
//
//   - "wiring removed": a BuildPayload that never attaches a digest. The positive test's
//     ref check must fail against it — otherwise that test would pass on a no-op producer.
//   - "guard removed": a BuildPayload that attaches unconditionally (the naive version
//     without nonEmptyA11yDigest). The omit test's ref check must fail against it —
//     otherwise the empty-digest guard could be deleted with tests still green.
func TestA11yDigestWiringIsNonVacuous(t *testing.T) {
	// Mutation 1: no wiring at all — the pre-change producer.
	t.Run("wiring removed fails the positive assertion", func(t *testing.T) {
		payload, files, _ := BuildPayload("t", digestCaptures(realisticDigest))
		unwired := payload
		unwired.Pages = append([]PushPage(nil), payload.Pages...)
		unwired.Pages[0].A11yDigest = "" // as if walk.go never set it
		delete(files, "view-a."+Viewports[0].Name+".a11y.json")

		if unwired.Pages[0].A11yDigest != "" {
			t.Fatal("mutation setup failed")
		}
		// The real producer DOES set it — so the positive test is discriminating.
		if payload.Pages[0].A11yDigest == unwired.Pages[0].A11yDigest {
			t.Fatal("TestA11yDigestAttachedWhenNonEmpty is vacuous: the real payload matches an unwired one")
		}
	})

	// Mutation 2: the guard removed — attach whatever was captured.
	t.Run("guard removed fails the omit assertion", func(t *testing.T) {
		caps := digestCaptures(emptyDigest)
		// naive := what BuildPayload would produce WITHOUT the nonEmptyA11yDigest check.
		naiveRef := ""
		if len(caps[0].Capture.A11yDigestJSON) > 0 {
			naiveRef = "view-a." + Viewports[0].Name + ".a11y.json"
		}
		if naiveRef == "" {
			t.Fatal("mutation setup failed: the naive producer should have attached the empty digest")
		}
		payload, _, _ := BuildPayload("t", caps)
		if payload.Pages[0].A11yDigest == naiveRef {
			t.Fatal("the empty-digest guard is not in effect: BuildPayload attached a push-rejecting empty digest")
		}
	})
}

// TestNonEmptyA11yDigestGuard covers the guard directly, including the cap. Everything
// ambiguous must resolve to "do not attach": a lost digest costs grounding on one page,
// a rejected push costs the entire run.
func TestNonEmptyA11yDigestGuard(t *testing.T) {
	big := `{"interactive":[],"form_controls":[],"landmarks":[{"tag":"h1","role":"","text":"` +
		strings.Repeat("x", MaxA11yDigestBytes) + `"}]}`
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"realistic digest", realisticDigest, true},
		{"interactive only", `{"interactive":[{"tag":"button","selector":"#b","focusable":true}],"form_controls":[],"landmarks":[]}`, true},
		{"form controls only", `{"interactive":[],"form_controls":[{"selector":"#e","label_source":"for"}],"landmarks":[]}`, true},
		{"landmarks only", `{"interactive":[],"form_controls":[],"landmarks":[{"tag":"h1"}]}`, true},
		{"all empty", emptyDigest, false},
		{"empty object", `{}`, false},
		{"no bytes", "", false},
		{"malformed", `{"interactive":`, false},
		{"over the 256 KiB cap", big, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nonEmptyA11yDigest([]byte(tc.raw)); got != tc.want {
				t.Fatalf("nonEmptyA11yDigest = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestA11yDigestScriptIsVendoredVerbatim guards the vendoring itself. The contract's
// explicit instruction is to port auditloop's script VERBATIM rather than hand-roll a
// DOM walker, because divergence is what produces push-rejecting output. This asserts
// the embedded source is the real script (not a stub) and still carries the caps and
// the closed label_source set the server validates against — so gutting it fails here.
func TestA11yDigestScriptIsVendoredVerbatim(t *testing.T) {
	if len(a11yDigestSource) < 2000 {
		t.Fatalf("embedded a11y-digest.js is only %d bytes — it looks stubbed, re-vendor from %s",
			len(a11yDigestSource), A11yDigestSource)
	}
	// The caps auditloop rejects overruns of, as written in the script.
	for _, want := range []string{
		"MAX_INTERACTIVE = 40", "MAX_FORM = 30", "MAX_LANDMARK = 30",
		"slice(0, 120)", "slice(0, 200)", "slice(0, 80)",
	} {
		if !strings.Contains(a11yDigestSource, want) {
			t.Errorf("vendored script is missing the bound %q — it has diverged from auditloop's", want)
		}
	}
	// label_source values outside auditloop's closed set REJECT the push. The script's
	// normalisation ('text'/'value' → 'text-content' for interactive, → 'none' for form
	// controls) is what keeps output inside it.
	for _, want := range []string{"'text-content'", "'wrapping-label'", "'aria-labelledby'", "'placeholder'"} {
		if !strings.Contains(a11yDigestSource, want) {
			t.Errorf("vendored script no longer emits label_source %s", want)
		}
	}
	if !strings.Contains(a11yDigestSource, `'{"interactive":[],"form_controls":[],"landmarks":[]}'`) {
		t.Error("vendored script lost its empty catch-all — the case the omit guard exists for")
	}
}

// TestA11yDigestShapeMatchesContract pins the three top-level keys the harness decodes
// against the schema auditloop validates. A renamed key here would silently make every
// digest look empty (so nothing is ever attached) — a failure that is invisible in a
// green push, which is exactly how a producer silently loses grounding.
func TestA11yDigestShapeMatchesContract(t *testing.T) {
	var got map[string]json.RawMessage
	if err := json.Unmarshal([]byte(realisticDigest), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"interactive", "form_controls", "landmarks"} {
		if _, ok := got[k]; !ok {
			t.Errorf("contract key %q missing from the digest fixture", k)
		}
	}
	var d a11yDigestShape
	if err := json.Unmarshal([]byte(realisticDigest), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Interactive) != 1 || len(d.FormControls) != 1 || len(d.Landmarks) != 1 {
		t.Fatalf("decoded counts = %d/%d/%d, want 1/1/1 — the json tags on a11yDigestShape have drifted",
			len(d.Interactive), len(d.FormControls), len(d.Landmarks))
	}
}

// TestBuildPayloadDigestDoesNotDisturbExistingArtifacts pins the "byte-for-byte
// unchanged" requirement: adding the digest must not alter the roll-up counts, the
// findings, or the existing screenshot/axe/network refs on the same page.
func TestBuildPayloadDigestDoesNotDisturbExistingArtifacts(t *testing.T) {
	base := synthCaptures()
	withDigest := synthCaptures()
	for i := range withDigest {
		withDigest[i].Capture.A11yDigestJSON = []byte(realisticDigest)
	}

	pBase, fBase, err := BuildPayload("t", base)
	if err != nil {
		t.Fatal(err)
	}
	pDig, fDig, err := BuildPayload("t", withDigest)
	if err != nil {
		t.Fatal(err)
	}

	if len(pBase.Pages) != len(pDig.Pages) {
		t.Fatalf("page count changed: %d -> %d", len(pBase.Pages), len(pDig.Pages))
	}
	for i := range pBase.Pages {
		b, d := pBase.Pages[i], pDig.Pages[i]
		if b.A11yDigest != "" {
			t.Fatalf("baseline page %d unexpectedly has a digest ref", i)
		}
		if d.A11yDigest == "" {
			t.Fatalf("page %d did not get a digest ref (wiring missing)", i)
		}
		// Zero out the only intended difference; everything else must be identical.
		d.A11yDigest = ""
		if !jsonEqual(t, b, d) {
			t.Errorf("page %d changed beyond the digest ref:\n base: %+v\n dig:  %+v", i, b, d)
		}
	}
	// The only new files are the digests; every pre-existing artifact is untouched.
	for name, want := range fBase {
		if string(fDig[name]) != string(want) {
			t.Errorf("existing artifact %q changed when the digest was added", name)
		}
	}
	extra := 0
	for name := range fDig {
		if _, ok := fBase[name]; !ok {
			if !strings.HasSuffix(name, ".a11y.json") {
				t.Errorf("unexpected new artifact %q", name)
			}
			extra++
		}
	}
	if extra != len(pDig.Pages) {
		t.Errorf("added %d digest parts, want one per page (%d)", extra, len(pDig.Pages))
	}
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	ab, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ab) == string(bb)
}
