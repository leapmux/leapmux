import type { ResolvedThemeMode, TerminalThemeValue, ThemeDefinition, ThemeMode, ThemeValue, ThemeVariant, ThemeVariantChoice } from '~/styles/themes'
import { createEffect, createRoot, createSignal, on, onCleanup } from 'solid-js'
import { DEFAULT_THEME_ID, isThemeId, MATCH_UI, resolveVariant, themeById, variantById } from '~/styles/themes'
import { KEY_BROWSER_PREFS, loadBrowserPrefs, updateBrowserPref } from './browserStorage'

/**
 * The live theme: which palette the app paints, and the DOM that carries it.
 *
 * ONE OWNER, WITH OR WITHOUT A PROVIDER. `PreferencesContext` resolves the
 * device tier over the account tier and pushes the answer here through
 * `applyTheme`; the desktop launcher, which renders outside every provider and
 * has no hub connection to hold an account tier, writes the device tier
 * directly through `writeDeviceTheme`. Both land on the same signals and the
 * same `leapmux:browser-prefs` field, so a theme picked on the launcher is
 * already the resolved value once the providers mount.
 *
 * This module used to be an effect in app.tsx plus a `window.__leapmux_setTheme`
 * global. The global existed only to reach across the provider boundary, and a
 * module-level store crosses it without one.
 */

export const DEFAULT_THEME_VALUE: ThemeValue = { name: DEFAULT_THEME_ID, mode: 'system' }

const MODES: readonly ThemeMode[] = ['system', 'light', 'dark']

export function isThemeMode(value: unknown): value is ThemeMode {
  return typeof value === 'string' && (MODES as readonly string[]).includes(value)
}

/**
 * Read a stored `{ name, mode }` document, degrading each half on its own.
 *
 * Returns `undefined` for a non-document, which is how a TIER says "nothing
 * stored here". `PreferencesContext` needs that distinction: a device tier that
 * parsed an absent field into a full default would read as an override pinning
 * the theme, and the account value could never win again.
 *
 * Within a document the degrade is per FIELD, not per document: a theme name
 * from a newer build must not also discard the perfectly valid mode beside it,
 * or a user who downgrades loses their dark mode along with their palette.
 */
export function parseThemeValue(raw: unknown): ThemeValue | undefined {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw))
    return undefined
  const { name, mode, variant } = raw as { name?: unknown, mode?: unknown, variant?: unknown }
  const themeName = typeof name === 'string' && isThemeId(name) ? name : DEFAULT_THEME_ID
  return {
    name: themeName,
    mode: isThemeMode(mode) ? mode : 'system',
    ...withVariant(themeName, variant),
  }
}

/**
 * Keep the halves of a stored `variant` that the named theme can honour, and
 * drop the rest.
 *
 * A half is dropped when it names no variant this build carries, a variant of
 * ANOTHER theme, or a variant of the other polarity. The last is the one worth
 * spelling out: `{ dark: 'catppuccin-latte' }` names a real variant that cannot
 * answer for dark, and painting a light palette under `data-theme="dark"` is a
 * worse answer than the theme's own default.
 *
 * Dropped rather than repaired, because a variant is the half a user picks
 * LAST: losing it falls back to the palette the theme shipped, while guessing a
 * replacement would paint something nobody chose. Switching theme therefore
 * clears the old choice instead of carrying a stale id.
 *
 * Returns a partial so an absent or fully unusable `variant` leaves the key off
 * the document entirely rather than storing `{}`.
 */
function withVariant(themeName: string, raw: unknown): { variant?: ThemeVariantChoice } {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw))
    return {}
  const theme = themeById(themeName)
  const { light, dark } = raw as { light?: unknown, dark?: unknown }
  const kept: ThemeVariantChoice = {}
  for (const [polarity, id] of [['light', light], ['dark', dark]] as const) {
    if (typeof id !== 'string')
      continue
    const found = variantById(id)
    if (found && found.polarity === polarity && theme.variants.includes(found))
      kept[polarity] = id
  }
  return Object.keys(kept).length > 0 ? { variant: kept } : {}
}

/** The terminal preference's default: both halves follow the UI. */
export const DEFAULT_TERMINAL_THEME_VALUE: TerminalThemeValue = { name: MATCH_UI, mode: MATCH_UI }

/**
 * Read a stored terminal or syntax `{ name, mode }` document, and REPAIR one
 * that breaks the invariant `TerminalThemeValue` states.
 *
 * A non-document returns `undefined`, exactly as `parseThemeValue` does, and
 * for the same reason: that is how a TIER says "nothing stored here".
 *
 * A document answers in one of two shapes, and the NAME decides which:
 *
 *   - `match-ui`, or a palette this build cannot resolve, means the whole
 *     choice follows the UI, so both halves take the sentinel. "Follow the app"
 *     is the safer answer for a name we cannot resolve: it leaves the terminal
 *     agreeing with what the user can see, instead of pinning it to a palette
 *     they never chose.
 *   - a resolvable palette keeps its own mode. A `match-ui` mode beside it
 *     degrades to `system` rather than dragging the palette back to the app,
 *     because the name is the half the user picked deliberately.
 */
export function parseTerminalThemeValue(raw: unknown): TerminalThemeValue | undefined {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw))
    return undefined
  const { name, mode, variant } = raw as { name?: unknown, mode?: unknown, variant?: unknown }
  // `isThemeId` rejects the sentinel too, so this one test covers "follow the
  // app" and "a palette this build does not carry" together.
  if (typeof name !== 'string' || !isThemeId(name))
    return DEFAULT_TERMINAL_THEME_VALUE
  return {
    name,
    mode: isThemeMode(mode) ? mode : 'system',
    // Same repair the UI theme gets. A row following the app carries no variant
    // of its own -- it reads the app's -- so the sentinel branch above returns
    // before this and never stores one.
    ...withVariant(name, variant),
  }
}

const DARK_QUERY = '(prefers-color-scheme: dark)'

/**
 * The dark-scheme media query, or `undefined` where there is none.
 *
 * `matchMedia` is checked as a FUNCTION, not merely assumed present because
 * `window` is. This module builds its store at import time, so a host without
 * `matchMedia` would throw before any component renders and take the whole app
 * down rather than degrading to the light palette -- and every module that
 * transitively imports this one would fail to load with it. Absent means "the
 * host cannot tell us", which resolves the same way an OS set to light does.
 */
function darkQuery(): MediaQueryList | undefined {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function')
    return undefined
  return window.matchMedia(DARK_QUERY)
}

function prefersDark(): boolean {
  return darkQuery()?.matches ?? false
}

function storedTheme(): ThemeValue {
  return parseThemeValue(loadBrowserPrefs().theme) ?? DEFAULT_THEME_VALUE
}

function createThemeStore() {
  const [theme, setTheme] = createSignal<ThemeValue>(storedTheme())
  const [systemDark, setSystemDark] = createSignal(prefersDark())

  const resolvedMode = (): ResolvedThemeMode => {
    const mode = theme().mode
    if (mode === 'light' || mode === 'dark')
      return mode
    return systemDark() ? 'dark' : 'light'
  }

  const resolvedTheme = (): ThemeDefinition => themeById(theme().name)

  /**
   * The variant actually painted: the theme's, for the polarity now showing.
   *
   * Every surface reads one field of this, so the palette, the terminal set and
   * the syntax theme cannot disagree about which look is live.
   */
  const resolvedVariant = (): ThemeVariant =>
    resolveVariant(resolvedTheme(), theme().variant?.[resolvedMode()], resolvedMode())

  // Subscribe for the app's whole life, whatever the UI mode is. `systemMode`
  // is read by preferences the UI theme does not answer for -- a syntax theme
  // pinned to `system` while the app is pinned to dark -- so a subscription
  // that detached whenever the UI stopped following the OS would leave those
  // readers on the scheme the OS had when the user pinned the app.
  //
  // `on` with an explicit dependency keeps the effect from re-subscribing every
  // time `systemDark` itself changes, which would tear down the listener that
  // just fired. Re-running on the MODE is what lets a host install `matchMedia`
  // after this module is imported, which is how the tests drive it.
  createEffect(on(() => theme().mode, () => {
    // Re-read BEFORE the subscription, and unconditionally. A host with no
    // media query still has to answer, and answering with whatever the signal
    // last held would report a scheme nothing can currently observe.
    setSystemDark(prefersDark())
    const query = darkQuery()
    if (!query)
      return
    const onChange = (event: MediaQueryListEvent) => setSystemDark(event.matches)
    query.addEventListener('change', onChange)
    onCleanup(() => query.removeEventListener('change', onChange))
  }))

  // The DOM write. All four attributes always, never "the attribute is absent
  // means light" — global.css.ts pairs a light and a dark selector per VARIANT
  // and needs the positive statement to theme a subtree.
  //
  // `data-ui-light` and `data-ui-dark` are both written whichever polarity is
  // showing, because the rule that paints a subtree carrying the OPPOSITE
  // `data-theme` keys off the attribute for THAT polarity. Writing only the
  // showing one would leave a light terminal inside a dark app unpainted.
  createEffect(() => {
    if (typeof document === 'undefined')
      return
    const mode = resolvedMode()
    const definition = resolvedTheme()
    const root = document.documentElement
    const pinned = theme().variant
    // `data-ui-theme` names the resolved THEME, and no stylesheet reads it:
    // every palette rule keys on `data-ui-light` / `data-ui-dark` below, because
    // a rule has to name a variant rather than a theme. It stays as the one
    // place the theme's own identity is observable from outside the app -- the
    // E2E suites assert on it, and it is what a reader inspecting <html> needs
    // in order to tell WHICH theme resolved when two of its variants look alike.
    root.setAttribute('data-ui-theme', definition.id)
    root.setAttribute('data-theme', mode)
    root.setAttribute('data-ui-light', resolveVariant(definition, pinned?.light, 'light').id)
    root.setAttribute('data-ui-dark', resolveVariant(definition, pinned?.dark, 'dark').id)

    // The PWA / mobile-browser chrome colour, taken from the palette rather
    // than restated. Three literals used to disagree here: this effect said
    // #ffffff, entry-server.tsx and manifest.webmanifest said #F7F5F2, and the
    // actual light --background was rgb(255 254 252).
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta)
      meta.setAttribute('content', resolvedVariant().palette['--background'] ?? '')
  })

  // Follow theme changes made in other tabs.
  //
  // The event is only used to learn WHICH key changed; the value is read back
  // through `loadBrowserPrefs`. Parsing `e.newValue` directly -- which this did
  // -- reads the raw `{ v, e }` TTL envelope that `localStorageSet` writes, so
  // `JSON.parse(e.newValue).theme` was always `undefined`, the comparison
  // against an equally-undefined old value was never true, and cross-tab theme
  // sync had silently done nothing: a second tab kept the old theme until it
  // was reloaded.
  //
  // Only a DEVICE override is followed. `leapmux:browser-prefs` is one
  // consolidated document holding every device preference, so this event fires
  // for a diff view or an Enter-key mode written in another tab -- writes that
  // say nothing about the theme. An account-tier theme leaves the `theme` field
  // ABSENT from that document, so answering with `storedTheme()`'s
  // `DEFAULT_THEME_VALUE` fallback would repaint an account palette to Default
  // on every unrelated device write, and nothing would restore it:
  // `PreferencesApplier` re-runs on `preferences.theme()`, which no storage
  // event touches. The device tier is the only tier this document can speak
  // for, so an absent field means "no opinion", not "the default".
  if (typeof window !== 'undefined') {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== KEY_BROWSER_PREFS)
        return
      const deviceTheme = parseThemeValue(loadBrowserPrefs().theme)
      if (deviceTheme)
        setTheme(deviceTheme)
    }
    window.addEventListener('storage', onStorage)
    onCleanup(() => window.removeEventListener('storage', onStorage))
  }

  return {
    /** The preference as chosen: `mode` may still be `'system'`. */
    theme,
    /** `'light'` or `'dark'`, after `'system'` is answered by the OS. */
    resolvedMode,
    /**
     * What the OS itself asks for, whatever the app is pinned to.
     *
     * The terminal and syntax preferences each carry their own mode, and
     * `system` there means the OS -- the same thing it means in the Theme row.
     * Answering them with `resolvedMode` instead would make `system` mean "the
     * app's mode" in two rows out of three.
     */
    systemMode: (): ResolvedThemeMode => (systemDark() ? 'dark' : 'light'),
    /** The palette currently painted. Falls back to Default for an unknown name. */
    resolvedTheme,
    /** The variant currently painted: the theme's, for the polarity showing. */
    resolvedVariant,

    /**
     * Paint `value`. Applies only -- it stores nothing.
     *
     * This is the seam `PreferencesApplier` drives: PreferencesContext owns
     * where the value came from (device tier over account tier) and what
     * writing it means, and this module owns what painting it means.
     */
    applyTheme(value: ThemeValue): void {
      setTheme(parseThemeValue(value) ?? DEFAULT_THEME_VALUE)
    },

    /**
     * Write the DEVICE tier of the `theme` preference and paint the result.
     *
     * For the one surface that has no `PreferencesProvider` above it: the
     * desktop launcher. Everywhere else goes through the provider, so that the
     * dialog's scope chip and Reset keep describing the write that happened.
     * Calling this under a provider would leave that provider's own device-tier
     * signal stale.
     *
     * IT REPLACES, and takes a whole value to say so. It used to take a
     * `Partial` and merge it over the current theme, which made the two halves
     * of `useThemeChooser` disagree: the provider branch writes through
     * `dualScalar`, which replaces, so the same `onChange` meant one thing with
     * a provider mounted and another without. Nothing needed the merge -- the
     * one caller always passed a whole `{ name, mode }` -- and a merge cannot
     * express "clear the variant", because an absent key reads as "keep".
     */
    writeDeviceTheme(value: ThemeValue): void {
      const next = parseThemeValue(value) ?? DEFAULT_THEME_VALUE
      updateBrowserPref('theme', next)
      setTheme(next)
    },
  }
}

/**
 * Created in a root of its own: these effects outlive any component, and
 * without an owner Solid discards their `onCleanup` and leaks the `matchMedia`
 * and `storage` listeners on every hot reload.
 */
export const themeStore = createRoot(createThemeStore)

export const { applyTheme, writeDeviceTheme } = themeStore
