package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This is the MISSING HALF of class_coverage_web_test.go.
//
// TestEveryTemplateClassExistsInAStylesheet walks one way — every class handed to
// h.Class must have a rule — and catches the "purged Tailwind build wasn't
// regenerated" failure. It is structurally blind the other way: a CSS RULE that
// nothing emits is invisible to it. Delete a template and the rule it styled sits in
// app.css forever, and `TestNoRevivedDeadClasses` does not help — that is a hardcoded
// blocklist of six named tokens, not a general reverse check.
//
// So this guard walks the other way: every `.cm-*` class app.css mentions must be
// emitted by something. It found `.cm-crumb-name`, `.cm-vstatus-date` and
// `.cm-gen-sep` — three rules styling markup that no longer exists — before any of
// them were removed.
//
// # Scope: app.css, and only the `cm-` namespace
//
//   - assets/output.css is a PURGED Tailwind build. Every selector in it exists
//     because the purge scanner found the token in a .go file, so "unemitted rule"
//     is not a state it can be in. Scanning it would report thousands of Tailwind
//     internals as dead.
//   - civitai-theme.css and civitai-components.css are VENDORED third-party design
//     system files. We do not use every component they ship, and pruning them is
//     not this guard's business.
//   - `cm-` is this repo's own hand-written namespace (see CLAUDE.md: custom CSS
//     goes in app.css as a `.cm-*` class), which makes it exactly the set where an
//     unemitted rule is our bug.
//
// # Two traps this guard is deliberately built around
//
//   - **A CSS comment must not satisfy the check.** Comments are stripped before
//     anything is read, and classes are collected from SELECTOR text only — the run
//     of characters preceding a `{` — never from a declaration value. app.css also
//     carries deliberate TOMBSTONE comments where the old blur rules lived, worded
//     so a grep-based guard cannot be satisfied by them; stripping comments keeps
//     that intact rather than defeating it.
//   - **A class can be emitted from JS, not just from h.Class.** `.cm-pop-open` is
//     held by the hover controller in modelPageScript, `.cm-wf-highlight` by an
//     inline script, `.cm-graph-panning` by the graph pan handler — none of them is
//     an h.Class argument. The emitter search therefore reads every string literal
//     in the package's non-test source, which is where all of that JS lives.

// cssRuleReachabilityAllow is an ASSERTED DEBT LEDGER, not an exemption list — the
// same discipline as .github/deadcode-allow.txt and contrast_web_test.go's accepted
// pairs. TestEveryCMClassInAppCSSIsEmitted fails when this set GROWS (a new unemitted
// rule) and equally when it SHRINKS. SHRINKING has TWO shapes and the test asserts
// both, because iterating the styled classes alone only ever sees the first:
//
//   - the rule is still in app.css and the class is EMITTED again, so the reason no
//     longer holds; and
//   - the RULE HAS BEEN DELETED — the entry has no subject left, so the reason
//     describes CSS that is not in the file.
//
// Deleting the rule is the ordinary way one of these gets resolved, which makes the
// second shape the common one rather than the exotic one.
//
// Empty is the correct state.
var cssRuleReachabilityAllow = map[string]string{}

func TestEveryCMClassInAppCSSIsEmitted(t *testing.T) {
	classes := cmClassesInAppCSS(t)
	if len(classes) < 100 {
		t.Fatalf("only found %d .cm-* classes in app.css — the selector scan is broken, "+
			"so nothing could ever be reported dead", len(classes))
	}

	emitted := emittedClassText(t)
	if len(emitted) < 10000 {
		t.Fatalf("only collected %d bytes of emitter text — the source scan is broken, "+
			"so every rule would look dead", len(emitted))
	}
	// h.Class arguments composed through consts/vars are resolved by the forward
	// guard's scanner; fold them in so a class that never appears as a bare literal
	// (shellMeasure is exactly that shape) is not reported dead.
	scan := scanClassCalls(t)
	prefixes := dynamicClassPrefixes(t)

	styled := map[string]bool{}
	for _, c := range classes {
		styled[c] = true
	}

	var dead []string
	for _, c := range classes {
		if classEmitted(c, emitted, scan, prefixes) {
			if why, ok := cssRuleReachabilityAllow[c]; ok {
				t.Errorf("stale allowlist entry %q: the class IS emitted now, so the "+
					"reason %q no longer holds. Delete the line from "+
					"cssRuleReachabilityAllow and take the win.", c, why)
			}
			continue
		}
		if _, ok := cssRuleReachabilityAllow[c]; ok {
			continue
		}
		dead = append(dead, c)
	}
	// The SECOND shrink direction — the twin of the sweep in
	// route_reachability_web_test.go, and it is missing for the same reason. The loop
	// above iterates the SUBJECTS (classes app.css styles), so it can only notice an
	// entry whose rule still exists and has become emitted again. An entry naming a
	// class whose RULE HAS BEEN DELETED has no subject to iterate and was silently
	// ignored, leaving a reason text describing CSS that is not in the file.
	//
	// Deleting the rule is the ordinary way one of these entries gets resolved, so
	// without this the ledger cannot see its own most likely resolution.
	var stale []string
	for c := range cssRuleReachabilityAllow {
		if !styled[c] {
			stale = append(stale, c)
		}
	}
	sort.Strings(stale)
	for _, c := range stale {
		t.Errorf("allowlist entry %q names a class app.css no longer STYLES — no "+
			"selector in the file mentions .%s, so the entry has no subject and "+
			"documents a rule that does not exist. The reason given was %q. Delete the "+
			"line from cssRuleReachabilityAllow.", c, c, cssRuleReachabilityAllow[c])
	}

	sort.Strings(dead)
	for _, c := range dead {
		t.Errorf("app.css styles .%s but NOTHING emits that class — no h.Class "+
			"argument and no string literal in this package's non-test source "+
			"mentions it. The rule is dead CSS: delete it, or wire up the markup it "+
			"was written for. If it must stay, add it to cssRuleReachabilityAllow "+
			"with a reason.", c)
	}
}

// cssComment matches a CSS block comment. Stripping these FIRST is what stops a
// commented-out rule — or one of app.css's deliberate tombstones — from registering
// as a live selector.
var cssComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// cmClassSelector matches a `.cm-…` class token inside selector text.
var cmClassSelector = regexp.MustCompile(`\.(cm-[A-Za-z0-9_-]+)`)

// cmClassesInAppCSS returns every `cm-*` class app.css references from a SELECTOR,
// deduped and sorted.
//
// Selector text is taken as the run of characters preceding each `{` — after the
// previous `}` or `{`. That is what keeps a declaration VALUE from ever being read
// as a selector, and it costs nothing to be exact here: matching rules rather than
// text is the whole point of the guard.
//
// A class that appears only as an ancestor or in a compound (`.cm-lift.cm-pop-open`,
// `.cm-rail .cm-rail-body`) counts as referenced and must still be emitted — such a
// selector cannot match anything if the markup never carries the class.
func cmClassesInAppCSS(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile("assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := cssComment.ReplaceAllString(string(b), " ")

	set := map[string]bool{}
	start := 0
	for i := 0; i < len(css); i++ {
		switch css[i] {
		case '{':
			for _, m := range cmClassSelector.FindAllStringSubmatch(css[start:i], -1) {
				set[m[1]] = true
			}
			start = i + 1
		case '}':
			start = i + 1
		}
	}

	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// emittedClassText concatenates the VALUE of every string literal in this package's
// non-test source.
//
// Literals only, never raw source: a Go comment naming a class — or this guard's own
// prose — must not count as an emitter. That is the same false-pass shape as the CSS
// comment above, one language over.
func emittedClassText(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	var sb strings.Builder
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					sb.WriteString(s)
					sb.WriteString("\n")
				}
				return true
			})
		}
	}
	return sb.String()
}

// expectedDynamicClassPrefixes pins every place this package builds a class name by
// gluing a literal prefix to a value only known at runtime — `h.Class("… cm-pill
// cm-pill-" + variant)` in statChip is the only one today.
//
// Such a class NEVER appears whole in the source, so the reverse guard cannot see it
// and reported all four `.cm-pill-{broken,dup,neutral,update}` rules as dead on its
// first run. They are live. A prefix listed here therefore makes every `.<prefix>*`
// rule count as emitted.
//
// That is a real HOLE — a genuinely dead `.cm-pill-typo` rule would hide inside it —
// so the set is pinned EXACTLY, the same instrument and for the same reason as
// classCoverageOpaqueBudget in the forward guard: the hole stays visible and cannot
// grow without someone saying so in a commit. It is deliberately preferred over
// allowlisting the four classes individually, because it SELF-HEALS: delete the
// `"cm-pill-" + variant` call site and all four immediately report dead again,
// whereas four allowlist entries would rot into a permanent exemption.
var expectedDynamicClassPrefixes = []string{"cm-pill-"}

func TestDynamicClassPrefixesAreBounded(t *testing.T) {
	got := dynamicClassPrefixes(t)
	sort.Strings(got)
	want := append([]string(nil), expectedDynamicClassPrefixes...)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("class prefixes composed with a runtime value = %v, pinned = %v.\n"+
			"Every `.<prefix>*` rule in app.css is invisible to "+
			"TestEveryCMClassInAppCSSIsEmitted. If you added a site, prefer a whole "+
			"literal class per branch so the rules stay covered; if you removed one, "+
			"drop it from expectedDynamicClassPrefixes.", got, want)
	}
}

// dynamicClassPrefixes finds h.Class arguments whose last token is a `cm-` literal
// glued to something unresolvable, and returns those literal prefixes.
//
// Only a prefix ending in `-` counts: that is a namespace boundary the author chose,
// whereas an arbitrary truncation ("cm-pil") would widen the hole to a whole family
// of unrelated names.
func dynamicClassPrefixes(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	set := map[string]bool{}
	for _, pkg := range pkgs {
		globals := packageStringDecls(pkg)
		for _, file := range pkg.Files {
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
					joined, _ := resolveClassExpr(call.Args[0], scope)
					for _, tok := range strings.Fields(joined) {
						i := strings.Index(tok, opaqueMark)
						if i <= 0 {
							continue
						}
						pre := tok[:i]
						if strings.HasPrefix(pre, "cm-") && strings.HasSuffix(pre, "-") {
							set[pre] = true
						}
					}
					return true
				})
				return true
			})
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// classEmitted reports whether anything emits this class.
//
// The token must be bounded on BOTH sides by a non-class-name byte, reusing the
// forward guard's isClassNameByte. Without the right-hand bound, `.cm-gen-sep` would
// be satisfied by the string "cm-gen-sep-wide" — or, in the real case that motivated
// the care, `.cm-rail-act` by "cm-rail-acts".
func classEmitted(class, emitted string, scan classScanResult, dynPrefixes []string) bool {
	if _, ok := scan.where[class]; ok {
		return true
	}
	for _, p := range dynPrefixes {
		if len(class) > len(p) && strings.HasPrefix(class, p) {
			return true
		}
	}
	for i := 0; ; {
		j := strings.Index(emitted[i:], class)
		if j < 0 {
			return false
		}
		lo := i + j
		hi := lo + len(class)
		leftOK := lo == 0 || !isClassNameByte(emitted[lo-1])
		rightOK := hi >= len(emitted) || !isClassNameByte(emitted[hi])
		if leftOK && rightOK {
			return true
		}
		i = hi
	}
}
