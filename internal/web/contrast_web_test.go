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

// pair is one foreground/background combination the UI actually produces.
type pair struct {
	what string // where this shows up, in UI terms
	fg   string // foreground token
	bg   bg
	min  float64
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
		{"body text on the page", "--civitai-color-text", plain("--civitai-color-body"), wcagAANormal},
		{"body text on surface-2", "--civitai-color-text", plain("--civitai-color-surface-2"), wcagAANormal},

		// The app's most-used secondary text (text-slate-400 / text-slate-500).
		{"dimmed text on the page", "--civitai-color-text-dimmed", plain("--civitai-color-body"), wcagAANormal},
		{"dimmed text on surface-2", "--civitai-color-text-dimmed", plain("--civitai-color-surface-2"), wcagAANormal},
		{"dimmed text in a light button (flagToggle off)", "--civitai-color-text-dimmed-text", tint("--civitai-color-text-dimmed", 12), wcagAANormal},

		// Brand foreground: text-indigo-*, outline/subtle buttons, the navbar brand.
		{"brand link/outline button on the page", "--civitai-color-primary-text", plain("--civitai-color-body"), wcagAANormal},
		{"brand text on surface-2", "--civitai-color-primary-text", plain("--civitai-color-surface-2"), wcagAANormal},
		{"brand text in a light button", "--civitai-color-primary-text", tint("--civitai-color-primary", 12), wcagAANormal},
		{"brand text in a light badge", "--civitai-color-primary-text", tint("--civitai-color-primary", 14), wcagAANormal},

		// Status foregrounds: text-emerald/amber/rose-*, recolored subtle buttons.
		{"success text on the page", "--civitai-color-success-text", plain("--civitai-color-body"), wcagAANormal},
		{"success text in a light badge", "--civitai-color-success-text", tint("--civitai-color-success", 14), wcagAANormal},
		{"success text in a light button", "--civitai-color-success-text", tint("--civitai-color-success", 12), wcagAANormal},
		{"error text on the page", "--civitai-color-error-text", plain("--civitai-color-body"), wcagAANormal},
		{"error text in a light badge", "--civitai-color-error-text", tint("--civitai-color-error", 14), wcagAANormal},
		{"warning text on the page", "--civitai-color-warning-text", plain("--civitai-color-body"), wcagAANormal},
		{"warning text in a light badge", "--civitai-color-warning-text", tint("--civitai-color-warning", 14), wcagAANormal},
		{"warning text in a light button", "--civitai-color-warning-text", tint("--civitai-color-warning", 12), wcagAANormal},
		{"info text in a light badge", "--civitai-color-info-text", tint("--civitai-color-info", 14), wcagAANormal},

		// Fills: the token is the BACKGROUND under primary-fg ink.
		{"filled button label", "--civitai-color-primary-fg", plain("--civitai-color-primary"), wcagAANormal},
		{"filled button label on hover", "--civitai-color-primary-fg", plain("--civitai-color-primary-hover"), wcagAANormal},
		{"selected facet chip label", "--civitai-color-primary-fg", plain("--civitai-color-primary"), wcagAANormal},
		{"filled warning button label (quarantine)", "--civitai-color-on-warning", plain("--civitai-color-warning"), wcagAANormal},

		// Non-text UI: the focus ring must be visible against the page.
		{"focus ring against the page", "--civitai-color-primary", plain("--civitai-color-body"), wcagAAUI},
	}
}

// TestTokenContrast is the regression gate AND the reporter. Run with -v to see
// every computed ratio for both themes.
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
				t.Logf("%-42s %-34s on %-44s %5.2f:1 (min %.1f)", p.what, p.fg, desc, got, p.min)
				if got < p.min {
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
