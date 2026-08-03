package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ZacxDev/civitai-manager/internal/comfy"
	"github.com/ZacxDev/civitai-manager/internal/store"
)

// ─────────────────────────────────────────────────────────────────────────────
// ONE OWNER FOR #run-status: EVERY WRITER GOES THROUGH runStatusBody.
//
// The PR's fix is that a response writing into #run-status ALSO carries the
// out-of-band clear of #cm-run-readiness, computed from the SAME snapshot
// (runStatusBody, run_pages.go). The shipped code is correct at every writer — but
// until this file existed the PROPERTY was enforced at exactly ONE of them.
//
// Measured on this branch by the PR audit: reverting 13 of the 14 runStatusBody call
// sites back to runStatusFragment left `go build`, `go vet` and the ENTIRE
// internal/web suite GREEN, and so did mutating renderRunStatus alone — the one
// response that must remove the line the user is currently looking at (the Generate
// click on /run, /run-with-params, /run-substitute, /run-with-options). A property
// enforced at one of fourteen sites is the same shape as the original defect: two
// containers, two swap paths, no single owner.
//
// TWO GUARDS, AND EACH PROVES SOMETHING THE OTHER CANNOT.
//
//	structural  — TestEveryRunStatusWriterGoesThroughRunStatusBody parses the
//	              package and pins WHO may call runStatusFragment directly. It sees
//	              all 14 sites at once and does not go stale when a 15th is added.
//	              It cannot see whether the emitted markup is right.
//	behavioural — TestEveryRunStartingEndpointClearsTheReadinessLine drives the real
//	              routes and asserts the real bytes. It proves the mechanism reaches
//	              the wire, including with the WRONG-ID mutation the structural check
//	              is blind to (runStatusBody(snap, 0, …) type-checks fine). It covers
//	              only the endpoints reachable without network or a real download.
//
// 🔴 THE STRUCTURAL ONE IS THE ANSWER TO "AN ENDPOINT TABLE GOES STALE". A table
// keyed on today's endpoint list is the same failure one level up — someone adds a
// 15th writer and the list silently does not cover it. The structural guard is
// derived from the source at run time, so a new writer that reaches for
// runStatusFragment fails it the moment it is written, with no list to update.
//
// HONEST LIMIT OF BOTH, stated here so it is not discovered as a surprise: a writer
// that renders SOMETHING ELSE ENTIRELY into #run-status is invisible to both. Two
// such bypasses exist and are deliberate — renderResolveFallback and
// renderSubstituteOffer (run_download.go), both reachable only from an
// already-rendered failure panel and both starting no run. Their comments name them
// as the site where a future change that DOES start a run would break this silently.
// ─────────────────────────────────────────────────────────────────────────────

// runStatusFragmentCallers is the ASSERTED LEDGER of every non-test function allowed
// to call runStatusFragment directly. Same discipline as
// .github/deadcode-allow.txt and routeReachabilityAllow: the guard fails when the set
// GROWS (a new writer bypassed runStatusBody) *and* when it SHRINKS (an entry that no
// longer describes anything — the function was renamed, deleted, or stopped calling
// it, and a stale ledger entry is how a scanner that has quietly stopped working
// still reports green).
var runStatusFragmentCallers = map[string]string{
	"runStatusBody": "THE owner. It is the fragment PLUS runReadinessCleared from the same " +
		"snapshot, and it is the only reason any handler's response clears the line.",
	"generateSection": "the FULL-PAGE render, and it must NOT use runStatusBody. hx-swap-oob is " +
		"processed only on an htmx response, so on a page load the marker would sit inert " +
		"in the markup; the page path decides the same question by not emitting the lazy " +
		"loader at all (see runZone).",
}

// minRunStatusBodyCallSites is the NEGATIVE CONTROL for the scan, as a literal —
// never derived from what the scan found, or the two would move together and no
// mutation could separate them (this repo has shipped exactly that: an expectation
// computed from the same source as the thing it measured).
//
// There were 14 call sites when this was written. The floor is deliberately well
// under that so an honest consolidation of two handlers does not fail the build,
// while the mutation this guard exists for — reverting the writers en masse, which
// leaves runStatusBody with a single caller — is far below it.
const minRunStatusBodyCallSites = 8

// runStatusCallSites returns, for every direct call to the named package-level
// function in this package's NON-TEST source, the enclosing function's name mapped to
// the source positions of its calls.
//
// Non-test only, via the shared nonTestGoFile filter: a _test.go file calling
// runStatusFragment is legitimate (a dozen already do) and must never register as a
// production writer.
func runStatusCallSites(t *testing.T, fn string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	files := 0
	out := map[string][]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			files++
			ast.Inspect(file, func(n ast.Node) bool {
				decl, ok := n.(*ast.FuncDecl)
				if !ok || decl.Body == nil {
					return true
				}
				caller := decl.Name.Name
				ast.Inspect(decl.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == fn {
						out[caller] = append(out[caller], fset.Position(call.Pos()).String())
					}
					return true
				})
				return true
			})
		}
	}
	// The harness's own precondition. A filter or a path that matched nothing
	// produces an empty result set, which reads exactly like a clean package.
	if files < 20 {
		t.Fatalf("the scan parsed only %d non-test files in this package — the scanner is "+
			"broken, so every result below is a fact about the scanner, not the code", files)
	}
	return out
}

// TestEveryRunStatusWriterGoesThroughRunStatusBody is the structural half.
//
// 🔴 MUTATION-VERIFIED against the exact revert the audit performed. Reverting
// renderRunStatus ALONE to runStatusFragment( fails it by name ("renderRunStatus
// calls runStatusFragment directly"); reverting all 13 non-poller sites fails it with
// all 11 enclosing functions named AND trips the call-site floor. `go build` and
// `go vet` stay clean under both, which is the whole point — nothing else on the
// gate can see either mutation.
func TestEveryRunStatusWriterGoesThroughRunStatusBody(t *testing.T) {
	fragment := runStatusCallSites(t, "runStatusFragment")
	body := runStatusCallSites(t, "runStatusBody")

	// (1) NOBODY NEW may call the fragment directly.
	var offenders []string
	for caller, at := range fragment {
		if _, ok := runStatusFragmentCallers[caller]; !ok {
			offenders = append(offenders, caller+" ("+strings.Join(at, ", ")+")")
		}
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s calls runStatusFragment directly, bypassing runStatusBody — its response "+
			"writes into #run-status WITHOUT the out-of-band clear of #%s, so a readiness "+
			"line already on screen survives the swap and states the same counts as the panel "+
			"landing under it. Call runStatusBody instead; if this really is a legitimate "+
			"exception, it goes in runStatusFragmentCallers WITH the reason.", o, runReadinessID)
	}

	// (2) NO ENTRY may be stale. A ledger describing something that no longer exists
	// is how a scanner that has stopped finding anything still reports green.
	for caller, why := range runStatusFragmentCallers {
		if len(fragment[caller]) == 0 {
			t.Errorf("runStatusFragmentCallers still lists %q (%s) but nothing in the non-test "+
				"source calls runStatusFragment from it. Either the function was renamed or "+
				"deleted — remove the entry — or this scan has stopped working, in which case "+
				"result (1) above is meaningless.", caller, why)
		}
	}

	// (3) THE FLOOR. Reverting the writers en masse satisfies (1) only by adding names,
	// but it also strands runStatusBody with a single caller; this catches that
	// independently, and catches a scan that silently matched nothing.
	total := 0
	for _, at := range body {
		total += len(at)
	}
	if total < minRunStatusBodyCallSites {
		t.Errorf("only %d call sites of runStatusBody across %d functions (floor %d). Either the "+
			"run-status writers have been reverted to runStatusFragment wholesale, or this scan "+
			"is not seeing the package at all.", total, len(body), minRunStatusBodyCallSites)
	}
}

// ── the behavioural half ─────────────────────────────────────────────────────

// runStatusWriterCase is one REAL endpoint that writes into #run-status.
//
// 🔴 shapes IS NOT DECORATION. A row whose handler would START A RUN when the
// singleton is free can only be exercised with a run already parked as running (it
// then takes the refusal branch and renders the OTHER job's snapshot — a real,
// shipped branch). A row that starts nothing is additionally exercised against a
// SETTLED run, which is the case with the larger blast radius: those responses carry
// no poller, so nothing re-asserts the clear ~1 s later and a missing OOB element
// leaves the stale line on screen permanently.
type runStatusWriterCase struct {
	name string
	// path is built from the seeded workflow id (one row's route carries no id).
	path func(id string) string
	// form is the POST body. modelsRoot is this row's private ComfyUI models root.
	form func(id, modelsRoot string) url.Values
	// setup runs before the POST. It is where a row makes its branch reachable.
	setup  func(t *testing.T, srv *Server, modelsRoot string)
	shapes []runShape
	// site names the runStatusBody call this row exercises, so a failure says which
	// one and a reader can check the mapping without re-deriving it.
	site string
}

// runStatusWriterCases covers every #run-status writer reachable WITHOUT outbound
// network and WITHOUT a real download.
//
// ⚠ THE LIST IS INCOMPLETE ON PURPOSE, AND THAT IS THE STALENESS THIS FILE ADMITS TO.
// The remaining runStatusBody sites — the post-resolution branches of
// download-and-run and install-option-and-run, and install-missing-and-run's
// download branch — all sit behind a live CivitAI round-trip or a real file fetch,
// which is not something a unit test may perform (see CLAUDE.md on the
// download-enqueue endpoint firing a real 2 GB fetch). They are covered by the
// STRUCTURAL guard above, which needs no fixture and no network. If you add a writer,
// the structural guard fails immediately; adding a row here is the optional extra.
var runStatusWriterCases = []runStatusWriterCase{{
	name:   "POST /workflows/{id}/run",
	site:   "renderRunStatus (run_handlers.go)",
	path:   func(id string) string { return "/workflows/" + id + "/run" },
	form:   func(string, string) url.Values { return url.Values{} },
	shapes: []runShape{shapeRunning},
}, {
	name:   "POST /workflows/{id}/run-with-params",
	site:   "renderRunStatus (run_handlers.go)",
	path:   func(id string) string { return "/workflows/" + id + "/run-with-params" },
	form:   func(string, string) url.Values { return url.Values{} },
	shapes: []runShape{shapeRunning},
}, {
	name:   "POST /workflows/{id}/run/queue",
	site:   "handleWorkflowRunQueue's respond closure (run_queue_handlers.go)",
	path:   func(id string) string { return "/workflows/" + id + "/run/queue" },
	form:   func(string, string) url.Values { return url.Values{"count": {"1"}} },
	shapes: []runShape{shapeRunning},
}, {
	name: "POST /workflows/{id}/download-and-run (already-installed fast path)",
	site: "handleWorkflowDownloadAndRun (run_download.go)",
	path: func(id string) string { return "/workflows/" + id + "/download-and-run" },
	form: func(string, string) url.Values {
		return url.Values{"filename": {readinessMissingModel}, "type": {readinessModelType}}
	},
	// The fast path is the branch that reaches runStatusBody without any network: an
	// eligible server whose destination already holds the file. Eligibility needs a
	// LOOPBACK comfy_url — port 1 on purpose, so a stray request can never reach the
	// operator's real ComfyUI on 8188.
	setup: func(t *testing.T, srv *Server, modelsRoot string) {
		t.Helper()
		srv.cfg.ComfyURL = "http://127.0.0.1:1"
		srv.cfg.ComfyModelPath = modelsRoot
		if !srv.comfyDownloadEligible() {
			t.Fatal("the fixture is not download-eligible, so this POST takes the resolve-fallback " +
				"branch and measures nothing about runStatusBody")
		}
		writeInstalledModel(t, modelsRoot)
	},
	shapes: []runShape{shapeRunning},
}, {
	name: "POST /workflows/{id}/install-missing-and-run (declined)",
	site: "renderRunActionDeclined (run_install_all.go)",
	path: func(id string) string { return "/workflows/" + id + "/install-missing-and-run" },
	form: func(string, string) url.Values {
		return url.Values{
			"missing_filename": {readinessMissingModel},
			"missing_type":     {readinessModelType},
		}
	},
	// Left NOT eligible (no comfy_model_path, no comfy_url), which is the declined
	// branch: it writes nothing, downloads nothing and starts no run — so it is safe
	// against a SETTLED run too, and that is the poller-free response this row is
	// really here for.
	setup: func(t *testing.T, srv *Server, _ string) {
		t.Helper()
		if srv.comfyDownloadEligible() {
			t.Fatal("the fixture IS download-eligible, so this POST would resolve against the " +
				"live CivitAI API instead of taking the declined branch")
		}
	},
	shapes: []runShape{shapeRunning, shapeFailed},
}, {
	name: "POST /workflows/{id}/comfy-setup",
	site: "handleComfySetupSave (run_comfy_setup.go)",
	path: func(id string) string { return "/workflows/" + id + "/comfy-setup" },
	form: func(_, modelsRoot string) url.Values { return url.Values{"model_path": {modelsRoot}} },
	// A successful save re-renders the run panel so a disabled CTA goes live in the
	// same interaction. It starts no run.
	shapes: []runShape{shapeRunning, shapeFailed},
}, {
	name: "POST /workflows/run/stop",
	site: "handleWorkflowRunStop (run_handlers.go)",
	path: func(string) string { return "/workflows/run/stop" },
	form: func(id, _ string) url.Values { return url.Values{"workflow_id": {id}} },
	// No comfy_url is configured, so stopRun's Interrupt is skipped entirely — this
	// row must never open a socket toward the operator's ComfyUI.
	shapes: []runShape{shapeRunning, shapeFailed},
}}

// readinessMissingModel is the file readinessUIGraph references and does not have.
// It must stay in sync with that fixture: workflowReferencesFile refuses a filename
// the workflow's stored graph does not name, so a drift here turns every install row
// into a 400 rather than a silent pass.
const readinessMissingModel = "delta_four.safetensors"

// readinessModelType routes that file to a real ComfyUI subfolder. It has to be on
// resolveTypeWhitelist AND on comfy.TypeSubdir, or civitaiTypeParam blanks it and the
// destination-exists fast path can never fire.
const readinessModelType = "Checkpoint"

// writeInstalledModel puts readinessMissingModel where an eligible server would look
// for it, so download-and-run takes the "already installed, just run" fast path
// instead of reaching the network.
func writeInstalledModel(t *testing.T, modelsRoot string) {
	t.Helper()
	sub, ok := comfy.TypeSubdir(readinessModelType)
	if !ok {
		t.Fatalf("comfy.TypeSubdir(%q) is not routable — the fixture cannot reach the fast path",
			readinessModelType)
	}
	dest, err := comfy.SafeModelDest(modelsRoot, sub, readinessMissingModel)
	if err != nil {
		t.Fatalf("SafeModelDest: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, []byte("not a real checkpoint"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestEveryRunStartingEndpointClearsTheReadinessLine is the behavioural half: every
// reachable #run-status writer, driven over its REAL route, must answer with the
// out-of-band clear of #cm-run-readiness while a run for that workflow is showing.
//
// 🔴 THE ASSERTION IS ON THE ELEMENT, NEVER ON PROSE OR ON A BARE SUBSTRING. A
// Contains(body, "hx-swap-oob") would be satisfied by any other out-of-band element
// the response happens to carry (#run-modes already does this on the preset path) —
// the same defect as the ` popover` check this package shipped that was satisfied by
// a trigger's ` popovertarget`, and the `Contains(body,"disabled")` that matched
// hx-disabled-elt. So the marker is read out of the brace-balanced extent of the
// element carrying id="cm-run-readiness" and nothing else.
func TestEveryRunStartingEndpointClearsTheReadinessLine(t *testing.T) {
	var sawPolled, sawPollerFree int

	for _, tc := range runStatusWriterCases {
		for _, shape := range tc.shapes {
			t.Run(tc.name+"/"+string(shape), func(t *testing.T) {
				modelsRoot := t.TempDir()
				srv := newReadinessServer(t)
				seedObjectInfo(t, srv, readinessInfo)
				// UI format: the format of all 71 workflows on the operator's real database,
				// and the only one /run/queue accepts (canQueueWorkflow).
				id := seedWorkflow(t, srv, store.WorkflowFormatUI, readinessUIGraph)
				wfID, err := strconv.ParseInt(id, 10, 64)
				if err != nil {
					t.Fatalf("workflow id: %v", err)
				}
				if tc.setup != nil {
					tc.setup(t, srv, modelsRoot)
				}
				installRunShape(t, srv, wfID, shape)

				rec := post(t, srv, tc.path(id), tc.form(id, modelsRoot), true)
				if rec.Code != 200 {
					t.Fatalf("%s = %d, want 200 (%s):\n%s", tc.path(id), rec.Code, tc.site, rec.Body.String())
				}
				body := rec.Body.String()

				// PRECONDITION, asserted rather than assumed: this response really is a
				// run-status write describing THE PARKED RUN. installRunShape stamps seq 7,
				// and dataRunSeq emits it on every fragment that represents an actual run —
				// so its absence means the handler answered with something else entirely
				// (a 200-shaped refusal, a retargeted form, an empty idle fragment) and
				// every assertion below would be green about the wrong response.
				if !strings.Contains(body, `data-run-seq="7"`) {
					t.Fatalf("the response carries no data-run-seq=\"7\", so it is not a run-status "+
						"write for the parked run — this row does not reach %s:\n%s", tc.site, body)
				}
				if strings.Contains(body, `id="run-poll"`) {
					sawPolled++
				} else {
					sawPollerFree++
				}

				// Name the outright-absent case in THIS guard's words. divExtent t.Fatal's on
				// a missing element with its own precondition message, and deleting the
				// element is exactly the regression this test exists to catch — reporting it
				// through someone else's error names nothing about the bug.
				if !strings.Contains(body, `id="`+runReadinessID+`"`) {
					t.Fatalf("%s answers into #run-status with NO #%s element at all, so a readiness "+
						"line already on screen is never removed: the user is left reading a "+
						"pre-click advisory stacked flush on a fresher run result that states the "+
						"same counts. Route this response through runStatusBody (%s):\n%s",
						tc.name, runReadinessID, tc.site, body)
				}
				s, e := divExtent(t, body, `id="`+runReadinessID+`"`)
				ext := body[s:e]
				if !strings.Contains(ext, `hx-swap-oob="true"`) {
					t.Errorf("%s emits #%s WITHOUT hx-swap-oob=\"true\", so htmx leaves the container "+
						"on screen untouched and the stale line survives (%s):\n%s",
						tc.name, runReadinessID, tc.site, ext)
				}
				// It must EMPTY the container, never refill it: an oob element carrying the
				// lazy loader would re-fetch — and re-pay the ~4.66 MB /object_info decode —
				// on every ~1 s poll tick, and on a stop response it would point at
				// /workflows/0/run/readiness.
				if strings.Contains(ext, "hx-get=") || strings.Contains(ext, "data-readiness=") {
					t.Errorf("%s REPOPULATES #%s out of band instead of emptying it (%s):\n%s",
						tc.name, runReadinessID, tc.site, ext)
				}
			})
		}
	}

	// TABLE-LEVEL NON-VACUITY. Both response shapes must be represented, because they
	// carry different consequences: a polled response is re-asserted ~1 s later by the
	// run poller (which bounds the damage of a missing clear), while a poller-free
	// terminal response is the LAST word — nothing revisits #run-status, so a missing
	// clear there leaves the stale line up until the user reloads. A table that only
	// ever produced polled responses would be measuring the bounded case only.
	if sawPolled == 0 {
		t.Errorf("no row produced a POLLED run-status response (%d poller-free) — the table no "+
			"longer covers the in-flight case", sawPollerFree)
	}
	if sawPollerFree == 0 {
		t.Errorf("no row produced a POLLER-FREE terminal run-status response (%d polled) — the "+
			"table covers only responses the 1 s poller would re-assert anyway, which is the "+
			"case with the SMALLER blast radius", sawPolled)
	}
}
