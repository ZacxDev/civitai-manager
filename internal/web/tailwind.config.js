/**
 * Tailwind config for civitai-manager's embedded UI.
 *
 * Class names live in Go string literals in the gomponents templates, so the
 * content globs point at this package's .go files. Regenerate the embedded
 * stylesheet after changing any template's classes:
 *
 *   nix-shell -p tailwindcss --run \
 *     "tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"
 *
 * THEMING (civitai design system): the app's fixed slate/indigo/emerald/amber/
 * rose/sky palette is remapped onto @civitai/theme's `--civitai-*` design
 * tokens, so every existing utility class resolves to a themed value that flips
 * with `data-theme="light|dark"`. Neutral (slate) shades use relative-rgb so
 * opacity modifiers (e.g. bg-slate-900/60) still work; semantic background
 * shades use a color-mix tint so a tinted panel keeps contrast with its
 * same-hue text. The actual DS components (button/input/card/badge/alert) are
 * authored with the `data-civitai-ui` contract instead of these utilities.
 */

// solid: token color, honoring Tailwind's <alpha-value> opacity modifier.
const solid = (name) => `rgb(from var(--civitai-color-${name}) r g b / <alpha-value>)`;
// tint: fixed low-alpha wash of a token (for tinted status backgrounds/borders).
const tint = (name, pct) => `color-mix(in srgb, var(--civitai-color-${name}) ${pct}%, transparent)`;

// CONTRAST SPLIT (see the WCAG block in assets/app.css): each intent token does
// two incompatible jobs — FILL under white text, and FOREGROUND on the body. On
// the dark theme no single value can pass WCAG AA at both, so the shades this app
// actually uses as TEXT (indigo/emerald/amber/rose 200-400, sky 200) resolve to
// the AA-contrast `--civitai-color-<intent>-text` token, while the shades used for
// fills/borders (indigo 500-700, amber 500-600, emerald 700) keep the base token.
// Changing which shade a template uses can therefore change its contrast — the
// pairs are pinned by internal/web/contrast_web_test.go.
const text = (name) => solid(`${name}-text`);

module.exports = {
  content: ["./*.go"],
  theme: {
    colors: {
      transparent: "transparent",
      current: "currentColor",
      black: "#000",
      white: solid("primary-fg"),
      // Neutral scale -> civitai body/surface/border/text tokens.
      slate: {
        50: solid("text"),
        100: solid("text"),
        200: solid("text"),
        300: solid("text"),
        400: solid("text-dimmed"),
        500: solid("text-dimmed"),
        600: solid("border"),
        700: solid("border"),
        800: solid("border"),
        900: solid("surface"),
        950: solid("body"),
      },
      // Brand -> primary. 200-400 are the TEXT shades (AA foreground token);
      // 500-700 stay on the base token — they paint form-control accents,
      // borders and fills, where the brand color is not read as text.
      indigo: {
        100: solid("primary-fg"),
        200: text("primary"),
        300: text("primary"),
        400: text("primary"),
        500: solid("primary"),
        600: solid("primary"),
        700: solid("primary"),
        900: tint("primary", 22),
        950: tint("primary", 22),
      },
      // Success -> emerald (400 is the only shade used, always as text).
      emerald: {
        200: text("success"),
        300: text("success"),
        400: text("success"),
        700: solid("success"),
        800: tint("success", 24),
        900: tint("success", 18),
        950: tint("success", 14),
      },
      // Warning -> amber (400 is used as text; 500/600 stay fills).
      amber: {
        100: text("warning"),
        200: text("warning"),
        300: text("warning"),
        400: text("warning"),
        500: solid("warning"),
        600: solid("warning"),
        700: tint("warning", 45),
        800: tint("warning", 24),
        900: tint("warning", 18),
        950: tint("warning", 14),
      },
      // Error -> error (rose); 300/400 are used as text.
      rose: {
        200: text("error"),
        300: text("error"),
        400: text("error"),
        800: tint("error", 45),
        900: tint("error", 18),
        950: tint("error", 14),
      },
      // Info -> info (sky).
      sky: {
        200: text("info"),
        900: tint("info", 18),
      },
    },
    extend: {},
  },
  plugins: [],
};
