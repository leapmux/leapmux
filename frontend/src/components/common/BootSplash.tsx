import type { Component } from 'solid-js'
import * as styles from './BootSplash.css'

/**
 * First-paint and Suspense chrome while the CSR graph loads.
 *
 * The same markup is inlined into `entry-server.tsx` inside `#app` so the
 * static HTML Go serves is never a blank page. Keep the two copies in lockstep:
 * this component is the Solid tree used after JS runs (Suspense / AuthGuard);
 * the server document is the zero-JS twin. Polarity comes from
 * `prefers-color-scheme` and `html[data-theme]` (see `~/lib/bootSplashTheme`).
 */
export const BootSplash: Component = () => (
  <div data-testid="boot-splash" class={styles.root} role="status" aria-live="polite">
    <img src="/icons/leapmux-icon.svg" width="64" height="64" alt="" />
    <p class={styles.label}>Loading LeapMux…</p>
  </div>
)
