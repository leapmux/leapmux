import type { Component } from 'solid-js'
import {
  BOOT_SPLASH_ICON_HEIGHT,
  BOOT_SPLASH_ICON_SRC,
  BOOT_SPLASH_ICON_WIDTH,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_TEST_ID,
} from '~/lib/bootSplashTheme'

/**
 * First-paint and Suspense chrome while the CSR graph loads.
 *
 * Copy and test id come from `~/lib/bootSplashTheme`. Visual styles come from
 * `bootSplashDocumentCss()` in the document `<head>` (selectors on
 * `#boot-splash` and `[data-testid="boot-splash"]`) — there is no second
 * vanilla-extract stylesheet to keep in lockstep.
 */
export const BootSplash: Component = () => (
  <div data-testid={BOOT_SPLASH_TEST_ID} role="status" aria-live="polite">
    <img
      src={BOOT_SPLASH_ICON_SRC}
      width={BOOT_SPLASH_ICON_WIDTH}
      height={BOOT_SPLASH_ICON_HEIGHT}
      alt=""
    />
    <p>{BOOT_SPLASH_LABEL}</p>
  </div>
)
