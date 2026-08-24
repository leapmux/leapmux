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
function withProvider() {
  let binding!: ReturnType<typeof useThemeChooser>
  let prefs!: PreferencesState
  function Probe() {
    binding = useThemeChooser()
    prefs = usePreferences()
    return null
  }
  render(() => <PreferencesProvider><Probe /></PreferencesProvider>)
  return { binding: () => binding, prefs: () => prefs }
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
// writes IS the persistence contract: a theme picked in the no-workspace empty
// state has to be the theme the Preferences dialog reports afterwards. Nothing
// else in the suite covers that end to end.
//
// There is no provider-less counterpart any more. The hook used to carry a
// second branch for the desktop launcher, which renders outside every provider;
// that surface now shows no theme control at all, because every stored
// preference is scoped to an account and the launcher has none.
describe('useThemeChooser', () => {
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
    // The provider's OWN device-tier signal moves with the write, not just the
    // stored document. Writing around the provider would leave the dialog
    // reporting "Account default" over a value the device had overridden.
    expect(prefs().dual.theme.browser()).toEqual({ name: 'gruvbox', mode: 'dark' })
    expect(prefs().theme()).toEqual({ name: 'gruvbox', mode: 'dark' })
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
      binding = useTerminalThemeChooser()
      return null
    }
    render(() => <PreferencesProvider><Probe /></PreferencesProvider>)
    void binding.onChange({ name: 'nord', mode: 'dark' })

    // The account tier, which is where an un-overridden row writes. These
    // cases used to force the device tier with a `deviceOnly` flag that no
    // surface passes any more; asserting on the RPC pins the same failure on
    // the tier the row actually uses.
    expect(clients.updateUserSetting).toHaveBeenCalledTimes(1)
    expect(clients.updateUserSetting).toHaveBeenCalledWith({
      key: 'terminal_theme',
      partialJson: JSON.stringify({ name: 'nord', mode: 'dark' }),
    })
  })

  it('writes the syntax key, and only that key', () => {
    let binding!: ReturnType<typeof useSyntaxThemeChooser>
    function Probe() {
      binding = useSyntaxThemeChooser()
      return null
    }
    render(() => <PreferencesProvider><Probe /></PreferencesProvider>)
    void binding.onChange({ name: 'solarized', mode: 'light' })

    expect(clients.updateUserSetting).toHaveBeenCalledTimes(1)
    expect(clients.updateUserSetting).toHaveBeenCalledWith({
      key: 'syntax_theme',
      partialJson: JSON.stringify({ name: 'solarized', mode: 'light' }),
    })
  })

  it('reads each row back from its own key', () => {
    let ui!: ReturnType<typeof useThemeChooser>
    let terminal!: ReturnType<typeof useTerminalThemeChooser>
    let syntax!: ReturnType<typeof useSyntaxThemeChooser>
    function Probe() {
      ui = useThemeChooser()
      terminal = useTerminalThemeChooser()
      syntax = useSyntaxThemeChooser()
      return null
    }
    render(() => <PreferencesProvider><Probe /></PreferencesProvider>)

    // `writeAccount` applies optimistically before it awaits, so the read back
    // is synchronous even though the write is an RPC.
    void ui.onChange({ name: 'catppuccin', mode: 'dark' })
    void terminal.onChange({ name: 'nord', mode: 'light' })
    void syntax.onChange({ name: 'gruvbox', mode: 'system' })

    expect(ui.value()).toEqual({ name: 'catppuccin', mode: 'dark' })
    expect(terminal.value()).toEqual({ name: 'nord', mode: 'light' })
    expect(syntax.value()).toEqual({ name: 'gruvbox', mode: 'system' })
  })
})
