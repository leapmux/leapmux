import type { Component } from 'solid-js'
import { ThemeChooser } from '~/components/common/ThemeChooser'
import { useThemeChooser } from '~/components/common/useThemeChooser'
import { themeStore } from '~/lib/themeStore'

/**
 * The Appearance section's Theme row: the same `<ThemeChooser>` the launcher,
 * the setup page and the no-workspace empty state render.
 *
 * A whole-setting custom editor, so it takes no props and binds itself -- the
 * shape every entry in CUSTOM_EDITORS has. `showLabel` is off because
 * `SettingRow` already renders the row's own "Theme" label above it.
 */
export const ThemeControl: Component<Record<string, never>> = () => {
  const binding = useThemeChooser()
  return <ThemeChooser value={binding.value()} onChange={binding.onChange} showLabel={false} systemMode={themeStore.systemMode()} />
}
