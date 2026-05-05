/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ["./web/templates/**/*.templ"],
  darkMode: 'class',
  theme: {
    extend: {
      fontFamily: {
        sans: ['Manrope', 'system-ui', 'sans-serif'],
        mono: ['"IBM Plex Mono"', 'ui-monospace', 'monospace'],
      },
      colors: {
        dark: {
          50:  '#f9fafb',
          100: '#f9fafb',
          200: '#dadde2',
          300: '#bcc1c9',
          400: '#9ca3af',
          500: '#646b79',
          600: '#49505d',
          700: '#2d3442',
          800: '#111827',
          850: '#0a101d',
          900: '#070c18',
          950: '#030712',
        },
        accent: {
          50:  '#f3f3fc',
          100: '#e7e7f9',
          200: '#bdbef9',
          300: '#8e90f5',
          400: '#5659f0',
          500: '#1418eb',
          600: '#1114c5',
          700: '#0e10a0',
          800: '#0b0c7a',
          900: '#0f104d',
        },
        success: {
          400: '#62e492',
          500: '#25da67',
        },
        warning: {
          400: '#f8ba4f',
          500: '#f59f0a',
        },
        error: {
          400: '#f05656',
          500: '#eb1414',
        },
        brand: {
          start: '#FF3B5C',
          end:   '#FF6B35',
        },
      },
      borderRadius: {
        '2xl': '18px',
        '3xl': '24px',
      },
    },
  },
  plugins: [require('@tailwindcss/forms')],
};
