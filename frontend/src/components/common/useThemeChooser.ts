import type { DualPreference } from '~/context/PreferencesContext'
import type { TerminalThemeValue, ThemeValue } from '~/styles/themes'
import { dualScalar } from '~/components/settings/registry/bindings'
import { usePreferences } from '~/context/PreferencesContext'

export interface ThemeChooserBinding<T> {
  value: () => T
  onChange: (value: T) => void | Promise<boolean | void>
}

/**
 * Bind a `<ThemeChooser>` to the `theme` preference.
 *
 * THE SINGLE WRITER. Every surface that shows the chooser calls this and
 * nothing else, so the Preferences dialog and the no-workspace empty state
 * write the same preference, in the same shape, to the same place. A theme
 * picked on the empty state is the theme the dialog shows afterwards, and it
 * survives reloading and restarting.
 *
 * The write goes through `dualScalar` — literally the binding the dialog's own
 * row uses — so the tier decision is one implementation shared by both: the
 * device tier when the key is already overridden on this device, otherwise the
 * account tier. Restating that rule here would be a second authority on which
 * tier a write lands on, and the scope chip would start describing writes it
 * did not make.
 *
 * THERE IS NO PROVIDER-LESS BRANCH, and one could not work. Every stored theme
 * is scoped to an account, so a surface that renders before the identity
 * resolves — the desktop launcher, `/login`, `/setup` — has nothing to read and
 * nowhere to write. Those surfaces carry no theme control at all: they paint
 * the default palette at the OS polarity, which `~/lib/themeStore` answers on
 * its own, with no provider and no stored value.
 */
export function useThemeChooser(): ThemeChooserBinding<ThemeValue> {
  const preferences = usePreferences()
  return providerBinding(preferences.dual.theme, preferences.theme)
}

/**
 * The same binding for the TERMINAL theme.
 *
 * The terminal row exists only in the Preferences dialog. The empty state
 * deliberately shows one control -- the terminal defaults to `match-ui`, so
 * choosing a palette there moves the terminal with it, and offering a second
 * picker before the user opens a terminal would be noise.
 */
export function useTerminalThemeChooser(): ThemeChooserBinding<TerminalThemeValue> {
  const preferences = usePreferences()
  return providerBinding(preferences.dual.terminalTheme, preferences.terminalTheme)
}

/**
 * The same binding for the SYNTAX theme.
 *
 * Dialog-only, for the same reason the terminal row is.
 */
export function useSyntaxThemeChooser(): ThemeChooserBinding<TerminalThemeValue> {
  const preferences = usePreferences()
  return providerBinding(preferences.dual.syntaxTheme, preferences.syntaxTheme)
}

/**
 * The tiered write, shared by all three rows so one rule decides the tier.
 *
 * It takes the RESOLVED accessor beside the preference because `dualScalar`
 * returns the non-generic `DualBinding`, whose `value` is an
 * `Accessor<unknown>`. `<ThemeChooser>` needs a typed value, so this is where
 * the type is re-attached; without it every control would carry a cast.
 */
function providerBinding<T>(
  pref: DualPreference<T>,
  resolved: () => T,
): ThemeChooserBinding<T> {
  const binding = dualScalar(pref)
  return {
    value: resolved,
    onChange: value => binding.set(value),
  }
}
