package web

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	g "maragu.dev/gomponents"
	h "maragu.dev/gomponents/html"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
)

// comfySetupContainerID is the STABLE container the setup fragment swaps its
// innerHTML into. Like every other streaming/replacing surface in this package the
// swap target is a container, never the element carrying the trigger.
const comfySetupContainerID = "comfy-setup"

// comfySetupProbeTimeout bounds the ComfyUI folder_paths probe. It is short on
// purpose: the probe is a convenience (it pre-fills a value the user could type),
// so a slow or wedged ComfyUI must degrade to the empty form quickly rather than
// hang the disclosure open on a spinner.
const comfySetupProbeTimeout = 5 * time.Second

// comfySetupInputID binds the path field to its visible label (textInput wires
// label/for from it).
const comfySetupInputID = "comfy-model-path-input"

// comfySetupDisclosure is the run panel's replacement for the dead-end sentence
// that used to sit under the disabled "Install N missing model files and run"
// button ("Installing automatically needs comfy_model_path set and comfy_url
// pointing at a ComfyUI on this machine").
//
// 🔴 The point of this control is that the primary recovery action was DISABLED and
// the panel's only response was to name a config key in config-file jargon. The
// operator was one setting away from it working — their ComfyUI answers on
// loopback — with no way to supply it from the page telling them it was missing.
//
// It is LAZY by construction: a closed <details> whose body is fetched on its first
// `toggle`. Two reasons, both load-bearing:
//
//   - The panel already renders ~135 controls, 123 of them hidden inside collapsed
//     pickers. Eagerly adding a form (input + submit) makes the thing this rework
//     exists to shrink measurably worse.
//   - The body needs a ComfyUI round-trip (see comfySetupFragment). Rendering it
//     inline would put a network call on the run panel's render path, which is
//     re-rendered by every declined action.
//
// It is deliberately shaped as a SETUP STEP, not a bare switch. The repo already
// shipped an inline "Remove helper" button beside success text that a user clicked
// without knowing it disabled a feature (see comfyext's disclosure); the lesson is
// that a control changing app configuration must announce what it is before it is
// reachable, so the summary says so and the body explains before it offers.
// It takes no CSRF token: the disclosure only issues a GET, and the form it loads
// carries the server's own token (see comfySetupFragment).
//
// 🔴 `configured` is why this renders in BOTH states. It used to have a single call
// site, inside the `!p.Available` branch, so once a path was saved the disclosure
// never rendered again — while its own body promised "Change it any time from this
// panel". This app has no settings page, so that made the ONE value deciding where
// gigabytes land uncorrectable in-app: only by writing the config.yaml this whole
// control exists to avoid, or by hand-editing SQLite. The flag only picks the
// summary wording — an unconfigured install is being asked to do a setup step, a
// configured one is being offered a change.
func comfySetupDisclosure(wfID int64, configured bool) g.Node {
	summary := "Set up automatic install — civitai-manager needs to know where ComfyUI keeps its models"
	if configured {
		summary = "Change where civitai-manager installs model files"
	}
	return h.Details(
		h.Class("mt-1"),
		hx("get", "/workflows/"+strconv.FormatInt(wfID, 10)+"/comfy-setup"),
		hx("trigger", "toggle once"),
		hx("target", "#"+comfySetupContainerID),
		hx("swap", "innerHTML"),
		h.Summary(h.Class("cursor-pointer text-xs text-indigo-400"), g.Text(summary)),
		h.Div(h.ID(comfySetupContainerID), h.Class("mt-1"),
			h.P(h.Class("text-xs text-slate-500"), g.Text("Checking your ComfyUI…"))),
	)
}

// handleComfySetupForm answers the disclosure's first open: it works out WHY the
// install action is unavailable and renders the smallest thing that fixes it.
//
// Loopback-gated (it hands out and accepts an arbitrary filesystem path — the same
// rule as the scan/browse/discover endpoints) even though it is a GET, because the
// SUGGESTION it returns is itself a filesystem-layout disclosure.
func (s *Server) handleComfySetupForm(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad workflow id", http.StatusBadRequest)
		return
	}
	s.render(w, http.StatusOK, s.comfySetupFragment(r.Context(), id, "", ""))
}

// comfySetupFragment is the disclosure's body.
//
// value is what to pre-fill the input with (a rejected submission's own text, so a
// typo is corrected rather than retyped); when empty the probe's suggestion is used
// instead. problem is a validation message from a rejected submission.
//
// 🔴 The comfy_url branch comes FIRST and offers no form. comfy_model_path is only
// HALF the precondition — comfyDownloadEligible also requires a LOOPBACK comfy_url,
// and on a server that fails that half, saving a path changes nothing. Offering the
// form anyway would persist a setting, report success, and leave the button exactly
// as dead as before, which is the same class of lie this whole rework is removing.
func (s *Server) comfySetupFragment(ctx context.Context, wfID int64, value, problem string) g.Node {
	if !comfyURLIsLocal(s.cfg.ComfyURL) {
		return h.Div(h.Class("space-y-1"),
			h.P(h.Class("text-xs text-amber-400"),
				g.Text("This server is not pointed at a ComfyUI on this machine, so it cannot install "+
					"model files for you. Set comfy_url to your local ComfyUI (for example "+
					"http://127.0.0.1:8188) and restart civitai-manager.")),
			h.P(h.Class("text-xs text-slate-500"),
				g.Text("Until then, use the per-file options below to download each model yourself.")),
		)
	}
	// 🔴 A CONFIGURED install gets the form too, pre-filled with the saved path.
	// This branch used to return that path as a bare sentence and nothing else, which
	// made the body's closing "Change it any time from this panel" false even once the
	// disclosure did render: there was no control to change it with. Showing the value
	// read-only is the same dead end as naming the config key.
	current := s.comfyModelPath()

	// Precedence: what the user just typed (so a typo is corrected, not retyped), then
	// the saved value, then the probe. The probe is LAST and is skipped entirely once
	// a value exists — it is a network round-trip whose only job is to seed an empty
	// field, and re-running it on a configured install would let a ComfyUI answer
	// overwrite the operator's own saved choice in the input.
	suggested := value
	if suggested == "" {
		suggested = current
	}
	if suggested == "" {
		suggested = s.suggestComfyModelPath(ctx)
	}

	var body []g.Node
	if current == "" {
		body = append(body, h.P(h.Class("text-xs text-slate-400"),
			g.Text("civitai-manager writes missing model files straight into ComfyUI's models folder. "+
				"Tell it where that is and the button above starts working.")))
	} else {
		body = append(body, h.P(h.Class("text-xs text-slate-400"),
			g.Text("civitai-manager currently installs model files into "+current+
				". Save a different folder to change that.")))
	}
	switch {
	case problem != "":
		body = append(body, h.P(g.Attr("role", "status"), h.Class("text-xs text-amber-400"), g.Text(problem)))
	case current != "":
		// Nothing to add: the line above already names the folder in force, and a
		// "check it looks right" nudge belongs to a PROBE result, not to the value the
		// operator chose themselves.
	case suggested != "":
		body = append(body, h.P(h.Class("text-xs text-slate-500"),
			g.Text("Your ComfyUI reports this folder — check it looks right, then save it.")))
	default:
		body = append(body, h.P(h.Class("text-xs text-slate-500"),
			g.Text("Your ComfyUI did not report a models folder, so type the full path to it "+
				"(the folder containing checkpoints/ and loras/).")))
	}

	body = append(body,
		// The civitai design-system text-input role, so the control inherits the
		// theme-aware surface both themes already style — no new coloured pair, and
		// nothing for the contrast table to gain.
		h.Div(h.Class("w-full max-w-full"),
			textInput("text-input", comfySetupInputID, "Path to ComfyUI's models folder",
				h.Type("text"),
				h.Name("model_path"),
				h.Value(suggested),
				h.Placeholder("/path/to/ComfyUI/models"),
				g.Attr("spellcheck", "false"),
				g.Attr("autocapitalize", "off"),
				g.Attr("autocorrect", "off"),
			),
		),
		h.Div(h.Class("pt-1"),
			civButton("filled", "sm", []g.Node{h.Type("submit")}, g.Text("Save folder")),
		),
		h.P(h.Class("text-xs text-slate-500"),
			g.Text("Saved in civitai-manager's own database — your ComfyUI is not modified, and no "+
				"config file is written. Change it any time from this panel.")),
	)

	return h.Form(
		hx("post", "/workflows/"+strconv.FormatInt(wfID, 10)+"/comfy-setup"),
		hx("target", "#"+runStatusContainerID),
		hx("swap", "innerHTML"),
		hx("disabled-elt", "find button[type='submit']"),
		h.Class("space-y-1"),
		csrfInput(s.csrf),
		g.Group(body),
	)
}

// suggestComfyModelPath asks the local ComfyUI where its models live, and returns
// "" when it cannot say.
//
// EVERY failure here is non-fatal by design: the endpoint is absent on older
// ComfyUI builds, the install may be genuinely ambiguous (comfy.ModelsRoot returns
// "" on a tie rather than guessing), and the probe is bounded by a short timeout.
// The caller degrades to an empty input, which is exactly what the user faced
// before this existed.
func (s *Server) suggestComfyModelPath(ctx context.Context) string {
	probe, cancel := context.WithTimeout(ctx, comfySetupProbeTimeout)
	defer cancel()
	fp, err := s.comfyFolderPaths(probe)
	if err != nil {
		s.log.Debug("comfy folder_paths probe failed", "err", err)
		return ""
	}
	root := comfy.ModelsRoot(fp)
	if root == "" {
		return ""
	}
	// 🔴 CORROBORATE the nomination against the filesystem. comfy.ModelsRoot counts
	// votes over a payload from a remote ComfyUI, and the thing it nominates is a
	// category directory's PARENT — which the payload never has to describe truthfully
	// and which need not itself look like a models root. Its vote floor stops a
	// one-category payload; it cannot stop a payload that simply lists several fake
	// categories under one parent, because inventing five costs no more than inventing
	// one. Requiring that at least one reported category directory actually EXISTS is
	// what makes the claim checkable rather than merely self-consistent: measured,
	// {"checkpoints":["/home/zach/checkpoints"], …} nominated /home/zach, and the
	// downstream re-validation accepted it because /home/zach exists and is writable.
	//
	// This is a SUGGESTION filter, not an access control — the residual risk needs a
	// hostile ComfyUI, which already implies code execution on that box, and a human
	// confirms the value on screen. It is here because the cost is one Stat and the
	// alternative is pre-filling a plausible-looking wrong answer.
	if !hasExistingCategoryDir(comfy.ModelsRootCategoryDirs(fp, root)) {
		s.log.Debug("comfy folder_paths suggestion not corroborated on disk", "root", root)
		return ""
	}
	// Only suggest something this server could actually use. Suggesting a path that
	// then fails validation on submit would read as the app contradicting itself.
	clean, problem := validateComfyModelPath(root)
	if problem != "" {
		s.log.Debug("comfy folder_paths suggestion rejected", "root", root, "reason", problem)
		return ""
	}
	return clean
}

// hasExistingCategoryDir reports whether ANY of the reported category directories
// is a real directory on this machine.
//
// One is enough on purpose: a legitimate ComfyUI lists categories it has not
// created yet (they are search paths, not a manifest), so requiring all of them —
// or even most — would reject real installs to buy nothing. What it rules out is
// the payload that describes a layout which does not exist at all.
func hasExistingCategoryDir(dirs []string) bool {
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}

// comfyFolderPaths runs the models-root probe through the test seam when one is
// installed, else against the configured ComfyUI.
//
// It is a SEPARATE seam rather than a method on the comfyClient interface on
// purpose: adding it there would force every existing fake in the package to grow
// a method none of them care about, for a probe used by exactly one surface.
func (s *Server) comfyFolderPaths(ctx context.Context) (map[string][]string, error) {
	if s.folderPathsFn != nil {
		return s.folderPathsFn(ctx)
	}
	return comfy.NewClient(s.cfg.ComfyURL, s.cfg.ComfyToken).FolderPaths(ctx)
}

// handleComfySetupSave persists the models folder and re-renders the run panel, so
// the primary CTA the user was looking at goes live in the same interaction.
//
// CSRF-protected and loopback-gated: it accepts an arbitrary filesystem path, which
// is precisely the capability extraPathsAllowed exists to fence off.
//
// 🔴 A REJECTED path is answered with the FORM (keeping what the user typed), not
// with the run panel. Re-rendering the panel on failure would swallow the reason
// and silently look like the save worked.
func (s *Server) handleComfySetupSave(w http.ResponseWriter, r *http.Request) {
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
	raw := r.FormValue("model_path")
	clean, problem := validateComfyModelPath(raw)
	if problem != "" {
		// 🔴 RETARGET, or a rejected path destroys the whole failure report. The form
		// posts with hx-target="#run-status" because SUCCESS must re-render the panel
		// (that is how the disabled CTA goes live in the same interaction) — but the
		// rejection answer is ~1.3 KB containing only this form, and swapping THAT into
		// #run-status deletes the headline, the lower-bound caveat, the CTA, the
		// missing-nodes panel, every per-file picker, Technical details and "Run
		// again". The user is left with a bare, context-free form.
		//
		// It is not a corner case: the typed-path branch is the documented degradation
		// for EVERY probe failure (404 / timeout / tie / rejected suggestion), so the
		// users most likely to be rejected are exactly those with no pre-filled value.
		//
		// The response is therefore the disclosure's INNER content and nothing else —
		// same shape handleComfySetupForm returns, so the wrapper div is deliberately
		// gone: with swap=innerHTML a wrapper carrying id="comfy-setup" would nest a
		// second element of that id inside the first, one level deeper per rejection.
		w.Header().Set("HX-Retarget", "#"+comfySetupContainerID)
		// 200 + the form: htmx swaps a 4xx into nothing by default, and this app has
		// no htmx:responseError handler anywhere (see the maturity control's shipped
		// dead-button bug), so a status-coded rejection here would be invisible.
		s.render(w, http.StatusOK, s.comfySetupFragment(r.Context(), id, raw, problem))
		return
	}
	if err := s.store.SetSetting(comfyModelPathSettingKey, clean); err != nil {
		s.renderError(w, "save comfy model path", err)
		return
	}
	s.log.Info("comfy_model_path set from the run panel", "path", clean)
	s.render(w, http.StatusOK, runStatusFragment(
		s.runJobState(), id, s.csrf, s.comfyDownloadEligible(), s.maturity()))
}
