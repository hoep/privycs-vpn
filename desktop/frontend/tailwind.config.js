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
