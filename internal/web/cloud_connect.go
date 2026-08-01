package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ZacxDev/civitai-manager/internal/config"
	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"
)

// comfyCloudSettingKey is the `settings` row holding the WEB-set comfy_cloud
// toggle ("1" on, anything else off). It is ordinary UI state, exactly like
// match_remote / theme / the NSFW mode — NOT a credential.
//
// Cloud auth introduces NO new secret: the orchestration client is built from the
// already-configured CivitAI Token (Server.cloud → comfy.NewCloudClient(_,
// cfg.Token) → `Authorization: Bearer <token>`). Nothing secret is ever written
// to the settings table by this feature, which is why the DB file's mode is left
// alone.
const comfyCloudSettingKey = "comfy_cloud"

// cloudConnectContainerID is the stable container the connect fragment swaps
// into. Like every other streaming/lazy surface here, responses replace this
// container's innerHTML and never outerHTML-replace the element that triggers the
// swap.
const cloudConnectContainerID = "cloud-connect"

// cloudEnabledFromConfig reports the config FILE's answer for comfy_cloud and
// whether the file actually gave one. An explicit `comfy_cloud:` in the config
// file WINS over the DB toggle — see cloudEnabled.
func (s *Server) cloudEnabledFromConfig() (enabled, configured bool) {
	if s.cfg.ComfyCloud == nil {
		return false, false
	}
	return *s.cfg.ComfyCloud, true
}

// cloudEnabled resolves the EFFECTIVE comfy_cloud state.
//
// Precedence, deliberately explicit (two sources silently disagreeing is worse
// than either alone):
//
//  1. An explicit `comfy_cloud:` in the config file wins, true or false. The web
//     toggle then renders read-only and says where the value comes from.
//  2. Otherwise the DB `settings` row set by the web toggle governs.
//  3. Otherwise OFF. Cloud runs egress the graph to civitai.com and spend Buzz,
//     so the default — and the answer on any store error — is off (fail closed).
func (s *Server) cloudEnabled() bool {
	if enabled, configured := s.cloudEnabledFromConfig(); configured {
		return enabled
	}
	v, err := s.store.GetSettingDefault(comfyCloudSettingKey, "0")
	if err != nil {
		// Fail CLOSED: never turn on an egress + Buzz-spending feature because a
		// settings read failed.
		s.log.Warn("read comfy_cloud setting", "err", err)
		return false
	}
	return v == "1"
}

// cloudTokenConfigured reports whether ANY layer supplied a CivitAI token. The
// token itself is never returned from here.
func (s *Server) cloudTokenConfigured() bool {
	return strings.TrimSpace(s.cfg.Token) != ""
}

// cloudConnectView is the resolved state the connect fragment renders. It carries
// only the REDACTED token — the raw secret never enters a view struct, and
// therefore never enters markup.
type cloudConnectView struct {
	wfID int64
	// hasToken is true when a CivitAI token was resolved from any layer.
	hasToken bool
	// redactedToken is config.RedactToken of the configured token ("****cdef", or
	// "(none)" when unset). NEVER the raw value.
	redactedToken string
	// enabled is the EFFECTIVE comfy_cloud state.
	enabled bool
	// configured is true when the config FILE set comfy_cloud explicitly, in which
	// case the toggle is read-only and enabled reflects the file.
	configured bool
	// note is an optional outcome message from a POST (e.g. a refused enable).
	note string
	// refreshPanel asks the fragment to emit a one-shot loader that re-fetches the
	// cloud panel, so a toggle immediately re-renders what sits below it.
	refreshPanel bool
}

// cloudConnectFragment renders the CivitAI-cloud connect block: where the cloud
// credential comes from (redacted), and the on/off toggle for comfy_cloud.
//
// There is deliberately no credential INPUT here. Cloud auth reuses the CivitAI
// token this app is already configured with, so a second place to type a token
// would create a second source of truth for the same secret. The block therefore
// only reports the token's presence (redacted) and points at the places that set
// it.
func cloudConnectFragment(v cloudConnectView, csrf string) g.Node {
	id := strconv.FormatInt(v.wfID, 10)

	body := []g.Node{cloudTokenLine(v)}

	switch {
	case v.configured:
		body = append(body, cloudConfigWinsNote(v.enabled))
	case !v.hasToken:
		// 🔴 usable = v.enabled, NOT false. The OFF transition is always available.
		// Disabling the control outright left the one state that matters unreachable:
		// remove the token from an install where cloud is ON (unset CIVITAI_TOKEN,
		// drop `token:`, revoke it upstream) and comfy_cloud is still "1" in the DB,
		// so cloudEnabled() reports ON — an egress-and-Buzz-spending feature whose
		// ONLY control was greyed out. The stale "1" then persists, so configuring a
		// token again silently re-enables cloud runs with no re-consent. The server
		// already accepted enabled=0 here (it refuses only `on && !hasToken`), so the
		// UI was MORE restrictive than the server in the unsafe direction.
		//
		// Enabling without a token stays refused in both halves: with cloud off there
		// is nothing to turn off, so usable is false again and the server still
		// rejects a forged enable.
		body = append(body, cloudNoTokenNote(v.enabled), cloudToggleButton(id, v.enabled, v.enabled, csrf))
	default:
		body = append(body, cloudToggleButton(id, v.enabled, true, csrf))
	}

	if strings.TrimSpace(v.note) != "" {
		body = append(body, h.P(h.Class("text-sm text-slate-300"), g.Text(v.note)))
	}
	if v.refreshPanel {
		body = append(body, cloudPanelRefresh(id))
	}
	return h.Div(h.Class("space-y-2"), g.Group(body))
}

// cloudTokenLine states, in one line, which credential cloud runs authenticate
// with and whether it is present — redacted, always. This is the whole "connect"
// story: no new secret, just the CivitAI token the app already has.
//
// 🔴 This is the FIRST place in this repo where any part of a secret is written
// into an HTTP response body, and it is deliberate — not an oversight, and not a
// precedent that widens on its own. Before this branch config.RedactToken was
// reachable only from config.Redacted()/String(), i.e. diagnostics and logs. The
// rule was changed on purpose, and the new boundary is exactly this:
//
//	a REDACTED credential (last four characters) may appear in a
//	loopback-gated response; the RAW credential may not, anywhere, ever.
//
// What it buys: an answer to "which credential am I actually using". This app
// resolves a token from four layers in precedence order — the --token flag, the
// CIVITAI_TOKEN environment variable, `token:` in the config file, and the
// official civitai CLI's own config as a last resort — and when more than one is
// present, the last four characters are the only thing that tells a user which
// one won. Nothing else on this page can distinguish them.
//
// Why it is acceptable here: the surface is loopback-gated and the write half is
// CSRF-protected, so it is not reachable from another device or another origin;
// and last-four is the standard redaction every cloud console uses for the same
// question.
//
// The raw half is enforced, not merely asserted: the endpoint-level sweep in
// TestCloudTokenNeverAppearsInRenderedHTML checks every surface this branch
// touches (GET/POST connect, the cloud panel, the workflow detail page, whatif)
// for the raw value AND for a distinctive fragment of it, and
// TestCloudTokenLineRendersOnlyTheRedactedTail pins this component directly. If
// this line ever renders anything but the redaction, both fail.
// It also states, in the same breath, what that token DOES — because the omission
// was the whole complaint. A user hit "Run for real (spends Buzz)" and reasonably
// expected to be asked to authorise the spend; there is no OAuth in this app and no
// per-run approval step, so the configured API token IS the authorisation. Nothing
// was broken and nothing needed fixing in the code — it had simply never been said.
func cloudTokenLine(v cloudConnectView) g.Node {
	label := "Cloud runs authenticate with your CivitAI API token: "
	return h.Div(h.Class("space-y-1"),
		h.P(h.Class("text-sm text-slate-300"),
			g.Text(label),
			h.Span(h.Class("font-mono text-xs text-slate-200"), g.Text(v.redactedToken)),
		),
		h.P(h.Class("text-xs text-slate-400"),
			g.Text("That token is also what SPENDS your Buzz. This app has no separate sign-in "+
				"and CivitAI asks for no per-run approval, so anything you start here is "+
				"charged straight to the account the token belongs to — the confirmation on "+
				"the run button is the only one you get.")),
	)
}

// cloudNoTokenNote explains the state where no CivitAI token is configured, and
// names every place that can supply one. It never offers a field — this app does
// not accept a secret over HTTP.
//
// `on` is the STORED preference. When it is true the user is in the state worth
// spelling out: cloud reads as on, no cloud run can actually authenticate, and
// supplying a token later resumes cloud runs immediately — so the copy tells them
// both that the preference is still set and that clearing it is the way to make
// that not happen.
func cloudNoTokenNote(on bool) g.Node {
	if on {
		return alert("warning", "No CivitAI token configured",
			g.Text("Cloud run is still switched on in your saved preference, but this app has no "+
				"CivitAI API token, so no cloud run can sign in. Turn it off below to clear that "+
				"preference — otherwise supplying a token again resumes cloud runs straight away, "+
				"without asking you first. A token comes from the CIVITAI_TOKEN environment "+
				"variable, the --token flag, or `token:` in your config file (the official civitai "+
				"CLI's token is picked up as a last resort), and takes effect after a restart. "+
				"Tokens are never entered or stored here."))
	}
	return alert("warning", "No CivitAI token configured",
		g.Text("Cloud runs sign in with your CivitAI API token, and this app has none. "+
			"Set one, then restart the server: the CIVITAI_TOKEN environment variable, "+
			"the --token flag, or `token:` in your config file. The official civitai CLI's "+
			"token is picked up automatically as a last resort. Tokens are never entered "+
			"or stored here."))
}

// cloudConfigWinsNote states the explicit precedence when the config FILE set
// comfy_cloud: the file wins and the toggle is read-only.
func cloudConfigWinsNote(enabled bool) g.Node {
	state := "off"
	if enabled {
		state = "on"
	}
	return alert("info", "Set in your config file",
		g.Text("Cloud run is turned "+state+" by `comfy_cloud: "+strconv.FormatBool(enabled)+"` "+
			"in your config file. The config file wins over this toggle, so change it there "+
			"(or remove the key to control it from here) and restart."))
}

// cloudToggleButton is the on/off pill for comfy_cloud. It mirrors the dashboard's
// flagToggle: a light civitai button recolored through tokenVars, which sets BOTH
// the fill token and its `-text` foreground (never a bare fill).
//
// usable means "the server would ACCEPT the transition this button offers" — not
// "the feature is fully configured". The two differ in exactly one place and it
// matters: with cloud ON and no token the button offers OFF, which the server
// accepts, so it must be live. Disabled is reserved for a click the server would
// refuse, so the control never lies about what a click would do — in either
// direction.
func cloudToggleButton(id string, on, usable bool, csrf string) g.Node {
	tok := "text-dimmed"
	label := "Cloud run is off — turn on"
	next := "1"
	if on {
		tok = "success"
		label = "Cloud run is on — turn off"
		next = "0"
	}
	attrs := []g.Node{
		h.Type("button"),
		h.StyleAttr(tokenVars(tok)),
		g.Attr("aria-pressed", strconv.FormatBool(on)),
	}
	if usable {
		attrs = append(attrs,
			hx("post", "/workflows/"+id+"/cloud/connect"),
			// %q, not concatenation: both values are a hex token and a "1"/"0" enum
			// today, so nothing needs escaping — but building JSON by gluing strings
			// is the habit that breaks the first time a value stops being either.
			// Same form as the resource chip's reveal control.
			hx("vals", fmt.Sprintf(`{"enabled":%q,"csrf_token":%q}`, next, csrf)),
			hx("target", "#"+cloudConnectContainerID),
			hx("swap", "innerHTML"),
		)
	} else {
		attrs = append(attrs, h.Disabled(), g.Attr("aria-disabled", "true"))
	}
	return h.Div(h.Class("flex"), civButton("light", "sm", attrs, g.Text(label)))
}

// cloudPanelRefresh is a one-shot loader that re-fetches the cloud panel into the
// STABLE #cloud-panel container after a toggle, so the panel below immediately
// reflects the new state. It targets a different, stable element — it never
// replaces itself.
func cloudPanelRefresh(id string) g.Node {
	return h.Div(
		hx("get", "/workflows/"+id+"/cloud"),
		hx("trigger", "load"),
		hx("target", "#"+cloudPanelContainerID),
		hx("swap", "innerHTML"),
	)
}

// cloudConnectState builds the view for workflow id from live config + settings.
func (s *Server) cloudConnectState(wfID int64) cloudConnectView {
	enabled, configured := s.cloudEnabledFromConfig()
	if !configured {
		enabled = s.cloudEnabled()
	}
	return cloudConnectView{
		wfID:     wfID,
		hasToken: s.cloudTokenConfigured(),
		// The ONLY derivative of the token that may leave this process over HTTP.
		// See cloudTokenLine for why a redacted credential in a response body is
		// allowed here and the raw one still is not, anywhere.
		redactedToken: config.RedactToken(strings.TrimSpace(s.cfg.Token)),
		enabled:       enabled,
		configured:    configured,
	}
}

// handleWorkflowCloudConnect renders the connect block. GET (no state change, no
// CSRF); loopback-gated like every other cloud surface — it describes a control
// that reaches civitai.com and spends Buzz.
func (s *Server) handleWorkflowCloudConnect(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, cloudConnectFragment(s.cloudConnectState(id), s.csrf))
}

// maxCloudConnectBody bounds the toggle's request body. It carries two short
// fields (enabled + csrf_token); anything larger is hostile, not a mistake.
const maxCloudConnectBody = 4 << 10

// handleWorkflowCloudConnectSet flips the DB-stored comfy_cloud toggle. CSRF is
// validated before any state change; loopback-gated.
//
// Three refusals, each of which keeps the server and the rendered control honest:
//   - a config-file `comfy_cloud:` wins, so the toggle cannot override it;
//   - turning cloud ON with no CivitAI token would store a state the user cannot
//     act on (the disabled button says exactly this);
//   - `enabled` must be exactly "1" or "0" — no other value is stored or echoed.
func (s *Server) handleWorkflowCloudConnectSet(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCloudConnectBody)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	// Strict enum. An unrecognized value is rejected outright rather than coerced,
	// so nothing attacker-supplied is ever stored or rendered back.
	var on bool
	switch r.FormValue("enabled") {
	case "1":
		on = true
	case "0":
		on = false
	default:
		http.Error(w, "bad enabled value", http.StatusBadRequest)
		return
	}

	view := s.cloudConnectState(id)
	if view.configured {
		view.note = "Cloud run is controlled by your config file, so this toggle did not change anything."
		s.render(w, http.StatusOK, cloudConnectFragment(view, s.csrf))
		return
	}
	if on && !view.hasToken {
		view.note = "Cloud run was not turned on: no CivitAI token is configured, so a cloud run could not authenticate."
		s.render(w, http.StatusOK, cloudConnectFragment(view, s.csrf))
		return
	}

	val := "0"
	if on {
		val = "1"
	}
	if err := s.store.SetSetting(comfyCloudSettingKey, val); err != nil {
		s.renderError(w, "save cloud setting", err)
		return
	}
	view.enabled = on
	view.refreshPanel = true
	s.render(w, http.StatusOK, cloudConnectFragment(view, s.csrf))
}
