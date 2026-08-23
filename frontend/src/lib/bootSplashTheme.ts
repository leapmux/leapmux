/**
 * Colours and polarity for the zero-JS boot splash.
 *
 * Taken from Default theme so the static HTML in `entry-server.tsx`, the Solid
 * `BootSplash`, and the blocking head script cannot drift from the palette
 * `themeStore` later paints.
 */
import type { ThemeMode } from '~/styles/themes'
import { paletteColorToHex, resolveVariant } from '~/styles/themes'
import { defaultTheme } from '~/styles/themes/default'

function hex(polarity: 'light' | 'dark', token: '--background' | '--foreground'): string {
  return paletteColorToHex(resolveVariant(defaultTheme, undefined, polarity).palette[token]!)
}

export const bootSplashLight = {
  background: hex('light', '--background'),
  foreground: hex('light', '--foreground'),
} as const

export const bootSplashDark = {
  background: hex('dark', '--background'),
  foreground: hex('dark', '--foreground'),
} as const

/**
 * Resolve splash polarity the same way `themeStore.resolvedMode` does for the
 * UI theme: an explicit light/dark pin wins; `system` (or anything else) follows
 * the OS.
 */
export function resolveBootPolarity(
  mode: ThemeMode | string | undefined,
  systemDark: boolean,
): 'light' | 'dark' {
  if (mode === 'light' || mode === 'dark')
    return mode
  return systemDark ? 'dark' : 'light'
}
