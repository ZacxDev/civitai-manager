package web

import (
	"strings"
	"testing"
)

// TestDescriptionHeadingsAreDemoted guards a defect that was LIVE in production and
// that the existing heading audit structurally could not see.
//
// THE BUG. bluemonday's UGCPolicy allows h1–h6, so a CivitAI model description
// injects its own <h1> into our page. Measured on the running instance at v0.1.97:
// /models/1386234 and /models/4384 each emitted TWO <h1>s, one of them arbitrary
// remote text. Descriptions render inside a card under an <h2>, so <h3> is the level
// that nests correctly.
//
// 🔴 WHY THE EXISTING GUARD MISSED IT — this is the fixture-cannot-reach-the-case
// mode, and it is why this test exists separately. TestEveryFullPageHasExactlyOneH1
// (ux_audit_web_test.go) walks real pages and counts <h1>, which is exactly the right
// assertion — but its model-page fixture has an EMPTY description, so the injected
// heading can never appear in what it measures. A correct assertion over a fixture
// that cannot reach the interesting input is green forever.
//
// The cases below are therefore chosen to reach it: a heading WITH an attribute, an
// uppercase tag, and a close tag — each of which a naive fix mishandles.
func TestDescriptionHeadingsAreDemoted(t *testing.T) {
	for _, c := range []struct {
		name, raw, want, why string
	}{
		{
			"bare h1", "<h1>Title</h1>", "<h3>Title</h3>",
			"the simple case a string replace would also catch",
		},
		{
			// 🔴 The case that breaks `strings.ReplaceAll(s, "<h1>", "<h3>")`.
			// bluemonday's own policies.go comment claims h1–h6 "take no attributes";
			// measured against v1.0.27 that is FALSE — `id` survives sanitization:
			//   <h1 class="x" id="y" onclick="alert(1)">  ->  <h1 id="y">
			// Rich descriptions carry attributes routinely, so this is the common case,
			// not the exotic one.
			"h1 with a surviving attribute",
			`<h1 class="x" id="y" onclick="alert(1)">Attrs</h1>`,
			`<h3 id="y">Attrs</h3>`,
			"the attribute must be preserved and the tag name still rewritten",
		},
		{
			"uppercase is normalised then demoted", "<H1>Upper</H1>", "<h3>Upper</h3>",
			"bluemonday lowercases tags, so the rewrite sees h1 either way",
		},
		{
			"h2 demotes too", "<h2>Section</h2>", "<h3>Section</h3>",
			"an <h2> would compete with the page's own section headings",
		},
		{
			"h3 and below are left alone", "<h3>Three</h3><h4>Four</h4>",
			"<h3>Three</h3><h4>Four</h4>",
			"they already nest under the page outline; rewriting them would flatten structure",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeDescription(c.raw)
			if got != c.want {
				t.Errorf("sanitizeDescription(%q)\n got: %q\nwant: %q\n%s", c.raw, got, c.want, c.why)
			}
		})
	}
}

// TestSanitizedDescriptionNeverEmitsATopLevelHeading is the invariant the page
// actually depends on, asserted independently of the exact rewrite above: whatever a
// description contains, it must not contribute an <h1> or <h2> to the page.
//
// It is deliberately a SEPARATE test from the table. The table pins the mapping and
// would go green again if someone "fixed" it by mapping h1 -> h2; this one would not.
func TestSanitizedDescriptionNeverEmitsATopLevelHeading(t *testing.T) {
	raw := `<h1>One</h1><p>text</p><h2 id="k">Two</h2><h1 class="c">Again</h1>`
	got := sanitizeDescription(raw)

	// Fixture reach: the input really does carry the headings under test, so a
	// sanitizer that silently dropped them entirely could not pass this by accident.
	if !strings.Contains(got, "One") || !strings.Contains(got, "Two") || !strings.Contains(got, "Again") {
		t.Fatalf("the heading TEXT must survive demotion — demoting is not deleting. got: %q", got)
	}
	for _, banned := range []string{"<h1", "<h2", "</h1", "</h2"} {
		if strings.Contains(got, banned) {
			t.Errorf("a sanitized description emitted %q, which competes with the page's own "+
				"heading outline (the page <h1> and its <h2> section titles). got: %q",
				banned, got)
		}
	}
}
