import type { PreferencesState } from '~/context/PreferencesContext'
import { render } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { loadBrowserPrefs, localStorageClearForTests } from '~/lib/browserStorage'
import { applyTheme, DEFAULT_THEME_VALUE, themeStore } from '~/lib/themeStore'
import { useSyntaxThemeChooser, useTerminalThemeChooser, useThemeChooser } from './useThemeChooser'

const clients = vi.hoisted(() => ({
  listUserSettings: vi.fn(),
  updateUserSetting: vi.fn(),
  resetUserSetting: vi.fn(),
}))

vi.mock('~/api/clients', () => ({
  userClient: {
    listUserSettings: clients.listUserSettings,
    updateUserSetting: clients.updateUserSetting,
    resetUserSetting: clients.resetUserSetting,
  },
}))

/** Render the hook with a provider above it, and expose both it and the context. */
function withProvider(options?: Parameters<typeof useThemeChooser>[0]) {
  let binding!: ReturnType<typeof useThemeChooser>
  let prefs!: PreferencesState
  function Probe() {
    binding = useThemeChooser(options)
    prefs = usePreferences()
    return null
  }
  render(() => <PreferencesProvider><Probe /></PreferencesProvider>)
  return { binding: () => binding, prefs: () => prefs }
}

/** Render the hook with NO provider — the desktop launcher's situation. */
function withoutProvider() {
  let binding!: ReturnType<typeof useThemeChooser>
  function Probe() {
    binding = useThemeChooser()
    return null
  }
  render(() => <Probe />)
  return { binding: () => binding }
}

beforeEach(() => {
  localStorageClearForTests()
  applyTheme(DEFAULT_THEME_VALUE)
  clients.listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
  clients.updateUserSetting.mockResolvedValue({})
  clients.resetUserSetting.mockResolvedValue({})
})

afterEach(() => {
  localStorageClearForTests()
  applyTheme(DEFAULT_THEME_VALUE)
  vi.clearAllMocks()
})

// Every surface that shows the theme picker binds through this hook, so what it
// writes IS the persistence contract: a theme picked on the launcher, on the
// setup page or in the empty state has to be the theme the Preferences dialog
// reports afterwards. Nothing else in the suite covers that end to end.
describe('useThemeChooser under a PreferencesProvider', () => {
  it('reads the resolved preference', () => {
    const { binding, prefs } = withProvider()
    expect(binding().value()).toEqual(DEFAULT_THEME_VALUE)

    prefs().dual.theme.setBrowser({ name: 'nord', mode: 'dark' })
    expect(binding().value()).toEqual({ name: 'nord', mode: 'dark' })
  })

  it('writes the ACCOUNT tier while the key is not overridden on this device', () => {
    // The same tier decision the dialog's own row makes, because it is the same
    // `dualScalar` binding. Restating the rule here would be a second authority
    // and the scope chip would start describing writes it did not make.
    const { binding } = withProvider()
    void binding().onChange({ name: 'catppuccin', mode: 'light' })

    expect(clients.updateUserSetting).toHaveBeenCalledWith({
      key: 'theme',
      partialJson: JSON.stringify({ name: 'catppuccin', mode: 'light' }),
    })
    expect('theme' in loadBrowserPrefs()).toBe(false)
  })

  it('writes the DEVICE tier once the key is overridden on this device', () => {
    const { binding, prefs } = withProvider()
    // Begin a device override the way the scope chip does: pin the resolved
    // value into the browser tier.
    prefs().dual.theme.setBrowser(prefs().theme())
    clients.updateUserSetting.mockClear()

    void binding().onChange({ name: 'gruvbox', mode: 'dark' })

    expect(loadBrowserPrefs().theme).toEqual({ name: 'gruvbox', mode: 'dark' })
    expect(clients.updateUserSetting).not.toHaveBeenCalled()
  })

  it('writes the DEVICE tier when the surface declares it has no session', () => {
    // `/setup` renders inside the provider but BEFORE any account exists. The
    // ordinary tiered write would go out unauthenticated, be refused, and roll
    // back -- the theme would apply and then revert under the user.
    const { binding } = withProvider({ deviceOnly: true })
    void binding().onChange({ name: 'github', mode: 'dark' })

    expect(loadBrowserPrefs().theme).toEqual({ name: 'github', mode: 'dark' })
    expect(clients.updateUserSetting).not.toHaveBeenCalled()
  })

  it('moves the provider\'s own device tier on a device-only write', () => {
    // Not through `themeStore.writeDeviceTheme`: both write the same field, but
    // only the provider path also moves its device-tier signal. Writing around
    // it would leave the dialog reporting "Account default" over a value the
    // device had overridden.
    const { binding, prefs } = withProvider({ deviceOnly: true })
    void binding().onChange({ name: 'one', mode: 'light' })

    expect(prefs().dual.theme.browser()).toEqual({ name: 'one', mode: 'light' })
    expect(prefs().theme()).toEqual({ name: 'one', mode: 'light' })
  })

  it('paints the choice immediately', () => {
    const { binding, prefs } = withProvider()
    // Begin a device override the way the scope chip does: pin the resolved
    // value into the browser tier.
    prefs().dual.theme.setBrowser(prefs().theme())
    void binding().onChange({ name: 'ayu', mode: 'dark' })

    expect(themeStore.theme()).toEqual({ name: 'ayu', mode: 'dark' })
    expect(themeStore.resolvedMode()).toBe('dark')
  })
})

// The two rows that exist only inside the dialog. Both share `providerBinding`
// with the UI row, so what these pin is that each is wired to its OWN key --
// the failure they exist for is a copy-paste that binds two rows to one
// preference, which no type would catch.
describe('the terminal and syntax rows bind their own preferences', () => {
  it('writes the terminal key, and only that key', () => {
    let binding!: ReturnType<typeof useTerminalThemeChooser>
    function Probe() {
      binding = useTerminalThemeChooser({ deviceOnly: true })
      return null
    }
    render(() => <PreferencesProvider><Probe /></PreferencesProvider>)
    void binding.onChange({ name: 'nord', mode: 'dark' })

    expect(loadBrowserPrefs().terminalTheme).toEqual({ name: 'nord', mode: 'dark' })
    expect('syntaxTheme' in loadBrowserPrefs()).toBe(false)
    expect('theme' in loadBrowserPrefs()).toBe(false)
  })

  it('writes the syntax key, and only that key', () => {
    let binding!: ReturnType<typeof useSyntaxThemeChooser>
    function Probe() {
      binding = useSyntaxThemeChooser({ deviceOnly: true })
      return null
    }
    render(() => <PreferencesProvider><Probe /></PreferencesProvider>)
    void binding.onChange({ name: 'solarized', mode: 'light' })

    expect(loadBrowserPrefs().syntaxTheme).toEqual({ name: 'solarized', mode: 'light' })
    expect('terminalTheme' in loadBrowserPrefs()).toBe(false)
    expect('theme' in loadBrowserPrefs()).toBe(false)
  })

  it('reads each row back from its own key', () => {
    let ui!: ReturnType<typeof useThemeChooser>
    let terminal!: ReturnType<typeof useTerminalThemeChooser>
    let syntax!: ReturnType<typeof useSyntaxThemeChooser>
    function Probe() {
      ui = useThemeChooser({ deviceOnly: true })
      terminal = useTerminalThemeChooser({ deviceOnly: true })
      syntax = useSyntaxThemeChooser({ deviceOnly: true })
      return null
    }
    render(() => <PreferencesProvider><Probe /></PreferencesProvider>)

    void ui.onChange({ name: 'catppuccin', mode: 'dark' })
    void terminal.onChange({ name: 'nord', mode: 'light' })
    void syntax.onChange({ name: 'gruvbox', mode: 'system' })

    expect(ui.value()).toEqual({ name: 'catppuccin', mode: 'dark' })
    expect(terminal.value()).toEqual({ name: 'nord', mode: 'light' })
    expect(syntax.value()).toEqual({ name: 'gruvbox', mode: 'system' })
  })
})

describe('useThemeChooser without a PreferencesProvider', () => {
  it('reads the live theme', () => {
    applyTheme({ name: 'solarized', mode: 'light' })
    const { binding } = withoutProvider()
    expect(binding().value()).toEqual({ name: 'solarized', mode: 'light' })
  })

  it('writes the device tier and never reaches for an account write', () => {
    // The launcher has no hub connection yet. An account write there would fail
    // against a hub the user has not connected to and surface an error on the
    // connect screen.
    const { binding } = withoutProvider()
    void binding().onChange({ name: 'tokyo-night', mode: 'dark' })

    expect(loadBrowserPrefs().theme).toEqual({ name: 'tokyo-night', mode: 'dark' })
    expect(clients.updateUserSetting).not.toHaveBeenCalled()
  })

  it('writes the same field a provider-backed device write produces', () => {
    // The round trip that makes the launcher's choice survive: no
    // launcher-specific key, no second shape.
    const { binding } = withoutProvider()
    void binding().onChange({ name: 'everforest', mode: 'dark' })
    const fromLauncher = loadBrowserPrefs()

    localStorageClearForTests()
    const { prefs } = withProvider()
    prefs().dual.theme.setBrowser({ name: 'everforest', mode: 'dark' })

    expect(loadBrowserPrefs()).toEqual(fromLauncher)
  })

  it('leaves the choice as the resolved value once a provider mounts', () => {
    // The launcher connects: PreferencesProvider seeds its device tier from the
    // document the launcher wrote, so the dialog reports "This device" over
    // exactly that value rather than reverting to the account default.
    const { binding } = withoutProvider()
    void binding().onChange({ name: 'rose-pine', mode: 'light' })

    const { prefs, binding: bound } = withProvider()
    expect(prefs().theme()).toEqual({ name: 'rose-pine', mode: 'light' })
    expect(prefs().dual.theme.browser()).toEqual({ name: 'rose-pine', mode: 'light' })
    expect(bound().value()).toEqual({ name: 'rose-pine', mode: 'light' })
  })
})
