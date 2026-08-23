import type { Component } from 'solid-js'

/**
 * First-paint and Suspense chrome while the CSR graph loads.
 *
 * The same markup is inlined into `entry-server.tsx` inside `#app` so the
 * static HTML Go serves is never a blank page. Keep the two copies in lockstep:
 * this component is the Solid tree used after JS runs (Suspense / AuthGuard);
 * the server document is the zero-JS twin. Inline styles only — the app CSS
 * chunk has not necessarily arrived yet when the static twin paints.
 */
const splashStyle = {
  'min-height': '100dvh',
  'display': 'flex',
  'align-items': 'center',
  'justify-content': 'center',
  'flex-direction': 'column',
  'gap': '1rem',
  'font-family': 'system-ui, sans-serif',
  'color': 'var(--foreground, #1a1917)',
  'background': 'var(--background, #fffefc)',
} as const

export const BootSplash: Component = () => (
  <div data-testid="boot-splash" style={splashStyle} role="status" aria-live="polite">
    <img src="/icons/leapmux-icon.svg" width="64" height="64" alt="" />
    <p style={{ 'margin': '0', 'font-size': '0.95rem' }}>Loading LeapMux…</p>
  </div>
)
