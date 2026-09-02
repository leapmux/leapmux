import type { Component } from 'solid-js'
import { ThemeChooser } from '~/components/common/ThemeChooser'
import { useTerminalThemeChooser } from '~/components/common/useThemeChooser'
import { usePreferences } from '~/context/PreferencesContext'
import { themeStore } from '~/lib/themeStore'

/**
 * The Appearance section's Terminal theme row: the same `<ThemeChooser>` the UI
 * theme uses, with "Match UI" offered in both halves.
 *
 * A separate ROW from the UI theme, and separate on purpose. The terminal
 * defaults to following the app, so a user who never opens this row gets one
 * coherent look -- but a terminal is a different surface with different habits,
 * and pinning it to a palette, or to dark while the app stays light, are both
 * things people want.
 *
 * The UI theme is passed in because it is what "Match UI" resolves to: it fills
 * the governed mode pills, and it seeds both halves when the user picks a
 * palette of their own instead.
 */
export const TerminalThemeControl: Component = () => {
  const preferences = usePreferences()
  const binding = useTerminalThemeChooser()
  return (
    <ThemeChooser
      value={binding.value()}
      onChange={binding.onChange}
      showLabel={false}
      matchUi={preferences.theme()}
      surface="terminal"
      systemMode={themeStore.systemMode()}
      label="Terminal theme"
    />
  )
}
