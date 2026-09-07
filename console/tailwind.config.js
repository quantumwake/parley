/** @type {import('tailwindcss').Config} */
// The shared terminal-ux "midnight" palette + IBM Plex Mono, same as the statefs consoles.
export default {
  content: ['./index.html', './src/**/*.{js,jsx}', './node_modules/@quantumwake/terminal-ux-components/dist/**/*.{js,cjs}'],
  theme: {
    extend: {
      fontFamily: { mono: ['IBM Plex Mono', 'monospace'] },
      colors: {
        midnight: {
          base: '#0e0e10', surface: '#161618', elevated: '#1e1e22', raised: '#28282e', border: '#333338',
          'border-subtle': '#404048', 'border-glow': '#505058', 'text-primary': '#ffffff', 'text-secondary': '#e8e8ec',
          'text-body': '#c8c8d0', 'text-muted': '#9898a0', 'text-subdued': '#707078', 'text-disabled': '#585860',
          'text-hint': '#484850', 'text-label': '#a0a0b0', danger: '#ef4444', 'danger-bright': '#f87171',
          warning: '#f59e0b', 'warning-bright': '#fbbf24', success: '#10b981', 'success-bright': '#34d399',
          info: '#3b82f6', 'info-bright': '#60a5fa', accent: '#8b5cf6', 'accent-bright': '#a78bfa',
          'accent-glow': '#c4b5fd', glow: '#7c3aed', electric: '#06b6d4',
        },
      },
    },
  },
  plugins: [],
}
