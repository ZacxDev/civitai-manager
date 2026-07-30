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
	return pageShell(get(t, srv, "/").Body.String())
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

	shell := pageShell(get(t, srv, "/").Body.String())
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

// TestRailExpandControlIsDesktopOnly guards the mobile drawer against the
// overlay, against the REAL shipped CSS rather than a copy of it. Below 1024px
// the rail is an off-canvas drawer where data-collapsed carries no meaning; a
// displayed full-height overlay there would cover the drawer's tiles.
func TestRailExpandControlIsDesktopOnly(t *testing.T) {
	b, err := assetsFS.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(b)

	base := strings.Index(css, ".cm-rail-expand {")
	if base < 0 {
		t.Fatal("app.css has no base .cm-rail-expand rule")
	}
	if !strings.Contains(css[base:base+64], "display: none") {
		t.Errorf("the base .cm-rail-expand rule must be display:none (mobile drawer); got %q", css[base:base+64])
	}

	desktop := strings.Index(css, "@media (min-width: 1024px)")
	if desktop < 0 {
		t.Fatal("app.css has no 1024px desktop block")
	}
	shown := strings.Index(css, `.cm-rail[data-collapsed="true"] .cm-rail-expand {`)
	if shown < 0 {
		t.Fatal("app.css never displays .cm-rail-expand in the collapsed desktop state")
	}
	if shown < desktop {
		t.Error("the .cm-rail-expand display rule is OUTSIDE the desktop media block — it " +
			"would overlay the mobile drawer's tiles")
	}
}
