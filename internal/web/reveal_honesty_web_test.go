package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// missingFolderGlyph is U+1F5C0 FOLDER — the codepoint the "open containing
// folder" button used to render as TEXT. It has no glyph in the common Linux font
// stacks, so the control shipped as a tofu box. It is spelled as an escape here on
// purpose: a literal 🗀 in this file would be indistinguishable from tofu in a
// diff, which is how it survived review in the first place.
const missingFolderGlyph = "\U0001F5C0"

// TestHasGraphicalSession pins the pre-launch guard.
//
// 🔴 EVERY CASE INJECTS ITS ENVIRONMENT. The whole point of hasGraphicalSession
// taking an env func is that this test's verdict cannot depend on whether the
// machine running `go test` has DISPLAY set — which is exactly the accident that
// made the bug invisible for so long (it reproduces on a dogfood instance started
// from a non-graphical shell and not on a desktop one).
func TestHasGraphicalSession(t *testing.T) {
	envOf := func(kv map[string]string) func(string) string {
		return func(k string) string { return kv[k] }
	}
	tests := []struct {
		name string
		goos string
		env  map[string]string
		want bool
	}{
		{
			name: "linux with neither display variable — nothing can appear",
			goos: "linux",
			env:  map[string]string{},
			want: false,
		},
		{
			name: "linux with X11",
			goos: "linux",
			env:  map[string]string{"DISPLAY": ":0"},
			want: true,
		},
		{
			name: "linux with Wayland only",
			goos: "linux",
			env:  map[string]string{"WAYLAND_DISPLAY": "wayland-0"},
			want: true,
		},
		{
			// An exported-but-empty DISPLAY is what a stripped systemd unit or an
			// `env DISPLAY= ` leaves behind. Treating "set" as "usable" would put the
			// false-success back for exactly the deployments this guard is for.
			name: "linux with a blank DISPLAY is still headless",
			goos: "linux",
			env:  map[string]string{"DISPLAY": "   "},
			want: false,
		},
		{
			// darwin/windows talk to a session window server, not to a socket named
			// by an env var. Reading DISPLAY there would refuse the control on
			// machines where it works.
			name: "darwin needs no display variable",
			goos: "darwin",
			env:  map[string]string{},
			want: true,
		},
		{
			name: "windows needs no display variable",
			goos: "windows",
			env:  map[string]string{},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasGraphicalSession(tc.goos, envOf(tc.env)); got != tc.want {
				t.Fatalf("hasGraphicalSession(%q, %v) = %v, want %v", tc.goos, tc.env, got, tc.want)
			}
		})
	}
}

// TestRevealRefusesWhenTheServerHasNoGraphicalSession is the false-success bug
// itself, reproduced at the HTTP level.
//
// Before the fix this POST returned
//
//	<span class="cm-res-open-msg" data-state="ok">Opened on the computer running
//	civitai-manager. (/home/…/models/sams)</span>
//
// on a server with no display, having launched xdg-open into a void that exits 0.
// Two things are asserted, and they are different failures:
//
//  1. NOTHING WAS EXECUTED. A refusal that still shelled out would be a cosmetic
//     fix — the process would still spawn, still find no handler, still exit 0.
//  2. The response does not claim success, and says WHY.
func TestRevealRefusesWhenTheServerHasNoGraphicalSession(t *testing.T) {
	root := t.TempDir()
	srv, op := newRevealServer(t, root)
	// Override the pinned seam: this server is the headless one.
	srv.graphicalFn = func() bool { return false }
	id := seedFile(t, srv, filepath.Join(root, "loras", "a.safetensors"))

	rec := post(t, srv, "/library/files/"+strconv.FormatInt(id, 10)+"/reveal", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d, body=%s", rec.Code, rec.Body.String())
	}
	if calls := op.calls(); len(calls) != 0 {
		t.Fatalf("the opener ran %d time(s) with no graphical session — it must be refused BEFORE launching: %#v",
			len(calls), calls)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "no graphical session") {
		t.Errorf("the outcome must say the server machine has no graphical session:\n%s", body)
	}
	if !strings.Contains(body, `data-state="error"`) {
		t.Errorf("a refusal must not render as the success state:\n%s", body)
	}
	// The exact false claim that shipped.
	if strings.Contains(body, "Opened on the computer") {
		t.Errorf("the response still claims a window opened:\n%s", body)
	}
	// The folder is still named — it is the fact the user can act on.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, filepath.Join(realRoot, "loras")) {
		t.Errorf("the refusal should still name the folder:\n%s", body)
	}
}

// TestRevealDoesNotAssertAWindowOpened guards the OTHER half of the same bug: even
// on the success path nothing has been observed.
//
// startFileManager reports from cmd.Start(), and xdg-open exits 0 with no handler
// installed at all — so "a process started" is the strongest true statement
// available, and the wording must not exceed it. This is a wording guard on
// purpose: the code and the message have to agree, and the message is the only
// part the user ever sees.
func TestRevealDoesNotAssertAWindowOpened(t *testing.T) {
	root := t.TempDir()
	srv, op := newRevealServer(t, root) // graphicalFn pinned true
	id := seedFile(t, srv, filepath.Join(root, "loras", "a.safetensors"))

	rec := post(t, srv, "/library/files/"+strconv.FormatInt(id, 10)+"/reveal", url.Values{}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d, body=%s", rec.Code, rec.Body.String())
	}
	// The fixture must reach the interesting case: the opener really was invoked,
	// so this is the success branch and not a silent refusal.
	if calls := op.calls(); len(calls) != 1 {
		t.Fatalf("opener called %d times, want 1 — this test is not on the success path", len(calls))
	}

	body := rec.Body.String()
	if strings.Contains(body, "Opened on the computer") {
		t.Errorf("the success message asserts an unobservable outcome — a started process is not an opened window:\n%s", body)
	}
	if !strings.Contains(body, "Asked the desktop") {
		t.Errorf("the success message must describe what was actually done:\n%s", body)
	}
	if !strings.Contains(body, `data-state="ok"`) {
		t.Errorf("the success path should still render the ok state:\n%s", body)
	}
}

// TestOpenFolderControlUsesAnInlineSVGIcon pins the icon fix.
//
// The old rendering was g.Text("\U0001F5C0") — a codepoint with no glyph in the
// common Linux font stacks, which paints a tofu box rather than a folder. The
// assertion is two-sided: the codepoint must be GONE (a fix that added an SVG
// beside the glyph would leave the tofu in place) and a real <svg> must be
// present.
func TestOpenFolderControlUsesAnInlineSVGIcon(t *testing.T) {
	out := renderString(t, resourceOpenControl(444, "csrf-tok", "", ""))

	if strings.Contains(out, missingFolderGlyph) {
		t.Errorf("the U+1F5C0 folder glyph is still rendered — it has no glyph in most Linux font stacks:\n%s", out)
	}
	if !strings.Contains(out, "<svg") || !strings.Contains(out, "cm-res-open-ico") {
		t.Errorf("the folder icon must be an inline SVG carrying .cm-res-open-ico:\n%s", out)
	}
	// The icon is decorative; the button's aria-label is the accessible name.
	if !strings.Contains(out, `aria-hidden="true"`) {
		t.Errorf("the icon must stay aria-hidden:\n%s", out)
	}
	if !strings.Contains(out, openFolderTitle) {
		t.Errorf("the button must keep its aria-label/title accessible name:\n%s", out)
	}
}

// TestNoMissingFolderGlyphInTemplates stops the codepoint coming back ANYWHERE in
// the package, not just in the one control it was found in. A glyph with no font
// coverage is invisible in a diff, so a human reviewer is the wrong instrument.
func TestNoMissingFolderGlyphInTemplates(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || name == "reveal_honesty_web_test.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		scanned++
		if strings.Contains(string(src), missingFolderGlyph) {
			t.Errorf("%s contains U+1F5C0, which renders as tofu on most Linux font stacks — use an inline SVG", name)
		}
	}
	// A scan that read nothing would pass vacuously.
	if scanned < 50 {
		t.Fatalf("only scanned %d .go files — the scan is broken, not the templates", scanned)
	}
}
