/** @type {import('tailwindcss').Config} */
// Tailwind v4 does not load this file by itself: scss/tailwind.css pulls it in
// with "@config". Keep design tokens here so there is a single place to edit.
//
// Every color is declared with light-dark(): the browser resolves it to the
// light or dark value from the OS preference because :root opts into both
// schemes (see scss/tailwind.css). No JavaScript, no class toggling.
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
        },
      },
    },
  },
  plugins: [],
};
