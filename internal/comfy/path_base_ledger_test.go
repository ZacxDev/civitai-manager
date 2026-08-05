package comfy

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// ONE OWNER FOR "WHAT IS THIS REFERENCE CALLED": comfy.PathBase.
//
// The defect this guards is not a broken function, it is the WRONG function
// spelled at one site. filepath.Base is OS-aware, so on Linux a backslash is an
// ordinary filename character and filepath.Base is a NO-OP over a reference a
// Windows machine wrote into a workflow graph. Five sites deciding "is this model
// file missing" called it, and two independent presence checks therefore reported
// a PRESENT file as MISSING.
//
// A behavioural test proves TODAY's sites are right. It cannot stop the SIXTH one
// from being written — and that is the shape this repo keeps shipping: an
// invariant held at one of fourteen writers. So this guard is structural: it
// parses the package's non-test source and pins WHO may take a basename by any
// route other than PathBase.
//
// WHAT IT PROVES, AND WHAT IT DOES NOT.
//
//	proves      — no non-test function in internal/comfy takes a basename with
//	              filepath.Base or path.Base except the enumerated ones.
//	does NOT    — that a listed exception is still correct, or that a call to
//	              PathBase was handed the RIGHT string. Handing PathBase the wrong
//	              variable type-checks perfectly. That half is
//	              path_base_behaviour_test.go's job; neither test closes the seam
//	              alone.
//
// 🔴 KNOWN HOLES, ENUMERATED. An earlier version of this comment conceded only
// "a third spelling", which understated it — an audit then produced THREE
// surviving mutants that were all the SAME stdlib function. Two are now closed and
// are listed here so nobody re-derives them as open; the rest are genuinely open.
//
//	CLOSED — aliased import (`fpx "path/filepath"` + `fpx.Base(p)`). The scan
//	  resolves each selector's package through the FILE'S IMPORT BLOCK, so the
//	  local identifier is irrelevant. This also used to slip the positive control,
//	  because a plain import elsewhere kept the filepath.* count high.
//	CLOSED — package-level initialiser (`var x = filepath.Base("a/b/c")`). The scan
//	  walks GenDecl value specs as well as FuncDecls.
//
//	OPEN — a function VALUE: `f := filepath.Base; return f(p)`. The call expression's
//	  Fun is a plain Ident, indistinguishable from any local call without type
//	  resolution. Closing it needs go/types, not go/ast.
//	OPEN — a dot-import (`. "path/filepath"`, then a bare `Base(p)`). Same shape as
//	  the above and deliberately not modelled; the repo does not dot-import.
//	OPEN — a genuinely different spelling: strings.LastIndex, a hand-rolled loop, a
//	  regexp. Nothing here can see those.
//	OPEN — SCOPE. This guard covers internal/comfy ONLY, and that gap is not
//	  theoretical: the graph-derived site in internal/library's autoLink was missed
//	  by exactly this boundary and had to be found by an audit. A sibling ledger for
//	  internal/library is open follow-up.
// ─────────────────────────────────────────────────────────────────────────────

// basenameAllow is the ASSERTED LEDGER of every non-test function in this package
// allowed to call filepath.Base or path.Base directly. Same discipline as
// .github/deadcode-allow.txt and internal/web's routeReachabilityAllow: the guard
// fails when the set GROWS (a new site open-coded the rule, and if its input is
// graph-derived it just reintroduced the bug) *and* when it SHRINKS — an entry
// describing nothing means the function was renamed, deleted, or stopped calling
// it, and a stale ledger is how a scanner that has quietly stopped working still
// reports green.
//
// 🔴 APPENDING TO THIS IS NOT A WAY TO GO GREEN. An entry is a recorded claim that
// the site's input is a REAL PATH ON THIS HOST (or a value whose separator is
// already normalised), not a value that came out of a workflow JSON. If your input
// came from a graph, the fix is PathBase — not a line here.
var basenameAllow = map[string]string{
	"ModelsRoot": "path.Base over a directory ComfyUI itself declared in its folder_paths " +
		"reply, already run through path.Clean on a slash-folded string. It asks which " +
		"CATEGORY a config directory names, not what a model reference is called — and it " +
		"needs path.Dir alongside it, which PathBase has no counterpart for.",
	"ModelsRootCategoryDirs": "same input and same question as ModelsRoot: a cleaned, " +
		"slash-folded configuration directory, compared against a category name.",
}

// The two negative/positive controls for the scan. Both are LITERALS, never
// derived from what the scan found — an expectation computed from the same source
// as its subject moves with it and no mutation can separate them (this repo has
// shipped exactly that bug).
const (
	// minParsedFiles: a broken parse must fail loudly rather than report "no
	// offenders". MEASURED at 29 non-test files when this was written.
	minParsedFiles = 20

	// minFilepathCallsSeen is the POSITIVE CONTROL. The scan's reassuring answer is
	// a near-EMPTY offender set, and a zero is indistinguishable from a scanner
	// wired to nothing. This package legitimately calls filepath.Clean/Join/Rel/Dir;
	// if the walker cannot see THOSE, its silence about filepath.Base means nothing.
	// MEASURED at 7 when this was written (all in download_target.go); the floor sits
	// under that so an honest refactor does not fail the build, while a walker that
	// has stopped seeing selector calls at all drops straight through it.
	minFilepathCallsSeen = 4

	// minPathBaseCallSites pins that the conversion is actually present. If every
	// site were reverted to filepath.Base the offender ledger would catch it — but
	// if they were reverted to some THIRD spelling the ledger does not know about,
	// this is what notices the owner went unused. MEASURED at 11.
	minPathBaseCallSites = 6
)

// selectorCall describes one `pkg.Fn(...)` call found in the package.
//
// 🔴 pkg is the RESOLVED IMPORT PATH ("path/filepath"), never the identifier text
// in the source. Matching on the identifier is what let an aliased import —
// `fpx "path/filepath"` then `fpx.Base(p)` — walk straight past this guard while
// `go build` stayed green. It also slipped the positive control, because the plain
// import elsewhere kept the filepath.* count high.
type selectorCall struct {
	pkg, fn string
	inFunc  string
	pos     string
}

// fileImports maps each import's LOCAL identifier to its import path for one file,
// so `fpx.Base` and `filepath.Base` resolve to the same subject. A dot-import has
// no local identifier and is deliberately not modelled — see the honest limits.
func fileImports(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, spec := range f.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		local := p
		if i := strings.LastIndex(p, "/"); i >= 0 {
			local = p[i+1:]
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				continue
			}
			local = spec.Name.Name
		}
		out[local] = p
	}
	return out
}

// scanSelectorCalls parses every non-test .go file in the package directory and
// returns each `path.Fn(...)` call (keyed by RESOLVED import path) together with
// the name of the declaration that encloses it, plus the count of files parsed.
//
// 🔴 It walks BOTH FuncDecls AND GenDecls. Walking only FuncDecls meant a
// package-level `var x = filepath.Base("a/b/c")` was never visited at all — a
// second way to open-code the rule with the guard none the wiser.
func scanSelectorCalls(t *testing.T) (calls []selectorCall, bareCalls []selectorCall, files int) {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files++
		imports := fileImports(f)

		// inspect attributes every call inside node to the declaration `owner`
		// names. A call inside a func literal is attributed to the enclosing named
		// declaration, which is the unit the ledger talks about.
		inspect := func(node ast.Node, owner string) {
			ast.Inspect(node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// Unwrap parentheses before classifying: `(filepath.Base)(p)` is a
				// *ast.ParenExpr and matched NEITHER arm below, so it slipped the
				// ledger entirely. gofmt does not add these parens and nobody writes
				// them by hand — but this guard's header bills itself as an
				// ENUMERATION of what it catches, and an enumeration that is wrong is
				// worse than one that is short. Found by an adversarial audit.
				fun := call.Fun
				for {
					p, ok := fun.(*ast.ParenExpr)
					if !ok {
						break
					}
					fun = p.X
				}
				switch fn := fun.(type) {
				case *ast.SelectorExpr:
					id, ok := fn.X.(*ast.Ident)
					if !ok {
						return true
					}
					pkgPath, ok := imports[id.Name]
					if !ok {
						return true // a method call on a value, not a package selector
					}
					calls = append(calls, selectorCall{
						pkg: pkgPath, fn: fn.Sel.Name,
						inFunc: owner, pos: fset.Position(call.Pos()).String(),
					})
				case *ast.Ident:
					bareCalls = append(bareCalls, selectorCall{
						fn: fn.Name, inFunc: owner,
						pos: fset.Position(call.Pos()).String(),
					})
				}
				return true
			})
		}

		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				fname := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					fname = recvTypeName(d.Recv.List[0].Type) + "." + fname
				}
				inspect(d, fname)
			case *ast.GenDecl:
				// Package-level var/const initialisers run at init time and are just
				// as capable of open-coding the rule as any function body.
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || len(vs.Values) == 0 {
						continue
					}
					owner := d.Tok.String() + " " + vs.Names[0].Name
					for _, v := range vs.Values {
						inspect(v, owner)
					}
				}
			}
		}
	}
	return calls, bareCalls, files
}

func recvTypeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	}
	return "?"
}

// TestNoGraphDerivedRefTakesItsBasenameOutsidePathBase is the structural half of
// the seam.
func TestNoGraphDerivedRefTakesItsBasenameOutsidePathBase(t *testing.T) {
	calls, bare, files := scanSelectorCalls(t)

	// PRECONDITION: a scan that parsed almost nothing must not read as "no
	// offenders".
	if files < minParsedFiles {
		t.Fatalf("scanner parsed only %d non-test files, expected >= %d — the scan is broken, "+
			"and its empty offender set says nothing", files, minParsedFiles)
	}

	// POSITIVE CONTROL: the walker can see filepath.* calls at all.
	filepathSeen := 0
	for _, c := range calls {
		if c.pkg == "path/filepath" {
			filepathSeen++
		}
	}
	if filepathSeen < minFilepathCallsSeen {
		t.Fatalf("scanner found only %d filepath.* calls, expected >= %d — it cannot see the "+
			"calls it is meant to police, so a clean result is meaningless",
			filepathSeen, minFilepathCallsSeen)
	}

	// POSITIVE CONTROL: the owner is actually used.
	pathBaseSites := 0
	for _, c := range bare {
		if c.fn == "PathBase" {
			pathBaseSites++
		}
	}
	if pathBaseSites < minPathBaseCallSites {
		t.Fatalf("only %d call sites reach PathBase, expected >= %d — the consolidation is "+
			"not in place", pathBaseSites, minPathBaseCallSites)
	}

	// Report what the scan actually measured, so a future reader can re-calibrate the
	// floors above from evidence rather than re-guessing them.
	t.Logf("scan: %d non-test files, %d filepath.* calls, %d PathBase call sites",
		files, filepathSeen, pathBaseSites)

	// The offender set: every function taking a basename by any route but PathBase.
	offenders := map[string][]string{}
	for _, c := range calls {
		if (c.pkg == "path/filepath" || c.pkg == "path") && c.fn == "Base" {
			offenders[c.inFunc] = append(offenders[c.inFunc], c.pkg+".Base at "+c.pos)
		}
	}

	// GROWS: a site the ledger does not know about.
	var unlisted []string
	for fn, where := range offenders {
		if _, ok := basenameAllow[fn]; !ok {
			unlisted = append(unlisted, fn+" ("+strings.Join(where, ", ")+")")
		}
	}
	sort.Strings(unlisted)
	if len(unlisted) > 0 {
		t.Errorf("these functions take a basename outside comfy.PathBase and are not in "+
			"basenameAllow:\n  %s\n\nIf the value came out of a workflow graph, the separator "+
			"is DATA and filepath.Base is a no-op for backslashes on Linux — use PathBase. "+
			"Only add a ledger entry if the input is a real path on THIS host, and say why.",
			strings.Join(unlisted, "\n  "))
	}

	// SHRINKS: a ledger entry describing nothing. Either the function is gone or it
	// stopped calling Base — both make the entry a lie the next reader will act on.
	var stale []string
	for fn := range basenameAllow {
		if _, ok := offenders[fn]; !ok {
			stale = append(stale, fn)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("basenameAllow lists %v, but no such function calls filepath.Base/path.Base "+
			"any more. Delete the stale entries — a ledger that describes nothing is how a "+
			"scanner that has stopped working still reports green.", stale)
	}
}
