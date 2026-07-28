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
	// The typography plugin is not installed; this class is inert markup left on
	// the description container (its real constraints come from .cm-model-desc).
	"prose-invert": true,

	// BEHAVIOUR HOOKS: cm-* classes that exist only for JS/selector targeting and
	// deliberately carry no CSS rule (all their look comes from the Tailwind
	// utilities sitting next to them in the same class list).
	"cm-reveal":     true, // cmReveal() click-to-reveal button
	"cm-chip":       true, // trigger-tag chip, styled by its utilities
	"cm-run-params": true, // run-params container, targeted by the run scripts

	// SCANNER ARTIFACT: statPill builds `"cm-pill-"+variant`, so the AST sees the
	// bare prefix. The real classes (.cm-pill-dup, …) do exist in app.css.
	"cm-pill-": true,

	// KNOWN PRE-EXISTING AND GENUINELY UNSTYLED — found by this test on its first
	// run, NOT introduced by the shell work, and left alone to keep that change
	// scoped. Each is a real silent failure worth a follow-up:
	//   text-green-500       — the palette maps green→emerald, so `green` does not
	//                          exist and the "Subscribed ✓" note is unstyled.
	//   bg-amber-950/30 and hover:bg-amber-950/50 — amber-950 resolves to a
	//                          color-mix() tint, which Tailwind cannot combine with
	//                          an /opacity modifier, so neither utility is emitted.
	"text-green-500":        true,
	"bg-amber-950/30":       true,
	"hover:bg-amber-950/50": true,
}

// TestEveryTemplateClassExistsInAStylesheet is this repo's guard against its
// classic SILENT failure: output.css is a committed, PURGED Tailwind build, so a
// class newly written into an h.Class("…") literal renders completely unstyled
// until the build is regenerated and committed. Nothing else catches that — the
// page still renders, it just looks wrong.
//
// It parses every non-test .go file in this package, collects the string literals
// handed to h.Class (including the literal parts of concatenations), and asserts
// each token has a rule in the built output.css or in the hand-written app.css.
func TestEveryTemplateClassExistsInAStylesheet(t *testing.T) {
	css := readStylesheets(t)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	// class token -> one source position, for a useful failure message.
	where := map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "Class" {
					return true
				}
				if len(call.Args) != 1 {
					return true
				}
				for _, lit := range stringLiteralParts(call.Args[0]) {
					for _, tok := range strings.Fields(lit) {
						if _, seen := where[tok]; !seen {
							where[tok] = fset.Position(call.Pos()).String()
						}
					}
				}
				return true
			})
		}
	}
	if len(where) < 100 {
		t.Fatalf("only collected %d class tokens — the AST scan is broken, not the CSS", len(where))
	}

	var missing []string
	for tok := range where {
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
			tok, where[tok])
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

// stringLiteralParts returns the string-literal operands of an expression,
// descending through `+` concatenations (e.g. "mx-auto "+shellMeasure+" px-4").
// Non-literal operands (identifiers, calls) are skipped — they are covered by
// their own h.Class sites or by a const the tests assert directly.
func stringLiteralParts(e ast.Expr) []string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return nil
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return nil
		}
		return []string{s}
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return nil
		}
		return append(stringLiteralParts(v.X), stringLiteralParts(v.Y)...)
	case *ast.ParenExpr:
		return stringLiteralParts(v.X)
	}
	return nil
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
	// The rail/nav custom CSS is hand-written (purge-proof) — assert it is there.
	app, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	for _, want := range []string{
		".cm-nav {", "position: sticky", "--cm-nav-h", "scroll-padding-top",
		".cm-rail {", ".cm-rail-scrim", ".cm-shell-rail {", ".cm-shell-rail-collapsed {",
		`.cm-rail[data-nsfw="blur"] .cm-rail-thumb`, ".cm-cardgrid {",
	} {
		if !strings.Contains(string(app), want) {
			t.Errorf("app.css missing %q", want)
		}
	}
}
