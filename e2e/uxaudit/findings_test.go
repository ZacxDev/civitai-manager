package uxaudit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// realisticAxeJSON is an axe result in EXACTLY the shape axeRunScript (axe.go)
// produces, modelled on what the live walk actually observes: a color-contrast rule
// with MANY offending nodes (the real run saw 61) whose example nodes are already
// trimmed to {target, html}, plus a second rule. It is the fixture for the
// one-finding-per-RULE and top-level-id assertions.
const realisticAxeJSON = `{"violations":[
  {"id":"color-contrast","impact":"serious",
   "help":"Elements must meet minimum color contrast ratio thresholds",
   "helpUrl":"https://dequeuniversity.com/rules/axe/4.12/color-contrast",
   "tags":["cat.color","wcag2aa","wcag143"],
   "nodeCount":61,
   "nodes":[{"target":[".stat-label"],"html":"<span class=\"stat-label\">Models</span>"},
            {"target":[".muted"],"html":"<p class=\"muted\">No sources yet</p>"},
            {"target":["a.nav"],"html":"<a class=\"nav\" href=\"/library\">Library</a>"},
            {"target":[".x4"],"html":"<i>4</i>"},
            {"target":[".x5"],"html":"<i>5</i>"}]},
  {"id":"empty-table-header","impact":"minor",
   "help":"Table header text should not be empty",
   "helpUrl":"https://dequeuniversity.com/rules/axe/4.12/empty-table-header",
   "tags":["cat.name-role-value","wcag2a"],
   "nodeCount":2,
   "nodes":[{"target":["th:nth-child(1)"],"html":"<th></th>"}]}
]}`

// ---------------------------------------------------------------------------
// A mirror of the RECEIVING side (auditloop), so these tests assert the gate the
// production consumer actually runs — not just our own encoding talking to itself.
// ---------------------------------------------------------------------------

// auditloopHasTopLevelStringID mirrors auditloop internal/plugin/map.go
// hasTopLevelStringID: is the pushed detail STRING a JSON object with a non-empty
// top-level "id"? When true, auditloop stores those bytes VERBATIM as the finding
// detail; when false it falls back to deriving an id from free text.
func auditloopHasTopLevelStringID(s string) bool {
	var meta struct {
		ID string `json:"id"`
	}
	return json.Unmarshal([]byte(s), &meta) == nil && meta.ID != ""
}

// auditloopStoredDetail mirrors auditloop internal/plugin/map.go a11yDetail — what
// actually lands in the findings table for a PUSHED a11y finding.
func auditloopStoredDetail(pushed string) string {
	if auditloopHasTopLevelStringID(pushed) {
		return pushed // stored verbatim
	}
	id := strings.TrimSpace(pushed)
	if i := strings.Index(id, " — "); i >= 0 {
		id = strings.TrimSpace(id[:i])
	}
	if len(id) > 128 {
		id = id[:128]
	}
	b, _ := json.Marshal(map[string]string{"id": id, "detail": pushed})
	return string(b)
}

// auditloopRuleIDs mirrors auditloop internal/worker/diff.go runA11yRuleIDs: the
// de-duplicated set of axe rule ids recovered from the STORED a11y finding details.
// This is the exact input to the P2 `new_a11y_rules` delta that a CI
// `--fail-on-regression` gate keys on — if this comes back empty, the gate is a
// silent no-op.
func auditloopRuleIDs(findings []PushFinding) []string {
	seen := map[string]bool{}
	for _, f := range findings {
		if f.Type != FindingA11y {
			continue
		}
		var meta struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(auditloopStoredDetail(f.Detail)), &meta) == nil && meta.ID != "" {
			seen[meta.ID] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// The gate-critical assertions.
// ---------------------------------------------------------------------------

// TestA11yFindingsOnePerRuleWithTopLevelID is THE regression test for the bug this
// change fixes. It asserts, on a realistic axe result:
//
//   - one finding per violated RULE, not per offending node (61 color-contrast nodes
//     collapse into exactly ONE color-contrast finding);
//   - every a11y finding's detail decodes to a JSON OBJECT with a NON-EMPTY top-level
//     "id" equal to the axe rule id (what makes auditloop store it verbatim);
//   - the rule-id set auditloop's P2 delta would recover is exactly the violated rule
//     set — i.e. the CI a11y regression gate is no longer a no-op;
//   - severity carries the axe impact through.
func TestA11yFindingsOnePerRuleWithTopLevelID(t *testing.T) {
	findings, err := A11yFindings([]byte(realisticAxeJSON))
	if err != nil {
		t.Fatalf("A11yFindings: %v", err)
	}

	if len(findings) != 2 {
		t.Fatalf("got %d a11y findings, want 2 (ONE PER RULE, not per node)", len(findings))
	}

	wantImpact := map[string]string{"color-contrast": "serious", "empty-table-header": "minor"}
	for i, f := range findings {
		if f.Type != FindingA11y {
			t.Errorf("finding %d: type = %q, want %q", i, f.Type, FindingA11y)
		}
		// The load-bearing assertion: a JSON object with a non-empty top-level id.
		id, ok := topLevelID(f.Detail)
		if !ok {
			t.Fatalf("finding %d: detail does not decode to an object with a non-empty top-level %q — auditloop's new_a11y_rules delta would be EMPTY and the CI a11y gate a NO-OP.\ndetail: %s",
				i, "id", f.Detail)
		}
		want, known := wantImpact[id]
		if !known {
			t.Errorf("finding %d: unexpected rule id %q", i, id)
			continue
		}
		if f.Severity != want {
			t.Errorf("rule %q: severity = %q, want the axe impact %q", id, f.Severity, want)
		}
		delete(wantImpact, id)
	}
	if len(wantImpact) != 0 {
		t.Errorf("rules missing from the findings: %v", wantImpact)
	}

	// The receiving side's gate input must now be populated and exact.
	gotIDs := auditloopRuleIDs(findings)
	wantIDs := []string{"color-contrast", "empty-table-header"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Errorf("auditloop would recover rule ids %v, want %v", gotIDs, wantIDs)
	}
}

// topLevelID decodes a pushed a11y detail and returns its top-level "id". It is the
// assertion under test in TestA11yDetailIDAssertionIsNonVacuous.
func topLevelID(detail string) (string, bool) {
	var meta struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(detail), &meta) != nil || meta.ID == "" {
		return "", false
	}
	return meta.ID, true
}

// TestA11yDetailIDAssertionIsNonVacuous proves the top-level-id assertion above can
// actually FAIL — a mutation test, so a passing suite is real evidence rather than a
// tautology. It takes the SAME detail the production code emits, strips the "id" key,
// and asserts the assertion helper rejects it; then it asserts a merely-NESTED id is
// rejected too (the id must be top-level, which is the field auditloop reads).
func TestA11yDetailIDAssertionIsNonVacuous(t *testing.T) {
	findings, err := A11yFindings([]byte(realisticAxeJSON))
	if err != nil {
		t.Fatalf("A11yFindings: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("no findings to mutate")
	}

	// Sanity: the real detail passes.
	if _, ok := topLevelID(findings[0].Detail); !ok {
		t.Fatal("baseline detail should carry a top-level id")
	}

	// MUTANT 1: drop the "id" key → the assertion must fail.
	var obj map[string]any
	if err := json.Unmarshal([]byte(findings[0].Detail), &obj); err != nil {
		t.Fatalf("detail is not a JSON object: %v", err)
	}
	delete(obj, "id")
	mutated, _ := json.Marshal(obj)
	if id, ok := topLevelID(string(mutated)); ok {
		t.Fatalf("assertion is VACUOUS: an id-less detail still yielded id=%q", id)
	}
	// A nested-only id (a plausible wrong encoding) must also be rejected.
	if _, ok := topLevelID(`{"violation":{"id":"color-contrast"}}`); ok {
		t.Fatal("assertion is VACUOUS: a NESTED id was accepted as top-level")
	}

	// MUTANT 2: the pre-fix state — no findings pushed at all → the gate input is
	// MUTANT 2: the id must be TOP-LEVEL specifically. A detail carrying the rule id
	// only in a nested object is the clean demonstration that the assertion inspects
	// the exact field auditloop reads (the "rename the id tag" mutation is a blunter
	// instrument — axeViolation is also used to DECODE the axe result, so renaming the
	// tag yields zero findings instead of id-less ones).
	nested, _ := json.Marshal(map[string]any{"violation": map[string]string{"id": "color-contrast"}})
	if _, ok := topLevelID(string(nested)); ok {
		t.Fatal("assertion is VACUOUS: a NESTED id was accepted as top-level")
	}
}

// TestA11yFindingsSkipsBlankRuleID asserts a violation with no rule id is DROPPED
// rather than pushed: it could not serve as a stable gate key, and auditloop's legacy
// fallback would otherwise derive a meaningless id from the JSON text.
func TestA11yFindingsSkipsBlankRuleID(t *testing.T) {
	findings, err := A11yFindings([]byte(`{"violations":[
		{"id":"","impact":"serious","help":"no id"},
		{"id":"  ","impact":"serious","help":"blank id"},
		{"id":"label","impact":"critical","help":"Form elements must have labels"}]}`))
	if err != nil {
		t.Fatalf("A11yFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1 (blank-id violations dropped)", len(findings))
	}
	if id, _ := topLevelID(findings[0].Detail); id != "label" {
		t.Errorf("kept finding id = %q, want %q", id, "label")
	}
}

// TestA11yFindingsCleanInputIsNotAnError asserts genuinely-clean/absent axe output
// yields no findings AND no error — a clean page must stay pushable.
func TestA11yFindingsCleanInputIsNotAnError(t *testing.T) {
	for _, in := range []string{"", `{"violations":null}`, `{"violations":[]}`, `{}`} {
		got, err := A11yFindings([]byte(in))
		if err != nil {
			t.Errorf("input %q: unexpected error %v", in, err)
		}
		if len(got) != 0 {
			t.Errorf("input %q: got %d findings, want 0", in, len(got))
		}
	}
}

// TestAxeScanFailureIsNotClean is the regression test for the severity hole this PR
// would otherwise have introduced.
//
// axeRunScript catches a thrown exception and returns {"error":"...","violations":[]}.
// Modelling no `error` field made an axe scan that NEVER RAN indistinguishable from a
// genuinely clean page. That was cosmetic while only roll-up counts were pushed; once
// findings drive auditloop's a11y rule set it corrupts the regression baseline: the
// errored run's rule set is empty → `resolved_a11y_rules` claims EVERY rule was fixed
// → the next good run reports every rule as NEW → a --fail-on-regression gate fails
// spuriously, on a report that simultaneously says "✓ no accessibility issues".
//
// So an errored scan must be an ERROR (fatal to the walk), never zero findings.
func TestAxeScanFailureIsNotClean(t *testing.T) {
	// The exact shape axeRunScript emits on a thrown exception.
	const errored = `{"error":"TypeError: axe.run is not a function","violations":[]}`

	got, err := A11yFindings([]byte(errored))
	if err == nil {
		t.Fatal("an ERRORED axe scan returned no error — it is indistinguishable from a clean page, which corrupts the a11y regression baseline")
	}
	if len(got) != 0 {
		t.Errorf("errored scan produced %d findings, want none", len(got))
	}
	var scanErr *ErrAxeScanFailed
	if !errors.As(err, &scanErr) {
		t.Errorf("error = %T (%v), want *ErrAxeScanFailed", err, err)
	} else if !strings.Contains(scanErr.Reason, "axe.run is not a function") {
		t.Errorf("error reason %q lost the underlying axe message", scanErr.Reason)
	}

	// It must propagate through CaptureFindings...
	if _, err := CaptureFindings(ViewCapture{AxeJSON: []byte(errored)}); err == nil {
		t.Error("CaptureFindings swallowed the axe scan failure")
	}
	// ...and all the way out of BuildPayload, so Walk fails loudly rather than pushing
	// a page that was never scanned as clean.
	caps := []CapturedView{{
		View:     View{Name: "dashboard"},
		Viewport: Viewports[0],
		Capture:  ViewCapture{ScreenshotPNG: tinyPNG, AxeJSON: []byte(errored)},
	}}
	_, _, err = BuildPayload("scan-failure", caps)
	if err == nil {
		t.Fatal("BuildPayload accepted a capture whose axe scan FAILED — the walk would push it as clean")
	}
	if !strings.Contains(err.Error(), "dashboard") {
		t.Errorf("error %q does not identify the offending view", err)
	}

	// Unparseable axe JSON is likewise not "clean".
	if _, err := A11yFindings([]byte("not json")); err == nil {
		t.Error("unparseable axe JSON returned no error — it would read as a clean page")
	}
}

// ---------------------------------------------------------------------------
// console / network findings + the third-party rule.
// ---------------------------------------------------------------------------

// TestConsoleFindingsCarryTextFirstPartyOnly asserts first-party console errors
// become findings CARRYING THE MESSAGE TEXT (the whole point — a count alone does not
// tell you what the app logged) and that third-party errors emit NO findings.
func TestConsoleFindingsCarryTextFirstPartyOnly(t *testing.T) {
	got := ConsoleFindings([]consoleEvent{
		{Text: "Failed to load resource: net::ERR_FAILED", URL: "http://127.0.0.1:1/app.js", FirstPart: true},
		{Text: "third-party analytics blew up", URL: "https://cdn.example/a.js", FirstPart: false},
		{Text: "", URL: "", FirstPart: true}, // empty text still becomes a visible finding
	})
	if len(got) != 2 {
		t.Fatalf("got %d console findings, want 2 (first-party only)", len(got))
	}
	for _, f := range got {
		if f.Type != FindingConsole {
			t.Errorf("type = %q, want %q", f.Type, FindingConsole)
		}
		if strings.Contains(f.Detail, "third-party") {
			t.Errorf("third-party console error leaked into findings: %q", f.Detail)
		}
		if strings.TrimSpace(f.Detail) == "" {
			t.Error("console finding detail is empty (unactionable)")
		}
	}
	if !strings.Contains(got[0].Detail, "net::ERR_FAILED") {
		t.Errorf("detail lost the message text: %q", got[0].Detail)
	}
	if !strings.Contains(got[0].Detail, "app.js") {
		t.Errorf("detail lost the source URL: %q", got[0].Detail)
	}
}

// TestNetworkFindingsStatusAndReason asserts first-party network errors become
// findings carrying the HTTP status (or the net:: failure reason) + the URL, with 4xx
// graded softer than 5xx/transport failures, and third-party errors excluded.
func TestNetworkFindingsStatusAndReason(t *testing.T) {
	got := NetworkFindings([]networkEvent{
		{URL: "http://127.0.0.1:8080/api/missing", Status: 404, Reason: "http_status", FirstPart: true},
		{URL: "http://127.0.0.1:8080/api/boom", Status: 500, Reason: "http_status", FirstPart: true},
		{URL: "http://127.0.0.1:8080/x.js", Reason: "net::ERR_CONNECTION_REFUSED", FirstPart: true},
		{URL: "https://cdn.example/t.gif", Status: 403, FirstPart: false},
	})
	if len(got) != 3 {
		t.Fatalf("got %d network findings, want 3 (first-party only)", len(got))
	}
	if !strings.Contains(got[0].Detail, "404") || !strings.Contains(got[0].Detail, "/api/missing") {
		t.Errorf("4xx detail = %q, want status + url", got[0].Detail)
	}
	if got[0].Severity != severityModerate {
		t.Errorf("4xx severity = %q, want %q", got[0].Severity, severityModerate)
	}
	if got[1].Severity != severitySerious {
		t.Errorf("5xx severity = %q, want %q", got[1].Severity, severitySerious)
	}
	if !strings.Contains(got[2].Detail, "net::ERR_CONNECTION_REFUSED") {
		t.Errorf("transport-failure detail = %q, want the net:: reason", got[2].Detail)
	}
	for _, f := range got {
		if strings.Contains(f.Detail, "cdn.example") {
			t.Errorf("third-party network error leaked into findings: %q", f.Detail)
		}
	}
}

// TestThirdPartyOnlyCaptureEmitsNoFindings is the explicit third-party rule: a view
// whose ONLY console/network errors are third-party emits no console/network findings
// at all — they stay bucketed in the counts (environmental, not this app's defects).
func TestThirdPartyOnlyCaptureEmitsNoFindings(t *testing.T) {
	vc := ViewCapture{
		AxeJSON:           []byte(`{"violations":[]}`),
		ConsoleThirdParty: 2,
		NetworkThirdParty: 1,
		Console: []consoleEvent{
			{Text: "analytics error", URL: "https://cdn.example/a.js", FirstPart: false},
			{Text: "pixel error", URL: "https://px.example/p.gif", FirstPart: false},
		},
		Network: []networkEvent{{URL: "https://cdn.example/x.js", Status: 502, FirstPart: false}},
	}
	got, err := CaptureFindings(vc)
	if err != nil {
		t.Fatalf("CaptureFindings: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d findings from a third-party-only capture, want 0: %+v", len(got), got)
	}
}

// ---------------------------------------------------------------------------
// caps + truncation.
// ---------------------------------------------------------------------------

// TestFindingCapsBoundThePayload asserts every emission cap holds, so a pathological
// page cannot inflate the single `metadata` JSON part toward the push size limits.
func TestFindingCapsBoundThePayload(t *testing.T) {
	// a11y: more violated rules than the cap → capped.
	var vs []string
	for i := 0; i < maxA11yFindings+40; i++ {
		vs = append(vs, fmt.Sprintf(`{"id":"rule-%d","impact":"minor","nodeCount":1}`, i))
	}
	capped, err := A11yFindings([]byte(`{"violations":[` + strings.Join(vs, ",") + `]}`))
	if err != nil {
		t.Fatalf("A11yFindings: %v", err)
	}
	if len(capped) != maxA11yFindings {
		t.Errorf("a11y findings = %d, want the cap %d", len(capped), maxA11yFindings)
	}

	// console: a runaway error loop → capped.
	var ces []consoleEvent
	for i := 0; i < maxConsoleFindings*4; i++ {
		ces = append(ces, consoleEvent{Text: "boom", FirstPart: true})
	}
	if got := ConsoleFindings(ces); len(got) != maxConsoleFindings {
		t.Errorf("console findings = %d, want the cap %d", len(got), maxConsoleFindings)
	}

	// network: likewise.
	var nes []networkEvent
	for i := 0; i < maxNetworkFindings*4; i++ {
		nes = append(nes, networkEvent{URL: "http://x/y", Status: 500, FirstPart: true})
	}
	if got := NetworkFindings(nes); len(got) != maxNetworkFindings {
		t.Errorf("network findings = %d, want the cap %d", len(got), maxNetworkFindings)
	}
}

// TestFindingTruncation asserts oversized strings/lists are TRUNCATED (not dropped,
// not passed through): node lists, selectors, node HTML, help text, and the
// console/network detail text all land within their caps, and the result stays valid
// UTF-8 + valid JSON.
func TestFindingTruncation(t *testing.T) {
	long := strings.Repeat("x", 4000)
	var nodes []string
	for i := 0; i < 20; i++ {
		nodes = append(nodes, fmt.Sprintf(`{"target":["%s","b","c","d","e","f"],"html":"%s"}`, long, long))
	}
	axe := fmt.Sprintf(`{"violations":[{"id":"color-contrast","impact":"%s","help":"%s","helpUrl":"%s","tags":["%s","b","c","d","e","f","g","h","i","j","k","l","m","n"],"nodeCount":99,"nodes":[%s]}]}`,
		long, long, long, long, strings.Join(nodes, ","))

	findings, err := A11yFindings([]byte(axe))
	if err != nil {
		t.Fatalf("A11yFindings: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(findings))
	}
	var got axeViolation
	if err := json.Unmarshal([]byte(findings[0].Detail), &got); err != nil {
		t.Fatalf("truncated detail is not valid JSON: %v", err)
	}
	if got.ID != "color-contrast" {
		t.Errorf("truncation lost the rule id: %q", got.ID)
	}
	if len(got.Nodes) > maxA11yNodes {
		t.Errorf("nodes = %d, want <= %d", len(got.Nodes), maxA11yNodes)
	}
	if len(got.Help) > maxHelpTextLen {
		t.Errorf("help = %d bytes, want <= %d", len(got.Help), maxHelpTextLen)
	}
	if len(got.HelpURL) > maxURLLen {
		t.Errorf("helpUrl = %d bytes, want <= %d", len(got.HelpURL), maxURLLen)
	}
	if len(got.Tags) > maxA11yTags {
		t.Errorf("tags = %d, want <= %d", len(got.Tags), maxA11yTags)
	}
	for _, tag := range got.Tags {
		if len(tag) > maxTagLen {
			t.Errorf("tag = %d bytes, want <= %d (count was capped but each tag was not)", len(tag), maxTagLen)
		}
	}
	// Impact is bounded in BOTH places it is emitted: the detail and the severity.
	if len(got.Impact) > maxImpactLen {
		t.Errorf("detail impact = %d bytes, want <= %d", len(got.Impact), maxImpactLen)
	}
	if len(findings[0].Severity) > maxImpactLen {
		t.Errorf("severity = %d bytes, want <= %d (untruncated impact leaked into severity)", len(findings[0].Severity), maxImpactLen)
	}
	if got.NodeCount != 99 {
		t.Errorf("nodeCount = %d, want the true count 99 preserved", got.NodeCount)
	}
	for _, n := range got.Nodes {
		if len(n.HTML) > maxNodeHTMLLen {
			t.Errorf("node html = %d bytes, want <= %d", len(n.HTML), maxNodeHTMLLen)
		}
		if len(n.Target) > maxNodeTargets {
			t.Errorf("node targets = %d, want <= %d", len(n.Target), maxNodeTargets)
		}
		for _, s := range n.Target {
			if len(s) > maxSelectorLen {
				t.Errorf("selector = %d bytes, want <= %d", len(s), maxSelectorLen)
			}
		}
	}
	if !utf8.ValidString(findings[0].Detail) {
		t.Error("truncated a11y detail is not valid UTF-8")
	}

	// Console + network detail text is capped too.
	cf := ConsoleFindings([]consoleEvent{{Text: long, URL: long, FirstPart: true}})
	if len(cf) != 1 || len(cf[0].Detail) > maxDetailTextLen+maxURLLen+16 {
		t.Errorf("console detail not truncated: %d bytes", len(cf[0].Detail))
	}
	nf := NetworkFindings([]networkEvent{{URL: long, Reason: long, FirstPart: true}})
	if len(nf) != 1 || len(nf[0].Detail) > maxDetailTextLen+maxURLLen+16 {
		t.Errorf("network detail not truncated: %d bytes", len(nf[0].Detail))
	}

	// Multi-byte truncation must land on a rune boundary. "→" is 3 bytes and
	// maxDetailTextLen (500) is NOT a multiple of 3, so a naive s[:500] would slice
	// mid-rune and yield invalid UTF-8 — which json.Marshal then silently rewrites to
	// U+FFFD, corrupting the pushed detail. This assertion therefore fails on a naive
	// truncation, not just on paper.
	multi := strings.Repeat("→", 3000)
	mf := ConsoleFindings([]consoleEvent{{Text: multi, FirstPart: true}})
	if len(mf) != 1 {
		t.Fatalf("got %d console findings, want 1", len(mf))
	}
	if !utf8.ValidString(mf[0].Detail) {
		t.Error("multi-byte truncation produced INVALID UTF-8 (sliced mid-rune)")
	}
	if strings.ContainsRune(mf[0].Detail, '�') {
		t.Error("multi-byte truncation produced a U+FFFD replacement char")
	}
	// Marshaling must not introduce a replacement char either (the wire path).
	enc, err := json.Marshal(mf[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(enc), '�') {
		t.Error("marshaling the truncated detail introduced U+FFFD (it was not valid UTF-8)")
	}
}

// ---------------------------------------------------------------------------
// BuildPayload integration.
// ---------------------------------------------------------------------------

// synthCapturesWithSignals is a synthetic capture set carrying REAL signal shapes (a
// populated axe result + first-party AND third-party console/network events) so the
// BuildPayload integration test exercises the whole mapping without a browser.
func synthCapturesWithSignals() []CapturedView {
	var out []CapturedView
	for _, name := range []string{"dashboard", "run-missing-models"} {
		for _, vp := range Viewports {
			out = append(out, CapturedView{
				View:     View{Name: name},
				Viewport: vp,
				Capture: ViewCapture{
					ScreenshotPNG:     tinyPNG,
					AxeJSON:           []byte(realisticAxeJSON),
					NetworkJSON:       []byte(`[]`),
					AxeViolations:     2,
					ConsoleFirstParty: 1,
					ConsoleThirdParty: 2,
					NetworkFirstParty: 1,
					NetworkThirdParty: 3,
					Console: []consoleEvent{
						{Text: "TypeError: x is not a function", URL: "http://127.0.0.1:9/app.js", FirstPart: true},
						{Text: "cdn boom", URL: "https://cdn.example/a.js", FirstPart: false},
						{Text: "pixel boom", URL: "https://px.example/p.gif", FirstPart: false},
					},
					Network: []networkEvent{
						{URL: "http://127.0.0.1:9/api/models", Status: 500, FirstPart: true},
						{URL: "https://cdn.example/1", Status: 404, FirstPart: false},
					},
				},
			})
		}
	}
	return out
}

// TestBuildPayloadEmitsFindingsAndPreservesCounts is the end-to-end mapping
// assertion: every page carries findings (a11y rules + first-party console/network
// only), the ROLL-UP COUNTS keep their existing values/meaning, no perf/layout
// findings are emitted, the recovered a11y rule set is non-empty (the CI gate works),
// and the resulting payload still passes Validate.
func TestBuildPayloadEmitsFindingsAndPreservesCounts(t *testing.T) {
	caps := synthCapturesWithSignals()
	payload, files, err := BuildPayload("findings integration", caps)
	if err != nil {
		t.Fatalf("BuildPayload: %v", err)
	}

	if len(payload.Pages) != len(caps) {
		t.Fatalf("pages = %d, want %d", len(payload.Pages), len(caps))
	}
	for i, pg := range payload.Pages {
		src := caps[i].Capture

		// Counts must be untouched by the findings work.
		if pg.AxeViolations != src.AxeViolations ||
			pg.ConsoleFirstParty != src.ConsoleFirstParty ||
			pg.ConsoleThirdParty != src.ConsoleThirdParty ||
			pg.NetworkFirstParty != src.NetworkFirstParty ||
			pg.NetworkThirdParty != src.NetworkThirdParty {
			t.Errorf("page %d roll-up counts changed: %+v", i, pg)
		}

		// 2 a11y rules + 1 first-party console + 1 first-party network = 4.
		if len(pg.Findings) != 4 {
			t.Fatalf("page %d: %d findings, want 4 (2 a11y rules + 1 console + 1 network)", i, len(pg.Findings))
		}
		byType := map[string]int{}
		for _, f := range pg.Findings {
			byType[f.Type]++
			if f.Type == "perf" || f.Type == "layout" {
				t.Errorf("page %d: emitted a %q finding — auditloop computes those server-side", i, f.Type)
			}
			if strings.Contains(f.Detail, "cdn.example") || strings.Contains(f.Detail, "px.example") {
				t.Errorf("page %d: third-party event leaked into a finding: %q", i, f.Detail)
			}
		}
		if byType[FindingA11y] != 2 || byType[FindingConsole] != 1 || byType[FindingNetwork] != 1 {
			t.Errorf("page %d: finding mix = %v", i, byType)
		}
		// a11y finding count matches the axe_violations rule count (consistent semantics).
		if byType[FindingA11y] != pg.AxeViolations {
			t.Errorf("page %d: %d a11y findings vs axe_violations = %d", i, byType[FindingA11y], pg.AxeViolations)
		}
		// The gate input is populated for every page.
		if ids := auditloopRuleIDs(pg.Findings); len(ids) != 2 {
			t.Errorf("page %d: auditloop would recover rule ids %v, want 2", i, ids)
		}
	}

	// The payload with findings must still satisfy the push contract.
	if err := payload.Validate(setOf(files)); err != nil {
		t.Fatalf("payload with findings failed Validate: %v", err)
	}

	// The a11y detail must arrive on the wire as a JSON *string* whose contents parse
	// into an object with a top-level id. Decoding into an INDEPENDENT struct (not our
	// own PushPayload) is what makes this non-vacuous: round-tripping our type through
	// our own type structurally cannot fail, whereas this asserts the actual wire
	// encoding — `"detail": "{\"id\":\"…\"}"` — which is what auditloop's server reads.
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Pages []struct {
			Findings []struct {
				Type   string `json:"type"`
				Detail string `json:"detail"` // MUST be a JSON string on the wire
			} `json:"findings"`
		} `json:"pages"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("wire payload did not decode into the server's field shape (detail must be a string): %v", err)
	}
	a11ySeen := 0
	for _, f := range wire.Pages[0].Findings {
		if f.Type != FindingA11y {
			continue
		}
		a11ySeen++
		if _, ok := topLevelID(f.Detail); !ok {
			t.Errorf("a11y detail is not an id-carrying object once decoded from the wire: %s", f.Detail)
		}
	}
	if a11ySeen == 0 {
		t.Error("no a11y findings survived to the wire")
	}
}

// TestA11yFindingsAgainstRealAxe is the LIVE-BROWSER proof of the gate-critical path.
//
// It exists because civitai-manager's own UI is currently a11y-CLEAN (0 axe violations
// across all 14 captures on v0.1.79 — the contrast violations were fixed), so the real
// walk cannot demonstrate a11y finding emission. This test serves a deliberately
// INACCESSIBLE fixture page over loopback, captures it through the SAME
// Browser.CaptureWith path the walk uses (real Chromium, real vendored axe-core), and
// asserts the resulting findings carry the top-level rule ids auditloop's
// `new_a11y_rules` delta needs. It proves the plumbing end to end on real axe output,
// not just on a hand-written fixture.
//
// Double-gated exactly like TestUXAuditWalk (UXAUDIT_WALK + a resolvable Chromium), so
// a plain `go test ./...` skips it cleanly and never depends on a working browser.
func TestA11yFindingsAgainstRealAxe(t *testing.T) {
	if os.Getenv("UXAUDIT_WALK") == "" {
		t.Skip("set UXAUDIT_WALK=1 to run the real-Chromium axe assertion (make ux-audit)")
	}
	execPath := ResolveChromium()
	if execPath == "" {
		t.Skip("no chromium found: set AUDITLOOP_CHROMIUM or put chromium on PATH")
	}

	// A page with violations axe reliably reports: an image with no alt text, an empty
	// table header, and an unlabeled form input.
	const badHTML = `<!doctype html><html lang="en"><head><title>bad</title></head><body>
<img src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==">
<table><tr><th></th><th>ok</th></tr><tr><td>1</td><td>2</td></tr></table>
<input type="text">
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(badHTML))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	br, err := NewBrowser(ctx, execPath)
	if err != nil {
		t.Fatalf("launch chromium: %v", err)
	}
	defer br.Close()

	vc, err := br.Capture(srv.URL+"/", Viewports[1]) // desktop
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if vc.AxeViolations == 0 {
		t.Fatalf("real axe found no violations on a deliberately inaccessible page — the scan is not working; axe json: %s", truncate(string(vc.AxeJSON), 400))
	}

	findings, err := A11yFindings(vc.AxeJSON)
	if err != nil {
		t.Fatalf("A11yFindings on real axe output: %v", err)
	}
	if len(findings) != vc.AxeViolations {
		t.Errorf("emitted %d a11y findings for %d violated rules — want one per RULE", len(findings), vc.AxeViolations)
	}
	ids := auditloopRuleIDs(findings)
	if len(ids) == 0 {
		t.Fatal("auditloop would recover NO rule ids from real axe output — new_a11y_rules would be empty and the CI a11y gate a no-op")
	}
	t.Logf("real axe → %d rules, ids auditloop recovers: %v", len(findings), ids)

	// image-alt is the most reliable of the three; assert it specifically so the test
	// is anchored to a known rule rather than "some id came back".
	found := false
	for _, id := range ids {
		if id == "image-alt" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the image-alt rule among %v", ids)
	}

	// Every finding must decode to an object with a non-empty top-level id.
	for i, f := range findings {
		if _, ok := topLevelID(f.Detail); !ok {
			t.Errorf("finding %d from REAL axe output lacks a top-level id: %s", i, f.Detail)
		}
	}
}

// TestDecodeConsoleArg asserts a CDP console argument renders as clean display text:
// a JSON string is unquoted AND unescaped (the live walk's real error message
// contains embedded quotes — `The selector "#run-modes select" …` — which the old
// outer-quote-trim left as literal \" noise in the shipped finding detail), while
// non-string args fall back to their JSON form.
func TestDecodeConsoleArg(t *testing.T) {
	cases := []struct{ raw, want string }{
		// The REAL message the live walk captures on run-missing-models.
		{`"The selector \"#run-modes select\" on hx-include returned no matches!"`,
			`The selector "#run-modes select" on hx-include returned no matches!`},
		{`"plain"`, "plain"},
		{`"tab\tand\nnewline"`, "tab\tand\nnewline"},
		{`42`, "42"},
		{`{"a":1}`, `{"a":1}`},
		{`null`, "null"},
	}
	for _, tc := range cases {
		if got := decodeConsoleArg([]byte(tc.raw)); got != tc.want {
			t.Errorf("decodeConsoleArg(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	// Non-vacuity: the naive outer-quote trim would leave the escapes behind.
	naive := strings.Trim(`"The selector \"#x\" matched"`, `"`)
	if !strings.Contains(naive, `\"`) {
		t.Fatal("expected the naive trim to leave literal backslash-escapes (assertion would be vacuous)")
	}
	if strings.Contains(decodeConsoleArg([]byte(`"The selector \"#x\" matched"`)), `\"`) {
		t.Error("decoded text still carries literal backslash-escapes")
	}
}

// TestCaptureFindingsEmptyCaptureIsNil asserts a genuinely clean view emits no
// findings (so a clean page is reported clean, rather than carrying empty noise).
func TestCaptureFindingsEmptyCaptureIsNil(t *testing.T) {
	got, err := CaptureFindings(ViewCapture{AxeJSON: []byte(`{"violations":[]}`)})
	if err != nil {
		t.Fatalf("CaptureFindings: %v", err)
	}
	if got != nil {
		t.Fatalf("clean capture emitted %d findings, want none", len(got))
	}
}
