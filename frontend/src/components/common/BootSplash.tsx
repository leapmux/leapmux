import type { Component } from 'solid-js'
import {
  BOOT_SPLASH_ICON_HEIGHT,
  BOOT_SPLASH_ICON_WIDTH,
  BOOT_SPLASH_LABEL,
  BOOT_SPLASH_TEST_ID,
} from '~/lib/bootSplashTheme'

/**
 * Inline leapmux mark shared by the static document splash and Solid
 * `BootSplash`. Path geometry must stay aligned with
 * `public/icons/leapmux-icon.svg` (see `bootSplashTheme.test.ts`).
 */
export const BootSplashIcon: Component = () => (
  <svg
    xmlns="http://www.w3.org/2000/svg"
    viewBox="0 0 64 64"
    width={BOOT_SPLASH_ICON_WIDTH}
    height={BOOT_SPLASH_ICON_HEIGHT}
    aria-hidden="true"
    focusable="false"
    data-boot-splash-icon
  >
    <rect width="64" height="64" rx="14" fill="#0D9488" />
    <path
      d="M16 20 L30 32 L16 44"
      fill="none"
      stroke="#FFFFFF"
      stroke-width="7"
      stroke-linecap="round"
      stroke-linejoin="round"
    />
    <path
      d="M44 17 L48 28 L58 32 L48 36 L44 47 L40 36 L30 32 L40 28 Z"
      fill="#F59E0B"
    />
  </svg>
)

/**
 * First-paint and Suspense chrome while the CSR graph loads.
 *
 * Copy and test id come from `~/lib/bootSplashTheme`. Visual styles come from
 * `bootSplashDocumentCss()` in the document `<head>` (selectors on
 * `#boot-splash` and `[data-testid="boot-splash"]`) — there is no second
 * vanilla-extract stylesheet to keep in lockstep.
 *
 * This component deliberately omits the static document splash id: that id is
 * reserved so the boot-failure watchdog can tell mount succeeded.
 */
export const BootSplash: Component = () => (
  <div data-testid={BOOT_SPLASH_TEST_ID} role="status" aria-live="polite">
    <BootSplashIcon />
    <p>{BOOT_SPLASH_LABEL}</p>
  </div>
)
