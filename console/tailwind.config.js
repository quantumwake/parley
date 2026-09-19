/** @type {import('tailwindcss').Config} */
// Poetix Studio's two themes (chalkboard, paper) as CSS variables, so parley and
// Poetix read as one family. Space-separated RGB channels keep /opacity working.
const c = (v) => `rgb(var(${v}) / <alpha-value>)`
export default {
  // terminal-ux-components' classes are generated here too; its midnight-*
  // colours map onto parley's variables below, so it follows both themes.
  content: ['./index.html', './src/**/*.{js,jsx}', './node_modules/@quantumwake/terminal-ux-components/dist/**/*.js'],
  theme: {
    borderRadius: { none: '0', DEFAULT: '0', sm: '0', md: '1px', lg: '2px', xl: '2px', '2xl': '2px', full: '9999px' },
    extend: {
      fontFamily: {
        serif: ['ui-serif', 'Iowan Old Style', 'Charter', 'Palatino', 'Georgia', 'serif'],
        ui: ['Inter', '-apple-system', 'BlinkMacSystemFont', 'system-ui', 'sans-serif'],
        mono: ['IBM Plex Mono', 'ui-monospace', 'SFMono-Regular', 'monospace'],
      },
      colors: {
        base: c('--c-base'), surface: c('--c-surface'), elevated: c('--c-elevated'), raised: c('--c-raised'),
        border: c('--c-border'), 'border-subtle': c('--c-border-subtle'), 'border-glow': c('--c-border-glow'),
        ink: c('--c-ink'), 'ink-2': c('--c-ink-2'), 'ink-body': c('--c-ink-body'), 'ink-muted': c('--c-ink-muted'),
        'ink-subdued': c('--c-ink-subdued'), 'ink-hint': c('--c-ink-hint'),
        accent: c('--c-accent'), 'accent-bright': c('--c-accent-bright'), success: c('--c-success'), danger: c('--c-danger'), info: c('--c-info'),
        midnight: {
          base: c('--c-base'), surface: c('--c-surface'), elevated: c('--c-elevated'), raised: c('--c-raised'),
          border: c('--c-border'), 'border-subtle': c('--c-border-subtle'), 'border-glow': c('--c-border-glow'),
          accent: c('--c-accent'), 'accent-bright': c('--c-accent-bright'),
          'text-primary': c('--c-ink'), 'text-secondary': c('--c-ink-2'), 'text-body': c('--c-ink-body'), 'text-label': c('--c-ink-muted'),
          'text-muted': c('--c-ink-muted'), 'text-subdued': c('--c-ink-subdued'), 'text-hint': c('--c-ink-hint'), 'text-disabled': c('--c-ink-disabled'),
          info: c('--c-info'), 'info-bright': c('--c-info'), success: c('--c-success'), 'success-bright': c('--c-success'),
          danger: c('--c-danger'), 'danger-bright': c('--c-danger'), warning: c('--c-accent-bright'), 'warning-bright': c('--c-accent-bright'),
        },
      },
    },
  },
  plugins: [],
}
