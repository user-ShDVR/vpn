/** @type {import('tailwindcss').Config} */

// Helper: color with opacity support via CSS variables (RGB tuple).
const withOpacity = (variableName) => ({ opacityValue }) => {
  if (opacityValue !== undefined) {
    return `rgba(var(${variableName}), ${opacityValue})`;
  }
  return `rgb(var(${variableName}))`;
};

module.exports = {
  content: ["./web/templates/**/*.templ"],
  darkMode: 'class',
  theme: {
    extend: {
      fontFamily: {
        sans: ['Manrope', 'system-ui', '-apple-system', 'sans-serif'],
        display: ['Outfit', 'Manrope', 'system-ui', 'sans-serif'],
        mono: ['"IBM Plex Mono"', 'ui-monospace', 'monospace'],
      },
      colors: {
        dark: {
          50:  withOpacity('--color-dark-50'),
          100: withOpacity('--color-dark-100'),
          200: withOpacity('--color-dark-200'),
          300: withOpacity('--color-dark-300'),
          400: withOpacity('--color-dark-400'),
          500: withOpacity('--color-dark-500'),
          600: withOpacity('--color-dark-600'),
          700: withOpacity('--color-dark-700'),
          800: withOpacity('--color-dark-800'),
          850: withOpacity('--color-dark-850'),
          900: withOpacity('--color-dark-900'),
          950: withOpacity('--color-dark-950'),
        },
        champagne: {
          50:  withOpacity('--color-champagne-50'),
          100: withOpacity('--color-champagne-100'),
          200: withOpacity('--color-champagne-200'),
          300: withOpacity('--color-champagne-300'),
          400: withOpacity('--color-champagne-400'),
          500: withOpacity('--color-champagne-500'),
          600: withOpacity('--color-champagne-600'),
          700: withOpacity('--color-champagne-700'),
          800: withOpacity('--color-champagne-800'),
          900: withOpacity('--color-champagne-900'),
          950: withOpacity('--color-champagne-950'),
        },
        accent: {
          50:  withOpacity('--color-accent-50'),
          100: withOpacity('--color-accent-100'),
          200: withOpacity('--color-accent-200'),
          300: withOpacity('--color-accent-300'),
          400: withOpacity('--color-accent-400'),
          500: withOpacity('--color-accent-500'),
          600: withOpacity('--color-accent-600'),
          700: withOpacity('--color-accent-700'),
          800: withOpacity('--color-accent-800'),
          900: withOpacity('--color-accent-900'),
          950: withOpacity('--color-accent-950'),
        },
        success: {
          50:  withOpacity('--color-success-50'),
          100: withOpacity('--color-success-100'),
          200: withOpacity('--color-success-200'),
          300: withOpacity('--color-success-300'),
          400: withOpacity('--color-success-400'),
          500: withOpacity('--color-success-500'),
          600: withOpacity('--color-success-600'),
          700: withOpacity('--color-success-700'),
          800: withOpacity('--color-success-800'),
          900: withOpacity('--color-success-900'),
          950: withOpacity('--color-success-950'),
        },
        warning: {
          50:  withOpacity('--color-warning-50'),
          100: withOpacity('--color-warning-100'),
          200: withOpacity('--color-warning-200'),
          300: withOpacity('--color-warning-300'),
          400: withOpacity('--color-warning-400'),
          500: withOpacity('--color-warning-500'),
          600: withOpacity('--color-warning-600'),
          700: withOpacity('--color-warning-700'),
          800: withOpacity('--color-warning-800'),
          900: withOpacity('--color-warning-900'),
          950: withOpacity('--color-warning-950'),
        },
        error: {
          50:  withOpacity('--color-error-50'),
          100: withOpacity('--color-error-100'),
          200: withOpacity('--color-error-200'),
          300: withOpacity('--color-error-300'),
          400: withOpacity('--color-error-400'),
          500: withOpacity('--color-error-500'),
          600: withOpacity('--color-error-600'),
          700: withOpacity('--color-error-700'),
          800: withOpacity('--color-error-800'),
          900: withOpacity('--color-error-900'),
          950: withOpacity('--color-error-950'),
        },
        brand: {
          start: '#FF3B5C',
          end:   '#FF6B35',
        },
      },
      borderRadius: {
        '2xl': '18px',
        '3xl': '24px',
        bento: '24px',
        'bento-sm': '16px',
        linear: '8px',
        'linear-lg': '12px',
      },
      spacing: {
        bento: '16px',
        'bento-lg': '24px',
      },
      backdropBlur: {
        linear: '12px',
      },
      boxShadow: {
        glow: '0 0 20px rgba(var(--color-accent-500), 0.15)',
        'glow-lg': '0 0 40px rgba(var(--color-accent-500), 0.2)',
        soft: '0 2px 15px -3px rgba(0, 0, 0, 0.3), 0 4px 6px -4px rgba(0, 0, 0, 0.2)',
        card: '0 4px 24px -4px rgba(0, 0, 0, 0.4)',
      },
      animation: {
        'fade-in':         'fadeIn 0.4s cubic-bezier(0.16, 1, 0.3, 1)',
        'fade-in-fast':    'fadeIn 0.2s cubic-bezier(0.16, 1, 0.3, 1)',
        'slide-up':        'slideUp 0.5s cubic-bezier(0.16, 1, 0.3, 1)',
        'slide-down':      'slideDown 0.5s cubic-bezier(0.16, 1, 0.3, 1)',
        'scale-in':        'scaleIn 0.4s cubic-bezier(0.16, 1, 0.3, 1)',
        'glow-pulse':      'glowPulse 2s ease-in-out infinite',
        float:             'float 3s ease-in-out infinite',
        'traffic-shimmer': 'trafficShimmer 2s ease-in-out infinite',
      },
      keyframes: {
        fadeIn:   { '0%': { opacity: '0' }, '100%': { opacity: '1' } },
        slideUp:  { '0%': { opacity: '0', transform: 'translateY(20px)' }, '100%': { opacity: '1', transform: 'translateY(0)' } },
        slideDown:{ '0%': { opacity: '0', transform: 'translateY(-20px)' }, '100%': { opacity: '1', transform: 'translateY(0)' } },
        scaleIn:  { '0%': { opacity: '0', transform: 'scale(0.95)' }, '100%': { opacity: '1', transform: 'scale(1)' } },
        float:    { '0%, 100%': { transform: 'translateY(0)' }, '50%': { transform: 'translateY(-5px)' } },
        glowPulse:{
          '0%, 100%': { boxShadow: '0 0 20px rgba(var(--color-accent-500), 0.3)' },
          '50%':       { boxShadow: '0 0 40px rgba(var(--color-accent-500), 0.6)' },
        },
        trafficShimmer: {
          '0%':   { transform: 'translateX(-100%)' },
          '100%': { transform: 'translateX(200%)' },
        },
      },
      transitionTimingFunction: {
        smooth: 'cubic-bezier(0.4, 0, 0.2, 1)',
      },
    },
  },
  plugins: [
    require('@tailwindcss/forms'),
    function ({ addVariant }) {
      addVariant('light', '.light &');
    },
  ],
};
