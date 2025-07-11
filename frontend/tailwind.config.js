/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,jsx,ts,tsx}"
  ],
  theme: {
    extend: {
      colors: {
        primary: {
          DEFAULT: "#2563eb", // blue-600
          light: "#3b82f6",   // blue-500
          dark: "#1e40af",    // blue-800
        },
        accent: "#f59e42",    // orange-400
        background: "#f8fafc",// slate-50
        surface: "#ffffff",   // white
        muted: "#64748b",     // slate-500
        border: "#e2e8f0",    // slate-200
      },
    },
    extend: {},
  },
  plugins: [],
}
