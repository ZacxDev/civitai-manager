package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// THE SEAM: A NOTE URL IS UNTRUSTED TEXT, AND ONLY ONE FETCHER MAY EVER SEE ONE.
//
// modelDownloader (run_download.go) is a TWO-WAY switch on pendingDownload.SourceHF:
// the SSRF-hardened HuggingFace client, or the civitai SDK downloader. The civitai
// one blocks private dial targets but carries NO host allowlist, so a plan built
// from a note URL WITHOUT SourceHF would hand a stranger's github.com (or anything
// else) URL straight to it.
//
// The behavioural tests in note_links_web_test.go prove TODAY's single site is
// right. They cannot stop the SECOND one from being written — and "an invariant
// asserted at 1 of 14 writers" is the exact shape this repo keeps shipping. So
// these two guards are STRUCTURAL: they are derived from the source at run time and
// fail the moment a new site starts interpreting note URLs or building an install
// plan out of one.
//
// WHAT THEY PROVE, AND WHAT THEY DO NOT:
//
//	prove       — no non-test function outside the ledger interprets a note URL as a
//	              HuggingFace one, and every installPlan built in note_links.go
//	              declares the hardened source.
//	do NOT      — that a listed site is CORRECT, or that the plan's URL is the one
//	              the lookup returned. Handing installPlan the note's own unpinned
//	              /main/ URL sets SourceHF perfectly and still fetches a moving
//	              branch. That half is the behavioural tests' job; neither closes
//	              the seam alone.
// ─────────────────────────────────────────────────────────────────────────────

// noteURLInterpreters is the ASSERTED LEDGER of every non-test function allowed to
// decide whether a note URL is a HuggingFace /resolve/ URL. Same discipline as
// runStatusFragmentCallers and .github/deadcode-allow.txt: it fails when the set
// GROWS (a new site is making that call, and it had better route to the hardened
// client) *and* when it SHRINKS (an entry describing nothing means the function was
// renamed or deleted — or this scan has stopped working, in which case its silence
// about new sites means nothing).
//
// 🔴 APPENDING HERE IS NOT A WAY TO GO GREEN. An entry is a recorded claim that the
// site sends what it accepts to hf.Client and NOTHING ELSE anywhere near the civitai
// downloader.
var noteURLInterpreters = map[string]string{
	"noteLinkOffersFor": "the RENDER-side classification. It decides whether a link gets an " +
		"install control or is link-only, and it is pure — no client, no egress.",
	"handleWorkflowInstallFromNote": "the ACTION-side gate. A url this refuses is answered " +
		"with a reason and never reaches any downloader; a url it accepts is fetched by " +
		"hf.Client alone.",
}

// noteInstallPlanFile is the one file whose installPlan literals this guard
// inspects. Scoping it is deliberate: run_download.go's own literals answer a
// DIFFERENT question (a CivitAI plan legitimately has SourceHF false), so a
// package-wide "every plan is HF" rule would be false and would forbid its own
// subject.
const noteInstallPlanFile = "note_links.go"

// minNoteURLInterpreterCalls is the POSITIVE CONTROL for the selector scan, as a
// LITERAL — never derived from what the scan found, or expectation and subject move
// together and no mutation can separate them. The reassuring answer here is a small
// offender set, and a zero from a scanner wired to nothing looks identical. There
// were 2 call sites when this was written.
const minNoteURLInterpreterCalls = 2

// noteSelectorCallSites returns, for every call to pkg.Fn in this package's NON-TEST
// source, the enclosing function name mapped to the call positions.
//
// It resolves the selector's package through the FILE'S IMPORT BLOCK, not the
// identifier text, so an aliased `hfx "…/internal/hf"` cannot walk past it.
func noteSelectorCallSites(t *testing.T, importPath, fn string) map[string][]string {
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
			locals := map[string]bool{}
			for _, spec := range file.Imports {
				p := strings.Trim(spec.Path.Value, `"`)
				if p != importPath {
					continue
				}
				local := p
				if i := strings.LastIndex(p, "/"); i >= 0 {
					local = p[i+1:]
				}
				if spec.Name != nil {
					local = spec.Name.Name
				}
				locals[local] = true
			}
			if len(locals) == 0 {
				continue
			}
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
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != fn {
						return true
					}
					pkgIdent, ok := sel.X.(*ast.Ident)
					if !ok || !locals[pkgIdent.Name] {
						return true
					}
					out[caller] = append(out[caller], fset.Position(call.Pos()).String())
					return true
				})
				return true
			})
		}
	}
	if files < 20 {
		t.Fatalf("the scan parsed only %d non-test files in this package — the scanner is "+
			"broken, so every result below is a fact about the scanner, not the code", files)
	}
	return out
}

// TestEveryNoteURLInterpreterIsEnumerated is the first structural half.
func TestEveryNoteURLInterpreterIsEnumerated(t *testing.T) {
	sites := noteSelectorCallSites(t, "github.com/ZacxDev/civitai-manager/internal/hf", "ParseResolveURL")

	var offenders []string
	total := 0
	for caller, at := range sites {
		total += len(at)
		if _, ok := noteURLInterpreters[caller]; !ok {
			offenders = append(offenders, caller+" ("+strings.Join(at, ", ")+")")
		}
	}
	sort.Strings(offenders)
	for _, o := range offenders {
		t.Errorf("%s interprets a note URL as a HuggingFace one and is not in "+
			"noteURLInterpreters. A note URL is untrusted author text: whatever this site "+
			"accepts must be fetched by hf.Client and by nothing else — the civitai "+
			"downloader has no host allowlist. Route it through the existing gate, or add "+
			"an entry WITH the reason it is safe.", o)
	}
	for caller, why := range noteURLInterpreters {
		if len(sites[caller]) == 0 {
			t.Errorf("noteURLInterpreters still lists %q (%s) but nothing in the non-test "+
				"source calls hf.ParseResolveURL from it. Either it was renamed or deleted — "+
				"remove the entry — or this scan has stopped working, in which case the "+
				"offender result above is meaningless.", caller, why)
		}
	}
	if total < minNoteURLInterpreterCalls {
		t.Errorf("the scan found %d hf.ParseResolveURL call sites, under the floor of %d. "+
			"Either the note-link classification was removed (in which case delete this "+
			"guard and its ledger) or the selector scan can no longer see the calls, and "+
			"its silence proves nothing.", total, minNoteURLInterpreterCalls)
	}
}

// installPlanLiteralsIn returns every `installPlan{…}` composite literal in one
// non-test file of this package: the enclosing function name, the position, and
// whether the literal sets SourceHF to the untyped boolean `true`.
func installPlanLiteralsIn(t *testing.T, filename string) []struct {
	Fn, Pos  string
	SourceHF bool
} {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	var out []struct {
		Fn, Pos  string
		SourceHF bool
	}
	seenFile := false
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if name != filename {
				continue
			}
			seenFile = true
			ast.Inspect(file, func(n ast.Node) bool {
				decl, ok := n.(*ast.FuncDecl)
				if !ok || decl.Body == nil {
					return true
				}
				fn := decl.Name.Name
				ast.Inspect(decl.Body, func(n ast.Node) bool {
					lit, ok := n.(*ast.CompositeLit)
					if !ok {
						return true
					}
					id, ok := lit.Type.(*ast.Ident)
					if !ok || id.Name != "installPlan" {
						return true
					}
					hf := false
					for _, el := range lit.Elts {
						kv, ok := el.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						k, ok := kv.Key.(*ast.Ident)
						if !ok || k.Name != "SourceHF" {
							continue
						}
						if v, ok := kv.Value.(*ast.Ident); ok && v.Name == "true" {
							hf = true
						}
					}
					out = append(out, struct {
						Fn, Pos  string
						SourceHF bool
					}{fn, fset.Position(lit.Pos()).String(), hf})
					return true
				})
				return true
			})
		}
	}
	if !seenFile {
		t.Fatalf("the scan never saw %s — the file was renamed or the filter is broken, so "+
			"any result below is a fact about the scanner", filename)
	}
	return out
}

// TestEveryNoteInstallPlanIsHuggingFaceSourced is the second structural half: a plan
// built out of a note URL must DECLARE the hardened client, because the alternative
// is not "no download" — it is the civitai downloader, which has no host allowlist.
//
// The empty case is a real hazard here (a file with no literals passes vacuously),
// so the count is asserted as its own positive control.
func TestEveryNoteInstallPlanIsHuggingFaceSourced(t *testing.T) {
	lits := installPlanLiteralsIn(t, noteInstallPlanFile)
	if len(lits) == 0 {
		t.Fatalf("no installPlan literal found in %s. Either the note install stopped "+
			"building one (delete this guard) or the scan cannot see it, and a green here "+
			"would mean nothing.", noteInstallPlanFile)
	}
	for _, l := range lits {
		if !l.SourceHF {
			t.Errorf("the installPlan built in %s (%s) does not set SourceHF: true. "+
				"modelDownloader is a two-way switch and the civitai downloader carries NO "+
				"host allowlist, so this plan would hand an author-written URL to it.",
				l.Fn, l.Pos)
		}
	}
}
