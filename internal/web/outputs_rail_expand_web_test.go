package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The COLLAPSED rail edge is ONE big operable control (railExpandControl).
// ---------------------------------------------------------------------------

// elemWithClass returns the full markup of the element carrying `class="<cls>"`,
// from its opening `<` through the first matching close tag. It is deliberately
// dumb — the rail's markup is flat and generated, so scanning back to `<` and
// forward to the first `</tag>` is sufficient and needs no HTML parser.
func elemWithClass(body, cls, closeTag string) string {
	i := strings.Index(body, `class="`+cls+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<")
	if start < 0 {
		return ""
	}
	j := strings.Index(body[start:], closeTag)
	if j < 0 {
		return body[start:]
	}
	return body[start : start+j+len(closeTag)]
}

// collapseRailState flips the persisted rail state to collapsed and returns the
// re-rendered shell.
func collapseRailState(t *testing.T, srv *Server) string {
	t.Helper()
	rec := post(t, srv, "/settings/outputs-rail", url.Values{"collapsed": {"true"}}, true)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("collapse POST status = %d, want 204", rec.Code)
	}
	return pageShell(get(t, srv, librarySubscriptionsHref).Body.String())
}

// TestCollapsedRailEdgeIsOneOperableControl pins the collapsed edge: a single
// real <button> spanning the strip, carrying the same CSRF-protected POST as the
// head's collapse control.
func TestCollapsedRailEdgeIsOneOperableControl(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	shell := collapseRailState(t, srv)

	btn := elemWithClass(shell, "cm-rail-expand", "</button>")
	if btn == "" {
		if !strings.Contains(shell, "cm-rail-expand") {
			t.Fatalf("the collapsed rail has no .cm-rail-expand control at all; shell = %q",
				firstN(shell, 900))
		}
		t.Fatalf(".cm-rail-expand is present but resolved to no element; shell = %q", firstN(shell, 900))
	}

	// A REAL control, not a clickable wrapper: a native <button> is focusable and
	// Enter/Space-activated with no script at all. A <div onclick> is none of that.
	if !strings.HasPrefix(btn, "<button") {
		t.Errorf(".cm-rail-expand must be a <button> (a clickable div is not keyboard-operable); got %q", btn)
	}
	if !strings.Contains(btn, `type="button"`) {
		t.Errorf("expand control must be type=button; got %q", btn)
	}
	// An accessible name — otherwise AT announces "button" and nothing else.
	if !strings.Contains(btn, `aria-label="Expand recent outputs"`) {
		t.Errorf("expand control needs the accessible name \"Expand recent outputs\"; got %q", btn)
	}
	// Nothing may remove it from the tab order or replace the native activation.
	if strings.Contains(btn, "tabindex") {
		t.Errorf("expand control must not carry a tabindex (a native button is already tabbable); got %q", btn)
	}
	if strings.Contains(btn, "onclick") {
		t.Errorf("expand control must go through htmx + the server, not an inline onclick; got %q", btn)
	}

	// The SAME audited endpoint + CSRF token as the head's collapse button —
	// server-side state answered with HX-Refresh, not a client-side class toggle.
	if !strings.Contains(btn, `hx-post="/settings/outputs-rail"`) {
		t.Errorf("expand control must POST /settings/outputs-rail; got %q", btn)
	}
	if !strings.Contains(btn, `&#34;csrf_token&#34;:&#34;`+srv.csrf+`&#34;`) {
		t.Errorf("expand control must carry the CSRF token in its hx-vals; got %q", btn)
	}
	if !strings.Contains(btn, `&#34;collapsed&#34;:&#34;false&#34;`) {
		t.Errorf("expand control must POST collapsed=false; got %q", btn)
	}

	// The vertical label lives INSIDE the control — the thing naming the edge is
	// the thing you click — and is aria-hidden so the name is announced once.
	if !strings.Contains(btn, "cm-rail-vlabel") {
		t.Errorf("the vertical label must live inside the expand control, not beside it; got %q", btn)
	}
	if !strings.Contains(btn, `aria-hidden="true"`) {
		t.Errorf("the label inside the expand control must be aria-hidden; got %q", btn)
	}

	// The POST it advertises actually works end-to-end.
	if rec := post(t, srv, "/settings/outputs-rail", url.Values{"collapsed": {"false"}}, true); rec.Code != http.StatusNoContent {
		t.Fatalf("the expand POST returned %d, want 204", rec.Code)
	}
	if v, _ := srv.store.GetSettingDefault(outputsRailSettingKey, ""); v != "false" {
		t.Errorf("after the expand POST the setting = %q, want \"false\"", v)
	}
}

// TestExpandedRailBodyIsNotWrappedInAToggle is the other half: when the rail is
// EXPANDED its body is a grid of tile LINKS, so nothing may wrap or overlay it
// with a control that would swallow their clicks.
func TestExpandedRailBodyIsNotWrappedInAToggle(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	shell := pageShell(get(t, srv, librarySubscriptionsHref).Body.String())
	if !strings.Contains(shell, `data-collapsed="false"`) {
		t.Fatalf("precondition: the rail should render expanded; shell = %q", firstN(shell, 400))
	}
	if strings.Contains(shell, "cm-rail-expand") {
		t.Error("the EXPANDED rail must not render the full-edge expand control — it would " +
			"overlay the tiles and eat their clicks")
	}
	if !strings.Contains(shell, "cm-rail-collapse") {
		t.Error("the expanded rail lost its head collapse button")
	}
	if !strings.Contains(shell, `class="cm-rail-item"`) {
		t.Error("the expanded rail lost its tile links")
	}
}

// TestCollapsedRailDropsTheRedundantHeadButton: the collapsed edge has exactly
// ONE expand affordance. Two controls firing the same POST (a full-edge button
// with a smaller one painted on top) is a duplicate tab stop announcing the same
// action twice.
func TestCollapsedRailDropsTheRedundantHeadButton(t *testing.T) {
	srv, root := newOutputsServer(t, "127.0.0.1:8787")
	wf := seedWF(t, srv, "wf")
	seedGen(t, srv, root, &wf, "wf", []byte("X"))

	shell := collapseRailState(t, srv)
	if strings.Contains(shell, "cm-rail-collapse") {
		t.Error("the collapsed edge must not ALSO render the head collapse button — the " +
			"whole edge is already the control")
	}
	// 2 mentions = class="cm-rail-expand" + class="cm-rail-expand-glyph".
	if n := strings.Count(shell, "cm-rail-expand"); n != 2 {
		t.Errorf("expected exactly one expand control (2 class mentions incl. its glyph), got %d", n)
	}
	// The mobile drawer survives: data-collapsed is a DESKTOP concept and must not
	// strip the drawer's only way out.
	if !strings.Contains(shell, "cm-rail-close") {
		t.Error("the collapsed rail lost the mobile drawer's close button")
	}
	if !strings.Contains(shell, `onclick="cmRailDrawer(false)"`) {
		t.Error("the collapsed rail lost the mobile drawer wiring")
	}
}

// cssPrelude normalizes the text between two block delimiters into the selector
// or at-rule prelude it represents: /* comments */ removed, whitespace collapsed
// to single spaces, trimmed.
//
// Stripping comments is not cosmetic. Every block in app.css is preceded by a
// documentation comment, so the raw text before a `{` reads
// "/* Desktop: a real right-hand column … */\n@media (min-width: 1024px)" — which
// compares equal to nothing and made the containment check below fail on
// perfectly correct CSS the first time it ran.
func cssPrelude(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); {
		if strings.HasPrefix(raw[i:], "/*") {
			end := strings.Index(raw[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(raw[i])
		i++
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// enclosingAtRules returns the at-rule preludes (`@media …`, `@supports …`, …)
// whose blocks CONTAIN byte offset pos, outermost first.
//
// 🔴 It exists because "is this rule inside that media query" cannot be answered
// by comparing string indexes. The first version of the test below did exactly
// that — `strings.Index(css, "@media (min-width: 1024px)")` finds the FIRST such
// block in the file (~1000 lines before the rail CSS) and then asserts only
// `shown > that`, which is true for essentially any position in the second half
// of the stylesheet, INCLUDING one outside every media block. It was green
// against the very bug it is named for.
//
// So this tracks BRACE DEPTH, which is the only thing that actually answers the
// question. It skips /* comments */ and quoted strings, because both can contain
// braces and would otherwise desynchronise the depth count.
func enclosingAtRules(css string, pos int) []string {
	var stack []string
	prelStart, i := 0, 0
	for i < len(css) && i < pos {
		if strings.HasPrefix(css[i:], "/*") {
			end := strings.Index(css[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			continue
		}
		switch c := css[i]; c {
		case '"', '\'':
			i++
			for i < len(css) && css[i] != c {
				if css[i] == '\\' {
					i++
				}
				i++
			}
			i++
			continue
		case '{':
			stack = append(stack, cssPrelude(css[prelStart:i]))
			i++
			prelStart = i
			continue
		case '}':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			i++
			prelStart = i
			continue
		case ';':
			i++
			prelStart = i
			continue
		}
		i++
	}
	// Only at-rules are containment-relevant; a plain selector block cannot nest
	// another rule in the CSS this project writes.
	out := make([]string, 0, len(stack))
	for _, s := range stack {
		if strings.HasPrefix(s, "@") {
			out = append(out, s)
		}
	}
	return out
}

// railExpandDesktopSelector is the rule that DISPLAYS the collapsed-edge overlay.
const railExpandDesktopSelector = `.cm-rail[data-collapsed="true"] .cm-rail-expand {`

// desktopMediaPrelude is the ONE at-rule that rule is allowed to live in.
const desktopMediaPrelude = "@media (min-width: 1024px)"

// TestRailExpandControlIsDesktopOnly guards the mobile drawer against the
// overlay, against the REAL shipped CSS rather than a copy of it. Below 1024px
// the rail is an off-canvas drawer where data-collapsed carries no meaning; a
// displayed full-height overlay there would cover the drawer's tiles.
//
// It asserts CONTAINMENT by brace depth (see enclosingAtRules), so it goes red
// for BOTH ways the rule can escape: moved outside every media block, or moved
// into a DIFFERENT one.
func TestRailExpandControlIsDesktopOnly(t *testing.T) {
	b, err := assetsFS.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(b)

	// The mobile-first DEFAULT: display:none, and NOT inside any at-rule (a
	// media-scoped default would leave the overlay displayed everywhere else).
	base := strings.Index(css, ".cm-rail-expand {")
	if base < 0 {
		t.Fatal("app.css has no base .cm-rail-expand rule")
	}
	end := base + 64
	if end > len(css) {
		end = len(css)
	}
	if !strings.Contains(css[base:end], "display: none") {
		t.Errorf("the base .cm-rail-expand rule must be display:none (mobile drawer); got %q", css[base:end])
	}
	if at := enclosingAtRules(css, base); len(at) != 0 {
		t.Errorf("the BASE .cm-rail-expand rule is inside %v — the display:none default must be "+
			"unconditional, or the overlay is displayed wherever that at-rule does not apply", at)
	}

	// The rule that DISPLAYS it must be inside the rail's own desktop media block.
	shown := strings.Index(css, railExpandDesktopSelector)
	if shown < 0 {
		t.Fatal("app.css never displays .cm-rail-expand in the collapsed desktop state")
	}
	at := enclosingAtRules(css, shown)
	if len(at) == 0 {
		t.Fatalf("the .cm-rail-expand display rule is OUTSIDE every at-rule — below 1024px the "+
			"rail is an off-canvas DRAWER, so a displayed full-height overlay would cover its "+
			"tiles and swallow their clicks. It must live inside %q.", desktopMediaPrelude)
	}
	if len(at) != 1 || at[0] != desktopMediaPrelude {
		t.Fatalf("the .cm-rail-expand display rule is enclosed by %v, want exactly [%q] — a "+
			"different media block does not restrict it to the desktop rail", at, desktopMediaPrelude)
	}
}

// TestEnclosingAtRulesActuallyTracksBraces proves the GUARD'S OWN MACHINERY can
// tell the three cases apart, on a fixture where the answer is known by
// construction.
//
// Without this, a bug in enclosingAtRules (say, always returning the first
// at-rule in the file) would make the test above green for the wrong reason —
// which is exactly the failure mode that made its predecessor vacuous.
func TestEnclosingAtRulesActuallyTracksBraces(t *testing.T) {
	const fixture = `
@media (min-width: 1024px) {
  .early { color: red; }
}
/* a comment with braces { } and a "quote */
.outside { content: "}"; }
@media (prefers-reduced-motion: reduce) {
  .wrong-block { display: flex; }
}
@media (min-width: 1024px) {
  @supports (display: grid) {
    .nested { display: grid; }
  }
  .right-block { display: flex; }
}
`
	cases := []struct {
		needle string
		want   []string
	}{
		{".early {", []string{"@media (min-width: 1024px)"}},
		// After the first block CLOSES — the depth must have come back down.
		{".outside {", nil},
		{".wrong-block {", []string{"@media (prefers-reduced-motion: reduce)"}},
		{".right-block {", []string{"@media (min-width: 1024px)"}},
		{".nested {", []string{"@media (min-width: 1024px)", "@supports (display: grid)"}},
	}
	for _, c := range cases {
		pos := strings.Index(fixture, c.needle)
		if pos < 0 {
			t.Fatalf("fixture is missing %q", c.needle)
		}
		got := enclosingAtRules(fixture, pos)
		if len(got) != len(c.want) {
			t.Errorf("%s: enclosed by %v, want %v", c.needle, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: enclosed by %v, want %v", c.needle, got, c.want)
				break
			}
		}
	}
}
