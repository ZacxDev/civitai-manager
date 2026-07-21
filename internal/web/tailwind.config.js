/**
 * Tailwind config for civitai-manager's embedded UI.
 *
 * Class names live in Go string literals in the gomponents templates, so the
 * content globs point at this package's .go files. Regenerate the embedded
 * stylesheet after changing any template's classes:
 *
 *   nix-shell -p tailwindcss --run \
 *     "tailwindcss -c tailwind.config.js -i input.css -o assets/output.css --minify"
 */
module.exports = {
  content: ["./*.go"],
  theme: {
    extend: {},
  },
  plugins: [],
};
