import { style } from '@vanilla-extract/css'
import { BOOT_SPLASH_GAP, bootSplashDark, bootSplashLight } from '~/lib/bootSplashTheme'

/**
 * Solid-side boot splash. Mirrors `bootSplashDocumentCss()` in
 * `~/lib/bootSplashTheme` (used by `entry-server.tsx`).
 *
 * Literals only — not `var(--background)`. The app CSS chunk publishes a light
 * `:root` palette before `themeStore` runs, so a `var(--background, darkHex)`
 * fallback never wins under `prefers-color-scheme: dark`.
 */
export const root = style({
  'minHeight': '100dvh',
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'flexDirection': 'column',
  'gap': BOOT_SPLASH_GAP,
  'fontFamily': 'system-ui, sans-serif',
  'background': bootSplashLight.background,
  'color': bootSplashLight.foreground,
  '@media': {
    '(prefers-color-scheme: dark)': {
      background: bootSplashDark.background,
      color: bootSplashDark.foreground,
    },
  },
  'selectors': {
    ':global(html[data-theme="light"]) &': {
      background: bootSplashLight.background,
      color: bootSplashLight.foreground,
    },
    ':global(html[data-theme="dark"]) &': {
      background: bootSplashDark.background,
      color: bootSplashDark.foreground,
    },
  },
})

export const label = style({
  margin: 0,
  fontSize: '0.95rem',
})
