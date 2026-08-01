package web

import (
	"regexp"
	"strings"
	"testing"
)

// TestBrandLinkIsAnInlineSVGToHome pins the four properties the brand mark was
// built for: it is INLINE markup (offline invariant), it goes to "/", it is drawn
// with currentColor rather than a literal (theme invariant), and it renders at
// nav size.
func TestBrandLinkIsAnInlineSVGToHome(t *testing.T) {
	body := renderString(t, brandLink())

	if !strings.HasPrefix(body, `<a href="/"`) {
		t.Errorf("the brand must be ONE link to /:\n%s", body)
	}
	if !strings.Contains(body, "<svg") {
		t.Errorf("the brand mark must be an INLINE <svg> — an <img>/CDN font would break the offline invariant:\n%s", body)
	}
	// No external reference of any kind: an <img src>, a <use href> into another
	// document, or a url() would each be a fetch the offline invariant forbids.
	for _, forbidden := range []string{"<img", "<use", "url(", "http://", "https://"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the brand mark must fetch nothing, found %q:\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, `stroke="currentColor"`) || !strings.Contains(body, `fill="currentColor"`) {
		t.Errorf("the mark must be painted with currentColor so it inherits the themed link colour:\n%s", body)
	}
	// Sized explicitly so it cannot collapse to 0×0 or inflate the bar when the
	// stylesheet has not yet applied.
	if !strings.Contains(body, `width="22" height="22"`) {
		t.Errorf("the mark must carry intrinsic nav-height dimensions:\n%s", body)
	}
}

// hexLiteral matches a CSS/SVG hex colour (#abc or #aabbcc).
var hexLiteral = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)

// TestBrandMarkHasNoHardcodedColour is the theme guard. A literal in the mark
// would freeze it to one data-theme — the exact failure the --civitai-* token
// system exists to prevent, and one that no server-side markup test would notice
// unless it looks for the literal itself.
//
// The FAVICON is deliberately out of scope and lives in a separate file: it
// renders in browser chrome where there is no cascade, so `currentColor` there
// resolves to black. See brand.go.
func TestBrandMarkHasNoHardcodedColour(t *testing.T) {
	// The fixture must actually contain the geometry, or "no hex found" would be
	// true for the boring reason that there is nothing to inspect.
	if !strings.Contains(brandMarkSVG, "<path") {
		t.Fatalf("fixture is wrong: brandMarkSVG carries no geometry:\n%s", brandMarkSVG)
	}
	if m := hexLiteral.FindAllString(brandMarkSVG, -1); len(m) > 0 {
		t.Errorf("the brand mark carries hardcoded colour literals %v — it must inherit currentColor:\n%s", m, brandMarkSVG)
	}
	// Nor may it name a raw CSS colour keyword. `none` and `currentColor` are the
	// only two colour-position values allowed here.
	for _, kw := range []string{`"white"`, `"black"`, `"#fff"`, `rgb(`, `hsl(`} {
		if strings.Contains(brandMarkSVG, kw) {
			t.Errorf("the brand mark must not name a literal colour (%q):\n%s", kw, brandMarkSVG)
		}
	}
}

// TestBrandLinkHasExactlyOneAccessibleName guards against the double-announce
// bug: the link's name comes from its VISIBLE wordmark, so an aria-label or an
// exposed <title> inside the <svg> would make a screen reader say the app name
// twice.
//
// The two wordmark spans are `hidden sm:inline` / `sm:hidden`, i.e. display:none
// at the opposite breakpoint — and a display:none subtree is excluded from the
// accessible-name computation, so exactly one contributes at any viewport. That
// is why TWO spans is not two names.
func TestBrandLinkHasExactlyOneAccessibleName(t *testing.T) {
	body := renderString(t, brandLink())

	if strings.Contains(body, "aria-label") {
		t.Errorf("the brand link must take its name from the visible wordmark, not an aria-label:\n%s", body)
	}
	if strings.Contains(body, "<title") {
		t.Errorf("the <svg> must not carry a <title> — the link already has a visible name:\n%s", body)
	}
	if strings.Contains(body, "h.Title") || strings.Contains(body, " title=") {
		t.Errorf("the brand link must not carry a title attribute either:\n%s", body)
	}
	// The mark itself must be OUT of the accessibility tree, and out of the tab
	// order (IE/Edge legacy make an <svg> focusable by default).
	if !strings.Contains(body, `aria-hidden="true"`) || !strings.Contains(body, `focusable="false"`) {
		t.Errorf("the <svg> must be aria-hidden and non-focusable:\n%s", body)
	}
	// Exactly the two breakpoint variants of the wordmark, no more: a third
	// visible text node inside the anchor would concatenate into the name.
	if n := strings.Count(body, "cm-brand-name"); n != 2 {
		t.Errorf("expected exactly 2 wordmark spans (one per breakpoint), got %d:\n%s", n, body)
	}
	if !strings.Contains(body, `class="cm-brand-name hidden sm:inline">`+brandName+`<`) {
		t.Errorf("the wide wordmark must be the full name and hidden below sm:\n%s", body)
	}
	if !strings.Contains(body, `class="cm-brand-name sm:hidden">`+brandNameShort+`<`) {
		t.Errorf("the narrow wordmark must be the short name and hidden at sm and up:\n%s", body)
	}
}

// TestFaviconIsEmbeddedAndLinked pins the tab icon end to end: the file is in the
// embedded FS (so it is served offline by the existing /assets FileServer) and
// the document head points at it.
func TestFaviconIsEmbeddedAndLinked(t *testing.T) {
	raw, err := assetsFS.ReadFile("assets/favicon.svg")
	if err != nil {
		t.Fatalf("favicon.svg is not in the go:embed set (assets.go): %v", err)
	}
	if !strings.Contains(string(raw), "<svg") || !strings.Contains(string(raw), "<path") {
		t.Errorf("the embedded favicon is not a drawable SVG:\n%s", raw)
	}
	// It must be self-contained: a favicon that referenced anything external
	// would be a fetch from browser chrome, outside every page-level guard.
	//
	// The xmlns declaration is stripped first — `xmlns="http://www.w3.org/2000/svg"`
	// is a NAMESPACE IDENTIFIER, never fetched, and it is mandatory for a
	// standalone SVG document. Without this the URL check below could not pass at
	// all, which is a test that fails at nothing rather than one that guards.
	body := strings.ReplaceAll(string(raw), `xmlns="http://www.w3.org/2000/svg"`, "")
	if strings.Contains(body, "xmlns") {
		t.Fatalf("the favicon carries an unexpected second xmlns; the URL check below would be inspecting it:\n%s", raw)
	}
	for _, forbidden := range []string{"http://", "https://", "<image", "<use"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("the favicon must reference nothing external, found %q", forbidden)
		}
	}

	head := renderString(t, page("X", "csrf", fullMaturityRange(), railData{}))
	if !strings.Contains(head, `<link rel="icon" type="image/svg+xml" href="`+faviconHref+`">`) {
		t.Errorf("the document head must declare the favicon:\n%s", firstN(head, 1200))
	}
}
