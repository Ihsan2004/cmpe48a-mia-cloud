/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        // Canvas — bone-white page background
        background: 'var(--canvas-bone)',
        surface: {
          DEFAULT: 'var(--canvas-bone)',
          elevated: 'var(--canvas-muted)',
        },
        // Primary — ink charcoal (used for primary CTA fills)
        primary: {
          DEFAULT: 'var(--ink-charcoal)',
          hover: 'var(--ink-secondary)',
          active: 'var(--ink-charcoal)',
        },
        // Accent — ochre (decorative fill + darker ochre-ink for text/rings)
        accent: {
          DEFAULT: 'var(--accent-ochre-ink)',
          fill: 'var(--accent-ochre)',
          hover: 'var(--ink-charcoal)',
          active: 'var(--ink-charcoal)',
        },
        secondary: {
          DEFAULT: 'var(--canvas-muted)',
          hover: 'var(--canvas-muted)',
          active: 'var(--canvas-muted)',
        },
        // Alert stays warm-red but keyed to the ink family
        danger: '#b23a2d',
        // Text hierarchy on bone canvas
        text: {
          DEFAULT: 'var(--ink-charcoal)',
          secondary: 'var(--ink-secondary)',
          tertiary: 'rgba(245, 245, 243, 0.45)',
          muted: 'rgba(245, 245, 243, 0.30)',
        },
        // Borders/dividers
        border: 'var(--canvas-muted)',
        // Direct brand tokens for explicit use
        canvas: {
          bone: 'var(--canvas-bone)',
          muted: 'var(--canvas-muted)',
        },
        ink: {
          charcoal: 'var(--ink-charcoal)',
          secondary: 'var(--ink-secondary)',
        },
        ochre: {
          DEFAULT: 'var(--accent-ochre)',
          ink: 'var(--accent-ochre-ink)',
        },
      },
      fontFamily: {
        heading: ['Fraunces', 'Georgia', 'Times New Roman', 'serif'],
        serif: ['Fraunces', 'Georgia', 'Times New Roman', 'serif'],
        body: ['Geist', 'ui-sans-serif', 'system-ui', '-apple-system', 'sans-serif'],
        sans: ['Geist', 'ui-sans-serif', 'system-ui', '-apple-system', 'sans-serif'],
        mono: ['SF Mono', 'Consolas', 'Monaco', 'monospace'],
      },
      fontSize: {
        // Syncrun editorial scale — 1.25 modular
        'micro': ['13px', { lineHeight: '1.5' }],
        'caption': ['13px', { lineHeight: '1.5' }],
        'meta': ['15px', { lineHeight: '1.5' }],
        'sm': ['15px', { lineHeight: '1.5' }],
        'base': ['17px', { lineHeight: '1.55' }],
        'body': ['17px', { lineHeight: '1.55' }],
        'lg': ['17px', { lineHeight: '1.55' }],
        'h4': ['20px', { lineHeight: '1.3' }],
        'h3': ['24px', { lineHeight: '1.3' }],
        'h2': ['36px', { lineHeight: '1.15', letterSpacing: '-0.01em' }],
        'h1': ['52px', { lineHeight: '1.06', letterSpacing: '-0.015em' }],
        'display': ['72px', { lineHeight: '1.02', letterSpacing: '-0.02em' }],
      },
      letterSpacing: {
        'eyebrow': '0.14em',
      },
      spacing: {
        '3xs': '2px',
        '2xs': '4px',
        'xs': '8px',
        'sm': '12px',
        'md': '16px',
        'lg': '24px',
        'xl': '32px',
        '2xl': '48px',
        '3xl': '64px',
      },
      borderRadius: {
        'none': '0px',
        'sm': '0.375rem',
        'md': '0.5rem',
        'lg': '0.625rem',
        'xl': '0.875rem',
        'full': '9999px',
      },
      boxShadow: {
        // Syncrun restraint — no decorative shadows.
        // These tokens collapse to 'none' so any legacy class using them
        // no longer produces the "SaaS card" look.
        'subtle': 'none',
        'medium': 'none',
        'strong': 'none',
        'glow': '0 0 0 2px var(--canvas-bone), 0 0 0 4px var(--accent-ochre-ink)',
        'card-hover': 'none',
      },
      animation: {
        'fade-in-up': 'fadeInUp 0.3s ease-out forwards',
        'fade-in': 'fadeIn 0.2s ease-out forwards',
        'shimmer': 'shimmer 1.5s ease-in-out infinite',
      },
      keyframes: {
        fadeInUp: {
          '0%': { opacity: '0', transform: 'translateY(10px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        fadeIn: {
          '0%': { opacity: '0' },
          '100%': { opacity: '1' },
        },
        shimmer: {
          '0%': { backgroundColor: 'var(--canvas-bone)' },
          '50%': { backgroundColor: 'var(--canvas-muted)' },
          '100%': { backgroundColor: 'var(--canvas-bone)' },
        },
      },
    },
  },
  plugins: [],
}
