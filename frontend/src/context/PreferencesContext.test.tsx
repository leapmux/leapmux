import type { JSX } from 'solid-js'
import { render, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { KEY_BROWSER_PREFS, KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, loadBrowserPrefs, localStorageClearForTests, localStorageGet, localStorageSet } from '~/lib/browserStorage'
import { buildFontFamily } from '~/lib/fontStack'
import { goldenAccountSchema } from '~/test-support/accountSchema'

// Mock the user settings API: ListUserSettings returns no values (every
// account key stays at its default), and the write RPCs succeed with an echo
// of the effective value unless a test overrides them.
const listUserSettings = vi.hoisted(() => vi.fn().mockResolvedValue({ descriptors: [], values: [] }))
const updateUserSetting = vi.hoisted(() => vi.fn().mockResolvedValue({}))
const resetUserSetting = vi.hoisted(() => vi.fn().mockResolvedValue({}))

vi.mock('~/api/clients', () => ({
  userClient: {
    listUserSettings,
    updateUserSetting,
    resetUserSetting,
  },
  authClient: {},
}))

/** Let every already-settled promise chain run to completion. */
function flushMicrotasks(): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, 0))
}

/** A SettingValue-shaped object for seeding listUserSettings responses. */
function settingValue(key: string, effectiveJson: string, customized = false) {
  return { key, effectiveJson, customized, valueJson: effectiveJson, secretSet: {} }
}

type Prefs = ReturnType<typeof usePreferences>

function captureContext(): { get: () => Prefs } {
  let prefs: Prefs | undefined
  function Capture(): JSX.Element {
    prefs = usePreferences()
    return null
  }
  render(() => (
    <PreferencesProvider>
      <Capture />
    </PreferencesProvider>
  ))
  return {
    get: () => {
      if (!prefs)
        throw new Error('Preferences context not yet captured')
      return prefs
    },
  }
}

beforeEach(() => {
  localStorageClearForTests()
  listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
  updateUserSetting.mockResolvedValue({})
  resetUserSetting.mockResolvedValue({})
  // Clear the global theme setter that PreferencesContext.setBrowserTheme calls.
  ;(window as unknown as { __leapmux_setTheme?: (v: string) => void }).__leapmux_setTheme = undefined
})

afterEach(() => {
  localStorageClearForTests()
})

describe('preferencesContext — browser-level theme override', () => {
  it('starts with no browser-level override when localStorage is empty', () => {
    const ctx = captureContext()
    expect(ctx.get().dual.theme.browser()).toBeNull()
    // Theme should resolve to the hardcoded account default.
    expect(ctx.get().theme()).toBe('system')
  })

  it('persists a browser-level dark theme to localStorage', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')

    expect(ctx.get().dual.theme.browser()).toBe('dark')
    expect(ctx.get().theme()).toBe('dark')
    expect(loadBrowserPrefs().theme).toBe('dark')
  })

  it('persists a browser-level light theme to localStorage', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('light')

    expect(ctx.get().dual.theme.browser()).toBe('light')
    expect(loadBrowserPrefs().theme).toBe('light')
  })

  it('clearing the browser theme removes the key from the consolidated prefs blob', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')
    expect(loadBrowserPrefs().theme).toBe('dark')

    ctx.get().dual.theme.setBrowser(null)
    expect(ctx.get().dual.theme.browser()).toBeNull()
    // The serialized blob should not have a `theme` field after clearing.
    expect('theme' in loadBrowserPrefs()).toBe(false)
  })

  it('falls back to account default once the browser override is cleared', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')
    expect(ctx.get().theme()).toBe('dark')

    ctx.get().dual.theme.setBrowser(null)
    expect(ctx.get().theme()).toBe('system')
  })

  it('hydrates the browser theme from localStorage on provider mount (simulated reload)', () => {
    // Pre-seed localStorage with a stored preference and mount fresh.
    localStorageSet(KEY_BROWSER_PREFS, { theme: 'dark' })
    const ctx = captureContext()
    expect(ctx.get().dual.theme.browser()).toBe('dark')
    expect(ctx.get().theme()).toBe('dark')
  })

  it('notifies the global theme setter when set', () => {
    const setter = vi.fn()
    ;(window as unknown as { __leapmux_setTheme: (v: string) => void }).__leapmux_setTheme = setter

    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')
    expect(setter).toHaveBeenCalledWith('dark')

    ctx.get().dual.theme.setBrowser(null)
    // null should fall back to account default ("system" by default).
    expect(setter).toHaveBeenLastCalledWith('system')
  })
})

describe('preferencesContext — browser-level diff view override', () => {
  it('starts with no browser-level override and resolves to the account default', () => {
    const ctx = captureContext()
    expect(ctx.get().dual.diffView.browser()).toBeNull()
    expect(ctx.get().diffView()).toBe('unified')
  })

  it('round-trips browser-level "unified" through localStorage', () => {
    const ctx = captureContext()
    ctx.get().dual.diffView.setBrowser('unified')
    expect(ctx.get().dual.diffView.browser()).toBe('unified')
    expect(loadBrowserPrefs().diffView).toBe('unified')
    expect(ctx.get().diffView()).toBe('unified')
  })

  it('round-trips browser-level "split" through localStorage', () => {
    const ctx = captureContext()
    ctx.get().dual.diffView.setBrowser('split')
    expect(ctx.get().dual.diffView.browser()).toBe('split')
    expect(loadBrowserPrefs().diffView).toBe('split')
    expect(ctx.get().diffView()).toBe('split')
  })

  it('clearing the browser diff view removes the key from the consolidated prefs blob', () => {
    const ctx = captureContext()
    ctx.get().dual.diffView.setBrowser('split')
    expect(loadBrowserPrefs().diffView).toBe('split')

    ctx.get().dual.diffView.setBrowser(null)
    expect(ctx.get().dual.diffView.browser()).toBeNull()
    expect('diffView' in loadBrowserPrefs()).toBe(false)
  })

  it('hydrates the browser diff view from localStorage on provider mount', () => {
    localStorageSet(KEY_BROWSER_PREFS, { diffView: 'split' })
    const ctx = captureContext()
    expect(ctx.get().dual.diffView.browser()).toBe('split')
    expect(ctx.get().diffView()).toBe('split')
  })
})

describe('preferencesContext — multiple prefs in one blob', () => {
  it('writes multiple browser overrides to a single consolidated key', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')
    ctx.get().dual.diffView.setBrowser('split')
    ctx.get().dual.turnEndSound.setBrowser('none')

    const prefs = loadBrowserPrefs()
    expect(prefs.theme).toBe('dark')
    expect(prefs.diffView).toBe('split')
    expect(prefs.turnEndSound).toBe('none')
  })

  it('clearing one pref does not clear the others', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')
    ctx.get().dual.diffView.setBrowser('split')

    ctx.get().dual.diffView.setBrowser(null)
    const prefs = loadBrowserPrefs()
    expect(prefs.theme).toBe('dark')
    expect('diffView' in prefs).toBe(false)
  })
})

describe('preferencesContext — revealAfterDownload (default-on)', () => {
  // The save flow asks the OS to "reveal in Finder/Explorer" after
  // writing. Most users want it; we only persist an explicit `false`
  // when the user opts out — `undefined` is implicit consent.
  it('defaults to true when localStorage is empty', () => {
    const ctx = captureContext()
    expect(ctx.get().revealAfterDownload()).toBe(true)
    // Nothing serialized while no opt-out has happened.
    expect('revealAfterDownload' in loadBrowserPrefs()).toBe(false)
  })

  it('opts out by persisting `false` to the consolidated prefs blob', () => {
    const ctx = captureContext()
    ctx.get().setRevealAfterDownload(false)
    expect(ctx.get().revealAfterDownload()).toBe(false)
    expect(loadBrowserPrefs().revealAfterDownload).toBe(false)
  })

  it('opts back in by clearing the key from the blob (not storing `true`)', () => {
    const ctx = captureContext()
    ctx.get().setRevealAfterDownload(false)
    expect(loadBrowserPrefs().revealAfterDownload).toBe(false)

    ctx.get().setRevealAfterDownload(true)
    expect(ctx.get().revealAfterDownload()).toBe(true)
    // Default-on prefs round-trip the absence of the key, not `true`.
    expect('revealAfterDownload' in loadBrowserPrefs()).toBe(false)
  })

  it('hydrates a stored `false` from localStorage on provider mount', () => {
    localStorageSet(KEY_BROWSER_PREFS, { revealAfterDownload: false })
    const ctx = captureContext()
    expect(ctx.get().revealAfterDownload()).toBe(false)
  })

  it('does not interact with other persisted prefs in the same blob', () => {
    const ctx = captureContext()
    ctx.get().dual.theme.setBrowser('dark')
    ctx.get().setRevealAfterDownload(false)
    expect(loadBrowserPrefs().theme).toBe('dark')
    expect(loadBrowserPrefs().revealAfterDownload).toBe(false)

    // Opting back in must not clobber the theme.
    ctx.get().setRevealAfterDownload(true)
    expect(loadBrowserPrefs().theme).toBe('dark')
    expect('revealAfterDownload' in loadBrowserPrefs()).toBe(false)
  })

  it('round-trips terminalOsNotifications in browser prefs', () => {
    const ctx = captureContext()
    expect(ctx.get().terminalOsNotifications()).toBe(false)
    ctx.get().setTerminalOsNotifications(true)
    expect(loadBrowserPrefs().terminalOsNotifications).toBe(true)
    ctx.get().setTerminalOsNotifications(false)
    expect('terminalOsNotifications' in loadBrowserPrefs()).toBe(false)
  })

  it('showComposerStatusBar defaults to true and round-trips through browser prefs', () => {
    const ctx = captureContext()
    expect(ctx.get().showComposerStatusBar()).toBe(true)

    // Opting out stores `false`; opting back in removes the key.
    ctx.get().setShowComposerStatusBar(false)
    expect(ctx.get().showComposerStatusBar()).toBe(false)
    expect(loadBrowserPrefs().showComposerStatusBar).toBe(false)

    ctx.get().setShowComposerStatusBar(true)
    expect(ctx.get().showComposerStatusBar()).toBe(true)
    expect('showComposerStatusBar' in loadBrowserPrefs()).toBe(false)
  })

  it('hydrates a stored `false` for showComposerStatusBar from localStorage', () => {
    localStorageSet(KEY_BROWSER_PREFS, { showComposerStatusBar: false })
    const ctx = captureContext()
    expect(ctx.get().showComposerStatusBar()).toBe(false)
  })
})

describe('preferencesContext — reload from API', () => {
  it('runs reload() on mount without throwing when the API returns no values', async () => {
    // The default mock returns empty lists. Provider should tolerate that
    // without throwing and signal values should remain at defaults.
    const ctx = captureContext()
    await waitFor(() => {
      expect(ctx.get().theme()).toBe('system')
    })
  })

  it('parses each SettingValue.effectiveJson into per-key account signals', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [
        settingValue('theme', '"dark"'),
        settingValue('terminal_theme', '"light"'),
        settingValue('ui_fonts', '{"enabled":true,"fonts":["Inter"]}'),
        settingValue('mono_fonts', '{"enabled":false,"fonts":[]}'),
        settingValue('diff_view', '"split"'),
        settingValue('turn_end_sound', '"none"'),
        settingValue('turn_end_sound_volume', '42'),
        settingValue('debug_logging', 'true'),
        settingValue('keybindings', '[{"key":"$mod+k","command":"chat.sendMessage"}]', true),
      ],
    })
    const ctx = captureContext()
    await waitFor(() => {
      expect(ctx.get().dual.theme.account()).toBe('dark')
    })
    expect(ctx.get().dual.terminalTheme.account()).toBe('light')
    expect(ctx.get().dual.uiFonts.account()).toEqual({ enabled: true, fonts: ['Inter'] })
    expect(ctx.get().dual.monoFonts.account()).toEqual({ enabled: false, fonts: [] })
    expect(ctx.get().dual.diffView.account()).toBe('split')
    expect(ctx.get().dual.turnEndSound.account()).toBe('none')
    expect(ctx.get().dual.turnEndSoundVolume.account()).toBe(42)
    expect(ctx.get().dual.debugLogging.account()).toBe(true)
    expect(ctx.get().customKeybindings()).toEqual([{ key: '$mod+k', command: 'chat.sendMessage' }])
    expect(ctx.get().accountCustomized().keybindings).toBe(true)
    expect(ctx.get().accountCustomized().theme).toBe(false)
  })

  it('keeps defaults for values whose effectiveJson is empty or malformed', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', ''), settingValue('debug_logging', 'not-json{')],
    })
    const ctx = captureContext()
    await waitFor(() => {
      expect(ctx.get().dual.theme.account()).toBe('system')
    })
    expect(ctx.get().dual.debugLogging.account()).toBe(false)
  })
})

describe('preferencesContext — per-key account writes', () => {
  it('writes the partial as JSON and applies the server effective value on success', async () => {
    updateUserSetting.mockResolvedValue({ value: settingValue('theme', '"dark"', true) })
    const ctx = captureContext()

    await expect(ctx.get().updateUserSetting('theme', 'dark')).resolves.toBeUndefined()
    expect(updateUserSetting).toHaveBeenCalledWith({ key: 'theme', partialJson: '"dark"' })
    await waitFor(() => expect(ctx.get().dual.theme.account()).toBe('dark'))
    await waitFor(() => expect(ctx.get().accountCustomized().theme).toBe(true))
  })

  // The setter REJECTS rather than resolving false: SettingRow catches a
  // rejected `set` and renders the reason under the control, so a refused
  // write no longer reverts with nothing said.
  it('reverts the optimistic value AND rejects when the write is refused', async () => {
    updateUserSetting.mockRejectedValue(new Error('network down'))
    const ctx = captureContext()

    const pending = ctx.get().dual.theme.setAccount('dark')
    // The optimistic value shows immediately, then the rejection reverts it.
    expect(ctx.get().dual.theme.account()).toBe('dark')
    await expect(pending).rejects.toThrow(/network down|Failed to save/)
    expect(ctx.get().dual.theme.account()).toBe('system')
  })

  it('stringifies object partials and whole arrays verbatim', async () => {
    const ctx = captureContext()
    await ctx.get().updateUserSetting('ui_fonts', { enabled: true })
    expect(updateUserSetting).toHaveBeenLastCalledWith({ key: 'ui_fonts', partialJson: '{"enabled":true}' })

    await ctx.get().updateUserSetting('keybindings', [{ key: '$mod+k', command: 'chat.sendMessage' }])
    expect(updateUserSetting).toHaveBeenLastCalledWith({
      key: 'keybindings',
      partialJson: '[{"key":"$mod+k","command":"chat.sendMessage"}]',
    })
  })

  it('setCustomKeybindings writes the whole array through the keybindings key', async () => {
    updateUserSetting.mockResolvedValue({
      value: settingValue('keybindings', '[{"key":"F9","command":"app.newAgent"}]', true),
    })
    const ctx = captureContext()
    ctx.get().setCustomKeybindings([{ key: 'F9', command: 'app.newAgent' }])
    await waitFor(() => {
      expect(ctx.get().customKeybindings()).toEqual([{ key: 'F9', command: 'app.newAgent' }])
    })
    expect(updateUserSetting).toHaveBeenCalledWith({
      key: 'keybindings',
      partialJson: '[{"key":"F9","command":"app.newAgent"}]',
    })
  })

  it('resetUserSetting applies the returned default and rejects without changing the value', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', '"dark"', true)],
    })
    resetUserSetting.mockResolvedValue({ value: settingValue('theme', '"system"', false) })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().dual.theme.account()).toBe('dark'))

    await expect(ctx.get().resetUserSetting('theme')).resolves.toBeUndefined()
    await waitFor(() => expect(ctx.get().dual.theme.account()).toBe('system'))

    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', '"dark"', true)],
    })
    resetUserSetting.mockRejectedValue(new Error('reset refused'))
    const failing = captureContext()
    await waitFor(() => expect(failing.get().dual.theme.account()).toBe('dark'))
    await expect(failing.get().resetUserSetting('theme')).rejects.toThrow(/reset refused|Failed to reset/)
    expect(failing.get().dual.theme.account()).toBe('dark')
  })
})

describe('preferencesContext — font tiers', () => {
  it('resolves fonts from the browser whole-object override before the account value', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('mono_fonts', '{"enabled":true,"fonts":["Account Mono"]}')],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().dual.monoFonts.account()).toEqual({ enabled: true, fonts: ['Account Mono'] }))

    // No override: account value resolves through.
    expect(ctx.get().monoFonts()).toEqual({ enabled: true, fonts: ['Account Mono'] })
    expect(ctx.get().monoFonts().enabled).toBe(true)

    // Whole-object override wins; null restores the account value.
    ctx.get().dual.monoFonts.setBrowser({ enabled: true, fonts: ['Browser Mono'] })
    expect(ctx.get().monoFonts()).toEqual({ enabled: true, fonts: ['Browser Mono'] })
    expect(loadBrowserPrefs().monoFontOverride).toEqual({ enabled: true, fonts: ['Browser Mono'] })

    ctx.get().dual.monoFonts.setBrowser(null)
    expect(ctx.get().monoFonts()).toEqual({ enabled: true, fonts: ['Account Mono'] })
    expect('monoFontOverride' in loadBrowserPrefs()).toBe(false)
  })

  // uiFontFamily has no default stack behind it: a disabled or empty tier
  // must yield undefined, so the caller leaves the CSS property off rather
  // than writing an empty font-family.
  it('yields no uiFontFamily until the tier is enabled and holds a font', async () => {
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().uiFontFamily()).toBeUndefined())

    ctx.get().dual.uiFonts.setBrowser({ enabled: false, fonts: ['Inter'] })
    expect(ctx.get().uiFontFamily()).toBeUndefined()

    ctx.get().dual.uiFonts.setBrowser({ enabled: true, fonts: [] })
    expect(ctx.get().uiFontFamily()).toBeUndefined()

    ctx.get().dual.uiFonts.setBrowser({ enabled: true, fonts: ['Inter'] })
    expect(ctx.get().uiFontFamily()).toBe('"Inter"')
  })

  it('derives monoFontFamily from the resolved tier with the default fallback', async () => {
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().monoFontFamily()).toContain('monospace'))

    ctx.get().dual.monoFonts.setBrowser({ enabled: true, fonts: ['Custom Mono'] })
    expect(ctx.get().monoFontFamily()).toContain('"Custom Mono"')

    // An enabled tier with an empty stack falls back to the default family.
    ctx.get().dual.monoFonts.setBrowser({ enabled: true, fonts: [] })
    expect(ctx.get().monoFontFamily()).not.toContain('"Custom Mono"')
  })
})

// One parse guards BOTH tiers of a preference. A stored browser value that
// the hub would refuse must not reach the screen either: localStorage is
// editable by hand, survives a downgrade, and outlives the value set it was
// written against.
describe('preferencesContext — a stored browser value passes the same parse', () => {
  it('refuses a browser theme outside the allowed set and keeps the account value', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { theme: 'chartreuse' })
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', '"dark"')],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().dual.theme.account()).toBe('dark'))

    expect(ctx.get().dual.theme.browser()).toBeNull()
    expect(ctx.get().theme()).toBe('dark')
  })

  it('refuses a browser font tier whose enabled flag is not a boolean', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { monoFontOverride: { enabled: 'yes', fonts: ['Bad'] } })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().monoFonts()).toEqual({ enabled: false, fonts: [] }))

    expect(ctx.get().dual.monoFonts.browser()).toBeNull()
    expect(ctx.get().monoFontFamily()).not.toContain('"Bad"')
  })

  it('keeps only the two declared fields of a font tier', async () => {
    localStorageSet(KEY_BROWSER_PREFS, {
      uiFontOverride: { enabled: true, fonts: ['Inter'], stale: 'dropped' },
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().uiFonts().enabled).toBe(true))

    expect(ctx.get().dual.uiFonts.browser()).toEqual({ enabled: true, fonts: ['Inter'] })
  })

  it('refuses a browser turn-end sound outside the allowed set', async () => {
    localStorageSet(KEY_BROWSER_PREFS, { turnEndSound: 'fanfare' })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().turnEndSound()).toBe('ding-dong'))

    expect(ctx.get().dual.turnEndSound.browser()).toBeNull()
  })
})

// A key that no setting declares must not be counted as customized: the
// badge would then sit over a value that no signal holds. A known key on
// the same path records it, which is what makes the drop observable.
describe('preferencesContext — an undeclared account key', () => {
  it('is not recorded as customized when a write returns it', async () => {
    updateUserSetting.mockResolvedValue({ value: settingValue('a_key_from_a_newer_hub', '"x"', true) })
    const ctx = captureContext()

    await ctx.get().updateUserSetting('a_key_from_a_newer_hub', 'x')
    expect(ctx.get().accountCustomized().a_key_from_a_newer_hub).toBeUndefined()
  })

  // Keybindings are the one key whose malformed document CLEARS the value
  // instead of keeping it. Stale bindings left in force after the stored
  // override became unreadable would fire commands the user cannot see.
  it('clears the keybindings when the stored document is unreadable', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('keybindings', '[{"key":"F9","command":"app.newAgent"}]')],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().customKeybindings()).toHaveLength(1))

    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('keybindings', 'not-json{')],
    })
    await ctx.get().reload()
    expect(ctx.get().customKeybindings()).toEqual([])
  })

  it('leaves every declared key at its value', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', '"dark"'), settingValue('a_key_from_a_newer_hub', '"x"', true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().dual.theme.account()).toBe('dark'))

    expect(ctx.get().theme()).toBe('dark')
  })
})

// Every browser-only boolean stores the value that DIFFERS from its
// default, so a preference left alone costs no bytes.
describe('preferencesContext — browser-only booleans', () => {
  it('showHiddenMessages defaults to off and stores only the opt-in', () => {
    const ctx = captureContext()
    expect(ctx.get().showHiddenMessages()).toBe(false)
    expect('showHiddenMessages' in loadBrowserPrefs()).toBe(false)

    ctx.get().setShowHiddenMessages(true)
    expect(ctx.get().showHiddenMessages()).toBe(true)
    expect(loadBrowserPrefs().showHiddenMessages).toBe(true)

    ctx.get().setShowHiddenMessages(false)
    expect(ctx.get().showHiddenMessages()).toBe(false)
    expect('showHiddenMessages' in loadBrowserPrefs()).toBe(false)
  })

  it('expandAgentThoughts defaults to on and stores only the opt-out', () => {
    const ctx = captureContext()
    expect(ctx.get().expandAgentThoughts()).toBe(true)
    expect('expandAgentThoughts' in loadBrowserPrefs()).toBe(false)

    ctx.get().setExpandAgentThoughts(false)
    expect(loadBrowserPrefs().expandAgentThoughts).toBe(false)

    ctx.get().setExpandAgentThoughts(true)
    expect('expandAgentThoughts' in loadBrowserPrefs()).toBe(false)
  })

  it('hydrates each stored boolean on provider mount', () => {
    localStorageSet(KEY_BROWSER_PREFS, { expandAgentThoughts: false, showHiddenMessages: true })
    const ctx = captureContext()
    expect(ctx.get().expandAgentThoughts()).toBe(false)
    expect(ctx.get().showHiddenMessages()).toBe(true)
  })
})

describe('preferencesContext — superseded account write replies', () => {
  // Each write takes its sequence when the user ASKS, and the per-key
  // queue holds the newer request until the older one settles. So the
  // reply to the older write arrives while the newer write is still in
  // flight. Every response applies the server's effective value onto the
  // signal, so that stale reply puts the OLDER value back on screen for
  // the whole window: two fast clicks on a toggle, and it snaps back.
  it('ignores a superseded response and keeps the newest value', async () => {
    const first = deferred<{ value: unknown }>()
    const second = deferred<{ value: unknown }>()
    updateUserSetting
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const ctx = captureContext()

    // Both writes are issued in one burst, so the second takes its
    // sequence while the first is still in flight and the first reply is
    // stale before it arrives.
    const a = ctx.get().updateUserSetting('theme', 'dark')
    const b = ctx.get().updateUserSetting('theme', 'light')

    first.resolve({ value: settingValue('theme', '"dark"', true) })
    await a
    // The stale reply must not put its value on screen even for the time
    // the newer write is still in flight.
    expect(ctx.get().dual.theme.account()).toBe('system')

    second.resolve({ value: settingValue('theme', '"light"', true) })
    await b
    expect(ctx.get().dual.theme.account()).toBe('light')
  })

  // A superseded FAILURE must not roll back either: the value it would
  // restore predates a write the user has since made.
  it('does not roll back a superseded failed write over a newer value', async () => {
    const first = deferred<{ value: unknown }>()
    const second = deferred<{ value: unknown }>()
    updateUserSetting
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    const ctx = captureContext()

    const stale = ctx.get().dual.theme.setAccount('dark')
    const later = ctx.get().dual.theme.setAccount('light')
    expect(ctx.get().dual.theme.account()).toBe('light')

    first.reject(new Error('network down'))
    await expect(stale).rejects.toThrow()
    // The value the rollback would restore is 'system', captured before
    // the FIRST write and already replaced by a write the user made since.
    expect(ctx.get().dual.theme.account()).toBe('light')

    second.resolve({ value: settingValue('theme', '"light"', true) })
    await later
  })

  // A write to a DIFFERENT key is independent and must still apply.
  it('keeps per-key sequences independent', async () => {
    updateUserSetting.mockImplementation(async ({ key }: { key: string }) => ({
      value: key === 'theme'
        ? settingValue('theme', '"dark"', true)
        : settingValue('diff_view', '"split"', true),
    }))
    const ctx = captureContext()

    await Promise.all([
      ctx.get().updateUserSetting('theme', 'dark'),
      ctx.get().updateUserSetting('diff_view', 'split'),
    ])
    expect(ctx.get().dual.theme.account()).toBe('dark')
    expect(ctx.get().dual.diffView.account()).toBe('split')
  })
})

// The hub declares each account key ONCE, in Go, and the golden file is
// what both sides read. A parse here that is narrower than the hub is a
// live defect -- it refuses the hub's own stored document -- and a parse
// that is wider puts a value on screen the hub would refuse.
describe('preferencesContext — parses exactly what the hub declares', () => {
  // Read through the ONE helper that knows where the golden file lives and
  // what shape it holds. A second reader here re-declared both, so the
  // widening that added `unit` and `customId` to the file reached the
  // registry tests and not this one.
  const golden = goldenAccountSchema()

  /** How to read one enum key's account signal, and its built-in default. */
  const enumReaders: Record<string, { read: (p: Prefs) => unknown, fallback: string }> = {
    theme: { read: p => p.dual.theme.account(), fallback: 'system' },
    terminal_theme: { read: p => p.dual.terminalTheme.account(), fallback: 'match-ui' },
    diff_view: { read: p => p.dual.diffView.account(), fallback: 'unified' },
    turn_end_sound: { read: p => p.dual.turnEndSound.account(), fallback: 'ding-dong' },
  }

  const enumKeys = golden.filter(k => k.fields.some(f => (f.enumValues?.length ?? 0) > 0))

  // Without this, a key the hub adds later gets no parse coverage and
  // nothing says so.
  it('drives every enum key the hub declares', () => {
    expect(enumKeys.length).toBeGreaterThan(0)
    expect(enumKeys.map(k => k.key).sort()).toEqual(Object.keys(enumReaders).sort())
  })

  it('accepts every enum value the hub declares', async () => {
    for (const key of enumKeys) {
      const reader = enumReaders[key.key]
      for (const option of key.fields[0].enumValues ?? []) {
        localStorageClearForTests()
        listUserSettings.mockResolvedValue({
          descriptors: [],
          values: [settingValue(key.key, JSON.stringify(option.value), true)],
        })
        const ctx = captureContext()
        await waitFor(() => expect(reader.read(ctx.get()), `${key.key} = ${option.value}`).toBe(option.value))
      }
    }
  })

  it('refuses a value outside the declared set and keeps the default', async () => {
    for (const key of enumKeys) {
      const reader = enumReaders[key.key]
      localStorageClearForTests()
      listUserSettings.mockResolvedValue({
        descriptors: [],
        values: [
          settingValue(key.key, '"a-value-from-a-newer-hub"', true),
          settingValue('debug_logging', 'true', true),
        ],
      })
      const ctx = captureContext()
      await waitFor(() => expect(ctx.get().dual.debugLogging.account()).toBe(true))
      expect(reader.read(ctx.get()), `${key.key} refused`).toBe(reader.fallback)
      // A refused value must not be recorded as customized either: the
      // badge and the Reset would sit over a value no signal holds.
      expect(ctx.get().accountCustomized()[key.key]).toBeUndefined()
    }
  })

  // The hub refuses `v < 0 || v > 100`, and `useTurnEnd` assigns
  // `volume / 100` to an HTMLAudioElement, which throws IndexSizeError
  // synchronously for anything outside 0..1 -- no sound, and no turn-end
  // event either, because the rate limiter already recorded the play.
  it('accepts the turn-end volume bounds and refuses outside them', async () => {
    for (const [stored, expected] of [[0, 0], [100, 100], [-1, 100], [101, 100], [42, 42]] as const) {
      localStorageClearForTests()
      localStorageSet(KEY_BROWSER_PREFS, { turnEndSoundVolume: stored })
      const ctx = captureContext()
      await waitFor(() => expect(ctx.get().turnEndSoundVolume(), `stored ${stored}`).toBe(expected))
    }
  })

  it('refuses a stored enter-key mode outside the two declared modes', () => {
    localStorageSet(KEY_BROWSER_PREFS, { enterKeyMode: 'x' })
    expect(captureContext().get().enterKeyMode()).toBe('cmd-enter-sends')
  })

  it('hydrates a declared enter-key mode', () => {
    localStorageSet(KEY_BROWSER_PREFS, { enterKeyMode: 'enter-sends' })
    expect(captureContext().get().enterKeyMode()).toBe('enter-sends')
  })

  // Two readers of ONE field must not disagree. `~/lib/terminal` guards it
  // and resolves 'auto'; an unguarded accessor here handed the settings
  // row an enum value absent from its three options.
  it('refuses a stored terminal renderer outside the three options', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'x' })
    expect(captureContext().get().terminalRenderer()).toBe('auto')
  })

  it('hydrates a declared terminal renderer', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'canvas' })
    expect(captureContext().get().terminalRenderer()).toBe('canvas')
  })
})

// `FontFamilyValue.Fonts` is `json:"fonts,omitempty"`, so the hub's own
// document for an enabled tier with no families is `{"enabled":true}` --
// and that is the MANDATORY first state, because the stack row stays
// hidden until the tier is on.
describe('preferencesContext — font tier parse', () => {
  it('accepts the hub document for an enabled tier whose fonts key is absent', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('ui_fonts', '{"enabled":true}', true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().uiFonts()).toEqual({ enabled: true, fonts: [] }))
    expect(ctx.get().accountCustomized().ui_fonts).toBe(true)
  })

  it('accepts an explicit empty list the same way', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('mono_fonts', '{"enabled":true,"fonts":[]}', true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().monoFonts()).toEqual({ enabled: true, fonts: [] }))
  })

  // `usersettings.validateFontFamily` refuses each of these on the write
  // path. One parse guards BOTH tiers, so a hand-edited localStorage
  // document must not put on screen what the hub would refuse.
  it.each([
    ['a control character', '{"enabled":true,"fonts":["My\\nFont"]}'],
    ['an invisible format character', '{"enabled":true,"fonts":["Fi\\u200bra"]}'],
    ['a byte order mark', '{"enabled":true,"fonts":["\\ufeffInter"]}'],
    ['a repeated space', '{"enabled":true,"fonts":["Fira  Code"]}'],
    ['a leading space', '{"enabled":true,"fonts":[" Inter"]}'],
    ['an empty name', '{"enabled":true,"fonts":[""]}'],
    ['a non-string element', '{"enabled":true,"fonts":[7]}'],
  ])('refuses a font name carrying %s', async (_label, effectiveJson) => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', '"dark"'), settingValue('ui_fonts', effectiveJson, true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().dual.theme.account()).toBe('dark'))

    expect(ctx.get().uiFonts()).toEqual({ enabled: false, fonts: [] })
    // A refused document leaves the flag alone: a "Customized" badge over
    // a value the row is not showing is worse than no badge.
    expect(ctx.get().accountCustomized().ui_fonts).toBeUndefined()
  })

  // The name rule relaxed, and this parse relaxed with it, because the two must
  // agree: a value this parse refuses and the hub stores leaves the row showing
  // a font the account does not have. The CSS stays safe because
  // `buildFontFamily` escapes a quote and a backslash at the emitter, which is
  // the guard that covers a hand-edited document like this one.
  // `family` is a LITERAL, not `buildFontFamily([name])`. `uiFontFamily()` is
  // defined as `buildFontFamily(tier.fonts)`, so comparing the two calls
  // asserts `buildFontFamily(x) === buildFontFamily(x)`, which holds for every
  // possible body — including one with the escape deleted. This commit makes
  // that escape the ONLY guard on a quote and a backslash, so the expected CSS
  // has to be spelled out.
  it.each([
    ['a quote', '{"enabled":true,"fonts":["Ev\\"il"]}', 'Ev"il', '"Ev\\"il"'],
    ['a backslash', '{"enabled":true,"fonts":["back\\\\slash"]}', 'back\\slash', '"back\\\\slash"'],
    ['a dollar', '{"enabled":true,"fonts":["Fira$Code"]}', 'Fira$Code', '"Fira$Code"'],
    ['a percent', '{"enabled":true,"fonts":["Fira%Code"]}', 'Fira%Code', '"Fira%Code"'],
  ])('keeps a font name carrying %s, and escapes it at the emitter', async (_label, effectiveJson, name, family) => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('ui_fonts', effectiveJson, true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().uiFonts()).toEqual({ enabled: true, fonts: [name] }))
    expect(ctx.get().uiFontFamily()).toBe(family)
    // …and the same string is what the emitter produces on its own, so the two
    // stay one rule rather than two.
    expect(buildFontFamily([name])).toBe(family)
  })

  it('keeps a font name the hub would store', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('ui_fonts', '{"enabled":true,"fonts":["Noto Sans KR","Inter"]}', true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().uiFonts().fonts).toEqual(['Noto Sans KR', 'Inter']))
  })

  // One name the hub would REFUSE refuses the WHOLE tier, which is what
  // `parseFontTier` states: a document that carries a name the account cannot
  // hold is not a document this side renders half of.
  //
  // The rule now FOLDS, so a repeated space is refused where it used to pass,
  // and each of these reaches the parse from a hand-edited localStorage
  // document that never passes the hub's validator at all.
  it.each([
    ['a repeated space', 'Fira  Code'],
    ['surrounding whitespace', '  Fira Code  '],
    ['an invisible format character', 'Fira\u200BCode'],
    ['a control character', 'Fira\u0000Code'],
    ['a name over the byte limit', 'a'.repeat(129)],
    ['a CJK name over the BYTE limit but under a character count', '\u4E00'.repeat(43)],
    // The length guard in `isStorableFontName` runs BEFORE `sanitizeName`, so
    // a hand-edited document cannot make every page load run three regex
    // passes over a megabyte. It must not change the ANSWER, only the work.
    ['a name of a megabyte', 'x'.repeat(1_000_000)],
  ])('refuses the whole tier when one name carries %s', async (_label, bad) => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('ui_fonts', JSON.stringify({ enabled: true, fonts: [bad, 'Inter'] }), true)],
    })
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().uiFonts()).toEqual({ enabled: false, fonts: [] }))
  })
})

// The key lives on its own, so its default is a DELETED key: storing
// `true` would be an override in the opposite direction and would stop a
// changed default from reaching a browser that never touched it.
describe('preferencesContext — directoryPickerShowHidden', () => {
  it('defaults to true with nothing stored', () => {
    const ctx = captureContext()
    expect(ctx.get().directoryPickerShowHidden()).toBe(true)
    expect(localStorageGet(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN)).toBeUndefined()
  })

  it('stores the opt-out and deletes the key on the way back', () => {
    const ctx = captureContext()
    ctx.get().setDirectoryPickerShowHidden(false)
    expect(ctx.get().directoryPickerShowHidden()).toBe(false)
    expect(localStorageGet(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN)).toBe(false)

    ctx.get().setDirectoryPickerShowHidden(true)
    expect(ctx.get().directoryPickerShowHidden()).toBe(true)
    expect(localStorageGet(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN)).toBeUndefined()
  })

  it('hydrates a stored false', () => {
    localStorageSet(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, false)
    expect(captureContext().get().directoryPickerShowHidden()).toBe(false)
  })

  // A stored string read as truthy everywhere except the toggle bound to
  // it, which compares against `true` and rendered OFF over a picker that
  // was showing hidden files.
  it('refuses a non-boolean stored value and keeps the default', () => {
    localStorageSet(KEY_DIRECTORY_SELECTOR_SHOW_HIDDEN, 'true')
    expect(captureContext().get().directoryPickerShowHidden()).toBe(true)
  })
})

describe('preferencesContext — batchBrowserPrefWrites', () => {
  it('applies every write in the body to one document', () => {
    const ctx = captureContext()
    ctx.get().batchBrowserPrefWrites(() => {
      ctx.get().dual.theme.setBrowser('dark')
      ctx.get().dual.diffView.setBrowser('split')
      ctx.get().setShowHiddenMessages(true)
    })
    const prefs = loadBrowserPrefs()
    expect(prefs.theme).toBe('dark')
    expect(prefs.diffView).toBe('split')
    expect(prefs.showHiddenMessages).toBe(true)
  })

  it('stores nothing until the body returns', () => {
    const ctx = captureContext()
    ctx.get().batchBrowserPrefWrites(() => {
      ctx.get().dual.theme.setBrowser('dark')
      expect('theme' in loadBrowserPrefs()).toBe(false)
    })
    expect(loadBrowserPrefs().theme).toBe('dark')
  })

  // The `finally` is what closes the batch. Without it every later write
  // in the page accumulates into a document nothing ever stores.
  it('closes the batch when the body throws, keeping what it wrote', () => {
    const ctx = captureContext()
    expect(() => ctx.get().batchBrowserPrefWrites(() => {
      ctx.get().dual.theme.setBrowser('dark')
      throw new Error('mid-batch failure')
    })).toThrow('mid-batch failure')
    expect(loadBrowserPrefs().theme).toBe('dark')

    ctx.get().dual.diffView.setBrowser('split')
    expect(loadBrowserPrefs().diffView).toBe('split')
  })

  // A nested call must not adopt a second document and store it over the
  // outer one, which would lose every write the outer batch made first.
  it('lets a nested batch run inside the outer one without storing', () => {
    const ctx = captureContext()
    ctx.get().batchBrowserPrefWrites(() => {
      ctx.get().dual.theme.setBrowser('dark')
      ctx.get().batchBrowserPrefWrites(() => {
        ctx.get().dual.diffView.setBrowser('split')
      })
      expect('diffView' in loadBrowserPrefs()).toBe(false)
    })
    const prefs = loadBrowserPrefs()
    expect(prefs.theme).toBe('dark')
    expect(prefs.diffView).toBe('split')
  })
})

// Two writes to ONE key must reach the hub in the order the user made
// them. The reply guard above decides which ANSWER is applied; it cannot
// decide which REQUEST the hub commits first, and `mutateUserPrefs` merges
// the partial under a row lock, so the request that COMMITS LAST is the
// one the hub keeps.
describe('preferencesContext — per-key request ordering', () => {
  it('does not issue a second write for a key while the first is in flight', async () => {
    const first = deferred<{ value: unknown }>()
    const sent: string[] = []
    updateUserSetting.mockImplementation(({ partialJson }: { partialJson: string }) => {
      sent.push(partialJson)
      if (sent.length === 1)
        return first.promise
      return Promise.resolve({ value: settingValue('ui_fonts', '{"enabled":true,"fonts":["A","B"]}', true) })
    })
    const ctx = captureContext()

    const a = ctx.get().updateUserSetting('ui_fonts', { fonts: ['A'] })
    const b = ctx.get().updateUserSetting('ui_fonts', { fonts: ['A', 'B'] })

    // Two fast clicks on "+" in a font stack. The hub must not receive the
    // second document while it is still committing the first: it would
    // otherwise be free to store the ONE-font document last, leaving the
    // screen showing two fonts the account does not hold.
    await Promise.resolve()
    expect(sent).toEqual(['{"fonts":["A"]}'])

    first.resolve({ value: settingValue('ui_fonts', '{"enabled":true,"fonts":["A"]}', true) })
    await a
    await b
    expect(sent).toEqual(['{"fonts":["A"]}', '{"fonts":["A","B"]}'])
    expect(ctx.get().dual.uiFonts.account()).toEqual({ enabled: true, fonts: ['A', 'B'] })
  })

  // A refused write must not stall its key: the user's next edit is the one
  // thing that can repair the failure they just saw.
  it('issues the next write for a key after the previous one fails', async () => {
    const first = deferred<{ value: unknown }>()
    const sent: string[] = []
    updateUserSetting.mockImplementation(({ partialJson }: { partialJson: string }) => {
      sent.push(partialJson)
      if (sent.length === 1)
        return first.promise
      return Promise.resolve({ value: settingValue('theme', '"light"', true) })
    })
    const ctx = captureContext()

    const a = ctx.get().updateUserSetting('theme', 'dark').catch(() => {})
    const b = ctx.get().updateUserSetting('theme', 'light')
    first.reject(new Error('network down'))
    await a
    await b

    expect(sent).toEqual(['"dark"', '"light"'])
    expect(ctx.get().dual.theme.account()).toBe('light')
  })

  // Writes to DIFFERENT keys are independent: one key's in-flight request
  // must not hold another key's back.
  it('issues writes for two keys without either waiting on the other', async () => {
    const held = deferred<{ value: unknown }>()
    const sent: string[] = []
    updateUserSetting.mockImplementation(({ key }: { key: string }) => {
      sent.push(key)
      if (key === 'theme')
        return held.promise
      return Promise.resolve({ value: settingValue('diff_view', '"split"', true) })
    })
    const ctx = captureContext()

    const a = ctx.get().updateUserSetting('theme', 'dark')
    await ctx.get().updateUserSetting('diff_view', 'split')
    expect(sent).toEqual(['theme', 'diff_view'])

    held.resolve({ value: settingValue('theme', '"dark"', true) })
    await a
  })
})

// A reload carries a snapshot of the WHOLE account, so two reloads with no
// write between them carry identical per-key write stamps: those stamps
// cannot separate them, and both replies applied in arrival order.
describe('preferencesContext — superseded reloads', () => {
  it('drops a stale reload reply over the newer one', async () => {
    const stale = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockReturnValueOnce(stale.promise)
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [settingValue('theme', '"dark"')],
    })
    const ctx = captureContext()
    await ctx.get().reload()
    expect(ctx.get().dual.theme.account()).toBe('dark')

    stale.resolve({ descriptors: [], values: [settingValue('theme', '"light"')] })
    await flushMicrotasks()
    expect(ctx.get().dual.theme.account()).toBe('dark')
  })

  it('does not record a stale load failure over a newer success', async () => {
    const stale = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockReturnValueOnce(stale.promise)
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    const ctx = captureContext()
    await ctx.get().reload()
    expect(ctx.get().accountLoadError()).toBeNull()

    // Nothing on the page clears `accountLoadError` a second time, so a
    // stale failure recorded here would state a load error for the rest of
    // the session over settings that loaded correctly.
    stale.reject(new Error('network down'))
    await flushMicrotasks()
    expect(ctx.get().accountLoadError()).toBeNull()
  })

  it('records a load failure that IS the newest', async () => {
    listUserSettings.mockRejectedValue(new Error('network down'))
    const ctx = captureContext()
    await waitFor(() => expect(ctx.get().accountLoadError()).toContain('network down'))
  })
})

/** A promise plus its resolvers, for pinning completion order. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  // The rejection is asserted by the test that arms it; without this a
  // rejection scheduled before its `await` trips vitest's unhandled check.
  promise.catch(() => {})
  return { promise, resolve, reject }
}
