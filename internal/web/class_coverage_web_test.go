package web

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

// classCoverageExempt lists class tokens that legitimately have NO rule of their
// own in either stylesheet. Keep this list tiny and justified — every entry is a
// hole in the guard below.
var classCoverageExempt = map[string]bool{
	// Tailwind's `group` is a MARKER: it only ever appears inside a generated
	// `.group:hover .group-hover\:…` selector, never as a rule of its own.
	"group": true,
}

// TestNoRevivedDeadClasses pins the specific unstyled tokens this guard's
// exemption list used to hold open. Each was a REAL silent failure — the class
// reached the DOM and painted nothing — and each is now fixed at the source:
//
//	text-green-500        → text-emerald-400 (the config REPLACES theme.colors,
//	                        so no `green` scale is ever generated)
//	bg-amber-950/30 and
//	hover:bg-amber-950/50 → .cm-fix-cta (amber-950 is a color-mix() tint and
//	                        Tailwind cannot fold <alpha-value> into a color-mix(),
//	                        so neither utility was ever emitted)
//	prose-invert          → deleted (@tailwindcss/typography is not installed)
//	cm-reveal, cm-run-params → deleted (no rule, no selector, no JS hook)
//	cm-chip               → given a real rule in app.css
//
// The exemptions are gone, so TestEveryTemplateClassExistsInAStylesheet would now
// fail if any of them came back. This test states the intent directly, so a future
// re-introduction fails with the REASON rather than a bare "no rule" line.
func TestNoRevivedDeadClasses(t *testing.T) {
	scan := scanClassCalls(t)
	for tok, why := range map[string]string{
		"text-green-500":        "the palette has no `green` scale — use text-emerald-400",
		"bg-amber-950/30":       "amber-950 is a color-mix() tint; /opacity is never emitted — use .cm-fix-cta",
		"hover:bg-amber-950/50": "amber-950 is a color-mix() tint; /opacity is never emitted — use .cm-fix-cta",
		"prose-invert":          "@tailwindcss/typography is not installed — this class does nothing",
		"cm-reveal":             "no rule and no selector anywhere — a pure no-op marker",
		"cm-run-params":         "no rule and no selector anywhere — a pure no-op marker",
	} {
		if where, ok := scan.where[tok]; ok {
			t.Errorf("dead class %q is back at %s: %s", tok, where, why)
		}
	}
}

// TestEveryTemplateClassExistsInAStylesheet is this repo's guard against its
// classic SILENT failure: output.css is a committed, PURGED Tailwind build, so a
// class newly written into an h.Class("…") literal renders completely unstyled
// until the build is regenerated and committed. Nothing else catches that — the
// page still renders, it just looks wrong.
//
// It parses every non-test .go file in this package, collects the class tokens
// handed to h.Class, and asserts each has a rule in the built output.css or in the
// hand-written app.css.
//
// The argument is resolved through package-level string consts/vars AND through
// local string variables built by literal assignment/append (`bodyClass := "…"` /
// `bodyClass += " " + c` in layout.go is exactly that shape) — an earlier version
// only understood a bare literal, so anything reaching h.Class through a variable
// was silently invisible to the whole guard. What still cannot be resolved (a
// value from a function call, a parameter, a slice index) is COUNTED and pinned by
// classCoverageOpaqueBudget below, so the remaining hole is visible in the test
// output and cannot grow without someone noticing.
func TestEveryTemplateClassExistsInAStylesheet(t *testing.T) {
	css := readStylesheets(t)
	scan := scanClassCalls(t)

	if len(scan.where) < 100 {
		t.Fatalf("only collected %d class tokens — the AST scan is broken, not the CSS", len(scan.where))
	}

	var missing []string
	for tok := range scan.where {
		if classCoverageExempt[tok] {
			continue
		}
		if !cssHasClass(css, tok) {
			missing = append(missing, tok)
		}
	}
	sort.Strings(missing)
	for _, tok := range missing {
		t.Errorf("class %q (%s) has no rule in output.css or app.css — regenerate the "+
			"Tailwind build:\n  cd internal/web && nix-shell -p tailwindcss --run "+
			`"tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"`,
			tok, scan.where[tok])
	}
}

// classCoverageOpaqueBudget pins how many h.Class call sites still hand the guard
// above a value it cannot resolve to literals (a function result, a parameter, a
// computed slice). Those sites are BLIND SPOTS: a class reaching the DOM only
// through one of them would render unstyled with no test failing.
//
// The number exists so the hole stays visible and cannot grow silently. Lower it
// when a site is made resolvable; raising it means knowingly adding a blind spot,
// so say why in the commit.
const classCoverageOpaqueBudget = 4

// TestClassCoverageBlindSpotsAreBounded fails when a new unresolvable h.Class site
// appears, and also when the count DROPS — a stale budget quietly re-opens room
// for a blind spot, so it is pinned exactly.
func TestClassCoverageBlindSpotsAreBounded(t *testing.T) {
	scan := scanClassCalls(t)
	if len(scan.opaque) != classCoverageOpaqueBudget {
		sort.Strings(scan.opaque)
		t.Errorf("h.Class sites the coverage scan cannot resolve = %d, pinned budget = %d.\n"+
			"These sites are invisible to TestEveryTemplateClassExistsInAStylesheet:\n  %s\n"+
			"If you removed one, lower classCoverageOpaqueBudget; if you added one, prefer a "+
			"literal/const so the class stays covered.",
			len(scan.opaque), classCoverageOpaqueBudget, strings.Join(scan.opaque, "\n  "))
	}
}

// readStylesheets returns both stylesheets with CSS escaping REMOVED, so a class
// token can be matched literally (Tailwind writes `.sm\:hidden`, `.max-w-\[1800px\]`).
func readStylesheets(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	for _, p := range []string{"assets/output.css", "assets/app.css"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		sb.Write(b)
		sb.WriteString("\n")
	}
	return strings.ReplaceAll(sb.String(), `\`, "")
}

// cssHasClass reports whether the (unescaped) CSS contains a selector for class.
// A match must be followed by a CSS selector boundary so `.text-sm` does not
// satisfy a lookup for a longer name that merely starts the same way.
func cssHasClass(css, class string) bool {
	needle := "." + class
	for i := 0; ; {
		j := strings.Index(css[i:], needle)
		if j < 0 {
			return false
		}
		end := i + j + len(needle)
		if end >= len(css) || !isClassNameByte(css[end]) {
			return true
		}
		i = end
	}
}

func isClassNameByte(b byte) bool {
	return b == '-' || b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// opaqueMark stands in for a part of a class expression the scan could not
// resolve. It is a byte that cannot occur in a class name, so any token containing
// it is a partial/dynamic token and is discarded — that is what keeps
// `"cm-pill cm-pill-"+variant` contributing the real "cm-pill" while dropping the
// truncated "cm-pill-".
const opaqueMark = "\x01"

// classScanResult is what scanClassCalls found: every resolvable class token with
// one source position, plus the positions of h.Class sites whose argument could
// not be resolved to literals at all.
type classScanResult struct {
	where  map[string]string
	opaque []string
}

// scanClassCalls parses the package and resolves every h.Class argument.
func scanClassCalls(t *testing.T) classScanResult {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	res := classScanResult{where: map[string]string{}}
	for _, pkg := range pkgs {
		globals := packageStringDecls(pkg)
		for _, file := range pkg.Files {
			// Per-function local string variables, so h.Class(bodyClass) resolves.
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				scope := localStringDecls(fn, globals)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || len(call.Args) != 1 {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel == nil || sel.Sel.Name != "Class" {
						return true
					}
					joined, resolved := resolveClassExpr(call.Args[0], scope)
					pos := fset.Position(call.Pos()).String()
					if !resolved {
						res.opaque = append(res.opaque, pos)
					}
					for _, tok := range strings.Fields(joined) {
						if strings.Contains(tok, opaqueMark) {
							continue // a dynamic fragment, not a whole class name
						}
						if _, seen := res.where[tok]; !seen {
							res.where[tok] = pos
						}
					}
					return true
				})
				return true
			})
		}
	}
	return res
}

// packageStringDecls maps package-level const/var names to their resolved value
// (e.g. shellMeasure -> "max-w-[1800px]"), so a class held in a const stays
// covered instead of vanishing from the scan.
func packageStringDecls(pkg *ast.Package) map[string]string {
	out := map[string]string{}
	for _, file := range pkg.Files {
		for _, d := range file.Decls {
			gen, ok := d.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if s, ok := resolveClassExpr(vs.Values[i], out); ok {
						out[name.Name] = s
					}
				}
			}
		}
	}
	return out
}

// localStringDecls collects a function's local string variables, unioning every
// value assigned to each (`x := "a"`, `x = "b"`, `x += " " + y`). A union is right
// for coverage: every branch's classes must be styled.
func localStringDecls(fn *ast.FuncDecl, globals map[string]string) map[string]string {
	scope := map[string]string{}
	for k, v := range globals {
		scope[k] = v
	}
	// Two passes: an assignment may appear after the h.Class call that reads it.
	for pass := 0; pass < 2; pass++ {
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
				return true
			}
			name, ok := as.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			val, resolved := resolveClassExpr(as.Rhs[0], scope)
			if !resolved {
				val += opaqueMark
			}
			switch as.Tok {
			case token.DEFINE, token.ASSIGN, token.ADD_ASSIGN:
				// Separate with a space so tokens never fuse across assignments.
				if prev, seen := scope[name.Name]; seen && as.Tok == token.ADD_ASSIGN {
					scope[name.Name] = prev + " " + val
				} else if prev, seen := scope[name.Name]; seen && !strings.Contains(prev, val) {
					scope[name.Name] = prev + " " + val
				} else {
					scope[name.Name] = val
				}
			}
			return true
		})
	}
	return scope
}

// resolveClassExpr flattens a class expression to a single string, substituting
// opaqueMark for anything it cannot resolve. The bool reports whether the WHOLE
// expression resolved to literals.
func resolveClassExpr(e ast.Expr, scope map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return opaqueMark, false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return opaqueMark, false
		}
		return s, true
	case *ast.Ident:
		if s, ok := scope[v.Name]; ok {
			return s, !strings.Contains(s, opaqueMark)
		}
		return opaqueMark, false
	case *ast.ParenExpr:
		return resolveClassExpr(v.X, scope)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return opaqueMark, false
		}
		l, lok := resolveClassExpr(v.X, scope)
		r, rok := resolveClassExpr(v.Y, scope)
		return l + r, lok && rok
	}
	return opaqueMark, false
}

// TestShellMeasureIsInTheBuiltCSS pins the two arbitrary-value classes the wide
// shell depends on. They are ARBITRARY values (not part of Tailwind's default
// scale), so they exist in output.css only because the JIT scanner found them in
// a .go literal — exactly the fragile case worth an explicit assertion.
func TestShellMeasureIsInTheBuiltCSS(t *testing.T) {
	css := readStylesheets(t)
	if !strings.Contains(shellMeasure, "1800px") {
		t.Fatalf("shellMeasure = %q, expected the ~1800px cap", shellMeasure)
	}
	if !cssHasClass(css, shellMeasure) {
		t.Errorf("%s is missing from the built output.css — the wide shell would fall back to full width", shellMeasure)
	}
	// Both the nav and <main> now reach the measure through the CONST, so the token
	// no longer appears as a bare literal at either h.Class site. Assert the
	// coverage scan still resolves it — otherwise the DRY cleanup would have
	// silently dropped the measure out of the guard above.
	if scan := scanClassCalls(t); scan.where[shellMeasure] == "" {
		t.Errorf("the class-coverage scan no longer resolves %s through the shellMeasure "+
			"const — the measure is only pinned by this test", shellMeasure)
	}
	// The rail/nav custom CSS is hand-written (purge-proof) — assert it is there.
	app, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	for _, want := range []string{
		".cm-nav {", "position: sticky", "--cm-nav-h", "scroll-padding-top",
		".cm-rail {", ".cm-rail-scrim", ".cm-shell-rail {", ".cm-shell-rail-collapsed {",
		// The nav's maturity range control is hand-written .cm-* CSS for the same
		// reason the rail is: output.css is a purged static build, so a Tailwind
		// utility added here would be unstyled until someone regenerates it.
		//
		// These three replaced a `.cm-rail[data-nsfw="blur"] .cm-out-thumb`
		// assertion that guarded the rail's NSFW blur. That rule is GONE, not
		// broken: the blur/show toggle was replaced by a maturity range that omits
		// out-of-band content server-side, and the rail (the user's own outputs)
		// is not filtered by it at all. Nothing else asserted the control's CSS,
		// so the coverage moves here rather than disappearing.
		".cm-maturity {", ".cm-maturity-select {", ".cm-maturity-legend {",
		".cm-cardgrid {",
		// The nav rework's hand-written CSS, here for the same purge-proofing
		// reason as everything above it. These are PRESENCE checks only — the
		// panel's two-mode POSITIONING (the load-bearing part) is guarded by
		// TestNavMenuPanelEscapesTheScrollStrip in nav_menu_css_web_test.go, which
		// slices out the individual rule bodies. A bare "position: fixed" here
		// would have been satisfied by .cm-rail's own rule ~900 lines away.
		".cm-brand-mark {", ".cm-navmenu {", ".cm-navmenu-summary {",
		".cm-navmenu-panel {", ".cm-rail-title-link {",
		// /disks: the capacity rows and their meter.
		".cm-disk-row {", ".cm-disk-path {", ".cm-meter {", ".cm-meter-fill {",
	} {
		if !strings.Contains(string(app), want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}
