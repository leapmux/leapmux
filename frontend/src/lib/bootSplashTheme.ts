/**
 * Colours, copy, document CSS, and the blocking boot script for the zero-JS
 * splash. Shared by `entry-server.tsx` and `BootSplash` so the static HTML Go
 * serves and the Solid Suspense/AuthGuard chrome cannot drift.
 *
 * Palette comes from Default theme so the splash matches what `themeStore`
 * paints after hydration.
 */
import type { ThemeMode } from '~/styles/themes'
import { KEY_BROWSER_PREFS } from '~/lib/browserStorage'
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

/** `data-testid` on both the static splash and the Solid component. */
export const BOOT_SPLASH_TEST_ID = 'boot-splash'

/** Visible label; keep the ellipsis character identical in both trees. */
export const BOOT_SPLASH_LABEL = 'Loading LeapMux…'

export const BOOT_SPLASH_ICON_SRC = '/icons/leapmux-icon.svg'
export const BOOT_SPLASH_ICON_WIDTH = 64
export const BOOT_SPLASH_ICON_HEIGHT = 64

/**
 * Spacing token for the splash column gap. Used in `bootSplashDocumentCss`.
 * That stylesheet also seeds `--space-4: 1rem` so the gap resolves before
 * oat's theme sheet loads.
 */
export const BOOT_SPLASH_GAP = 'var(--space-4)'

/** Literal that matches oat's `--space-4` (see `@knadh/oat` `01-theme.css`). */
export const BOOT_SPLASH_SPACE_4 = '1rem'

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

/**
 * Read `theme.mode` from a `leapmux:browser-prefs` TTL envelope (`{ v, e }`).
 * Returns `"system"` when the raw value is missing, expired, or malformed.
 *
 * The blocking head script cannot import this function; it inlines the same
 * checks. The matrix in `bootSplashTheme.test.ts` compares script outcomes to
 * `resolveBootPolarity(parseBootPrefsThemeMode(...), systemDark)` so the two
 * paths cannot drift silently.
 */
export function parseBootPrefsThemeMode(raw: string | null, nowMs: number): string {
  if (!raw)
    return 'system'
  try {
    const wrap = JSON.parse(raw) as { e?: unknown, v?: { theme?: { mode?: unknown } } }
    if (
      wrap
      && typeof wrap.e === 'number'
      && wrap.e > nowMs
      && wrap.v
      && wrap.v.theme
      && typeof wrap.v.theme.mode === 'string'
    ) {
      return wrap.v.theme.mode
    }
  }
  catch {
    // Malformed JSON → system.
  }
  return 'system'
}

/**
 * Inline document CSS for the static splash, the Solid `BootSplash` (same
 * `data-testid`), and the html/body fill that covers the brief gap between
 * Solid `mount` clearing `#app` and the next splash paint. This is the only
 * splash stylesheet — do not reintroduce a vanilla-extract twin.
 */
export function bootSplashDocumentCss(): string {
  const lightBg = bootSplashLight.background
  const lightFg = bootSplashLight.foreground
  const darkBg = bootSplashDark.background
  const darkFg = bootSplashDark.foreground
  return `
:root{--space-4:${BOOT_SPLASH_SPACE_4}}
html,body{margin:0;background:${lightBg}}
@media (prefers-color-scheme: dark){
  html,body{background:${darkBg}}
}
html[data-theme="light"],html[data-theme="light"] body{background:${lightBg}}
html[data-theme="dark"],html[data-theme="dark"] body{background:${darkBg}}
#boot-splash,[data-testid="${BOOT_SPLASH_TEST_ID}"]{
  min-height:100dvh;display:flex;align-items:center;justify-content:center;
  flex-direction:column;gap:${BOOT_SPLASH_GAP};font-family:system-ui,sans-serif;
  background:${lightBg};
  color:${lightFg};
}
@media (prefers-color-scheme: dark){
  #boot-splash,[data-testid="${BOOT_SPLASH_TEST_ID}"]{
    background:${darkBg};
    color:${darkFg};
  }
}
html[data-theme="light"] #boot-splash,html[data-theme="light"] [data-testid="${BOOT_SPLASH_TEST_ID}"]{
  background:${lightBg};
  color:${lightFg};
}
html[data-theme="dark"] #boot-splash,html[data-theme="dark"] [data-testid="${BOOT_SPLASH_TEST_ID}"]{
  background:${darkBg};
  color:${darkFg};
}
#boot-splash p,[data-testid="${BOOT_SPLASH_TEST_ID}"] p{margin:0;font-size:.95rem}
`.trim()
}

/**
 * Blocking head script: paint `data-theme` and chrome `theme-color` from the
 * device tier before first paint. Cannot import `browserStorage` at runtime in
 * the browser document — the key and `{ v, e }` shape must stay aligned with
 * that module (see `KEY_BROWSER_PREFS` / `parseBootPrefsThemeMode`).
 *
 * Prefs parse failures stay inside an inner try so a bad envelope still paints
 * `system` polarity (same as `parseBootPrefsThemeMode`), rather than leaving
 * `data-theme` unset.
 *
 * When the device tier pins light or dark, every `theme-color` meta is set to
 * that colour and its `media` attribute is removed so an OS preference cannot
 * override the pin. System mode keeps the dual media metas and only rewrites
 * the non-media fallback to the current OS answer.
 */
export function bootThemeScript(): string {
  const lightBg = bootSplashLight.background
  const darkBg = bootSplashDark.background
  const key = KEY_BROWSER_PREFS
  return `(function(){var mode="system";try{var raw=localStorage.getItem(${JSON.stringify(key)});if(raw){var wrap=JSON.parse(raw);if(wrap&&typeof wrap.e==="number"&&wrap.e>Date.now()&&wrap.v&&wrap.v.theme&&typeof wrap.v.theme.mode==="string")mode=wrap.v.theme.mode;}}catch(e){}try{var dark=mode==="dark"||(mode!=="light"&&window.matchMedia("(prefers-color-scheme: dark)").matches);var root=document.documentElement;root.setAttribute("data-theme",dark?"dark":"light");root.style.colorScheme=dark?"dark":"light";root.style.backgroundColor=dark?${JSON.stringify(darkBg)}:${JSON.stringify(lightBg)};var color=dark?${JSON.stringify(darkBg)}:${JSON.stringify(lightBg)};var metas=document.querySelectorAll('meta[name="theme-color"]');var pinned=mode==="light"||mode==="dark";if(pinned){for(var i=0;i<metas.length;i++){metas[i].setAttribute("content",color);metas[i].removeAttribute("media");}}else{var fallback=document.querySelector('meta[name="theme-color"]:not([media])');if(fallback)fallback.setAttribute("content",color);}}catch(e){}})();`
}
