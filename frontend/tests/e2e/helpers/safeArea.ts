import type { Page } from '@playwright/test'

/**
 * Real `env(safe-area-inset-*)` values for the specs that measure geometry
 * against them.
 *
 * Desktop Chromium reports every inset as 0, so a layout that only misbehaves
 * with a notch or a status bar looks correct in this suite. The experimental
 * CDP command `Emulation.setSafeAreaInsetsOverride` overrides Blink's CSS
 * environment variables — the same `env()` path production uses, not a
 * custom-property shim. Chromium-only, and this suite's only project is
 * chromium.
 *
 * Two specs consume it: `181-dialog-safe-area-geometry.spec.ts` for the modal
 * insets, and `186-boot-splash-handoff.spec.ts` for the splash column.
 */
export interface SafeInsets {
  top: number
  right: number
  bottom: number
  left: number
}

/** iPhone 14 Pro portrait — Dynamic Island + home indicator. */
export const IPHONE_PORTRAIT: SafeInsets = {
  top: 47,
  right: 0,
  bottom: 34,
  left: 0,
}

/**
 * iPhone 14 Pro landscape with the notch on the RIGHT.
 * A spec that pairs this with a CSS width of 844 also exercises the
 * desktop-band dialog path (≥ `breakpoints.sm`, so not the phone full-bleed
 * rules) with non-zero horizontal insets.
 */
export const IPHONE_LANDSCAPE_NOTCH_RIGHT: SafeInsets = {
  top: 0,
  right: 59,
  bottom: 21,
  left: 59,
}

/** A desktop display, or a phone in a browser tab: the browser chrome covers every edge. */
export const ZERO_INSETS: SafeInsets = {
  top: 0,
  right: 0,
  bottom: 0,
  left: 0,
}

/**
 * Inject real `env(safe-area-inset-*)` values through CDP.
 *
 * Pass every edge, including 0: an omitted key makes that variable undefined
 * even if a previous override set it
 * (https://chromedevtools.github.io/devtools-protocol/tot/Emulation/#method-setSafeAreaInsetsOverride).
 */
export async function applySimulatedSafeArea(page: Page, insets: SafeInsets): Promise<void> {
  const session = await page.context().newCDPSession(page)
  await session.send('Emulation.setSafeAreaInsetsOverride', {
    insets: {
      top: insets.top,
      right: insets.right,
      bottom: insets.bottom,
      left: insets.left,
    },
  })
}
