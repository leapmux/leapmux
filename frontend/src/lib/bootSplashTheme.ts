/**
 * Colours, copy, document CSS, the blocking boot scripts, the boot-phase
 * checklist, and the client-side removal of the static splash. Shared by
 * `entry-server.tsx`, `entry-client.tsx`, and `BootSplash` so the static HTML
 * Go serves and the Solid Suspense/AuthGuard chrome cannot drift.
 *
 * Palette comes from Default theme so the splash matches what `themeStore`
 * paints after hydration.
 */
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

/**
 * Remove the static document splash. The client entry calls this right after
 * `mount()` returns. This doc is the canonical explanation of the handoff;
 * the comments at the other sites point here.
 *
 * With `ssr: false`, the SPA mount is solid's plain `render()`. It appends
 * into `#app` and removes no existing child. Without this call, the
 * 100%-height splash keeps the booted app below the fold, and the watchdog
 * fails the boot at 45s.
 *
 * The call runs AFTER mount on purpose. Mount is synchronous, so an entry
 * graph that throws never reaches this call, and the watchdog still owns
 * that failure class.
 *
 * A no-op when the document shipped no splash. In a hydrating build the node
 * survives hydration — hydration claims only nodes that carry a hydration
 * key, and the static splash carries none — so the call removes the node
 * there too.
 */
export function removeStaticBootSplash(): void {
  document.getElementById(BOOT_SPLASH_STATIC_ID)?.remove()
}

/** Visible label; keep the ellipsis character identical in both trees. */
export const BOOT_SPLASH_LABEL = 'Loading LeapMux…'

/**
 * The boot checklist, in order. Both trees render these rows identically;
 * which row is DONE and which is ACTIVE is carried by
 * {@link BOOT_SPLASH_PHASE_ATTRIBUTE} on `<html>` and styled purely by CSS in
 * `bootSplashDocumentCss` — there is no shared runtime state, so the pre-JS
 * static splash and the post-mount Solid splash advance in lockstep.
 *
 * The attribute advances in this order: the document script
 * (`bootPhaseScript`) marks `loading-bundles` once the splash markup is
 * parsed, `entry-client` marks `mounting` when the entry graph executes,
 * `AuthProvider.bootstrap` marks `system-info` → `session`, then either
 * `ready` (signed-out / non-shell) or `workspaces` (authenticated shell).
 * AppShell then marks `tabs` → `ready` around its list and CRDT wait.
 *
 * {@link BOOT_SPLASH_SHELL_PHASES} sit after the core rows and stay
 * `display:none` until {@link setBootShell} opts the document into the shell
 * path — login, signup, and password-setup never show them.
 */
export const BOOT_SPLASH_CORE_PHASES = [
  { key: 'initializing', label: 'Initializing' },
  { key: 'loading-bundles', label: 'Loading bundles' },
  { key: 'mounting', label: 'Mounting application' },
  { key: 'system-info', label: 'Loading system info' },
  { key: 'session', label: 'Restoring session' },
] as const

/** Shell-only rows: workspaces list, then CRDT tabs. Hidden off the shell path. */
export const BOOT_SPLASH_SHELL_PHASES = [
  { key: 'workspaces', label: 'Loading workspaces' },
  { key: 'tabs', label: 'Restoring tabs' },
] as const

/** Core then shell — the full ordered checklist the phase CSS walks. */
export const BOOT_SPLASH_PHASES = [
  ...BOOT_SPLASH_CORE_PHASES,
  ...BOOT_SPLASH_SHELL_PHASES,
] as const

export type BootPhaseKey = (typeof BOOT_SPLASH_PHASES)[number]['key']

/** Terminal phase: every visible row checked. Not a row. */
export const BOOT_PHASE_READY = 'ready'

/**
 * Fade duration for the splash entrance and for the exit crossfade over the
 * ready shell. One number so the CSS transition and the AppShell unmount
 * timer cannot drift.
 */
export const BOOT_SPLASH_ENTER_MS = 220

export type BootPhase = BootPhaseKey | typeof BOOT_PHASE_READY

/** Attribute on `<html>` that carries the current boot phase to the CSS. */
export const BOOT_SPLASH_PHASE_ATTRIBUTE = 'data-boot-phase'

/**
 * Attribute on `<html>` that reveals {@link BOOT_SPLASH_SHELL_PHASES}.
 * Set only on the authenticated AppShell path; absent on login/signup/etc.
 */
export const BOOT_SPLASH_SHELL_ATTRIBUTE = 'data-boot-shell'

/** Class on each shell-only checklist row; CSS hides it without the attribute. */
export const BOOT_SPLASH_SHELL_ROW_CLASS = 'boot-splash-progress-shell'

/** Client-side phase advance; see {@link BOOT_SPLASH_PHASES} for the order. */
export function setBootPhase(phase: BootPhase): void {
  document.documentElement.setAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE, phase)
}

/**
 * Show or hide the shell-only checklist rows. `true` on the AppShell path;
 * `false` on login/signup/password-setup and on logout.
 */
export function setBootShell(enabled: boolean): void {
  if (enabled)
    document.documentElement.setAttribute(BOOT_SPLASH_SHELL_ATTRIBUTE, '')
  else
    document.documentElement.removeAttribute(BOOT_SPLASH_SHELL_ATTRIBUTE)
}

export const BOOT_SPLASH_ICON_WIDTH = 64
export const BOOT_SPLASH_ICON_HEIGHT = 64

/**
 * Spacing token for the splash column gap. Used in `bootSplashDocumentCss`.
 * That stylesheet also seeds `--space-4: 1rem` on the splash itself, so the
 * gap resolves before oat's theme sheet loads.
 */
export const BOOT_SPLASH_GAP = 'var(--space-4)'

/** Literal that matches oat's `--space-4` (see `@knadh/oat` `01-theme.css`). */
export const BOOT_SPLASH_SPACE_4 = '1rem'

/**
 * Line height for every text node in the splash. A literal that matches oat's
 * `--leading-normal`, stated on the splash itself.
 *
 * Oat states its line height on `body`, inside `@layer base`:
 * `body,dialog,[popover]{...;line-height:var(--leading-normal)}`. So the splash
 * `<p>` INHERITED `normal` until that sheet landed and `1.5` after it. At
 * `.95rem` that grows the line box from 18px to 22.8px, and half of the new
 * leading sits above the glyphs: the centered column rose 2.4px, so the space
 * under the icon widened by 2.4px part way through boot.
 *
 * Being unlayered wins the splash stylesheet nothing here. An unlayered
 * declaration outranks a layered one only for a property it actually declares,
 * and this stylesheet declared no `line-height` at all -- neither on the splash
 * nor on `body` -- so the value arrived purely by inheritance.
 *
 * Stated on the splash container, NOT seeded as `:root{--leading-normal:1.5}`
 * the way `--space-4` is. That seed shape is unusable for a token the app also
 * reads: the seed is unlayered and oat's `:root` sits in `@layer theme`, so it
 * would pin `--leading-normal` for the WHOLE app, permanently, from a
 * stylesheet that exists to paint one splash.
 *
 * Unitless on purpose. The failure panel's `<pre>` sets `.85rem`, and a length
 * would give it the line box of the `.95rem` paragraph.
 */
export const BOOT_SPLASH_LINE_HEIGHT = '1.5'

/** Title when the static splash gives up and shows the failure panel. */
export const BOOT_SPLASH_FAIL_TITLE = 'Could not start LeapMux'

/** Reload control on the static failure panel. */
export const BOOT_SPLASH_RELOAD_LABEL = 'Reload'

/**
 * How long the static `#boot-splash` may remain before the watchdog treats
 * boot as failed. The client entry removes that node right after `mount()`
 * (see {@link removeStaticBootSplash}); Suspense/AuthGuard splash uses
 * `data-testid` only, so a slow auth bootstrap does not trip this.
 *
 * Generous on purpose: mobile LTE cold start still finishes well under this,
 * and a tight budget would flash the failure panel on a working but slow path.
 */
export const BOOT_SPLASH_FAIL_TIMEOUT_MS = 45_000

/** Detail when the watchdog timer fires with no earlier script fault. */
export const BOOT_SPLASH_FAIL_TIMEOUT_DETAIL
  = 'The app did not start in time. Check your network connection, then reload the page.'

/**
 * Attribute-driven row states for the progress checklist, appended to
 * `bootSplashDocumentCss`. For each phase value: every row ABOVE it is done
 * (check visible, label half-muted), the phase's own row is active (full
 * label, pulsing dots), rows below stay pending. With no attribute at all the
 * first row is active — the document has rendered but `bootPhaseScript` has
 * not run yet. `ready` checks every row and leaves the list visible: AppShell
 * keeps this splash up until workspaces and tabs land, so the finished
 * checklist must still be readable.
 *
 * Shell-only rows (`BOOT_SPLASH_SHELL_PHASES`) stay `display:none` until
 * `data-boot-shell` is set, so login/signup never show them. Phase selectors
 * still cover those keys: when the rows are hidden the rules match nothing.
 *
 * Selectors are deliberately unscoped: the `boot-splash-progress-*` classes
 * exist only inside the two splash trees, and the phase attribute persists on
 * `<html>` after boot, where these rules then match nothing.
 */
function bootProgressPhaseCss(): string {
  const attr = BOOT_SPLASH_PHASE_ATTRIBUTE
  const rows = (scope: string, keys: readonly string[], suffix: string, decl: string): string =>
    keys.map(k => `${scope} .boot-splash-row-${k}${suffix}{${decl}}`).join('')

  const phases = BOOT_SPLASH_PHASES.map((phase, index) => {
    const scope = `html[${attr}="${phase.key}"]`
    const done = BOOT_SPLASH_PHASES.slice(0, index).map(p => p.key)
    return [
      rows(scope, done, ' .boot-splash-progress-label', 'opacity:.6'),
      rows(scope, done, ' .boot-splash-progress-check', 'opacity:1'),
      rows(scope, [phase.key], ' .boot-splash-progress-label', 'opacity:1'),
      rows(scope, [phase.key], ' .boot-splash-progress-dots', 'opacity:1'),
    ].join('')
  }).join('')

  const unstarted = `html:not([${attr}])`
  const ready = `html[${attr}="${BOOT_PHASE_READY}"]`
  const all = BOOT_SPLASH_PHASES.map(p => p.key)
  const shellAttr = BOOT_SPLASH_SHELL_ATTRIBUTE
  const shellRow = BOOT_SPLASH_SHELL_ROW_CLASS
  const id = BOOT_SPLASH_STATIC_ID
  const testId = BOOT_SPLASH_TEST_ID
  // Specificity must beat `#id .boot-splash-progress-row{display:grid}` or the
  // shell rows would stay visible on login.
  return [
    `#${id} .${shellRow},[data-testid="${testId}"] .${shellRow}{display:none}`,
    `html[${shellAttr}] #${id} .${shellRow},html[${shellAttr}] [data-testid="${testId}"] .${shellRow}{display:grid}`,
    phases,
    rows(unstarted, [BOOT_SPLASH_PHASES[0].key], ' .boot-splash-progress-label', 'opacity:1'),
    rows(unstarted, [BOOT_SPLASH_PHASES[0].key], ' .boot-splash-progress-dots', 'opacity:1'),
    rows(ready, all, ' .boot-splash-progress-label', 'opacity:.6'),
    rows(ready, all, ' .boot-splash-progress-check', 'opacity:1'),
  ].join('')
}

/**
 * Inline document CSS for the static splash, the Solid `BootSplash` (same
 * `data-testid`), and the html/body fill that holds the splash geometry and
 * polarity from first paint until the app stylesheet takes over. This is the
 * only splash stylesheet — do not reintroduce a vanilla-extract twin.
 *
 * Body rules here match `~/styles/global.css.ts` (`position: fixed`, safe-area
 * padding, `#app` fill) so first paint already uses the geometry that lands
 * when the app stylesheet loads. Without that lockstep the flex-centered
 * splash jumped down when `padding-top: env(safe-area-inset-top)` arrived.
 *
 * Being unlayered buys this stylesheet nothing on a property it does not
 * DECLARE. An unlayered declaration outranks a layered one, so oat cannot take
 * a property back — but silence is not a declaration, and oat then answers
 * unopposed. Two shapes of that bug already landed here:
 *
 * - `line-height`, inherited. The splash took `normal` from `body` and then
 *   oat's `1.5`, and the column moved mid-boot. See
 *   {@link BOOT_SPLASH_LINE_HEIGHT}.
 * - The failure panel's `<pre>`, direct. Oat's `@layer base` `pre` rule adds
 *   `padding: var(--space-4)`, a `--faint` background, a radius and the mono
 *   family, so the same panel measured 32px taller with oat than without.
 *
 * So state a value for every property the splash depends on, and NEUTRALIZE
 * oat's element rules inside the splash (`margin:0`, `padding:0`,
 * `font:inherit`) rather than restating what oat would have painted. Restating
 * would copy palette tokens like `--faint` into this file, which is a second
 * source for a colour the themes own. Where a literal must match oat,
 * `bootSplashTheme.test.ts` reads oat's own stylesheet and compares.
 *
 * Sizing is split on purpose:
 * - `#boot-splash` fills `#app` (`min-height: 100%`) and must not use
 *   `100dvh`, or safe-area padding on body re-centers it downward.
 * - `[data-testid]:not(#boot-splash)` (Solid Suspense/AuthGuard) also sets
 *   `min-height: 100dvh` so a missing definite-height ancestor cannot collapse
 *   it to content size. The `:not(#id)` keeps the static node on `%` only.
 *
 * It also owns `color-scheme` for the PRE-HYDRATION WINDOW ONLY, which is why
 * `bootThemeScript` may not write one inline. Two properties make that exact:
 *
 * - `:not([data-ui-theme])` ENDS the window. `themeStore`'s DOM effect writes
 *   `data-ui-theme` in the same step as `data-theme` and the two variant
 *   attributes, and nothing else writes it, so this rule stops matching the
 *   instant the app owns the answer. Without that clause it never yields:
 *   `html[data-theme]` is (0,1,1) and `lightVariantSelector`'s self match is
 *   `[data-ui-light="X"]` at (0,1,0), so the app's own light `color-scheme`
 *   would be permanently shadowed -- agreeing today by coincidence, and wrong
 *   the day a palette wants to say something else.
 * - Inside the window it is (0,2,1), above `global.css.ts`'s unlayered
 *   `html { color-scheme: light }` at (0,0,1), so form controls and scrollbars
 *   are dark from first paint on a dark OS.
 *
 * A cascade layer cannot do this job: `global.css.ts` is unlayered, and an
 * unlayered declaration wins over a layered one whatever the specificity.
 */
export function bootSplashDocumentCss(): string {
  const lightBg = bootSplashLight.background
  const lightFg = bootSplashLight.foreground
  const darkBg = bootSplashDark.background
  const darkFg = bootSplashDark.foreground
  const id = BOOT_SPLASH_STATIC_ID
  const testId = BOOT_SPLASH_TEST_ID
  // Shimmer gradient stops per polarity: the sweep must be BRIGHTER than the
  // base glyphs in both polarities — mixed toward the page colour under a dark
  // foreground, toward white under a light one. The gradient itself only ever
  // reads these custom properties, never `currentColor`: the shimmer rule
  // paints the label with `color: transparent`, and a `currentColor` stop
  // would resolve against exactly that.
  const lightLo = lightFg
  const lightHi = `color-mix(in srgb, ${lightFg} 55%, ${lightBg})`
  const darkLo = darkFg
  const darkHi = `color-mix(in srgb, ${darkFg} 45%, #ffffff)`
  return `
html,body,#app{margin:0;height:100%;width:100%;overflow:hidden}
html,body{background:${lightBg}}
@media (prefers-color-scheme: dark){
  html,body{background:${darkBg}}
}
html[data-theme="light"],html[data-theme="light"] body{background:${lightBg}}
html[data-theme="dark"],html[data-theme="dark"] body{background:${darkBg}}
html[data-theme="light"]:not([data-ui-theme]){color-scheme:light}
html[data-theme="dark"]:not([data-ui-theme]){color-scheme:dark}
body{
  position:fixed;top:0;left:0;width:100%;height:100dvh;
  padding-top:env(safe-area-inset-top,0px);box-sizing:border-box;
}
#${id},[data-testid="${testId}"]{
  --space-4:${BOOT_SPLASH_SPACE_4};
  --boot-splash-lo:${lightLo};
  --boot-splash-hi:${lightHi};
  box-sizing:border-box;width:100%;height:100%;
  display:flex;align-items:center;justify-content:center;
  flex-direction:column;gap:${BOOT_SPLASH_GAP};font-family:system-ui,sans-serif;
  line-height:${BOOT_SPLASH_LINE_HEIGHT};
  background:${lightBg};
  color:${lightFg};
  animation:boot-splash-enter ${BOOT_SPLASH_ENTER_MS}ms ease-out both;
}
/*
  Entrance is for cold first paint only. Once the phase attribute has moved
  past initializing, a Solid splash that replaces the static node (or that
  AppShell overlays after AuthGuard) must stay opaque — a second fade would
  blink the logo the user is already reading.
*/
html[${BOOT_SPLASH_PHASE_ATTRIBUTE}]:not([${BOOT_SPLASH_PHASE_ATTRIBUTE}="initializing"]) [data-testid="${testId}"]:not(#${id}){animation:none}
@keyframes boot-splash-enter{from{opacity:0}to{opacity:1}}
#${id}{min-height:100%}
[data-testid="${testId}"]:not(#${id}){min-height:100dvh}
@media (prefers-color-scheme: dark){
  #${id},[data-testid="${testId}"]{
    --boot-splash-lo:${darkLo};
    --boot-splash-hi:${darkHi};
    background:${darkBg};
    color:${darkFg};
  }
}
html[data-theme="light"] #${id},html[data-theme="light"] [data-testid="${testId}"]{
  --boot-splash-lo:${lightLo};
  --boot-splash-hi:${lightHi};
  background:${lightBg};
  color:${lightFg};
}
html[data-theme="dark"] #${id},html[data-theme="dark"] [data-testid="${testId}"]{
  --boot-splash-lo:${darkLo};
  --boot-splash-hi:${darkHi};
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
#${id} .boot-splash-error pre{margin:0 auto;padding:0;background:none;border-radius:0;font-family:inherit;width:max-content;max-width:min(100%,20rem);overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;font-size:.85rem;text-align:left}
#${id} .boot-splash-error button{
  font:inherit;cursor:pointer;padding:.5rem 1rem;border-radius:.375rem;
  border:1px solid currentColor;background:transparent;color:inherit;
}
/*
  Label shimmer. The sweep paints through background-clip, so the text node
  never changes and assistive tech reads the plain label. Guarded: without
  clipping, or without color-mix for the stops, the declarations inside would
  leave a transparent label, so engines lacking either keep the solid colour.
  The gradient is sized to three element-widths and translated by exactly one
  period per cycle, so one highlight passes per loop with no visible seam —
  both gradient ends are the base colour.
*/
@supports ((-webkit-background-clip: text) or (background-clip: text)) and (color: color-mix(in srgb, red, blue)){
  #${id} .boot-splash-label,[data-testid="${testId}"] .boot-splash-label{
    background:linear-gradient(100deg,var(--boot-splash-lo) 30%,var(--boot-splash-hi) 50%,var(--boot-splash-lo) 70%);
    background-size:300% 100%;
    -webkit-background-clip:text;
    background-clip:text;
    color:transparent;
    -webkit-text-fill-color:transparent;
    animation:boot-splash-shimmer 1.8s linear infinite;
  }
}
@keyframes boot-splash-shimmer{from{background-position:150% 0}to{background-position:0 0}}
/*
  Progress checklist. Every row is rendered in both trees from first paint and
  rows change state by opacity alone, so the block's height — and with it the
  logo and label positions — never moves while a phase advances. Oat's ul and
  li element rules (padding-inline-start, margin-block-end, disc markers) are
  neutralized here rather than restated, per the doctrine above.
*/
#${id} .boot-splash-progress,[data-testid="${testId}"] .boot-splash-progress{
  margin:0;padding:0;list-style:none;
  display:flex;flex-direction:column;gap:.375rem;
  font-size:.8rem;line-height:1.25;
  transition:opacity 150ms;
}
#${id} .boot-splash-progress-row,[data-testid="${testId}"] .boot-splash-progress-row{
  display:grid;grid-template-columns:1fr auto 1fr;align-items:center;
  width:14rem;max-width:100%;margin:0;padding:0;list-style:none;font:inherit;
}
/*
  The middle column centers each label; the flanking 1fr columns give the
  status slot a fixed start however wide its contents are, so a check
  appearing can never shift the text. Check and dots share one grid cell.
*/
.boot-splash-progress-label{grid-column:2;grid-row:1;text-align:center;white-space:nowrap;opacity:.45;transition:opacity 150ms}
.boot-splash-progress-status{grid-column:3;grid-row:1;justify-self:start;margin-left:.5rem;width:1rem;display:grid;place-items:center}
.boot-splash-progress-status>*{grid-area:1/1}
/* Brand teal, same literal as the BootSplashIcon background rect. */
.boot-splash-progress-check{display:block;width:.9em;height:.9em;opacity:0;transition:opacity 150ms;color:#0D9488}
.boot-splash-progress-dots{display:inline-flex;gap:.25rem;opacity:0;transition:opacity 150ms}
.boot-splash-progress-dot{width:.1875rem;height:.1875rem;border-radius:9999px;background:currentColor;animation:boot-splash-dot 1.2s ease-in-out infinite}
.boot-splash-progress-dot:nth-child(2){animation-delay:.15s}
.boot-splash-progress-dot:nth-child(3){animation-delay:.3s}
@keyframes boot-splash-dot{0%,100%{opacity:.2}50%{opacity:1}}
${bootProgressPhaseCss()}
@media (prefers-reduced-motion: reduce){
  #${id},[data-testid="${testId}"],#${id} .boot-splash-label,[data-testid="${testId}"] .boot-splash-label,.boot-splash-progress-dot{animation:none}
  .boot-splash-progress,.boot-splash-progress-label,.boot-splash-progress-check,.boot-splash-progress-dots{transition:none}
}
`.trim()
}

/**
 * Blocking head script: state the OS polarity on `<html>` before first paint.
 *
 * IT READS NO STORAGE. Every stored preference is scoped to an account, and
 * this script is inlined into static HTML that runs before any module, before
 * any request, and therefore before any identity — there is no account whose
 * theme it could legitimately read. So the pre-auth answer is the OS answer,
 * which is also what `themeStore` paints until preferences resolve.
 *
 * It writes only `data-theme`, and never an inline style. An inline declaration
 * outranks every author rule, so an inline `color-scheme` here could not be
 * overridden by the palette rule that carries the app's own polarity: a dark app
 * under a light OS kept `color-scheme: light`, and `light-dark()` resolved to
 * the light branch inside a dark palette. `bootSplashDocumentCss` states the
 * same polarity as a RULE instead, which the palette can outrank.
 *
 * The `theme-color` metas need no pin branch any more. `entry-server` emits one
 * per polarity behind a `media` query, so the OS picks between them without
 * help; only the non-media fallback, for engines that ignore `media` here, is
 * rewritten to the current answer.
 */
export function bootThemeScript(): string {
  const lightBg = bootSplashLight.background
  const darkBg = bootSplashDark.background
  return `(function(){try{var dark=window.matchMedia("(prefers-color-scheme: dark)").matches;document.documentElement.setAttribute("data-theme",dark?"dark":"light");var fallback=document.querySelector('meta[name="theme-color"]:not([media])');if(fallback)fallback.setAttribute("content",dark?${JSON.stringify(darkBg)}:${JSON.stringify(lightBg)});}catch(e){}})();`
}

/**
 * Inline document script: advance the splash checklist to "Loading bundles"
 * the instant the static splash markup is parsed — "Initializing" is already
 * true by the time this runs, so the checklist starts honest. The entry graph
 * takes over from here; `entry-client` is the next advance.
 *
 * Like the other boot scripts: inline, no modules, reads nothing, and wrapped
 * in try/catch so a broken DOM never breaks the document.
 */
export function bootPhaseScript(): string {
  const attr = BOOT_SPLASH_PHASE_ATTRIBUTE
  const phase = BOOT_SPLASH_PHASES[1].key
  return `(function(){try{document.documentElement.setAttribute(${JSON.stringify(attr)},${JSON.stringify(phase)});}catch(e){}})();`
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
 * Success signal: the static node `#boot-splash` is gone. The client entry
 * removes it right after `mount()` (see {@link removeStaticBootSplash}). A
 * MutationObserver finishes the watchdog then (clears the timer and removes
 * the capture listener). The Suspense/AuthGuard `BootSplash` keeps only
 * `data-testid`, so a long auth bootstrap does not look like a failed boot.
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
