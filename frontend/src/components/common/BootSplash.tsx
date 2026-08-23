import type { Component } from 'solid-js'
import {
  BOOT_SPLASH_ICON_HEIGHT,
  BOOT_SPLASH_ICON_SRC,
  BOOT_SPLASH_ICON_WIDTH,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_TEST_ID,
} from '~/lib/bootSplashTheme'
import * as styles from './BootSplash.css'

/**
 * First-paint and Suspense chrome while the CSR graph loads.
 *
 * Copy and test id come from `~/lib/bootSplashTheme`, which also feeds the
 * static HTML in `entry-server.tsx`. Polarity comes from `prefers-color-scheme`
 * and `html[data-theme]`.
 */
export const BootSplash: Component = () => (
  <div data-testid={BOOT_SPLASH_TEST_ID} class={styles.root} role="status" aria-live="polite">
    <img
      src={BOOT_SPLASH_ICON_SRC}
      width={BOOT_SPLASH_ICON_WIDTH}
      height={BOOT_SPLASH_ICON_HEIGHT}
      alt=""
    />
    <p class={styles.label}>{BOOT_SPLASH_LABEL}</p>
  </div>
)
