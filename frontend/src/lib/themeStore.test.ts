import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, loadBrowserPrefs, localStorageClearForTests, localStorageSet } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_TERMINAL_THEME_VALUE, DEFAULT_THEME_VALUE, isThemeMode, parseTerminalThemeValue, parseThemeValue, themeStore } from '~/lib/themeStore'
import { resolveVariant, themeById } from '~/styles/themes'

/**
 * Install a controllable `prefers-color-scheme: dark` and return a setter that
 * flips it and dispatches the `change` event the store subscribes to.
 *
 * jsdom implements no `matchMedia` at all, which is also why the store guards
 * for its absence rather than assuming `window` implies it.
 */
function stubMatchMedia(initialDark: boolean) {
  let dark = initialDark
  const listeners = new Set<(e: MediaQueryListEvent) => void>()
  const query = {
    get matches() {
      return dark
    },
    media: '(prefers-color-scheme: dark)',
    addEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) => void listeners.add(fn),
    removeEventListener: (_: string, fn: (e: MediaQueryListEvent) => void) => void listeners.delete(fn),
  }
  const matchMedia = vi.fn(() => query as unknown as MediaQueryList)
  Object.defineProperty(window, 'matchMedia', { value: matchMedia, configurable: true, writable: true })
  return {
    query,
    listenerCount: () => listeners.size,
    set(next: boolean) {
      dark = next
      for (const fn of [...listeners])
        fn({ matches: next } as MediaQueryListEvent)
    },
  }
}

function clearMatchMedia() {
  Reflect.deleteProperty(window, 'matchMedia')
}

const root = () => document.documentElement

beforeEach(() => {
  localStorageClearForTests()
  clearMatchMedia()
  root().removeAttribute('data-theme')
  root().removeAttribute('data-ui-theme')
  root().removeAttribute('data-ui-light')
  root().removeAttribute('data-ui-dark')
  document.head.querySelector('meta[name="theme-color"]')?.remove()
  applyTheme(DEFAULT_THEME_VALUE)
})

afterEach(() => {
  localStorageClearForTests()
  clearMatchMedia()
  applyTheme(DEFAULT_THEME_VALUE)
})

describe('parseThemeValue', () => {
  it('reads a well-formed document', () => {
    expect(parseThemeValue({ name: 'nord', mode: 'dark' })).toEqual({ name: 'nord', mode: 'dark' })
  })

  it('reports a non-document as no stored value at all', () => {
    // The distinction a TIER depends on. A device tier that turned an absent
    // field into a full default would read as an override pinning the theme,
    // and the account value could never win again.
    for (const raw of [undefined, null, 'dark', 42, true, ['nord']])
      expect(parseThemeValue(raw), JSON.stringify(raw)).toBeUndefined()
  })

  it('degrades each half on its own', () => {
    // A palette from a newer build must not also discard the valid mode beside
    // it, or a downgrade costs the user their dark mode as well as the palette.
    expect(parseThemeValue({ name: 'from-the-future', mode: 'dark' }))
      .toEqual({ name: 'default', mode: 'dark' })
    expect(parseThemeValue({ name: 'nord', mode: 'sepia' }))
      .toEqual({ name: 'nord', mode: 'system' })
    expect(parseThemeValue({})).toEqual(DEFAULT_THEME_VALUE)
  })

  it('refuses a name that differs only in case or spacing', () => {
    // The stored spelling must equal a catalogued id exactly; a near-miss is a
    // palette this build does not carry.
    for (const name of ['Nord', 'nord ', ' nord', 'rose_pine'])
      expect(parseThemeValue({ name, mode: 'dark' })!.name, name).toBe('default')
  })
})

describe('parseTerminalThemeValue', () => {
  it('reads a well-formed pinned document', () => {
    expect(parseTerminalThemeValue({ name: 'nord', mode: 'dark' })).toEqual({ name: 'nord', mode: 'dark' })
  })

  it('reports a non-document as no stored value at all', () => {
    // The same TIER distinction parseThemeValue draws, and for the same reason.
    for (const raw of [undefined, null, 'match-ui', 42, true, ['nord']])
      expect(parseTerminalThemeValue(raw), JSON.stringify(raw)).toBeUndefined()
  })

  it('holds the two halves to one decision', () => {
    // The invariant TerminalThemeValue states. A mixed document is not a third
    // setting -- it is one nothing produces -- so parsing repairs it, and the
    // NAME decides which way, because it is the half a user picks deliberately.
    expect(parseTerminalThemeValue({ name: 'ayu', mode: 'match-ui' }))
      .toEqual({ name: 'ayu', mode: 'system' })
    expect(parseTerminalThemeValue({ name: 'match-ui', mode: 'dark' }))
      .toEqual(DEFAULT_TERMINAL_THEME_VALUE)
  })

  it('degrades an unresolvable palette to following the app', () => {
    // "Follow the app" leaves the terminal agreeing with what the user can
    // see; pinning it to Default would be a palette they never chose.
    for (const name of ['from-the-future', 'Nord', 'nord ', 'rose_pine', '', 7]) {
      expect(parseTerminalThemeValue({ name, mode: 'dark' }), String(name))
        .toEqual(DEFAULT_TERMINAL_THEME_VALUE)
    }
  })

  it('degrades an unreadable mode beside a real palette, keeping the palette', () => {
    expect(parseTerminalThemeValue({ name: 'gruvbox', mode: 'sepia' }))
      .toEqual({ name: 'gruvbox', mode: 'system' })
    expect(parseTerminalThemeValue({ name: 'gruvbox' }))
      .toEqual({ name: 'gruvbox', mode: 'system' })
  })
})

describe('isThemeMode', () => {
  it('accepts the three modes and nothing else', () => {
    for (const mode of ['system', 'light', 'dark'])
      expect(isThemeMode(mode)).toBe(true)
    for (const mode of ['', 'System', 'sepia', null, undefined, 0])
      expect(isThemeMode(mode)).toBe(false)
  })
})

describe('themeStore resolution', () => {
  it('resolves an explicit mode without consulting the OS', () => {
    const media = stubMatchMedia(true)
    applyTheme({ name: 'default', mode: 'light' })
    expect(themeStore.resolvedMode()).toBe('light')

    applyTheme({ name: 'default', mode: 'dark' })
    media.set(false)
    expect(themeStore.resolvedMode()).toBe('dark')
  })

  it('follows the OS while the mode is system, and stops when it is not', () => {
    const media = stubMatchMedia(false)
    applyTheme({ name: 'default', mode: 'system' })
    expect(themeStore.resolvedMode()).toBe('light')

    media.set(true)
    expect(themeStore.resolvedMode()).toBe('dark')

    // Pinning the mode must detach the subscription, or a later OS change
    // would repaint a user who explicitly asked not to follow it.
    applyTheme({ name: 'default', mode: 'light' })
    expect(themeStore.resolvedMode()).toBe('light')
    media.set(false)
    expect(themeStore.resolvedMode()).toBe('light')
  })

  it('reports the OS through systemMode while the app is pinned away from it', () => {
    // The terminal and syntax preferences carry their own `system`, which means
    // the OS -- so this reader has to stay live after the UI stops following
    // it. A subscription that detached with the UI would freeze this answer at
    // whatever the OS said when the user pinned the app.
    const media = stubMatchMedia(false)
    applyTheme({ name: 'default', mode: 'dark' })
    expect(themeStore.systemMode()).toBe('light')

    media.set(true)
    expect(themeStore.systemMode()).toBe('dark')
    // And it stays the OS's answer, not the app's.
    expect(themeStore.resolvedMode()).toBe('dark')

    applyTheme({ name: 'default', mode: 'light' })
    expect(themeStore.systemMode()).toBe('dark')
    expect(themeStore.resolvedMode()).toBe('light')
  })

  it('resolves to light where the host implements no matchMedia', () => {
    // The store is built at import time, so throwing here would take down every
    // module that transitively imports it rather than degrading the palette.
    clearMatchMedia()
    applyTheme({ name: 'default', mode: 'system' })
    expect(themeStore.resolvedMode()).toBe('light')
    // `systemMode` answers on the same host and must not report a scheme
    // nothing can observe. Absent means "the host cannot tell us", which
    // resolves the way an OS set to light does.
    expect(themeStore.systemMode()).toBe('light')
  })

  it('falls back to the default palette for a name this build does not carry', () => {
    applyTheme({ name: 'from-the-future', mode: 'dark' })
    expect(themeStore.resolvedTheme().id).toBe('default')
  })

  it('paints the palette the name selects', () => {
    applyTheme({ name: 'nord', mode: 'dark' })
    expect(themeStore.resolvedTheme()).toBe(themeById('nord'))
  })
})

describe('themeStore DOM application', () => {
  it('states BOTH attributes positively, including light', () => {
    // Light used to be "the attribute is absent". global.css.ts now pairs a
    // light and a dark selector per theme and needs the positive statement, or
    // a subtree cannot carry the opposite variant.
    stubMatchMedia(false)
    applyTheme({ name: 'catppuccin', mode: 'light' })
    expect(root().getAttribute('data-ui-theme')).toBe('catppuccin')
    expect(root().getAttribute('data-theme')).toBe('light')

    applyTheme({ name: 'gruvbox', mode: 'dark' })
    expect(root().getAttribute('data-ui-theme')).toBe('gruvbox')
    expect(root().getAttribute('data-theme')).toBe('dark')
  })

  it('writes data-ui-theme as the RESOLVED palette, never the unknown name', () => {
    // The attribute drives the CSS selector, so an unresolvable name here would
    // match no rule at all and leave the app on :root's fallback palette.
    applyTheme({ name: 'from-the-future', mode: 'dark' })
    expect(root().getAttribute('data-ui-theme')).toBe('default')
  })

  it('takes the PWA chrome colour from the palette rather than a literal', () => {
    const meta = document.createElement('meta')
    meta.setAttribute('name', 'theme-color')
    document.head.append(meta)

    applyTheme({ name: 'nord', mode: 'dark' })
    expect(meta.getAttribute('content')).toBe(resolveVariant(themeById('nord'), undefined, 'dark').palette['--background'])

    applyTheme({ name: 'nord', mode: 'light' })
    expect(meta.getAttribute('content')).toBe(resolveVariant(themeById('nord'), undefined, 'light').palette['--background'])
  })

  it('does not fail when the page carries no theme-color meta tag', () => {
    expect(() => applyTheme({ name: 'ayu', mode: 'dark' })).not.toThrow()
    expect(root().getAttribute('data-ui-theme')).toBe('ayu')
  })
})

describe('themeStore.writeDeviceTheme', () => {
  it('persists the whole value to the shared browser-prefs document', () => {
    themeStore.writeDeviceTheme({ name: 'solarized', mode: 'dark' })
    expect(loadBrowserPrefs().theme).toEqual({ name: 'solarized', mode: 'dark' })
    expect(themeStore.theme()).toEqual({ name: 'solarized', mode: 'dark' })
  })

  // IT REPLACES, and the caller carries the half it did not touch. The merge
  // this used to do made the two halves of `useThemeChooser` disagree: the
  // provider branch writes through `dualScalar`, which replaces, so the same
  // `onChange` meant one thing with a provider mounted and another without.
  // `ThemeChooser.commit` already spreads the current value before it calls,
  // which is why nothing needed the merge.
  it('replaces the stored value rather than merging over it', () => {
    themeStore.writeDeviceTheme({ name: 'everforest', mode: 'light' })
    themeStore.writeDeviceTheme({ name: 'everforest', mode: 'dark' })
    expect(loadBrowserPrefs().theme).toEqual({ name: 'everforest', mode: 'dark' })

    // A whole value from a DIFFERENT palette carries both halves, so nothing
    // of the previous one survives.
    themeStore.writeDeviceTheme({ name: 'nord', mode: 'light' })
    expect(loadBrowserPrefs().theme).toEqual({ name: 'nord', mode: 'light' })
    expect(themeStore.theme()).toEqual({ name: 'nord', mode: 'light' })
  })

  // A variant pin is a field of the same document, so a replace must be able to
  // CLEAR it. A merge could not: an absent key reads as "keep".
  it('clears a variant pin the previous value carried', () => {
    themeStore.writeDeviceTheme({ name: 'catppuccin', mode: 'dark', variant: { dark: 'catppuccin-macchiato' } })
    expect(loadBrowserPrefs().theme).toMatchObject({ variant: { dark: 'catppuccin-macchiato' } })

    themeStore.writeDeviceTheme({ name: 'catppuccin', mode: 'dark' })
    expect(loadBrowserPrefs().theme).toEqual({ name: 'catppuccin', mode: 'dark' })
  })

  it('writes the same key any other device-tier write uses', () => {
    // Not a launcher-specific key: the Preferences dialog reads this exact
    // field, so a theme picked before connecting is the one it reports after.
    themeStore.writeDeviceTheme({ name: 'one', mode: 'light' })
    const stored = loadBrowserPrefs()
    expect(Object.keys(stored)).toEqual(['theme'])
    expect(stored.theme).toEqual({ name: 'one', mode: 'light' })
  })

  it('stores the default palette for a name it cannot resolve', () => {
    themeStore.writeDeviceTheme({ name: 'not-a-theme', mode: 'dark' })
    expect(loadBrowserPrefs().theme).toEqual({ name: 'default', mode: 'dark' })
  })
})

describe('themeStore cross-tab sync', () => {
  it('re-reads the stored value when another tab rewrites the prefs document', () => {
    localStorageSet(KEY_BROWSER_PREFS, { theme: { name: 'tokyo-night', mode: 'dark' } })
    window.dispatchEvent(new StorageEvent('storage', { key: KEY_BROWSER_PREFS }))
    expect(themeStore.theme()).toEqual({ name: 'tokyo-night', mode: 'dark' })
  })

  it('ignores a storage event for an unrelated key', () => {
    applyTheme({ name: 'nord', mode: 'dark' })
    localStorageSet(KEY_BROWSER_PREFS, { theme: { name: 'ayu', mode: 'light' } })
    window.dispatchEvent(new StorageEvent('storage', { key: 'leapmux:something-else' }))
    expect(themeStore.theme()).toEqual({ name: 'nord', mode: 'dark' })
  })

  it('leaves an ACCOUNT-tier theme alone when another tab writes an unrelated preference', () => {
    // `leapmux:browser-prefs` is ONE document holding every device preference,
    // so this event fires for a diff view or an Enter-key mode written in
    // another tab. A user whose theme lives at account scope has no `theme`
    // field in that document, and answering with the default repainted them
    // from their palette to Default on any unrelated write -- with nothing to
    // restore it, because PreferencesApplier re-runs on `preferences.theme()`,
    // which no storage event touches.
    applyTheme({ name: 'nord', mode: 'dark' })
    localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })
    window.dispatchEvent(new StorageEvent('storage', { key: KEY_BROWSER_PREFS }))
    expect(themeStore.theme()).toEqual({ name: 'nord', mode: 'dark' })
  })

  it('still follows a DEVICE override written by another tab', () => {
    // The repair must not cost the sync it exists for: a device-tier theme
    // written elsewhere still arrives.
    applyTheme({ name: 'nord', mode: 'dark' })
    localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split', theme: { name: 'ayu', mode: 'light' } })
    window.dispatchEvent(new StorageEvent('storage', { key: KEY_BROWSER_PREFS }))
    expect(themeStore.theme()).toEqual({ name: 'ayu', mode: 'light' })
  })

  it('reads the value back through the store rather than off the event', () => {
    // `newValue` carries the raw `{ v, e }` TTL envelope that localStorageSet
    // writes, so parsing it directly reads `undefined` for every field. That
    // bug made cross-tab theme sync silently do nothing.
    localStorageSet(KEY_BROWSER_PREFS, { theme: { name: 'github', mode: 'light' } })
    window.dispatchEvent(new StorageEvent('storage', {
      key: KEY_BROWSER_PREFS,
      newValue: JSON.stringify({ theme: { name: 'nord', mode: 'dark' } }),
    }))
    expect(themeStore.theme()).toEqual({ name: 'github', mode: 'light' })
  })
})

describe('themeStore variant attributes', () => {
  // `data-ui-light` and `data-ui-dark` are the ONLY attributes the palette rules
  // key on -- `global.css.ts` and `SpanLines.css.ts` both select on them, and
  // `data-ui-theme` is read by no stylesheet at all. Nothing asserted either
  // one: dropping a `setAttribute` left the app painting the theme's DEFAULT
  // variant under both polarities, with every test still green.

  it('writes BOTH polarities, whichever one is showing', () => {
    // The rule that paints a subtree carrying the OPPOSITE `data-theme` keys off
    // the attribute for THAT polarity, so writing only the showing one leaves a
    // light terminal inside a dark app unpainted.
    stubMatchMedia(false)
    applyTheme({ name: 'catppuccin', mode: 'light' })

    const catppuccin = themeById('catppuccin')
    expect(root().getAttribute('data-ui-light'))
      .toBe(resolveVariant(catppuccin, undefined, 'light').id)
    expect(root().getAttribute('data-ui-dark'))
      .toBe(resolveVariant(catppuccin, undefined, 'dark').id)
  })

  it('carries the PINNED variant, which is the whole point of the picker', () => {
    // A stored `variant` choice reaching the DOM is what makes picking Gruvbox
    // "Soft" different from picking Gruvbox. Without this the picker wrote a
    // preference the stylesheet never saw.
    const gruvbox = themeById('gruvbox')
    const light = gruvbox.variants.filter(v => v.polarity === 'light')
    const dark = gruvbox.variants.filter(v => v.polarity === 'dark')
    // Pick a NON-default variant of each side, or the case proves nothing.
    const pinnedLight = light.find(v => v.id !== gruvbox.defaults.light) ?? light[0]!
    const pinnedDark = dark.find(v => v.id !== gruvbox.defaults.dark) ?? dark[0]!

    applyTheme({
      name: 'gruvbox',
      mode: 'dark',
      variant: { light: pinnedLight.id, dark: pinnedDark.id },
    })

    expect(root().getAttribute('data-ui-light')).toBe(pinnedLight.id)
    expect(root().getAttribute('data-ui-dark')).toBe(pinnedDark.id)
    expect(pinnedDark.id, 'the case needs a variant that is not the default').not.toBe(gruvbox.defaults.dark)
  })

  it('falls back to the theme\'s own default for a variant it does not carry', () => {
    // A variant id from another palette, or from a newer build. It must resolve
    // to a variant of THIS theme rather than land on the attribute verbatim,
    // where it would match no rule and leave the app on the fallback palette.
    applyTheme({
      name: 'nord',
      mode: 'dark',
      variant: { light: 'gruvbox-soft-light', dark: 'from-the-future' },
    })

    const nord = themeById('nord')
    expect(root().getAttribute('data-ui-dark')).toBe(nord.defaults.dark)
    expect(root().getAttribute('data-ui-light')).toBe(nord.defaults.light)
  })
})
