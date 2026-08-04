package uxaudit

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// This file is the BROWSERLESS rot-guard for the fix-model dialog views — the same
// shape and the same rationale as walk_selectors_test.go. `make ux-audit` is
// double-gated out of `go test ./...`, so a selector that stops matching is invisible
// until someone runs the walk by hand; the hero run control sat broken for two
// releases exactly that way.
//
// What it guards: the run-fix-model / run-fix-model-blocked views drive the terminal
// missing-models panel one step further than the heroes do — they CLICK a per-file
// "Choose a model…" trigger and wait for that file's native <dialog> to open. Both the
// trigger selector and the dialog id are second copies of strings owned by
// internal/web (fixModelDialogID + fixModelRow's inline onclick), so they drift
// silently.

// csrfRe extracts the CSRF token from a rendered hx-vals attribute. The token is
// embedded as HTML-escaped JSON (`hx-vals="{&#34;csrf_token&#34;:&#34;…&#34;}"`), not
// as an <input>, which is the same extraction any curl-level reproduction of a click
// on this app has to do.
var csrfRe = regexp.MustCompile(`csrf_token&#34;:&#34;([^&]+)&#34;`)

// labCSRF pulls the server's CSRF token out of the run-control fragment. That
// fragment is the one place guaranteed to carry it for a given workflow, and it is
// already the fragment the run-control guard fetches.
func labCSRF(t *testing.T, baseURL string, wfID int64) string {
	t.Helper()
	body := getBody(t, baseURL+RunControlFragmentPath(wfID))
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token in %s — the run control fragment no longer embeds one, so this "+
			"guard cannot start a run at all (broken FIXTURE, not a stale selector)",
			RunControlFragmentPath(wfID))
	}
	return m[1]
}

func getBody(t *testing.T, u string) string {
	t.Helper()
	resp, err := labClient.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", u, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200\nbody:\n%s", u, resp.StatusCode, truncate(string(raw), 400))
	}
	return string(raw)
}

// runToTerminalPanel starts a real run on the lab server at baseURL and polls the run
// status fragment until the terminal missing-models panel has rendered, returning its
// HTML. Everything is hermetic — the lab's fake ComfyUI answers the run — so this is a
// browserless reproduction of exactly what the hero prep drives in Chromium.
func runToTerminalPanel(t *testing.T, baseURL string, wfID int64, postPath string) string {
	t.Helper()
	csrf := labCSRF(t, baseURL, wfID)

	form := url.Values{"csrf_token": {csrf}}
	resp, err := labClient.PostForm(
		fmt.Sprintf("%s/workflows/%d/%s", baseURL, wfID, postPath), form)
	if err != nil {
		t.Fatalf("POST run: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /workflows/%d/%s: status %d, want 200 — the run never started, so no "+
			"terminal panel can render (broken FIXTURE, not a stale selector)", wfID, postPath, resp.StatusCode)
	}

	statusURL := fmt.Sprintf("%s/workflows/%d/run/status", baseURL, wfID)
	deadline := time.Now().Add(30 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		last = getBody(t, statusURL)
		if strings.Contains(last, HeroMarker) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("run on workflow %d never reached the %q panel within 30s; last status fragment:\n%s",
		wfID, HeroMarker, truncate(last, 2000))
	return ""
}

// hasButtonWithDecoded is hasButtonWith over the DECODED attribute value.
//
// It exists because of a real mismatch that would otherwise make this guard vacuous
// in the wrong direction: the walk's selector is `button[onclick*="…('fix-model-0')"]`
// and a browser matches that against the decoded attribute, while the served bytes
// carry `onclick="document.getElementById(&#39;fix-model-0&#39;).showModal()"`. Comparing
// the raw HTML against the selector's string would report "no such trigger" on a
// perfectly working app. Decoding each button's OPEN TAG (not the whole document)
// keeps hasButtonWith's element discrimination — the attribute must still be on a
// <button> — while comparing the same text the browser compares.
func hasButtonWithDecoded(body, want string) bool {
	return hasButtonWith(html.UnescapeString(body), want)
}

// TestHasButtonWithDecodedSeesWhatTheBrowserSees is the negative control for the
// helper above: it proves the decoding step is doing work, i.e. that the raw form
// does NOT match and the decoded form does. Without it, "the guard passes" would be
// compatible with the decode being a no-op and the assertion being satisfied by some
// other coincidence.
func TestHasButtonWithDecodedSeesWhatTheBrowserSees(t *testing.T) {
	const served = `<button class="x" onclick="document.getElementById(&#39;fix-model-0&#39;).showModal()">Choose a model…</button>`
	want := FixModelOpenerJS(0)

	if hasButtonWith(served, want) {
		t.Fatalf("the RAW served HTML already contains %q — the decode step is pointless and this "+
			"helper's whole reason for existing is wrong; re-check how the onclick is escaped", want)
	}
	if !hasButtonWithDecoded(served, want) {
		t.Errorf("decoded matching did not find %q in %s", want, served)
	}
	// Element discrimination survives the decode: the attribute must be on a <button>.
	const onADiv = `<div onclick="document.getElementById(&#39;fix-model-0&#39;).showModal()"></div>`
	if hasButtonWithDecoded(onADiv, want) {
		t.Error("decoded matching accepted the opener on a <div> — it must still require a <button>")
	}
}

// fixModelFirstFilename is a FIXTURE fact, asserted rather than assumed: the lab's
// preflight reports exactly two missing models and the panel renders them in that
// order, so fix-model-0 is this file's dialog. If the fixture's ordering ever changes
// the assertion below says so instead of the walk silently opening the other row.
const fixModelFirstFilename = "detailer-MISSING.safetensors"

// TestFixModelDialogSelectorsMatchTheServedApp is the browserless rot-guard for the
// run-fix-model / run-fix-model-blocked views. It starts a REAL run against the lab's
// fake ComfyUI, waits for the terminal missing-models panel, and asserts that
// everything fixModelDialogPrep depends on is in the served HTML: the trigger (as a
// <button>, carrying the opener for THIS dialog) and the dialog itself.
//
// Non-vacuity: runToTerminalPanel fails loudly if the run never reaches the panel, so
// a broken fixture can never present as a matched selector.
func TestFixModelDialogSelectorsMatchTheServedApp(t *testing.T) {
	app := bootLabApp(t)

	for _, tc := range []struct {
		name    string
		baseURL func(*App) string
		// wantBlockedCopy is the ONE thing that differs between the two servers, and it
		// is why run-fix-model-blocked exists as a separate view rather than as a
		// duplicate of run-fix-model.
		wantBlockedCopy bool
	}{
		{"configured comfy_model_path", func(a *App) string { return a.URL }, false},
		{"unset comfy_model_path", func(a *App) string { return a.UnsetPathURL }, true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			panel := runToTerminalPanel(t, tc.baseURL(app), app.WorkflowID, RunPostPathAPI)

			// The trigger, on a <button>, carrying the opener naming THIS dialog. This is
			// the pair the walk's prep depends on: it clicks the button that opens
			// fix-model-N and then waits for fix-model-N to be [open], so a trigger that
			// no longer names its dialog would click something and hang.
			opener := FixModelOpenerJS(FixModelDialogIdx)
			if !hasButtonWithDecoded(panel, opener) {
				t.Errorf("no <button> in the missing-models panel carries %q — "+
					"fixModelDialogPrep would hang on WaitVisible until the 90s capture context "+
					"expires (a bare \"context deadline exceeded\"); update FixModelOpenerJS to the "+
					"app's current fixModelRow trigger", opener)
			}
			// The dialog the prep then waits for.
			if !strings.Contains(panel, `<dialog id="`+FixModelDialogID(FixModelDialogIdx)+`"`) {
				t.Errorf("the panel has no <dialog id=%q> — the prep would click the trigger and then "+
					"hang waiting for a dialog that does not exist", FixModelDialogID(FixModelDialogIdx))
			}
			// Fixture ordering: fix-model-0 really is the row the walk means to open.
			// Asserted on the ACCESSIBLE NAME, which is also what a reader of the capture
			// sees, rather than on DOM position.
			wantLabel := `aria-label="Choose a model for ` + fixModelFirstFilename + `"`
			if !strings.Contains(panel, wantLabel) {
				t.Errorf("no trigger carries %s — the lab's missing-model ordering changed, so the "+
					"walk is opening a different row than this guard describes", wantLabel)
			}

			// cardInstallBlockedText, the copy this whole pair of views exists to get in
			// front of axe. It renders ONLY from installAndRunButton's !dlEligible branch.
			// Both directions are asserted: present on the unset server, ABSENT on the
			// configured one. Without the absent half, a regression that rendered the
			// blocked copy unconditionally would satisfy the present half and the two views
			// would be auditing the same state under two names.
			const blockedCopy = "civitai-manager is not set up to install this file for you"
			got := strings.Contains(panel, blockedCopy)
			if got != tc.wantBlockedCopy {
				if tc.wantBlockedCopy {
					t.Errorf("the comfy_model_path-UNSET panel does NOT carry cardInstallBlockedText "+
						"(%q) — the run-fix-model-blocked view would capture the same state as "+
						"run-fix-model and that copy stays unscanned, which is the gap it was added "+
						"to close", blockedCopy)
				} else {
					t.Errorf("the CONFIGURED panel carries cardInstallBlockedText (%q) — install is "+
						"eligible here, so the blocked reason must not render; the two views are now "+
						"auditing the same branch", blockedCopy)
				}
			}
		})
	}
}

// detailsOpenTagRe matches a <details> open tag so the guard below can tell a LAZY
// disclosure (one that hx-gets its own body on toggle) from a static one.
var detailsOpenTagRe = regexp.MustCompile(`(?s)<details([^>]*)>`)

// TestRunPanelCarriesCollapsedDetails is the browserless ratchet behind the
// run-missing-models-expanded view. The view's whole justification is that the panel
// has collapsed content axe never sees; if that stops being true, the view is
// screenshotting the heroes' state under a second name and quietly doubling the run
// cost for nothing.
//
// It counts on the SERVED HTML rather than trusting the browser step, and it counts
// STATIC and LAZY separately because expandStaticDetails deliberately skips the lazy
// one — counting them together would let the static disclosures disappear while the
// total stayed put.
func TestRunPanelCarriesCollapsedDetails(t *testing.T) {
	app := bootLabApp(t)
	panel := runToTerminalPanel(t, app.URL, app.WorkflowID, RunPostPathAPI)

	// Only disclosures OUTSIDE the fix dialogs count. Measured at 52cb872 the dialogs
	// contain none at all, but stripping them keeps that from becoming a silent
	// assumption: showModal() makes the rest of the document inert, so a <details>
	// inside a dialog is not something this view can scan anyway.
	stripped := panel
	for {
		i := strings.Index(stripped, `<dialog id="`+FixModelDialogIDPrefix)
		if i < 0 {
			break
		}
		j := strings.Index(stripped[i:], "</dialog>")
		if j < 0 {
			break
		}
		stripped = stripped[:i] + stripped[i+j+len("</dialog>"):]
	}

	var static, lazy int
	for _, m := range detailsOpenTagRe.FindAllStringSubmatch(stripped, -1) {
		if strings.Contains(m[1], "hx-get") {
			lazy++
		} else {
			static++
		}
	}

	// Fixture reached the interesting case: the panel really is the custom-node-carrying
	// one. Without a missing NODE there is no attribution half and no disclosures at all,
	// and this test would report a stale count when the truth is a thinned fixture.
	if !strings.Contains(panel, "UltimateSDUpscale") {
		t.Fatalf("the panel does not mention the fixture's missing custom node — the collapsed "+
			"disclosures this view exists for come from the attribution half, so this is a broken "+
			"FIXTURE, not a changed count (static=%d lazy=%d)", static, lazy)
	}
	if static != MinStaticDetailsOnRunPanel {
		t.Errorf("the run panel carries %d static <details>, want %d — run-missing-models-expanded "+
			"exists to scan exactly this collapsed content, so if it genuinely shrank to 0 the view "+
			"should be deleted rather than left capturing the heroes' state twice; if it grew, take "+
			"the win and raise MinStaticDetailsOnRunPanel", static, MinStaticDetailsOnRunPanel)
	}
	// The lazy one is asserted as PRESENT-AND-EXCLUDED, not merely absent from the static
	// count: if it ever stopped carrying hx-get, expandStaticDetails would start opening
	// it and fire suggestComfyModelPath's ComfyUI round-trip on every run capture.
	if lazy != 1 {
		t.Errorf("the run panel carries %d hx-get <details>, want 1 — expandStaticDetails skips "+
			"exactly the lazy setup disclosure, and that skip is what keeps a 5s ComfyUI round-trip "+
			"off this capture", lazy)
	}
}

// TestFixModelDialogIsClosedUntilTheWalkOpensIt pins the premise of the whole change:
// the dialog's markup is in the DOM the moment the panel renders, WITHOUT the `open`
// attribute. That is why the prep waits on `[open]` and not on any text inside the
// dialog — a text wait would be satisfied instantly by content the browser is not
// displaying and axe would not scan, and the capture would silently be of the closed
// state, which is exactly the state that was already never scanned.
func TestFixModelDialogIsClosedUntilTheWalkOpensIt(t *testing.T) {
	app := bootLabApp(t)
	panel := runToTerminalPanel(t, app.URL, app.WorkflowID, RunPostPathAPI)

	dlg := `<dialog id="` + FixModelDialogID(FixModelDialogIdx) + `"`
	i := strings.Index(panel, dlg)
	if i < 0 {
		t.Fatalf("no %s in the panel — the fixture never reached the dialog at all", dlg)
	}
	end := strings.Index(panel[i:], ">")
	if end < 0 {
		t.Fatalf("unterminated <dialog> open tag")
	}
	openTag := panel[i : i+end+1]
	if strings.Contains(openTag, " open") {
		t.Errorf("the server renders %s ALREADY OPEN (%s) — then the walk's click is not what "+
			"reveals it, and this pair of views is not proving what it claims", dlg, openTag)
	}
	// Non-vacuity: the dialog really does hold the content the views exist to scan, so
	// "closed" above is a statement about a populated subtree and not an empty shell.
	dlgEnd := strings.Index(panel[i:], "</dialog>")
	if dlgEnd < 0 {
		t.Fatal("unterminated <dialog>")
	}
	body := panel[i : i+dlgEnd]
	for _, want := range []string{"Use matched model from CivitAI", "Install and run", "Replace with a model from my library"} {
		if !strings.Contains(body, want) {
			t.Errorf("the closed dialog does not contain %q — %d bytes of subtree, so the walk may be "+
				"opening a shell rather than the resolver UI", want, len(body))
		}
	}
}
