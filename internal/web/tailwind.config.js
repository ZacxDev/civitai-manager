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
      // Brand -> primary.
      indigo: {
        100: solid("primary-fg"),
        200: solid("primary"),
        300: solid("primary"),
        400: solid("primary"),
        500: solid("primary"),
        600: solid("primary"),
        700: solid("primary"),
        900: tint("primary", 22),
        950: tint("primary", 22),
      },
      // Success -> emerald.
      emerald: {
        200: solid("success"),
        300: solid("success"),
        400: solid("success"),
        700: solid("success"),
        800: tint("success", 24),
        900: tint("success", 18),
        950: tint("success", 14),
      },
      // Warning -> amber.
      amber: {
        100: solid("warning"),
        200: solid("warning"),
        300: solid("warning"),
        400: solid("warning"),
        500: solid("warning"),
        600: solid("warning"),
        700: tint("warning", 45),
        800: tint("warning", 24),
        900: tint("warning", 18),
        950: tint("warning", 14),
      },
      // Error -> error (rose).
      rose: {
        200: solid("error"),
        300: solid("error"),
        400: solid("error"),
        800: tint("error", 45),
        900: tint("error", 18),
        950: tint("error", 14),
      },
      // Info -> info (sky).
      sky: {
        200: solid("info"),
        900: tint("info", 18),
      },
    },
    extend: {},
  },
  plugins: [],
};
