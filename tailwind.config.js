/**
 * Tailwind config — slimwhats manager UI.
 *
 * F-02 design tokens. Brand green stays as the primary (matches F-01's
 * `#1a7f37` chromeHeader background). Status badge colors are kept close
 * to the F-01 palette via Tailwind's built-in shades.
 *
 * The `content` field tells Tailwind which files to scan for utility
 * classes. Our templates are inside Go source files (html/template string
 * literals), so we scan `.go` files. Tailwind v3 uses these to purge
 * unused utilities from the output.
 *
 * Token choices per US-033 AC:
 *   - brand green `#1a7f37` → `primary` (close to Tailwind's green-700)
 *   - status colors: green (connected), yellow (disconnected),
 *     red (logged_out/error), gray (created/pairing)
 *   - radii: `lg = 8px` (buttons, inputs), `xl = 12px` (cards),
 *     `full = 9999px` (badges only)
 *   - shadows: `card` and `card-hover`
 */

/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    './internal/handlers/**/*.go',
    './internal/handlers/static/**/*.{css,html}',
  ],
  theme: {
    extend: {
      colors: {
        // Brand green. DEFAULT is the header / primary action color;
        // the variants match the F-01 badge palette.
        primary: {
          DEFAULT: '#1a7f37',
          dark:    '#166a2e',
          light:   '#d4f7dc',
        },
      },
      borderRadius: {
        // Override Tailwind defaults so the 8px/12px values are reachable
        // as `rounded-lg` / `rounded-xl` without custom @apply chains.
        lg:  '8px',
        xl:  '12px',
        // `full` is already 9999px in Tailwind by default (badges only).
      },
      boxShadow: {
        card:       '0 1px 4px rgba(0,0,0,0.05)',
        'card-hover': '0 4px 12px rgba(0,0,0,0.08)',
      },
      fontFamily: {
        // Match the F-01 system-font stack.
        sans: ['-apple-system', 'BlinkMacSystemFont', '"Segoe UI"',
               'Roboto', 'Helvetica', 'Arial', 'sans-serif'],
        mono: ['ui-monospace', 'SFMono-Regular', 'Menlo', 'Monaco',
               'Consolas', '"Liberation Mono"', '"Courier New"', 'monospace'],
      },
    },
  },
  plugins: [],
};
