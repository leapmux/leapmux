import { globalStyle, style } from '@vanilla-extract/css'

// Map LeapMux's palette onto the ALTCHA widget's CSS custom properties so
// the widget follows the Oat theme and flips with data-theme="dark"
// automatically — the palette vars themselves change, these mappings are
// constant. Custom properties set on the host element override the
// widget's own :root defaults through inheritance, the theming mechanism
// the widget documents.
globalStyle('altcha-widget', {
  vars: {
    '--altcha-color-base': 'var(--card)',
    '--altcha-color-base-content': 'var(--foreground)',
    '--altcha-color-neutral': 'var(--border)',
    '--altcha-color-neutral-content': 'var(--muted-foreground)',
    '--altcha-color-primary': 'var(--primary)',
    '--altcha-color-primary-content': 'var(--primary-foreground)',
    '--altcha-color-success': 'var(--success)',
    '--altcha-color-success-content': 'var(--success-foreground)',
    '--altcha-color-error': 'var(--danger)',
    '--altcha-color-error-content': 'var(--danger-foreground)',
    '--altcha-border-color': 'var(--border)',
    '--altcha-border-radius': 'var(--radius-2)',
    '--altcha-checkbox-outline-color': 'var(--ring)',
    '--altcha-padding': 'var(--space-3)',
    '--altcha-max-width': '100%',
  },
})

export const field = style({
  width: '100%',
})

// The honeypot: present and fillable, but invisible and unreachable.
// Deliberately NOT display:none or type=hidden — naive bots inspect for
// exactly those before deciding to fill a field. Real users never tab to
// it (tabindex -1), screen readers skip it (aria-hidden), and autofill
// heuristics pass it by (unlabeled, off-screen).
export const honeypot = style({
  position: 'absolute',
  left: '-10000px',
  top: 'auto',
  width: '1px',
  height: '1px',
  overflow: 'hidden',
})

export const loading = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-3) 0',
  color: 'var(--muted-foreground)',
  fontSize: 'var(--text-7)',
})

export const loadError = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
})
