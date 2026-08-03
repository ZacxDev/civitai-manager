package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/comfyext"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// setupTestServer builds a server whose ComfyUI is local (so the setup form is
// offerable) with comfy_model_path deliberately UNSET — the operator's real state:
// no config.yaml at all, but a ComfyUI answering on loopback.
func setupTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return NewServer(st, stubReader{}, stubSubscriber{}, Config{
		BaseURL:             "https://civitai.com",
		DefaultPollInterval: time.Hour,
		Addr:                "127.0.0.1:8787",
		ComfyURL:            "http://127.0.0.1:8188",
	}, nil)
}

// modelsDir makes a real, writable directory shaped like a ComfyUI models root.
func modelsDir(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "ComfyUI", "models")
	if err := os.MkdirAll(filepath.Join(root, "checkpoints"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

// comfyInstallDir makes a models root whose PARENT is a directory comfyext
// recognises as a ComfyUI install.
//
// 🔴 custom_nodes/ alone is NOT enough — comfyext.LooksLikeRoot requires it AND a
// fingerprint (main.py / folder_paths.py / nodes.py / comfy/). A fixture with only
// custom_nodes/ makes DeriveRoot return "", which is a FIXTURE defect that reads
// exactly like a broken accessor; it cost a diagnosis here.
func comfyInstallDir(t *testing.T) string {
	t.Helper()
	root := modelsDir(t)
	install := filepath.Dir(root)
	if err := os.MkdirAll(filepath.Join(install, "custom_nodes"), 0o755); err != nil {
		t.Fatalf("mkdir custom_nodes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(install, "main.py"), []byte("# ComfyUI\n"), 0o644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}
	// PRECONDITION: prove the fixture actually reaches the derivation.
	if got := comfyext.DeriveRoot(root); got != install {
		t.Fatalf("fixture does not look like a ComfyUI install: DeriveRoot(%q) = %q, want %q", root, got, install)
	}
	return root
}

func getSetupForm(t *testing.T, srv *Server) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/workflows/7/comfy-setup", nil))
	return rec
}

// postSetupTo submits the setup form for a SPECIFIC workflow.
//
// 🔴 The workflow id is not cosmetic: runStatusFragment deliberately renders an
// empty div for a run belonging to a different workflow, so posting to the wrong
// id returns `<div></div>` and reads exactly like the save having done nothing.
func postSetupTo(t *testing.T, srv *Server, wfID, path, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	body := strings.NewReader("model_path=" + path + "&csrf_token=" + csrf)
	req := httptest.NewRequest(http.MethodPost, "/workflows/"+wfID+"/comfy-setup", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// postSetup is postSetupTo for the states that carry no real run (workflow id is
// then irrelevant to what is being asserted).
func postSetup(t *testing.T, srv *Server, path, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	return postSetupTo(t, srv, "7", path, csrf)
}

// TestComfyModelPathPrefersConfigOverTheStoredSetting pins the precedence rule
// lifted from Config.ComfyCloud: an explicit config-file/flag value is
// AUTHORITATIVE and the UI must never silently override it.
func TestComfyModelPathPrefersConfigOverTheStoredSetting(t *testing.T) {
	srv := setupTestServer(t)
	if got := srv.comfyModelPath(); got != "" {
		t.Fatalf("unset server: comfyModelPath = %q, want \"\"", got)
	}

	// Settings-only → the stored value governs.
	if err := srv.store.SetSetting(comfyModelPathSettingKey, "/from/settings"); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got := srv.comfyModelPath(); got != "/from/settings" {
		t.Fatalf("comfyModelPath = %q, want the stored value", got)
	}

	// Config set → config wins even though a row exists.
	srv.cfg.ComfyModelPath = "/from/config"
	if got := srv.comfyModelPath(); got != "/from/config" {
		t.Fatalf("comfyModelPath = %q, want the configured value to win", got)
	}
}

// TestComfyRootDerivesFromTheStoredModelPath: a root set up through the UI must
// behave like one set up in YAML — otherwise the manual git-clone command keeps
// printing its /path/to/ComfyUI placeholder after the user configured everything.
func TestComfyRootDerivesFromTheStoredModelPath(t *testing.T) {
	srv := setupTestServer(t)
	root := comfyInstallDir(t)
	if err := srv.store.SetSetting(comfyModelPathSettingKey, root); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	if got, want := srv.comfyRoot(), filepath.Dir(root); got != want {
		t.Fatalf("comfyRoot = %q, want %q", got, want)
	}
}

// TestValidateComfyModelPathRejectsUnusablePaths covers every refusal branch, and
// asserts each carries its OWN reason (a shared generic message would make the
// form unable to tell the user what is actually wrong).
func TestValidateComfyModelPathRejectsUnusablePaths(t *testing.T) {
	dir := modelsDir(t)
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	t.Run("accepts a real writable dir", func(t *testing.T) {
		got, problem := validateComfyModelPath("  " + dir + "  ")
		if problem != "" {
			t.Fatalf("rejected a good path: %s", problem)
		}
		if got != dir {
			t.Fatalf("cleaned to %q, want %q", got, dir)
		}
	})

	cases := map[string]struct {
		in   string
		want string
	}{
		"empty":       {"", "Enter the full path"},
		"blank":       {"   ", "Enter the full path"},
		"relative":    {"models", "absolute path"},
		"dot":         {"./models", "absolute path"},
		"missing":     {filepath.Join(dir, "nope"), "nothing at"},
		"is-a-file":   {file, "is a file, not a folder"},
		"nul-byte":    {"/tmp/a\x00b", "invalid character"},
		"home-tilde":  {"~/ComfyUI/models", "absolute path"},
		"parent-trav": {"../models", "absolute path"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, problem := validateComfyModelPath(c.in)
			if problem == "" {
				t.Fatalf("accepted %q (returned %q)", c.in, got)
			}
			if !strings.Contains(problem, c.want) {
				t.Fatalf("reason = %q, want it to mention %q", problem, c.want)
			}
			if got != "" {
				t.Fatalf("a rejected path must return no value, got %q", got)
			}
		})
	}
}

// TestComfySetupFormPreFillsFromTheRunningComfyUI is the whole point of probing:
// the user CONFIRMS a found path instead of typing one.
func TestComfySetupFormPreFillsFromTheRunningComfyUI(t *testing.T) {
	srv := setupTestServer(t)
	root := modelsDir(t)
	srv.folderPathsFn = func(context.Context) (map[string][]string, error) {
		return map[string][]string{
			"checkpoints": {filepath.Join(root, "checkpoints")},
			"loras":       {filepath.Join(root, "loras")},
		}, nil
	}

	body := getSetupForm(t, srv).Body.String()
	if !strings.Contains(body, `value="`+root+`"`) {
		t.Fatalf("form must pre-fill the probed root %q:\n%s", root, body)
	}
	if !strings.Contains(body, "Your ComfyUI reports this folder") {
		t.Errorf("a pre-filled form must say where the value came from:\n%s", body)
	}
	if !strings.Contains(body, `hx-post="/workflows/7/comfy-setup"`) {
		t.Errorf("form must POST back to the save endpoint:\n%s", body)
	}
	if !strings.Contains(body, `name="csrf_token"`) {
		t.Errorf("form must carry a CSRF token:\n%s", body)
	}
}

// TestComfySetupFormDegradesWhenTheProbeCannotAnswer: an older ComfyUI (404), a
// wedged one, and a genuinely ambiguous install must all land on the SAME
// type-it-yourself form rather than an error or a guess.
func TestComfySetupFormDegradesWhenTheProbeCannotAnswer(t *testing.T) {
	cases := map[string]func(context.Context) (map[string][]string, error){
		"endpoint absent": func(context.Context) (map[string][]string, error) {
			return nil, errors.New("folder_paths: HTTP 404")
		},
		"ambiguous tie": func(context.Context) (map[string][]string, error) {
			return map[string][]string{
				"checkpoints": {"/a/models/checkpoints"},
				"loras":       {"/b/models/loras"},
			}, nil
		},
		"suggests a path that does not exist": func(context.Context) (map[string][]string, error) {
			return map[string][]string{
				"checkpoints": {"/definitely/not/here/models/checkpoints"},
				"loras":       {"/definitely/not/here/models/loras"},
			}, nil
		},
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			srv := setupTestServer(t)
			srv.folderPathsFn = fn
			body := getSetupForm(t, srv).Body.String()
			if !strings.Contains(body, "did not report a models folder") {
				t.Fatalf("want the type-it-yourself form:\n%s", body)
			}
			if strings.Contains(body, `value="/definitely/not/here`) {
				t.Fatalf("must never pre-fill a path it just failed to validate:\n%s", body)
			}
			// Still a usable form, not a dead end.
			if !strings.Contains(body, `name="model_path"`) {
				t.Fatalf("degraded state must still offer the input:\n%s", body)
			}
		})
	}
}

// TestComfySetupRefusesToOfferAFormWhenComfyURLIsNotLocal is the fail-honest half:
// comfy_model_path is only HALF the precondition, so on a server whose ComfyUI is
// not local, saving a path would change nothing. Offering the form there would
// persist a setting, report success, and leave the button just as dead.
func TestComfySetupRefusesToOfferAFormWhenComfyURLIsNotLocal(t *testing.T) {
	srv := setupTestServer(t)
	srv.cfg.ComfyURL = "http://comfy.example.com:8188"
	srv.folderPathsFn = func(context.Context) (map[string][]string, error) {
		t.Error("a non-local ComfyUI must not be probed at all")
		return nil, nil
	}

	body := getSetupForm(t, srv).Body.String()
	if strings.Contains(body, `name="model_path"`) {
		t.Fatalf("must not offer a form that cannot help:\n%s", body)
	}
	if !strings.Contains(body, "not pointed at a ComfyUI on this machine") {
		t.Fatalf("must explain the real blocker:\n%s", body)
	}
}

// TestComfySetupSavePersistsAndMakesTheCTALive is the end-to-end claim the whole
// item rests on: a user looking at a REAL failed run, whose primary action is
// dead, saves the folder and gets a LIVE button back in the same interaction.
//
// 🔴 It drives an actual run to a settled preflight failure rather than calling the
// renderer directly. An earlier version asserted `data-run-seq` on a server with no
// run at all — dataRunSeq omits the attribute for seq<=0, so the fixture could not
// reach the state it was asserting. That is this repo's most-repeated vacuity mode,
// and here it showed up as a false RED, which is the lucky direction.
func TestComfySetupSavePersistsAndMakesTheCTALive(t *testing.T) {
	srv := setupTestServer(t)
	root := modelsDir(t)
	srv.runFn = func(context.Context, *store.Workflow, runUpdater, runOptions) (*runResult, error) {
		return &runResult{
			Preflight:     &comfy.PreflightReport{MissingModels: []string{"needed.safetensors"}},
			MissingModels: []comfy.MissingModel{{Filename: "needed.safetensors", Query: "needed", CivitaiType: "Checkpoint"}},
		}, nil
	}
	wfID := seedWorkflow(t, srv, store.WorkflowFormatAPI,
		`{"1":{"class_type":"CheckpointLoaderSimple","inputs":{"ckpt_name":"needed.safetensors"}}}`)

	if rec := post(t, srv, "/workflows/"+wfID+"/run-with-params", url.Values{}, true); rec.Code != http.StatusOK {
		t.Fatalf("start run = %d", rec.Code)
	}
	before := pollRunUntilDone(t, srv, wfID)

	// PRECONDITIONS — the panel really is in the blocked state, or nothing after
	// this proves anything.
	if !strings.Contains(before, "data-run-seq") {
		t.Fatalf("precondition: want a settled run panel:\n%s", before)
	}
	if strings.Contains(before, "install-missing-and-run") {
		t.Fatalf("precondition: the CTA must start DEAD:\n%s", before)
	}
	if !strings.Contains(before, "/comfy-setup") {
		t.Fatalf("precondition: the blocked panel must offer the setup step:\n%s", before)
	}

	rec := postSetupTo(t, srv, wfID, root, srv.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	stored, ok, err := srv.store.GetSetting(comfyModelPathSettingKey)
	if err != nil || !ok || stored != root {
		t.Fatalf("stored %q (ok=%v err=%v), want %q", stored, ok, err, root)
	}

	// THE CLAIM: the same panel now offers the live install action.
	after := rec.Body.String()
	if !strings.Contains(after, "data-run-seq") {
		t.Fatalf("save must answer with the run panel:\n%s", after)
	}
	if !strings.Contains(after, "/workflows/"+wfID+"/install-missing-and-run") {
		t.Fatalf("the CTA must be LIVE after the save:\n%s", after)
	}
	// ⚠ This used to assert "the setup step must be gone once it is done". That is
	// what made comfy_model_path a WRITE-ONCE value: the disclosure was the only
	// control in the app that can change it, there is no settings page, and a
	// wrong-but-writable path validates fine — so a saved mistake was correctable
	// only by hand-editing SQLite or writing the config.yaml this control exists to
	// avoid. What must change is the summary's PROMPT, not its presence: the setup
	// step becomes a change affordance.
	if !strings.Contains(after, "/comfy-setup") {
		t.Errorf("the folder must stay changeable after the save — this is the only "+
			"control in the app that can change it:\n%s", after)
	}
	if strings.Contains(after, "Set up automatic install") {
		t.Errorf("a configured install must not still be asked to SET UP the path:\n%s", after)
	}
	if !strings.Contains(after, "Change where civitai-manager installs model files") {
		t.Errorf("want the change-affordance summary after the save:\n%s", after)
	}
	if !srv.comfyDownloadEligible() {
		t.Error("the server must be eligible after the save")
	}
}

// TestRejectedPathRetargetsInsteadOfEatingTheFailureReport guards BOTH directions of
// the setup form's swap target, because they are genuinely different and the form can
// only carry one.
//
// 🔴 SUCCESS must land in #run-status — that is how the disabled CTA the user was
// looking at goes live in the same interaction. A REJECTION must not: the rejection
// answer is only the form, and swapping it into #run-status deletes the headline, the
// lower-bound caveat, the CTA, the missing-nodes panel, every per-file picker,
// Technical details and "Run again", leaving a bare context-free form. The typed-path
// branch is the documented degradation for EVERY probe failure (404, timeout, tie,
// rejected suggestion), so the users most likely to be rejected are exactly the ones
// with nothing pre-filled.
func TestRejectedPathRetargetsInsteadOfEatingTheFailureReport(t *testing.T) {
	srv := setupTestServer(t)
	root := modelsDir(t)

	// REJECTION — retargeted at the disclosure, and the response is its INNER content.
	rec := postSetup(t, srv, "not-an-absolute-path", srv.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (a 4xx is swallowed by htmx and would be invisible)", rec.Code)
	}
	body := rec.Body.String()
	// PRECONDITION: this really is the rejection branch, and it really is small enough
	// to destroy the report it would otherwise replace.
	if !strings.Contains(body, "absolute path") {
		t.Fatalf("precondition: want the rejection reason:\n%s", body)
	}
	if strings.Contains(body, "data-run-seq") {
		t.Fatalf("precondition: a rejection must not answer with the run panel:\n%s", body)
	}
	if got := rec.Header().Get("HX-Retarget"); got != "#"+comfySetupContainerID {
		t.Errorf("HX-Retarget = %q, want %q — without it this fragment replaces the whole "+
			"failure report:\n%s", got, "#"+comfySetupContainerID, body)
	}
	// With swap=innerHTML the response is the container's CONTENTS, so it must not
	// carry the container's own id: that would nest a second #comfy-setup one level
	// deeper on every rejection.
	if strings.Contains(body, `id="`+comfySetupContainerID+`"`) {
		t.Errorf("the retargeted response must be the container's INNER content, not a "+
			"second element carrying its id:\n%s", body)
	}
	// The typed value survives, so a typo is corrected rather than retyped.
	if !strings.Contains(body, `value="not-an-absolute-path"`) {
		t.Errorf("the rejected input must be preserved:\n%s", body)
	}

	// SUCCESS — NOT retargeted, so the panel re-renders and the CTA goes live.
	ok := postSetup(t, srv, root, srv.csrf)
	if ok.Code != http.StatusOK {
		t.Fatalf("status = %d", ok.Code)
	}
	if got := ok.Header().Get("HX-Retarget"); got != "" {
		t.Errorf("a SUCCESSFUL save must not retarget (got %q) — it has to replace the run "+
			"panel for the CTA to go live", got)
	}
}

// TestSuggestionIsCorroboratedAgainstTheFilesystem closes the remaining half of the
// hostile-payload gap.
//
// 🔴 comfy.ModelsRoot's vote floor stops a ONE-category payload, and nothing more: a
// hostile ComfyUI can list five fabricated categories under one parent as cheaply as
// one, and the parent it nominates need not itself look like anything. The
// downstream re-validation cannot help — it only asks whether the path exists, is a
// directory, and is writable, which any home directory satisfies. Measured before
// this check, a payload naming /home/zach/<category> for several categories got
// /home/zach accepted and PRE-FILLED into the form.
//
// So the suggestion must be checkable, not merely self-consistent: at least one
// reported category directory has to actually exist on disk.
func TestSuggestionIsCorroboratedAgainstTheFilesystem(t *testing.T) {
	// A directory that exists and is writable, but whose claimed category folders do
	// not exist — the shape of a home directory named by a hostile payload.
	bare := t.TempDir()

	t.Run("uncorroborated layout is not suggested", func(t *testing.T) {
		srv := setupTestServer(t)
		srv.folderPathsFn = func(context.Context) (map[string][]string, error) {
			return map[string][]string{
				"checkpoints": {filepath.Join(bare, "checkpoints")},
				"loras":       {filepath.Join(bare, "loras")},
				"vae":         {filepath.Join(bare, "vae")},
			}, nil
		}
		// PRECONDITION: the vote floor does NOT reject this payload, so what is being
		// measured is the corroboration and nothing else. Without this the test would
		// pass against a floor of 4 and prove nothing about the filesystem check.
		if got := comfy.ModelsRoot(map[string][]string{
			"checkpoints": {filepath.Join(bare, "checkpoints")},
			"loras":       {filepath.Join(bare, "loras")},
			"vae":         {filepath.Join(bare, "vae")},
		}); got != bare {
			t.Fatalf("precondition: the vote must nominate %q, got %q", bare, got)
		}
		// PRECONDITION: the nominated directory passes the downstream re-validation, so
		// it really would have been pre-filled.
		if _, problem := validateComfyModelPath(bare); problem != "" {
			t.Fatalf("precondition: %q must be re-validation-clean, got %q", bare, problem)
		}

		body := getSetupForm(t, srv).Body.String()
		if strings.Contains(body, `value="`+bare+`"`) {
			t.Errorf("a nomination whose category folders do not exist must not be pre-filled "+
				"(%q):\n%s", bare, body)
		}
		if !strings.Contains(body, "did not report a models folder") {
			t.Errorf("want the degraded type-it-yourself copy:\n%s", body)
		}
	})

	// POSITIVE CONTROL. Without it, an always-empty suggestion passes the case above.
	t.Run("a real layout is still suggested", func(t *testing.T) {
		srv := setupTestServer(t)
		root := modelsDir(t) // creates <root>/checkpoints for real
		srv.folderPathsFn = func(context.Context) (map[string][]string, error) {
			return map[string][]string{
				"checkpoints": {filepath.Join(root, "checkpoints")},
				"loras":       {filepath.Join(root, "loras")}, // never created — one is enough
			}, nil
		}
		body := getSetupForm(t, srv).Body.String()
		if !strings.Contains(body, `value="`+root+`"`) {
			t.Errorf("a corroborated root must still be pre-filled (%q):\n%s", root, body)
		}
	})
}

// TestBlockedCTANeverPostsWhileThePathIsUnset is the standing guard behind the
// state above, and it asserts exactly what its name says: as long as no models
// folder is known, nothing on this panel POSTs the batch install. A CTA that POSTs
// with no destination configured would resolve files and then fail at the write,
// after the network round-trips.
//
// 🔴 IT USED TO ALSO ASSERT `Contains(body, "disabled")`, and that assertion was
// GREEN BY ACCIDENT — the fifth guard pinning the dead button this PR removed, and
// the one missed when the other four were repointed. Nothing on the blocked panel
// renders a `disabled` attribute any more; the substring was satisfied by the setup
// CTA's `hx-disabled-elt="this"`, which is htmx's in-flight lockout and not a dead
// control at all. Measured: deleting that one cosmetic hx attribute (build clean,
// vet clean) made this test the ONLY failure in the package, reporting "the CTA must
// render disabled" — i.e. instructing whoever hit the red to reintroduce the very
// button this change removed. Do not restore it; a substring that a POSTing control
// and an inert one satisfy identically cannot express this invariant.
//
// The POSITIVE CONTROL below is what the removed line was really buying: without it
// a branch that rendered NOTHING would satisfy the no-POST assertion perfectly.
func TestBlockedCTANeverPostsWhileThePathIsUnset(t *testing.T) {
	srv := setupTestServer(t)
	body := renderString(t, runStatusFragment(twoMissingSnapshot(), 7, "tok", srv.comfyDownloadEligible(), fullMaturityRange()))

	if srv.comfyDownloadEligible() {
		t.Fatal("precondition: an unset path must be ineligible")
	}
	// POSITIVE CONTROL: the blocked branch really rendered its action. An empty
	// fragment passes the assertion below and proves nothing.
	if !strings.Contains(body, `hx-get="/workflows/7/comfy-setup"`) {
		t.Fatalf("precondition: want the blocked panel's setup action:\n%s", body)
	}
	// THE ASSERTION.
	if strings.Contains(body, "install-missing-and-run") {
		t.Fatalf("an unset models folder must not render a POSTing CTA:\n%s", body)
	}
}

// TestComfySetupSaveKeepsTheTypedValueOnRejection: a rejected path must come back
// as the FORM carrying what the user typed plus the reason — never as the run
// panel, which would swallow the reason and look like the save worked.
func TestComfySetupSaveKeepsTheTypedValueOnRejection(t *testing.T) {
	srv := setupTestServer(t)
	const typo = "/opt/ComfyUI/modles"

	rec := postSetup(t, srv, typo, srv.csrf)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (an htmx client discards a 4xx body)", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `value="`+typo+`"`) {
		t.Errorf("the typed value must survive a rejection:\n%s", body)
	}
	if !strings.Contains(body, "nothing at "+typo) {
		t.Errorf("the rejection must say what is wrong:\n%s", body)
	}
	if strings.Contains(body, "data-run-seq") {
		t.Errorf("a rejection must not answer with the run panel:\n%s", body)
	}
	if _, ok, _ := srv.store.GetSetting(comfyModelPathSettingKey); ok {
		t.Error("a rejected path must not be persisted")
	}
}

// TestComfySetupSaveRequiresCSRF: it is a state-changing POST like every other.
func TestComfySetupSaveRequiresCSRF(t *testing.T) {
	srv := setupTestServer(t)
	root := modelsDir(t)

	rec := postSetup(t, srv, root, "not-the-token")
	if rec.Code == http.StatusOK {
		t.Fatalf("a bad CSRF token must be refused, got %d", rec.Code)
	}
	if _, ok, _ := srv.store.GetSetting(comfyModelPathSettingKey); ok {
		t.Error("a CSRF-refused request must not persist anything")
	}
}

// TestComfySetupIsLoopbackGated: both halves take/hand out an arbitrary filesystem
// path, which is exactly what extraPathsAllowed fences off on a non-loopback bind.
func TestComfySetupIsLoopbackGated(t *testing.T) {
	root := modelsDir(t)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			srv := setupTestServer(t)
			srv.cfg.Addr = "0.0.0.0:8787"
			srv.folderPathsFn = func(context.Context) (map[string][]string, error) {
				t.Error("a gated request must not probe ComfyUI")
				return nil, nil
			}

			var rec *httptest.ResponseRecorder
			if method == http.MethodGet {
				rec = getSetupForm(t, srv)
			} else {
				rec = postSetup(t, srv, root, srv.csrf)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "non-loopback") {
				t.Fatalf("want the gating note:\n%s", body)
			}
			if strings.Contains(body, `name="model_path"`) {
				t.Errorf("a gated GET must not hand out a path form:\n%s", body)
			}
			if _, ok, _ := srv.store.GetSetting(comfyModelPathSettingKey); ok {
				t.Error("a gated POST must not persist anything")
			}
		})
	}
}

// TestComfySetupFormIsChangeableWhenAlreadyConfigured pins the CORRECTION to this
// test's own earlier expectation.
//
// ⚠ It used to assert the opposite — "no form is needed when it is already
// configured" — and that is exactly what made the body's closing promise, "Change it
// any time from this panel", false. comfy_model_path is the one value deciding where
// gigabytes land; this app has no settings page; and a wrong-but-writable choice
// passes validation. Read-only was therefore a dead end correctable only by writing
// the config.yaml this control exists to avoid, or by hand-editing SQLite.
//
// The fragment must now NAME the folder in force AND offer a pre-filled form to
// change it — the pre-fill is what stops "change" meaning "retype from memory".
func TestComfySetupFormIsChangeableWhenAlreadyConfigured(t *testing.T) {
	srv := setupTestServer(t)
	root := modelsDir(t)
	srv.cfg.ComfyModelPath = root

	body := getSetupForm(t, srv).Body.String()
	// Errorf, not Fatalf: the CHANGEABILITY assertions below are the point of this
	// test, and a Fatalf on the wording would stop the run before any of them
	// reported — red at a copy check reads as a copy problem and hides the dead end.
	if !strings.Contains(body, "currently installs model files into "+root) {
		t.Errorf("want the folder in force named:\n%s", body)
	}
	if !strings.Contains(body, `name="model_path"`) {
		t.Errorf("a configured install must still be able to CHANGE the folder:\n%s", body)
	}
	// Pre-filled with the saved value, so the promise is "change it", not "retype it".
	if !strings.Contains(body, `value="`+root+`"`) {
		t.Errorf("the form must be pre-filled with the saved path:\n%s", body)
	}
	// The promise the whole test exists for.
	if !strings.Contains(body, "Change it any time from this panel") {
		t.Errorf("want the change-it-any-time promise, now that it is true:\n%s", body)
	}
}

// TestComfySetupIsReachableInBothStates is the reachability half of the fix above: a
// form nobody can open is the same dead end as no form at all.
//
// 🔴 comfySetupDisclosure had ONE call site, inside installAllMissingAction's
// `!p.Available` branch, so the moment a path was saved the disclosure stopped
// rendering anywhere in the app. This is the repo's documented recurring defect —
// code that is correct and never runs — so the guard asserts the CALL SITES, through
// the real action, in both states.
//
// ⚠ The two states now reach it through DIFFERENT controls, which is the point of
// the current rework: blocked gets a real primary button (choosing the folder IS the
// recovery action), working gets a collapsed disclosure (it is an afterthought there).
// What must hold in both is that GET /workflows/{id}/comfy-setup is one click away.
func TestComfySetupIsReachableInBothStates(t *testing.T) {
	plan := batchInstallPlan{
		Available:   true,
		Installable: []comfy.MissingModel{{Filename: "upscaler.pth", CivitaiType: "Upscaler"}},
	}
	working := renderString(t, installAllMissingAction(plan, 1, 7, "tok"))
	// PRECONDITION: this really is the WORKING state — a live submit button, not the
	// control the blocked branch renders.
	if !strings.Contains(working, "install-missing-and-run") {
		t.Fatalf("precondition: want the enabled install action:\n%s", working)
	}
	if !strings.Contains(working, "/comfy-setup") {
		t.Errorf("a configured install must still reach the setup disclosure — it is the "+
			"only control in the app that can change comfy_model_path:\n%s", working)
	}
	if !strings.Contains(working, "Change where civitai-manager installs model files") {
		t.Errorf("the configured summary must offer a CHANGE, not a setup step:\n%s", working)
	}

	plan.Available = false
	plan.SetupCanHelp = true
	blocked := renderString(t, installAllMissingAction(plan, 1, 7, "tok"))
	if !strings.Contains(blocked, "/comfy-setup") {
		t.Errorf("the blocked panel must still offer the setup step:\n%s", blocked)
	}
	if !strings.Contains(blocked, "Set up automatic install") {
		t.Errorf("the blocked state's primary action must read as a setup step:\n%s", blocked)
	}
}

// TestBlockedSetupCTAIsReplacedByTheFormItLoads pins the shape of the blocked
// state's primary action, and the copy consequence that shape has.
//
// 🔴 The CTA renders INSIDE #comfy-setup and targets it with innerHTML, so clicking
// it replaces the button with the form. That is deliberate — it leaves no stale
// control that has already been used, it needs no `once` trigger (which would render
// the button permanently inert-looking), and a rejected save, which HX-Retargets to
// #comfy-setup, lands in the container it came from.
//
// The copy consequence is what this test exists for and it shipped wrong: the form's
// unconfigured branch said "Tell it where that is and THE BUTTON ABOVE starts
// working". True while the form lived in a disclosure beneath a disabled CTA; false
// the moment the form replaces that CTA. Caught by clicking it in a real browser —
// no markup assertion in this package could see it, because both halves are correct
// in isolation.
func TestBlockedSetupCTAIsReplacedByTheFormItLoads(t *testing.T) {
	plan := batchInstallPlan{
		Installable:  []comfy.MissingModel{{Filename: "upscaler.pth", CivitaiType: "Upscaler"}},
		SetupCanHelp: true,
	}
	blocked := renderString(t, installAllMissingAction(plan, 1, 7, "tok"))

	// PRECONDITION: this is the blocked branch, and the CTA really is the trigger.
	if !strings.Contains(blocked, `hx-get="/workflows/7/comfy-setup"`) {
		t.Fatalf("precondition: want the setup CTA:\n%s", blocked)
	}
	// The trigger sits INSIDE the container it swaps, which is what makes the click
	// replace it. Assert the ORDER of the two attributes' owners by extent, not by a
	// bare Contains of both strings — they are both present either way.
	ci := strings.Index(blocked, `id="`+comfySetupContainerID+`"`)
	bi := strings.Index(blocked, `hx-get="/workflows/7/comfy-setup"`)
	if ci < 0 || bi < 0 || ci > bi {
		t.Errorf("the CTA must render INSIDE #%s so the loaded form replaces it "+
			"(container at %d, button at %d):\n%s", comfySetupContainerID, ci, bi, blocked)
	}
	if !strings.Contains(blocked, `hx-swap="innerHTML"`) {
		t.Errorf("the CTA must swap the container's innerHTML:\n%s", blocked)
	}
	// Exactly one container: a second id="comfy-setup" on the page would make the
	// htmx target ambiguous and could nest one inside the other on every rejection.
	if n := strings.Count(blocked, `id="`+comfySetupContainerID+`"`); n != 1 {
		t.Errorf("want exactly one #%s container, got %d:\n%s", comfySetupContainerID, n, blocked)
	}

	// THE COPY CONSEQUENCE. The form that replaces the CTA must not point at a button
	// that is, by then, gone.
	srv := setupTestServer(t)
	form := renderString(t, srv.comfySetupFragment(context.Background(), 7, "", ""))
	if !strings.Contains(form, `name="model_path"`) {
		t.Fatalf("precondition: want the unconfigured setup form:\n%s", form)
	}
	if strings.Contains(form, "button above") {
		t.Errorf("the form replaces the button it would be referring to — it cannot tell "+
			"the reader that 'the button above' starts working:\n%s", form)
	}
}

// TestComfySetupDisclosureIsLazyAndAddsExactlyOneControl guards the measurement
// this rework is judged on. The working panel must carry the change-the-folder
// affordance WITHOUT eagerly rendering its form: the panel already carries ~123
// hidden controls, and making that worse is the opposite of the goal.
func TestComfySetupDisclosureIsLazyAndAddsExactlyOneControl(t *testing.T) {
	body := renderString(t, comfySetupDisclosure(7))

	if strings.Contains(body, `name="model_path"`) || strings.Contains(body, "<form") {
		t.Fatalf("the disclosure must NOT eagerly render the form:\n%s", body)
	}
	if strings.Contains(body, "<button") {
		t.Fatalf("the disclosure must add no button before it is opened:\n%s", body)
	}
	if n := strings.Count(body, "<summary"); n != 1 {
		t.Fatalf("want exactly one summary control, got %d:\n%s", n, body)
	}
	// Lazy: fetched on the details' own toggle, once.
	if !strings.Contains(body, `hx-trigger="toggle once"`) {
		t.Fatalf("the body must load on first open, not on page load:\n%s", body)
	}
	// The swap target is a STABLE container, never the trigger element itself
	// (this package's streaming-job invariant).
	if !strings.Contains(body, `hx-target="#`+comfySetupContainerID+`"`) ||
		!strings.Contains(body, `id="`+comfySetupContainerID+`"`) {
		t.Fatalf("must swap into a stable container:\n%s", body)
	}
}

// TestVanishedModelFolderIsReportedInsteadOfClaimedAsInForce is the guard for the
// state a saved path can reach on its own: the folder was deleted or its drive was
// unmounted, so comfyDownloadEligible's os.Stat fails and the primary CTA goes back
// to DISABLED — while nothing on screen said why.
//
// 🔴 The two halves of the panel contradicted each other. The summary read "Set up
// automatic install", implying no folder was known; the body read "civitai-manager
// currently installs model files into <gone path>", implying one was and it worked.
// The disabled button disproves the body, and the user's only route to the actual
// reason was to re-save the same value and read the rejection.
//
// This is reachable without any user error at all — an external drive that is not
// mounted at boot produces it — which is why it is a guard and not a hypothetical.
func TestVanishedModelFolderIsReportedInsteadOfClaimedAsInForce(t *testing.T) {
	srv := setupTestServer(t)
	root := modelsDir(t)
	if err := srv.store.SetSetting(comfyModelPathSettingKey, root); err != nil {
		t.Fatalf("seed the saved path: %v", err)
	}

	// PRECONDITION 1: while the folder exists this really is the WORKING state — the
	// server is eligible and the body names the folder in force. Without this the
	// assertions below could pass on a server that was never configured at all.
	if !srv.comfyDownloadEligible() {
		t.Fatalf("precondition: the server must start eligible with %s saved", root)
	}
	if before := getSetupForm(t, srv).Body.String(); !strings.Contains(before, "currently installs model files into "+root) {
		t.Fatalf("precondition: want the in-force line before the folder goes away:\n%s", before)
	}

	// The event: the folder goes away. Nothing else changes — the setting still holds
	// the same path, which is the whole point.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove the models dir: %v", err)
	}

	// PRECONDITION 2: the app really is in the blocked state now, and the saved value
	// really did survive. This is what makes the two claims collide.
	if srv.comfyDownloadEligible() {
		t.Fatalf("precondition: the server must be INELIGIBLE once %s is gone", root)
	}
	if got := srv.comfyModelPath(); got != root {
		t.Fatalf("precondition: the saved path must survive the folder, got %q want %q", got, root)
	}

	body := getSetupForm(t, srv).Body.String()

	// THE ASSERTION. The panel must not claim it installs into a folder it cannot use.
	if strings.Contains(body, "currently installs model files into") {
		t.Errorf("the setup body claims a vanished folder is in force while the CTA above it "+
			"is disabled — the app is contradicting itself and neither half says why:\n%s", body)
	}
	// And it must say WHAT is wrong, naming the folder, in the validator's own words
	// (the same sentence a re-save would answer with).
	if !strings.Contains(body, "cannot use the folder it has saved ("+root+")") {
		t.Errorf("the body must name the saved folder as unusable:\n%s", body)
	}
	if !strings.Contains(body, "There is nothing at "+root+" that this server can read.") {
		t.Errorf("the body must carry the validator's own reason, not a generic one:\n%s", body)
	}
	// The recovery is still one interaction away: the form is here and pre-filled, so
	// the path can be corrected rather than retyped.
	if !strings.Contains(body, `name="model_path"`) || !strings.Contains(body, `value="`+root+`"`) {
		t.Errorf("the pre-filled form must still be offered so the folder can be corrected:\n%s", body)
	}
}

// TestBlockedSetupCTADoesNotDenyASavedFolder pins the OTHER half of the same
// contradiction — the eagerly-rendered blocked control, which is all the user sees
// before opening anything.
//
// The blocked branch is chosen by batchInstallPlan.Available ("can this install run
// now"), not by "is a path saved", so it renders over a CONFIGURED install whenever
// the saved folder stops working — a deleted directory, an unmounted drive. It may
// therefore not assert anything about whether a folder is known.
//
// ⚠ This used to test the blocked `<summary>`; the blocked state now renders a
// primary button plus one explanatory line instead, so the guard follows the copy to
// where it lives.
func TestBlockedSetupCTADoesNotDenyASavedFolder(t *testing.T) {
	plan := batchInstallPlan{
		Installable:  []comfy.MissingModel{{Filename: "upscaler.pth", CivitaiType: "Upscaler"}},
		SetupCanHelp: true,
	}
	blocked := renderString(t, installAllMissingAction(plan, 1, 7, "tok"))
	working := renderString(t, installAllMissingAction(batchInstallPlan{
		Available:   true,
		Installable: plan.Installable,
	}, 1, 7, "tok"))

	// PRECONDITION: the two states really do render different copy, or the assertion
	// below is about a string that never varies.
	if blocked == working {
		t.Fatalf("precondition: the blocked and working states must differ:\n%s", blocked)
	}
	// It still reads as a setup step, and still names what it needs rather than a
	// config key — both are the affordance the blocked state depends on.
	if !strings.Contains(blocked, "Set up automatic install") {
		t.Errorf("the blocked state must still offer the setup step:\n%s", blocked)
	}
	if !strings.Contains(blocked, "where ComfyUI keeps its models") {
		t.Errorf("the blocked state must still name what it needs:\n%s", blocked)
	}
	// THE ASSERTION: it must not claim the folder is UNKNOWN. It is reached with a
	// folder saved-but-broken, where that is false.
	//
	// ⚠ Honest limit: this is a copy check, so it pins the two natural spellings of
	// the claim that actually shipped, not a general property. A reworded assertion of
	// ignorance would slip past it — the durable part is the comment on
	// blockedInstallAction explaining WHY this branch cannot be read as "no path is
	// saved".
	for _, denial := range []string{
		"needs to know where ComfyUI keeps its models",
		"does not know where ComfyUI keeps its models",
	} {
		if strings.Contains(blocked, denial) {
			t.Errorf("the blocked CTA denies that a folder is saved (%q), but it also "+
				"renders when one IS saved and merely stopped working:\n%s", denial, blocked)
		}
	}
}
