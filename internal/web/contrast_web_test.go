package web

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A deterministic WCAG 2.1 contrast checker for the shipped theme tokens.
//
// WHY THIS EXISTS
// ---------------
// The UI's colors are not literals in the templates: a Tailwind utility resolves
// through tailwind.config.js onto a `--civitai-*` design token, whose value comes
// from the vendored civitai-theme.css and is then overridden per-theme in
// app.css. Nothing checked that the END of that chain met WCAG AA, so failures
// shipped repeatedly — and were then misattributed, because a screenshot cannot
// tell you which token painted a pixel.
//
// This reads the REAL CSS files that ship (not a copy of their values), resolves
// each token for each theme, reproduces the color-mix() tints the component CSS
// derives, and computes the WCAG 2.1 relative-luminance contrast ratio. It is
// both the regression gate and a reporter:
//
//	go test ./internal/web/ -run TestTokenContrast -v
//
// prints every pair with its ratio, so adding a token or changing a value tells
// you immediately what it does to contrast in BOTH themes.
//
// Scope + honest limits: this checks token-to-token pairs, which is where the
// systematic failures live. It cannot know that some element puts token A on
// token B — that mapping is the table below, kept in sync by hand with the
// component CSS. A real browser + axe run (e2e/uxaudit) remains the ground truth
// for "which element actually renders which pair".
//
// THE LIGHT THEME DELIBERATELY FAILS — AND THAT IS ASSERTED, NOT SKIPPED
// ---------------------------------------------------------------------
// Brand fidelity was chosen over WCAG AA for the light theme (see the ACCEPTED
// AA DEBT block in assets/app.css), so most light pairs below sit under 4.5:1.
// Those pairs are NOT deleted and the checker is NOT weakened. Each carries a
// `debt` entry naming the theme, the exact measured ratio and the reason, and
// the test asserts the pair STILL fails at STILL that ratio. Consequences:
//
//   - a light pair that unexpectedly starts PASSING fails the build (the debt
//     entry is stale — delete it and enjoy the win);
//   - a light pair whose ratio MOVES in either direction fails the build (the
//     palette changed; re-measure and re-decide, don't drift);
//   - so the debt is pinned rather than forgotten, and an accidental edit to
//     the light palette cannot slip through as "well, it already failed".
//
// The dark theme carries no debt entries at all: every pair must pass AA there.
// ---------------------------------------------------------------------------

// wcagAANormal is the WCAG 2.1 AA threshold for normal-size text. Large text
// (>=18.66px bold / >=24px) may use 3:1, but every pair below is normal text.
const wcagAANormal = 4.5

// wcagAAUI is the WCAG 2.1 AA threshold for non-text UI components (borders,
// focus rings, control boundaries).
const wcagAAUI = 3.0

// srgbToLinear undoes the sRGB transfer function for one 0-255 channel.
func srgbToLinear(c float64) float64 {
	v := c / 255.0
	if v <= 0.04045 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

type rgb struct{ r, g, b float64 }

// relLuminance is WCAG 2.1's relative luminance.
func (c rgb) relLuminance() float64 {
	return 0.2126*srgbToLinear(c.r) + 0.7152*srgbToLinear(c.g) + 0.0722*srgbToLinear(c.b)
}

// contrastRatio is WCAG 2.1's (L1+0.05)/(L2+0.05), order-independent.
func contrastRatio(a, b rgb) float64 {
	la, lb := a.relLuminance(), b.relLuminance()
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// mixOver reproduces `color-mix(in srgb, <fg> pct%, transparent)` composited
// over bg — which is what the component CSS's tinted backgrounds resolve to
// once they are painted on the page body.
func mixOver(fg, bg rgb, pct float64) rgb {
	t := pct / 100.0
	return rgb{
		r: fg.r*t + bg.r*(1-t),
		g: fg.g*t + bg.g*(1-t),
		b: fg.b*t + bg.b*(1-t),
	}
}

var hexRe = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// parseHex parses #rgb / #rrggbb.
func parseHex(s string) (rgb, error) {
	s = strings.TrimSpace(s)
	if !hexRe.MatchString(s) {
		return rgb{}, fmt.Errorf("not a hex color: %q", s)
	}
	h := s[1:]
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	v := make([]float64, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(h[i*2:i*2+2], 16, 8)
		if err != nil {
			return rgb{}, err
		}
		v[i] = float64(n)
	}
	return rgb{v[0], v[1], v[2]}, nil
}

var (
	// A `[data-theme="x"] { … }` or `:root { … }` block.
	themeBlockRe = regexp.MustCompile(`(?s)(\[data-theme=['"](\w+)['"]\]|:root)\s*\{(.*?)\n\}`)
	// A `--civitai-color-foo: value;` declaration.
	declRe = regexp.MustCompile(`(--civitai-color-[a-z0-9-]+)\s*:\s*([^;]+);`)
	// A `var(--civitai-color-foo)` indirection.
	varRefRe = regexp.MustCompile(`^var\(\s*(--civitai-color-[a-z0-9-]+)\s*\)$`)
)

// themeTokens resolves every --civitai-color-* token for one theme, in the same
// order the browser does: civitai-theme.css first (`:root` then the matching
// `[data-theme]` block), then app.css's overrides, which are unlayered and load
// later so they win at equal specificity. `var(--other-token)` indirections are
// followed.
func themeTokens(t *testing.T, theme string) map[string]rgb {
	t.Helper()
	raw := map[string]string{}

	collect := func(css string) {
		for _, blk := range themeBlockRe.FindAllStringSubmatch(css, -1) {
			blockTheme, body := blk[2], blk[3]
			// `:root` applies to every theme; a [data-theme] block only to its own.
			if blockTheme != "" && blockTheme != theme {
				continue
			}
			for _, d := range declRe.FindAllStringSubmatch(body, -1) {
				raw[d[1]] = strings.TrimSpace(d[2])
			}
		}
	}

	themeCSS, err := os.ReadFile("assets/civitai-theme.css")
	if err != nil {
		t.Fatalf("read civitai-theme.css: %v", err)
	}
	collect(string(themeCSS))
	collect(appCSS(t))

	out := map[string]rgb{}
	for name := range raw {
		// Follow var() indirections (bounded, cycle-safe).
		val, cur := raw[name], name
		for i := 0; i < 8; i++ {
			m := varRefRe.FindStringSubmatch(val)
			if m == nil {
				break
			}
			next, ok := raw[m[1]]
			if !ok || m[1] == cur {
				break
			}
			val, cur = next, m[1]
		}
		c, err := parseHex(val)
		if err != nil {
			continue // rgba()/color-mix() washes aren't foreground/background pairs
		}
		out[name] = c
	}
	if len(out) < 10 {
		t.Fatalf("theme %q: resolved only %d tokens — the CSS parse is broken", theme, len(out))
	}
	return out
}

// bg describes a pair's background: either a plain token, or the color-mix tint
// the component CSS derives from a token and paints over the page body.
type bg struct {
	token   string
	tintPct float64 // 0 == use the token directly
}

func plain(tok string) bg           { return bg{token: tok} }
func tint(tok string, p float64) bg { return bg{token: tok, tintPct: p} }

// knownDebt records a pair the project has DELIBERATELY left below its WCAG AA
// threshold. `ratio` is the measured contrast ratio at the time the decision was
// taken; `why` is the one-line justification. Both are asserted — see the block
// comment at the top of this file for what that buys.
type knownDebt struct {
	ratio float64
	why   string
}

// debtTolerance is how far a pinned debt ratio may move before the test fails.
// It is wide enough to absorb last-ulp float differences and far too narrow to
// absorb any real color change (the closest two distinct pinned ratios below
// differ by 0.04).
const debtTolerance = 0.005

// The light theme's accepted-failure reasons. Kept as consts because they are
// one decision, not twenty-five independent ones.
const (
	whyBrandBlue = "brand fidelity chosen over AA for the light-theme primary fill " +
		"(#228BE6, the vendored CivitAI brand blue); see the ACCEPTED AA DEBT block in assets/app.css"
	whyVendoredIntent = "light-theme vendored status palette kept as-is — same brand-fidelity " +
		"call as the primary fill; see the ACCEPTED AA DEBT block in assets/app.css"
	whyDimmedTint = "light text-dimmed stays at v0.1.71's #6b7280 (4.79:1 on the body, AA-clean " +
		"there); darkening it further to clear the 12% tint too was rejected with the rest of the " +
		"light-theme palette changes"
	whySizeLarge = "the .cm-size-large orange stays at its pre-token literal #fb923c so the light " +
		"palette is untouched; it is a file-size magnitude hint, not a brand color, but changing it " +
		"is still a visible light-theme change"
)

// lightDebt is shorthand for "accepted failure in the light theme only".
func lightDebt(ratio float64, why string) map[string]knownDebt {
	return map[string]knownDebt{"light": {ratio: ratio, why: why}}
}

// pair is one foreground/background combination the UI actually produces.
type pair struct {
	what string // where this shows up, in UI terms
	fg   string // foreground token
	bg   bg
	min  float64
	// debt maps theme name -> accepted AA failure. A theme absent from this map
	// must meet `min`. A theme present in it must MISS `min`, at the recorded
	// ratio. Only "light" ever appears here.
	debt map[string]knownDebt
}

// uiPairs enumerates every token pair the shipped UI paints, with the component
// that paints it. Percentages mirror assets/civitai-components.css:
// button[data-variant=light] 12%, badge[data-variant=light] 14%, alert 12-14%.
//
// The `-text` tokens are foregrounds only; the base tokens stay the tint/fill
// source, which is why a tint's `bg` names the BASE token while `fg` names the
// `-text` one (see the WCAG block in assets/app.css).
func uiPairs() []pair {
	return []pair{
		// Body text.
		{"body text on the page", "--civitai-color-text", plain("--civitai-color-body"), wcagAANormal, nil},
		{"body text on surface-2", "--civitai-color-text", plain("--civitai-color-surface-2"), wcagAANormal, nil},

		// The app's most-used secondary text (text-slate-400 / text-slate-500).
		{"dimmed text on the page", "--civitai-color-text-dimmed", plain("--civitai-color-body"), wcagAANormal, nil},
		{"dimmed text on surface-2", "--civitai-color-text-dimmed", plain("--civitai-color-surface-2"), wcagAANormal, nil},
		// The nav bar is bg-slate-900 == --civitai-color-surface (see tailwind.config.js),
		// and the maturity range control's "Maturity" legend + the "–" between its two
		// ends are dimmed text sitting directly on it.
		{"dimmed text on surface (nav maturity legend)", "--civitai-color-text-dimmed", plain("--civitai-color-surface"), wcagAANormal, nil},
		{"dimmed text in a light button (flagToggle off)", "--civitai-color-text-dimmed-text", tint("--civitai-color-text-dimmed", 12), wcagAANormal,
			lightDebt(4.1348, whyDimmedTint)},

		// Brand foreground: text-indigo-*, outline/subtle buttons, the navbar brand.
		{"brand link/outline button on the page", "--civitai-color-primary-text", plain("--civitai-color-body"), wcagAANormal,
			lightDebt(3.5269, whyBrandBlue)},
		{"brand text on surface-2", "--civitai-color-primary-text", plain("--civitai-color-surface-2"), wcagAANormal,
			lightDebt(3.5269, whyBrandBlue)},
		{"brand text in a light button", "--civitai-color-primary-text", tint("--civitai-color-primary", 12), wcagAANormal,
			lightDebt(3.0774, whyBrandBlue)},
		{"brand text in a light badge", "--civitai-color-primary-text", tint("--civitai-color-primary", 14), wcagAANormal,
			lightDebt(3.0067, whyBrandBlue)},

		// Status foregrounds: text-emerald/amber/rose-*, recolored subtle buttons.
		{"success text on the page", "--civitai-color-success-text", plain("--civitai-color-body"), wcagAANormal,
			lightDebt(3.3964, whyVendoredIntent)},
		{"success text in a light badge", "--civitai-color-success-text", tint("--civitai-color-success", 14), wcagAANormal,
			lightDebt(2.9161, whyVendoredIntent)},
		{"success text in a light button", "--civitai-color-success-text", tint("--civitai-color-success", 12), wcagAANormal,
			lightDebt(2.9816, whyVendoredIntent)},
		// The matched library card's "Update available: <version>" CTA (.cm-upd-cta,
		// updateAvailableCTA in model_card_pages.go). It paints the SUCCESS `-text`
		// foreground on a 14% success tint of its own — the same geometry as a light
		// success badge, listed separately because it is a different element and a
		// future re-tint of this CTA must be re-measured here rather than assumed to
		// be covered by the badge row. TestUpdateCTAUsesTheTextToken pins that the
		// shipped rule really reads the `-text` half; this pins the ratio.
		{"update-available CTA (success) on its own tint", "--civitai-color-success-text", tint("--civitai-color-success", 14), wcagAANormal,
			lightDebt(2.9161, whyVendoredIntent)},
		{"error text on the page", "--civitai-color-error-text", plain("--civitai-color-body"), wcagAANormal,
			lightDebt(3.2567, whyVendoredIntent)},
		{"error text in a light badge", "--civitai-color-error-text", tint("--civitai-color-error", 14), wcagAANormal,
			lightDebt(2.7598, whyVendoredIntent)},
		{"warning text on the page", "--civitai-color-warning-text", plain("--civitai-color-body"), wcagAANormal,
			lightDebt(2.5483, whyVendoredIntent)},
		{"warning text in a light badge", "--civitai-color-warning-text", tint("--civitai-color-warning", 14), wcagAANormal,
			lightDebt(2.2351, whyVendoredIntent)},
		{"warning text in a light button", "--civitai-color-warning-text", tint("--civitai-color-warning", 12), wcagAANormal,
			lightDebt(2.2778, whyVendoredIntent)},
		{"info text in a light badge", "--civitai-color-info-text", tint("--civitai-color-info", 14), wcagAANormal,
			lightDebt(3.0067, whyBrandBlue)},

		// The .cm-* custom components in app.css paint intent foregrounds on the
		// surface token: status pills, the active library tab, the version-status
		// and "updated" popovers, the rail's "all" link, the ✓ indicator.
		{"status pill / active tab (brand) on surface", "--civitai-color-primary-text", plain("--civitai-color-surface"), wcagAANormal,
			lightDebt(3.5269, whyBrandBlue)},
		{"broken pill (warning) on surface", "--civitai-color-warning-text", plain("--civitai-color-surface"), wcagAANormal,
			lightDebt(2.5483, whyVendoredIntent)},
		{"duplicate pill (info) on surface", "--civitai-color-info-text", plain("--civitai-color-surface"), wcagAANormal,
			lightDebt(3.5269, whyBrandBlue)},
		{"in-library ✓ (success) on surface", "--civitai-color-success-text", plain("--civitai-color-surface"), wcagAANormal,
			lightDebt(3.3964, whyVendoredIntent)},
		{"huge-size label (error) on surface", "--civitai-color-error-text", plain("--civitai-color-surface"), wcagAANormal,
			lightDebt(3.2567, whyVendoredIntent)},
		// The orange size tier has no design-system token of its own.
		{"large-size label on the page", "--civitai-color-size-large", plain("--civitai-color-body"), wcagAANormal,
			lightDebt(2.2441, whySizeLarge)},
		{"large-size label on surface", "--civitai-color-size-large", plain("--civitai-color-surface"), wcagAANormal,
			lightDebt(2.2441, whySizeLarge)},

		// Fills: the token is the BACKGROUND under primary-fg ink.
		{"filled button label", "--civitai-color-primary-fg", plain("--civitai-color-primary"), wcagAANormal,
			lightDebt(3.5269, whyBrandBlue)},
		{"filled button label on hover", "--civitai-color-primary-fg", plain("--civitai-color-primary-hover"), wcagAANormal,
			lightDebt(4.1605, whyBrandBlue)},
		{"selected facet chip label", "--civitai-color-primary-fg", plain("--civitai-color-primary"), wcagAANormal,
			lightDebt(3.5269, whyBrandBlue)},
		{"filled warning button label (quarantine)", "--civitai-color-on-warning", plain("--civitai-color-warning"), wcagAANormal,
			lightDebt(2.5483, whyVendoredIntent)},

		// Non-text UI: the focus ring must be visible against the page. This one
		// still PASSES on light — 3.53:1 clears the 3:1 non-text threshold — which
		// is why the brand blue is only a text-contrast problem.
		{"focus ring against the page", "--civitai-color-primary", plain("--civitai-color-body"), wcagAAUI, nil},
	}
}

// TestTokenContrast is the regression gate AND the reporter. Run with -v to see
// every computed ratio for both themes, each tagged PASS or DEBT.
//
// A pair without a `debt` entry for the theme under test must meet its minimum.
// A pair WITH one must still miss it, at the pinned ratio — see the file header.
func TestTokenContrast(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		t.Run(theme, func(t *testing.T) {
			tokens := themeTokens(t, theme)
			body, ok := tokens["--civitai-color-body"]
			if !ok {
				t.Fatalf("theme %q defines no --civitai-color-body", theme)
			}
			for _, p := range uiPairs() {
				fg, ok := tokens[p.fg]
				if !ok {
					t.Errorf("%s: foreground token %s is undefined in the %s theme", p.what, p.fg, theme)
					continue
				}
				base, ok := tokens[p.bg.token]
				if !ok {
					t.Errorf("%s: background token %s is undefined in the %s theme", p.what, p.bg.token, theme)
					continue
				}
				back := base
				desc := p.bg.token
				if p.bg.tintPct > 0 {
					back = mixOver(base, body, p.bg.tintPct)
					desc = fmt.Sprintf("%s @%.0f%% over body", p.bg.token, p.bg.tintPct)
				}
				got := contrastRatio(fg, back)
				d, isDebt := p.debt[theme]

				tag := "PASS"
				if isDebt {
					tag = "DEBT"
				}
				t.Logf("%-4s %-42s %-34s on %-44s %5.2f:1 (min %.1f)", tag, p.what, p.fg, desc, got, p.min)

				switch {
				case isDebt && got >= p.min:
					// The debt was paid off — good news, but the entry is now a lie.
					t.Errorf("%s [%s]: %s on %s is %.4f:1, which MEETS the %.1f:1 minimum, but the pair "+
						"is still marked as accepted debt. Delete its `debt` entry in uiPairs() so the "+
						"pass is enforced from now on. (Recorded reason: %s)",
						p.what, theme, p.fg, desc, got, p.min, d.why)
				case isDebt && math.Abs(got-d.ratio) > debtTolerance:
					// The palette moved under an accepted failure. Re-decide, don't drift.
					t.Errorf("%s [%s]: %s on %s is %.4f:1, but this pair is pinned as accepted debt at "+
						"%.4f:1. The %s palette changed. Re-measure, decide whether the new value is "+
						"still acceptable, and update the `debt` entry in uiPairs() AND the ACCEPTED AA "+
						"DEBT block in assets/app.css. (Recorded reason: %s)",
						p.what, theme, p.fg, desc, got, d.ratio, theme, d.why)
				case isDebt:
					// Still exactly as bad as we signed up for.
				case got < p.min:
					t.Errorf("%s [%s]: %s on %s is %.2f:1, below the WCAG 2.1 AA minimum %.1f:1 — "+
						"fix the token in assets/app.css (see the WCAG block there), not the element",
						p.what, theme, p.fg, desc, got, p.min)
				}
			}
		})
	}
}

// TestContrastMathMatchesWCAGReference pins the arithmetic itself against known
// reference values, so a bug in the checker can never silently green-light a
// real contrast failure.
func TestContrastMathMatchesWCAGReference(t *testing.T) {
	cases := []struct {
		fg, bg string
		want   float64
	}{
		{"#000000", "#ffffff", 21.0},  // the definitional extremes
		{"#ffffff", "#ffffff", 1.0},   // identical colors
		{"#777777", "#ffffff", 4.478}, // just under AA — the classic boundary case
		{"#767676", "#ffffff", 4.541}, // the canonical "smallest passing grey"
		{"#1971c2", "#1a1b1e", 3.428}, // the shipped dark primary that failed
	}
	for _, c := range cases {
		fg, err := parseHex(c.fg)
		if err != nil {
			t.Fatalf("parse %s: %v", c.fg, err)
		}
		b, err := parseHex(c.bg)
		if err != nil {
			t.Fatalf("parse %s: %v", c.bg, err)
		}
		got := contrastRatio(fg, b)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("contrast(%s, %s) = %.3f, want %.3f", c.fg, c.bg, got, c.want)
		}
	}
}

// TestLightThemeIsAliasOnly is the "net visual change in the light theme is
// zero" claim, made mechanical.
//
// The AA fix is dark-theme-only, but its plumbing is not: tailwind.config.js,
// the [data-civitai-ui] variant rules in app.css and layout.go's tokenVars all
// now read `--civitai-color-<intent>-text` where they used to read
// `--civitai-color-<intent>`. If the light theme did not define those tokens the
// affected elements would fall through to an unset value; if it defined them as
// anything OTHER than the base token, the light theme would silently recolor.
//
// So every light `-text` token must resolve to EXACTLY its base token, and the
// two extra tokens the plumbing introduced must resolve to exactly what the
// element painted before they existed:
//
//   - `on-warning` is the ink tokenVarsFilled pins on a filled warning button.
//     Before it existed that button used the theme's ordinary filled-button ink,
//     `--civitai-color-primary-fg`.
//   - `size-large` replaced a `color: #fb923c` literal in `.cm-size-large`.
//
// Get any of these wrong and the light theme changes appearance — which is the
// one thing this branch is not allowed to do.
func TestLightThemeIsAliasOnly(t *testing.T) {
	light := themeTokens(t, "light")

	get := func(tok string) rgb {
		t.Helper()
		c, ok := light[tok]
		if !ok {
			t.Fatalf("%s does not resolve in the light theme — the elements pointed at it would "+
				"fall through to an unset value", tok)
		}
		return c
	}

	for _, intent := range []string{"primary", "info", "success", "error", "warning", "text-dimmed"} {
		base, text := get("--civitai-color-"+intent), get("--civitai-color-"+intent+"-text")
		if base != text {
			t.Errorf("light --civitai-color-%s-text is %v but its base --civitai-color-%s is %v. "+
				"On the light theme the `-text` tokens must be pure aliases: the AA fix is "+
				"dark-theme-only, and every element the plumbing repointed at `-text` must keep "+
				"painting the base color it painted before the split.", intent, text, intent, base)
		}
	}

	if got, want := get("--civitai-color-on-warning"), get("--civitai-color-primary-fg"); got != want {
		t.Errorf("light --civitai-color-on-warning is %v, want --civitai-color-primary-fg %v — "+
			"tokenVarsFilled would repaint the filled quarantine button's label", got, want)
	}

	// The literal `.cm-size-large` carried before --civitai-color-size-large existed.
	wantOrange, err := parseHex("#fb923c")
	if err != nil {
		t.Fatalf("parse #fb923c: %v", err)
	}
	if got := get("--civitai-color-size-large"); got != wantOrange {
		t.Errorf("light --civitai-color-size-large is %v, want the pre-token literal %v — "+
			"the 2-6GB size tier would change color on the light theme", got, wantOrange)
	}
}

// TestThemeTokensResolveForBothThemes guards the parser: every token the pair
// table names must resolve in BOTH themes. A token defined for only one theme
// falls back to the other theme's value (or to nothing) and silently breaks the
// theme-aware invariant.
func TestThemeTokensResolveForBothThemes(t *testing.T) {
	light, dark := themeTokens(t, "light"), themeTokens(t, "dark")
	for _, p := range uiPairs() {
		for _, tok := range []string{p.fg, p.bg.token} {
			if _, ok := light[tok]; !ok {
				t.Errorf("%s is undefined in the light theme", tok)
			}
			if _, ok := dark[tok]; !ok {
				t.Errorf("%s is undefined in the dark theme", tok)
			}
		}
	}
	// The two themes must actually differ, or one of them is not being applied.
	if light["--civitai-color-body"] == dark["--civitai-color-body"] {
		t.Error("light and dark resolve the same --civitai-color-body — the theme parse is wrong")
	}
}

// TestDarkThemeCarriesNoContrastDebt pins the shape of the decision, not just its
// numbers: the light theme may hold accepted AA failures, the dark theme may not.
// Without this, a future "just mark it as debt" edit could quietly downgrade the
// one theme that is supposed to be AA-clean.
func TestDarkThemeCarriesNoContrastDebt(t *testing.T) {
	for _, p := range uiPairs() {
		for theme := range p.debt {
			if theme != "light" {
				t.Errorf("%s: accepted contrast debt is recorded for the %q theme. Only the light "+
					"theme may carry debt — the dark theme must meet WCAG 2.1 AA on every pair. "+
					"Fix the token in assets/app.css instead of marking it as debt.", p.what, theme)
			}
		}
	}
}

// TestAcceptedDebtIsDocumentedInCSS keeps the test table and the CSS honest about
// each other. The ratios only mean something if the next person editing the light
// palette finds the decision written next to the colors, so app.css must carry the
// ACCEPTED AA DEBT block and the headline figure the decision was made on.
func TestAcceptedDebtIsDocumentedInCSS(t *testing.T) {
	css := appCSS(t)
	for _, want := range []string{
		"ACCEPTED AA DEBT",
		"#228BE6",              // the brand blue the decision is about
		"3.53:1",               // white-on-primary, the headline failing ratio
		"#1864ab",              // the rejected AA-clean alternative, recorded so it is not re-derived
		"contrast_web_test.go", // the pointer to where the debt is pinned
	} {
		if !strings.Contains(css, want) {
			t.Errorf("assets/app.css must document the accepted light-theme AA debt and mention %q — "+
				"the ratios in uiPairs() are only meaningful if the decision is recorded next to the colors", want)
		}
	}
	// Guard the specific reverts, so a re-darkened light palette cannot ship while
	// this file still claims the vendored brand colors are intact.
	for _, banned := range []string{
		"--civitai-color-primary: #1864ab",
		"--civitai-color-primary-hover: #145591",
		"--civitai-color-text-dimmed: #5f6672",
	} {
		if strings.Contains(css, banned) {
			t.Errorf("app.css declares %q — the light theme must keep the vendored brand palette "+
				"(see the ACCEPTED AA DEBT block); that darkening was deliberately reverted", banned)
		}
	}
}
