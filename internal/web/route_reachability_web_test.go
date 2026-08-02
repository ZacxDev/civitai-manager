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

// This file is the ROUTE half of this repo's reachability problem.
//
// `deadcode` (.github/deadcode.sh) proves a FUNCTION is unreachable from a root.
// It structurally cannot see an orphan ROUTE: a handler registered on the mux is
// reachable *by definition* — the mux references it — even when nothing in the UI
// ever emits its path. `GET /workflows/run/resolve-model` shipped exactly that way,
// registered and loopback-gated and making outbound egress, with ~8 tests around a
// surface no user could reach. Its path appeared in exactly one non-test place: its
// own mux.HandleFunc line.
//
// So this guard diffs the two sets:
//
//	registered  — every mux.Handle/HandleFunc pattern in this package
//	emitted     — every path-shaped string this package's non-test source produces
//
// and fails on a registered route no emitted path can reach.
//
// # What "emitted" means, and why the extraction is shaped this way
//
// Paths in this tree are built four different ways, and a guard that only read bare
// string literals would call three of them dead:
//
//	h.Href("/outputs")                                     — a literal
//	"/workflows/" + id + "/run/comfy-status"               — a concatenation
//	fmt.Sprintf("/workflows/%d/run-with-params", wfID)     — a format string
//	'/x/' + v + '/y'  inside a JS blob in a Go raw string  — a nested quoted token
//
// So every string literal is collected, every `+` chain is FLATTENED with its
// non-literal operands standing in as holes, `%d`/`%s`/… and `${…}` become holes,
// and quoted tokens nested inside a blob literal are pulled out too. A hole is then
// filled with a single path-safe character, which is what lets the concatenation
// above match the registered pattern `/workflows/{id}/run/comfy-status`.
//
// # The silent-failure mode, and what stops it
//
// The dangerous direction is LOOSENESS: a matcher sloppy enough that every route
// looks reachable reports a permanent, comfortable green and guards nothing. Three
// choices keep it tight:
//
//   - A hole fills to `[^/]+`, never to `.*` — a hole cannot swallow a path
//     separator, so `"/models/" + rest` can never satisfy `/models/{id}/download`.
//   - A candidate must START with `/`. A bare variable resolves to nothing but
//     holes and is discarded rather than matching everything.
//   - The mux pattern's own literal (`"GET /workflows/run/resolve-model"`) is
//     excluded at the registration site, so a route can never prove itself alive.
//
// The negative control for all of this is that the guard was written first, run
// against the tree that still contained the orphan route, and REPORTED it. A
// reachability guard that has never named a real orphan is not evidence.
//
// # The honest limit: this is a PATH check, not a path+method check
//
// Emitters do not carry their verb anywhere this scan can read reliably (an `h.Href`
// is a GET, an `h.Action` is a POST, an `hx("get", …)` hides the verb behind a helper
// argument). So `GET /x` counts as reachable when only `POST /x` is emitted. That is
// deliberately the conservative direction — it under-reports rather than crying wolf
// — but it means this guard cannot catch a route that is dead only in one verb.

// routeHole marks a chunk of a path expression this scan could not resolve — a
// variable, a function result, a format verb. It is a byte that cannot occur in a
// Go source path, so it can never collide with real path text.
const routeHole = "\x01"

// routeHoleFill is what a hole becomes before matching. A single ordinary
// character, so it satisfies a `[^/]+` wildcard segment and NOTHING wider: a hole
// deliberately cannot span a path separator.
const routeHoleFill = "1"

// registeredRoute is one mux.Handle/HandleFunc registration.
type registeredRoute struct {
	method  string // "GET", "POST", or "" when the pattern carries no verb
	path    string // the pattern with its verb stripped, e.g. "/models/{id}/title"
	pattern string // the raw pattern as written
	pos     string // file:line of the registration
}

// routeReachabilityAllow is an ASSERTED DEBT LEDGER, not an exemption list — the
// same discipline as .github/deadcode-allow.txt and contrast_web_test.go's accepted
// pairs. TestEveryRegisteredRouteIsEmitted fails when this set GROWS (a new orphan
// route) and equally when it SHRINKS (an entry became reachable, so the line is
// stale: delete it and take the win).
//
// Empty is the correct state. An entry here is a route the app registers and cannot
// reach, kept only with a written reason.
var routeReachabilityAllow = map[string]string{
	// The two LEGACY-URL redirects. Both are unreachable from the app BY DESIGN —
	// their whole job is to catch a URL a user already has (a bookmark, a typed or
	// truncated address) and send it to where the page lives now. Linking to either
	// from our own markup would be the bug.
	"GET /trash": "legacy-URL redirect to /disks (the trash page was folded into " +
		"/disks); by design nothing in-app links to it — it exists to catch old " +
		"bookmarks. handleTrashRedirect is 2 lines and does nothing else.",
	"GET /workflows": "legacy-URL redirect to /library?tab=workflows (workflows " +
		"were relocated into a Library tab in 3c71e88). /workflows/{id} is linked " +
		"everywhere, so truncating one of those URLs lands here — that is the case " +
		"it serves.",

	// A GENUINE ORPHAN, kept deliberately and flagged for follow-up rather than
	// deleted in the same change that introduced this guard.
	//
	// The run zone moved to /run-with-params in v0.1.97 and nothing has emitted a
	// POST to the bare /run since. e2e/uxaudit/walk_selectors_test.go even pins
	// `hx-post="/workflows/1/run"` as the OLD, absent shape.
	//
	// It is NOT deleted here because, unlike GET /workflows/run/resolve-model (whose
	// only dependant was its own tests), this route is the entry point ~10 web test
	// files use to drive the real run pipeline — run_install_all, run_missing_nodepack,
	// run_download, run_queue, nodepack, run_fix_popover. That machinery is live and
	// reached through /run-with-params; only the ROUTE is dead. Converting ~15 call
	// sites is a separate change with its own review, not a rider on this one.
	"POST /workflows/{id}/run": "REAL ORPHAN — superseded by /run-with-params in " +
		"v0.1.97, no emitter anywhere. Retained only because ~15 test call sites " +
		"across 8 files use it to drive the live run pipeline; converting them to " +
		"/run-with-params is follow-up work. Delete this entry when they move.",
}

func TestEveryRegisteredRouteIsEmitted(t *testing.T) {
	// Pass 2 of scanEmittedPaths borrows class_coverage_web_test.go's resolver, which
	// marks what it could not resolve with its own opaqueMark. If the two markers ever
	// drift apart, an unresolved chunk would arrive here as literal text instead of a
	// hole and could make an orphan route look reachable — a SILENT weakening, so pin
	// it rather than comment it.
	if opaqueMark != routeHole {
		t.Fatalf("opaqueMark %q and routeHole %q must be the same byte — "+
			"scanEmittedPaths reads the class-coverage resolver's output directly",
			opaqueMark, routeHole)
	}

	routes := scanRegisteredRoutes(t)
	if len(routes) < 50 {
		t.Fatalf("only found %d registered routes — the registration scan is broken, "+
			"not the routing table", len(routes))
	}

	emitted := scanEmittedPaths(t)
	if len(emitted) < 50 {
		t.Fatalf("only collected %d emitted paths — the emitter scan is broken, "+
			"so every route would look dead", len(emitted))
	}

	var orphans []string
	seen := map[string]bool{}
	for _, r := range routes {
		if seen[r.pattern] {
			continue
		}
		seen[r.pattern] = true
		if routeEmitted(r, emitted) {
			if why, ok := routeReachabilityAllow[r.pattern]; ok {
				t.Errorf("stale allowlist entry %q: the route IS emitted now, so the "+
					"reason %q no longer holds. Delete the line from "+
					"routeReachabilityAllow and take the win.", r.pattern, why)
			}
			continue
		}
		if _, ok := routeReachabilityAllow[r.pattern]; ok {
			continue
		}
		orphans = append(orphans, r.pattern+"  ("+r.pos+")")
	}
	sort.Strings(orphans)
	for _, o := range orphans {
		t.Errorf("orphan route %s is registered but NO path this package emits can "+
			"reach it — nothing links to it, so its handler is dead code behind a live "+
			"mux entry. Delete the route (and any tests that exercise only it), or add "+
			"an emitter. If it must stay unreachable, add it to routeReachabilityAllow "+
			"with a reason.", o)
	}
}

// scanRegisteredRoutes finds every `<mux>.Handle(…)` / `<mux>.HandleFunc(…)` whose
// first argument is a string literal. It keys on the METHOD NAME rather than on a
// variable called `mux`, so moving the routing table to another receiver cannot
// silently empty this guard — the len() floor above is the backstop for that.
func scanRegisteredRoutes(t *testing.T) []registeredRoute {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	var out []registeredRoute
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := handleCallPattern(n)
				if !ok {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				method, path := splitRoutePattern(s)
				out = append(out, registeredRoute{
					method:  method,
					path:    path,
					pattern: s,
					pos:     fset.Position(lit.Pos()).String(),
				})
				return true
			})
		}
	}
	return out
}

// handleCallPattern reports the first-argument string literal of a Handle/HandleFunc
// call. It is also what the emitter scan skips, so a route's own registration can
// never count as an emitter of itself.
func handleCallPattern(n ast.Node) (*ast.BasicLit, bool) {
	call, ok := n.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return nil, false
	}
	if sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
		return nil, false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return nil, false
	}
	return lit, true
}

// splitRoutePattern separates net/http's "METHOD /path" pattern form.
func splitRoutePattern(pattern string) (method, path string) {
	if i := strings.Index(pattern, " "); i >= 0 {
		return pattern[:i], strings.TrimSpace(pattern[i+1:])
	}
	return "", pattern
}

// routeEmitted reports whether any emitted path can reach this route.
func routeEmitted(r registeredRoute, emitted map[string]string) bool {
	re := routePatternRegexp(r.path)
	if re == nil {
		return true // unparseable pattern: fail OPEN rather than cry wolf
	}
	for p := range emitted {
		if re.MatchString(p) {
			return true
		}
	}
	return false
}

// routePatternRegexp compiles a net/http ServeMux pattern into a matcher over
// concrete paths.
//
//	/models/{id}/title  → ^/models/[^/]+/title$      (a wildcard is ONE segment)
//	/{$}                → ^/$                        (the exact-root pattern)
//	/assets/            → ^/assets/                  (a trailing slash is a subtree)
//	/x/{rest...}        → ^/x/.+                     (a trailing multi-segment wildcard)
//
// The `[^/]+` is the load-bearing part: it is what stops a hole in an emitted path
// from swallowing a separator and making a longer route look reachable.
func routePatternRegexp(path string) *regexp.Regexp {
	if path == "/{$}" {
		return regexp.MustCompile(`^/$`)
	}
	var b strings.Builder
	b.WriteString("^")
	rest := path
	for {
		i := strings.Index(rest, "{")
		if i < 0 {
			b.WriteString(regexp.QuoteMeta(rest))
			break
		}
		j := strings.Index(rest[i:], "}")
		if j < 0 {
			b.WriteString(regexp.QuoteMeta(rest))
			break
		}
		b.WriteString(regexp.QuoteMeta(rest[:i]))
		if strings.HasSuffix(rest[i:i+j], "...") {
			b.WriteString(".+")
		} else {
			b.WriteString("[^/]+")
		}
		rest = rest[i+j+1:]
	}
	// A trailing slash registers a SUBTREE, so anything beneath it reaches the
	// handler; every other pattern is exact.
	if !strings.HasSuffix(path, "/") {
		b.WriteString("$")
	}
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil
	}
	return re
}

// scanEmittedPaths collects every path-shaped string this package's non-test source
// can produce, mapped to one source position for the failure message.
func scanEmittedPaths(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nonTestGoFile, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	out := map[string]string{}
	record := func(raw, pos string) {
		for _, p := range normalizeEmittedPath(raw) {
			if _, seen := out[p]; !seen {
				out[p] = pos
			}
		}
	}

	for _, pkg := range pkgs {
		// Package-level consts/vars holding path fragments, resolved once.
		globals := packageStringDecls(pkg)
		for _, file := range pkg.Files {
			// A route's own mux pattern must never count as an emitter of itself.
			skip := map[*ast.BasicLit]bool{}
			ast.Inspect(file, func(n ast.Node) bool {
				if lit, ok := handleCallPattern(n); ok {
					skip[lit] = true
				}
				return true
			})

			// Pass 1: every string literal as written. This is what reaches a path
			// nested inside a JS blob, and a literal outside any function.
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || skip[lit] {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil {
					record(s, fset.Position(lit.Pos()).String())
				}
				return true
			})

			// Pass 2: composed expressions, resolved through local and package string
			// variables by the SAME resolver class_coverage_web_test.go uses — its
			// opaqueMark and this file's routeHole are deliberately the same byte, so a
			// value it could not resolve arrives here already marked as a hole.
			//
			// Without this pass, `base := "/workflows/" + wfID + "/run/presets"`
			// followed by `base+"/"+id+"/save"` resolves to a leading hole and is
			// discarded — which reported the preset save/delete routes as orphans on
			// this guard's first run. They are not; the path was simply in a variable.
			ast.Inspect(file, func(n ast.Node) bool {
				fn, ok := n.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					return true
				}
				scope := localPathDecls(fn, globals)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch n.(type) {
					case *ast.BinaryExpr, *ast.Ident:
					default:
						return true
					}
					if s, _ := resolveClassExpr(n.(ast.Expr), scope); s != "" {
						record(s, fset.Position(n.Pos()).String())
					}
					return true
				})
				return true
			})
		}
	}
	return out
}

// localPathDecls collects a function's local string variables for PATH resolution.
//
// It deliberately does NOT reuse localStringDecls, whose semantics are tuned to CSS
// class tokens in two ways that are wrong for a path:
//
//   - it UNIONS every value ever assigned to a name, separated by a space (right for
//     coverage — every branch's classes must be styled — but a path with a space in
//     it is not a path); and
//   - it APPENDS an extra opaqueMark when the right-hand side did not fully resolve,
//     to make a partial class token get discarded. resolveClassExpr has already
//     substituted a hole for the unresolved operand IN PLACE, so that extra trailing
//     marker turns `"/workflows/" + wfID + "/run/presets"` into a path with a
//     phantom segment on the end.
//
// That second point is not hypothetical: it is why this guard's second run still
// reported `POST /workflows/{id}/run/presets/{pid}/save` and `…/delete` as orphans.
// They are reachable — through exactly that variable.
//
// A name assigned twice with different values resolves to a bare hole, which will
// generally be discarded. That is the conservative direction: it can produce a
// false ORPHAN report (visible, triaged) but never a false "reachable".
func localPathDecls(fn *ast.FuncDecl, globals map[string]string) map[string]string {
	scope := map[string]string{}
	for k, v := range globals {
		scope[k] = v
	}
	// Two passes: an assignment may appear after the expression that reads it.
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
			if as.Tok != token.DEFINE && as.Tok != token.ASSIGN {
				return true
			}
			val, _ := resolveClassExpr(as.Rhs[0], scope)
			if prev, seen := scope[name.Name]; seen && prev != val {
				scope[name.Name] = routeHole
				return true
			}
			scope[name.Name] = val
			return true
		})
	}
	return scope
}

// routeFormatVerb matches a fmt verb (%d, %s, %q, %-4v, …) so a format string used
// as a path resolves to the same shape a concatenation would.
var routeFormatVerb = regexp.MustCompile(`%[+\-# 0-9.]*[a-zA-Z]`)

// routeJSInterp matches a JS template-literal interpolation, for paths built inside
// a script blob carried in a Go raw string.
var routeJSInterp = regexp.MustCompile(`\$\{[^}]*\}`)

// routeNestedQuoted pulls quoted tokens out of a blob literal — a Go raw string
// holding JS emits its paths as '…' / "…" / `…` inside itself, and the blob as a
// whole is not a path.
var routeNestedQuoted = regexp.MustCompile("[\"'`]([^\"'`\n]{1,200})[\"'`]")

// normalizeEmittedPath turns one source string into the set of concrete paths it
// could produce. It returns nothing for a string that is not a path.
func normalizeEmittedPath(raw string) []string {
	var out []string
	add := func(s string) {
		s = routeFormatVerb.ReplaceAllString(s, routeHole)
		s = routeJSInterp.ReplaceAllString(s, routeHole)
		// A query or fragment is not part of the route pattern.
		if i := strings.IndexAny(s, "?#"); i >= 0 {
			s = s[:i]
		}
		// A TRAILING hole is routinely an optional query string rather than a path
		// segment — `fmt.Sprintf("/models/%d/subscribe-control%s", id, workflowQuery(w))`
		// appends either "?workflow=…" or "". Without this variant that helper reads
		// as an orphan route, which is a FALSE POSITIVE the first run of this guard
		// produced for real. Only a trailing hole is trimmed: an interior one still
		// has to line up with a wildcard segment.
		variants := []string{s}
		if strings.HasSuffix(s, routeHole) {
			variants = append(variants, strings.TrimRight(s, routeHole))
		}
		for _, v := range variants {
			v = strings.ReplaceAll(v, routeHole, routeHoleFill)
			// Only an absolute path can reach a mux pattern. This is also what
			// discards a bare variable, which resolves to holes alone.
			if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \t\n\"'`") {
				continue
			}
			out = append(out, v)
		}
	}

	add(raw)
	// A blob literal (a JS script, a CSS chunk) is not itself a path, but may
	// carry one in a nested quoted token.
	if strings.ContainsAny(raw, "\n\t ") {
		for _, m := range routeNestedQuoted.FindAllStringSubmatch(raw, -1) {
			add(m[1])
		}
	}
	return out
}

func nonTestGoFile(fi os.FileInfo) bool {
	return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
}
