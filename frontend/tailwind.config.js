/** @type {import('tailwindcss').Config} */
// Tailwind v4 does not load this file by itself: scss/tailwind.css pulls it in
// with "@config". Keep design tokens here so there is a single place to edit.
//
// Every color is declared with light-dark(): the browser resolves it to the
// light or dark value from the OS preference because :root opts into both
// schemes (see scss/tailwind.css). No JavaScript, no class toggling.
//
// Contrast: every "ink" or "DEFAULT" text color below keeps at least 4.5:1
// against canvas/surface (light) and against the matching "soft" tint, so
// status text stays readable in both schemes (WCAG AA).
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  darkMode: "media",
  theme: {
    extend: {
      fontFamily: {
        icon: ['"Material Icons"', "sans-serif"],
      },
      colors: {
        // Page background and raised surfaces (cards, header).
        canvas: "light-dark(#f8fafc, #0b0f17)",
        surface: "light-dark(#ffffff, #111827)",
        // A slightly recessed surface (table header, code, hover rows).
        well: "light-dark(#f1f5f9, #1a2333)",
        // Text: primary and secondary.
        ink: "light-dark(#0f172a, #e5e7eb)",
        muted: "light-dark(#64748b, #9ca3af)",
        // Borders and dividers.
        line: "light-dark(#e2e8f0, #1f2937)",
        // Interactive accent with its hover state and the text drawn on it.
        accent: {
          DEFAULT: "light-dark(#4f46e5, #818cf8)",
          hover: "light-dark(#4338ca, #a5b4fc)",
          contrast: "light-dark(#ffffff, #0b0f17)",
          soft: "light-dark(#eef2ff, #1e1b4b)",
        },
        // Semantic status colors: DEFAULT is used for text and icons, soft
        // for badge/banner backgrounds, contrast for text on a solid fill.
        success: {
          DEFAULT: "light-dark(#15803d, #4ade80)",
          soft: "light-dark(#dcfce7, #14532d)",
          contrast: "light-dark(#ffffff, #052e16)",
        },
        warning: {
          DEFAULT: "light-dark(#a16207, #facc15)",
          soft: "light-dark(#fef9c3, #422006)",
          contrast: "light-dark(#ffffff, #1c1400)",
        },
        danger: {
          DEFAULT: "light-dark(#b91c1c, #f87171)",
          hover: "light-dark(#991b1b, #fca5a5)",
          soft: "light-dark(#fee2e2, #450a0a)",
          contrast: "light-dark(#ffffff, #1f0505)",
        },
        info: {
          DEFAULT: "light-dark(#1d4ed8, #60a5fa)",
          soft: "light-dark(#dbeafe, #172554)",
          contrast: "light-dark(#ffffff, #0b1a3a)",
        },
      },
      minHeight: {
        tap: "44px",
      },
      minWidth: {
        tap: "44px",
      },
    },
  },
  plugins: [],
};
