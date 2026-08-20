import type { DualPreference } from '~/context/PreferencesContext'
import type { TerminalThemeValue, ThemeValue } from '~/styles/themes'
import { dualScalar } from '~/components/settings/registry/bindings'
import { usePreferences, usePreferencesOptional } from '~/context/PreferencesContext'
import { themeStore } from '~/lib/themeStore'

export interface ThemeChooserBinding<T> {
  value: () => T
  onChange: (value: T) => void | Promise<boolean | void>
}

export interface ThemeChooserOptions {
  /**
   * Write the device tier and never the account tier.
   *
   * For a surface that renders BEFORE an account exists to write to. The
   * ordinary tiered write would issue `UpdateUserSetting` unauthenticated, the
   * hub would refuse it, and the optimistic value would roll back -- so the
   * user picks a theme, watches it apply, and watches it revert.
   *
   * A surface states this because it KNOWS it has no session, not because a
   * write happened to fail: inferring it from a failure would also swallow a
   * genuine validation refusal, and storing locally a value the hub refuses is
   * how a row ends up showing a default under a "Customized" badge.
   */
  deviceOnly?: boolean
}

/**
 * Bind a `<ThemeChooser>` to the `theme` preference.
 *
 * THE SINGLE WRITER. Every surface that shows the chooser calls this and
 * nothing else, so the Preferences dialog, the first-run setup page, the
 * no-workspace empty state and the desktop launcher all write the same
 * preference, in the same shape, to the same place. A theme picked at first
 * impression is the theme the dialog shows afterwards, and it survives
 * connecting, signing up, reloading and restarting.
 *
 * Two branches, and only two:
 *
 *   - Under a `PreferencesProvider`, the write goes through `dualScalar` —
 *     literally the binding the dialog's own row uses — so the tier decision is
 *     one implementation shared by all of them: the device tier when the key is
 *     already overridden on this device, otherwise the account tier. Restating
 *     that rule here would be a second authority on which tier a write lands
 *     on, and the scope chip would start describing writes it did not make.
 *
 *   - The desktop launcher renders outside every provider and has no hub
 *     connection, so there is no account tier to write. It writes the DEVICE
 *     tier of the same preference: the same `leapmux:browser-prefs` key, the
 *     same `theme` field, the same `{ name, mode }` shape. When the launcher
 *     connects, `PreferencesProvider` seeds its device tier from exactly that
 *     document, so the choice is already the resolved value and the dialog
 *     reports it as "This device" — the ordinary, reversible state for any
 *     device override.
 *
 * It never attempts an account write without a provider. That request would
 * fail against a hub the user has not connected to yet and would surface an
 * error on the connect screen.
 */
export function useThemeChooser(options?: ThemeChooserOptions): ThemeChooserBinding<ThemeValue> {
  const preferences = usePreferencesOptional()
  if (!preferences) {
    return {
      value: () => themeStore.theme(),
      onChange: value => themeStore.writeDeviceTheme(value),
    }
  }
  return providerBinding(preferences.dual.theme, preferences.theme, options)
}

/**
 * The same binding for the TERMINAL theme.
 *
 * No provider-less branch: the terminal row exists only in the Preferences
 * dialog, which is always inside the provider. The first-impression surfaces
 * deliberately show one control -- the terminal defaults to `match-ui`, so
 * choosing a palette there moves the terminal with it, and offering a second
 * picker before the user opens a terminal would be noise.
 */
export function useTerminalThemeChooser(
  options?: ThemeChooserOptions,
): ThemeChooserBinding<TerminalThemeValue> {
  const preferences = usePreferences()
  return providerBinding(preferences.dual.terminalTheme, preferences.terminalTheme, options)
}

/**
 * The same binding for the SYNTAX theme.
 *
 * No provider-less branch, for the same reason the terminal has none: this row
 * exists only in the Preferences dialog.
 */
export function useSyntaxThemeChooser(
  options?: ThemeChooserOptions,
): ThemeChooserBinding<TerminalThemeValue> {
  const preferences = usePreferences()
  return providerBinding(preferences.dual.syntaxTheme, preferences.syntaxTheme, options)
}

/** The tiered write, shared by all three rows so one rule decides the tier. */
function providerBinding<T>(
  pref: DualPreference<T>,
  resolved: () => T,
  options?: ThemeChooserOptions,
): ThemeChooserBinding<T> {
  const binding = dualScalar(pref)
  return {
    value: resolved,
    onChange: (value) => {
      // Through the PROVIDER even when device-only, never through
      // `themeStore.writeDeviceTheme`: both write the same localStorage field,
      // but only this one also moves the provider's own device-tier signal.
      // Writing around it would leave the dialog reading a stale tier and
      // reporting "Account default" over a value the device had overridden.
      if (options?.deviceOnly) {
        pref.setBrowser(value)
        return
      }
      return binding.set(value)
    },
  }
}
