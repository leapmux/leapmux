/**
 * Colours, copy, document CSS, and the blocking boot scripts for the zero-JS
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

/**
 * Shared DOM key for the static splash `id` and both trees' `data-testid`.
 * One string on purpose: the static node carries both attributes. The contract
 * is that Solid's `BootSplash` sets `data-testid` only and omits the `id`
 * attribute — not that these two exports differ.
 */
const BOOT_SPLASH_DOM_KEY = 'boot-splash'

/** `data-testid` on both the static splash and the Solid component. */
export const BOOT_SPLASH_TEST_ID = BOOT_SPLASH_DOM_KEY

/**
 * HTML `id` of the STATIC document splash only. Same string as
 * {@link BOOT_SPLASH_TEST_ID}. The boot-failure watchdog treats this node's
 * removal as "the client mounted", even when Suspense/AuthGuard still show a
 * splash that keeps only `data-testid`.
 */
export const BOOT_SPLASH_STATIC_ID = BOOT_SPLASH_DOM_KEY

/** Visible label; keep the ellipsis character identical in both trees. */
export const BOOT_SPLASH_LABEL = 'Loading LeapMux…'

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

/** Title when the static splash gives up and shows the failure panel. */
export const BOOT_SPLASH_FAIL_TITLE = 'Could not start LeapMux'

/** Reload control on the static failure panel. */
export const BOOT_SPLASH_RELOAD_LABEL = 'Reload'

/**
 * How long the static `#boot-splash` may remain before the watchdog treats
 * boot as failed. Solid mount removes that node; Suspense/AuthGuard splash
 * uses `data-testid` only, so a slow auth bootstrap does not trip this.
 *
 * Generous on purpose: mobile LTE cold start still finishes well under this,
 * and a tight budget would flash the failure panel on a working but slow path.
 */
export const BOOT_SPLASH_FAIL_TIMEOUT_MS = 45_000

/** Detail when the watchdog timer fires with no earlier script fault. */
export const BOOT_SPLASH_FAIL_TIMEOUT_DETAIL
  = 'The app did not start in time. Check your network connection, then reload the page.'

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
 *
 * Body rules here match `~/styles/global.css.ts` (`position: fixed`, safe-area
 * padding, `#app` fill) so first paint already uses the geometry that lands
 * when the app stylesheet loads. Without that lockstep the flex-centered
 * splash jumped down when `padding-top: env(safe-area-inset-top)` arrived.
 *
 * Sizing is split on purpose:
 * - `#boot-splash` fills `#app` (`min-height: 100%`) and must not use
 *   `100dvh`, or safe-area padding on body re-centers it downward.
 * - `[data-testid]:not(#boot-splash)` (Solid Suspense/AuthGuard) also sets
 *   `min-height: 100dvh` so a missing definite-height ancestor cannot collapse
 *   it to content size. The `:not(#id)` keeps the static node on `%` only.
 */
export function bootSplashDocumentCss(): string {
  const lightBg = bootSplashLight.background
  const lightFg = bootSplashLight.foreground
  const darkBg = bootSplashDark.background
  const darkFg = bootSplashDark.foreground
  const id = BOOT_SPLASH_STATIC_ID
  const testId = BOOT_SPLASH_TEST_ID
  return `
:root{--space-4:${BOOT_SPLASH_SPACE_4}}
html,body,#app{margin:0;height:100%;width:100%;overflow:hidden}
html,body{background:${lightBg}}
@media (prefers-color-scheme: dark){
  html,body{background:${darkBg}}
}
html[data-theme="light"],html[data-theme="light"] body{background:${lightBg}}
html[data-theme="dark"],html[data-theme="dark"] body{background:${darkBg}}
body{
  position:fixed;top:0;left:0;width:100%;height:100dvh;
  padding-top:env(safe-area-inset-top,0px);box-sizing:border-box;
}
#${id},[data-testid="${testId}"]{
  box-sizing:border-box;width:100%;height:100%;
  display:flex;align-items:center;justify-content:center;
  flex-direction:column;gap:${BOOT_SPLASH_GAP};font-family:system-ui,sans-serif;
  background:${lightBg};
  color:${lightFg};
}
#${id}{min-height:100%}
[data-testid="${testId}"]:not(#${id}){min-height:100%;min-height:100dvh}
@media (prefers-color-scheme: dark){
  #${id},[data-testid="${testId}"]{
    background:${darkBg};
    color:${darkFg};
  }
}
html[data-theme="light"] #${id},html[data-theme="light"] [data-testid="${testId}"]{
  background:${lightBg};
  color:${lightFg};
}
html[data-theme="dark"] #${id},html[data-theme="dark"] [data-testid="${testId}"]{
  background:${darkBg};
  color:${darkFg};
}
#${id} svg,[data-testid="${testId}"] svg{display:block;flex-shrink:0}
#${id} p,[data-testid="${testId}"] p{margin:0;font-size:.95rem;text-align:center}
#${id} .boot-splash-loading,#${id} .boot-splash-error{
  display:flex;flex-direction:column;align-items:center;gap:${BOOT_SPLASH_GAP};
}
#${id} .boot-splash-error{display:none;max-width:24rem;padding:0 ${BOOT_SPLASH_SPACE_4};text-align:center}
#${id}[data-boot-failed] .boot-splash-loading{display:none}
#${id}[data-boot-failed] .boot-splash-error{display:flex}
#${id} .boot-splash-error pre{margin:0 auto;width:max-content;max-width:min(100%,20rem);overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:.85rem;text-align:left}
#${id} .boot-splash-error button{
  font:inherit;cursor:pointer;padding:.5rem 1rem;border-radius:.375rem;
  border:1px solid currentColor;background:transparent;color:inherit;
}
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

/**
 * Document script that surfaces a boot failure when the static splash never
 * yields to Solid mount, or when a script resource fails to load.
 *
 * Solid's `ErrorBoundary` and `installGlobalErrorSink` only exist after the
 * entry graph runs. A missing or broken entry chunk leaves `#boot-splash` on
 * screen forever with no recovery — this watchdog is the UI for that class.
 *
 * Only `<script>` load errors count. Favicon, manifest, and stylesheet `<link>`
 * failures must not tombstone the splash: they are common and non-fatal.
 *
 * Success signal: the static node `#boot-splash` is gone (Solid `mount`
 * replaces `#app`). A MutationObserver finishes the watchdog then (clears the
 * timer and removes the capture listener). The Suspense/AuthGuard `BootSplash`
 * keeps only `data-testid`, so a long auth bootstrap does not look like a
 * failed boot.
 */
export function bootFailureWatchdogScript(): string {
  const id = BOOT_SPLASH_STATIC_ID
  const title = BOOT_SPLASH_FAIL_TITLE
  const reload = BOOT_SPLASH_RELOAD_LABEL
  const timeoutDetail = BOOT_SPLASH_FAIL_TIMEOUT_DETAIL
  const timeoutMs = BOOT_SPLASH_FAIL_TIMEOUT_MS
  // One IIFE string for the document. Keep logic flat; tests evaluate this
  // exact source via `new Function`.
  return `(function(){`
    + `var id=${JSON.stringify(id)};`
    + `var title=${JSON.stringify(title)};`
    + `var reloadLabel=${JSON.stringify(reload)};`
    + `var timeoutDetail=${JSON.stringify(timeoutDetail)};`
    + `var timeoutMs=${timeoutMs};`
    + `var done=false;`
    + `var timer=null;`
    + `var observer=null;`
    + `function root(){return document.getElementById(id);}`
    + `function finish(){`
    + `if(done)return;`
    + `done=true;`
    + `if(timer)clearTimeout(timer);`
    + `timer=null;`
    + `window.removeEventListener("error",onScriptError,true);`
    + `if(observer){observer.disconnect();observer=null;}`
    + `}`
    + `function fail(detail){`
    + `if(done)return;`
    + `var el=root();`
    + `if(!el){finish();return;}`
    + `finish();`
    + `el.setAttribute("data-boot-failed","true");`
    + `el.setAttribute("role","alert");`
    + `el.removeAttribute("aria-live");`
    + `var loading=el.querySelector(".boot-splash-loading");`
    + `if(loading)loading.setAttribute("hidden","");`
    + `var err=el.querySelector(".boot-splash-error");`
    + `if(!err)return;`
    + `err.removeAttribute("hidden");`
    + `var t=err.querySelector("[data-boot-fail-title]");`
    + `if(t)t.textContent=title;`
    + `var d=err.querySelector("[data-boot-fail-detail]");`
    + `if(d)d.textContent=detail||timeoutDetail;`
    + `var b=err.querySelector("[data-boot-reload]");`
    + `if(b){b.textContent=reloadLabel;b.onclick=function(){location.reload();}}`
    + `}`
    + `function onScriptError(ev){`
    + `var t=ev&&ev.target;`
    + `if(!t||!t.tagName)return;`
    + `if(String(t.tagName).toLowerCase()!=="script")return;`
    + `var src=t.src||"script";`
    + `fail("Failed to load\\n"+src);`
    + `}`
    + `window.addEventListener("error",onScriptError,true);`
    + `var app=document.getElementById("app");`
    + `if(app&&typeof MutationObserver==="function"){`
    + `observer=new MutationObserver(function(){if(!root())finish();});`
    + `observer.observe(app,{childList:true,subtree:true});`
    + `}`
    + `timer=setTimeout(function(){`
    + `if(done)return;`
    + `if(!root()){finish();return;}`
    + `fail(timeoutDetail);`
    + `},timeoutMs);`
    + `})();`
}
