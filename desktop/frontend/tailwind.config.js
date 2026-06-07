/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        primary: {
          50: '#ecfdf8',
          100: '#d1faf0',
          200: '#a7f3e1',
          300: '#6ee7cb',
          400: '#34d4b2',
          500: '#00cdab',
          600: '#00a88c',
          700: '#008672',
          800: '#006a5c',
          900: '#00574c',
          950: '#00332d',
        },
        // Privycs Design System accent extras — bright glow teal +
        // deep teals for gradients/scroll-progress, and the darkened
        // light-mode accent (#0a8f78) for teal text/icons on white,
        // which #00cdab fails contrast on.
        teal: {
          DEFAULT: '#00cdab',
          bright: '#16e0be',
          deep: '#0f766e',
          700: '#0c5f59',
          ink: '#0a8f78',
        },
        // Neutral ramp retinted to the design system's teal-tinted
        // "command-console" scale. Replaces Tailwind's default bluish
        // gray so every existing gray-* surface/text/border utility
        // (dark: pairs included) picks up the new CI without touching
        // components. Luminance steps track the defaults so existing
        // light/dark pairings stay legible.
        //   950 #070B0E ink · 900 #0A1014 ink-2 · 800 #0E161C surface
        //   700 #17242E surface-3 · 600 #3C4E58 fg-4 · 500 #5C7280 fg-3
        //   400 #93A7AD ~fg-2 · 300 #A7B6B9 · 100 #E7EEED · 50 #F2F7F6
        gray: {
          50:  '#f2f7f6',
          100: '#e7eeed',
          200: '#cbd8d7',
          300: '#a7b6b9',
          400: '#93a7ad',
          500: '#5c7280',
          600: '#3c4e58',
          700: '#17242e',
          800: '#0e161c',
          900: '#0a1014',
          950: '#070b0e',
        },
        secondary: {
          500: '#1f8efa',
          600: '#1a75d4',
        },
        danger: {
          500: '#ee423d',
          600: '#d63a36',
        },
        warning: {
          500: '#ffc107',
          600: '#e0aa00',
        },
      },
      fontFamily: {
        sans: ['Inter', 'Segoe UI', 'Roboto', 'Helvetica Neue', 'Arial', 'sans-serif'],
        mono: ['Fira Code', 'Consolas', 'Monaco', 'Courier New', 'monospace'],
      },
    },
  },
  plugins: [],
}
