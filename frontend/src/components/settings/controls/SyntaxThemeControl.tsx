import type { Component } from 'solid-js'
import { ThemeChooser } from '~/components/common/ThemeChooser'
import { useSyntaxThemeChooser } from '~/components/common/useThemeChooser'
import { usePreferences } from '~/context/PreferencesContext'
import { themeStore } from '~/lib/themeStore'

/**
 * The Appearance section's Syntax theme row: highlighted code in chat, the
 * editor, diffs and file views.
 *
 * The third surface, and the one whose change is not free. A palette and a
 * light/dark mode swap in CSS; a syntax theme cannot, because Shiki bakes the
 * resolved colour into every token span at tokenize time. Changing this
 * re-highlights, which is why it defaults to following the app and why the
 * empty state's single picker does not offer it.
 */
export const SyntaxThemeControl: Component<Record<string, never>> = () => {
  const preferences = usePreferences()
  const binding = useSyntaxThemeChooser()
  return (
    <ThemeChooser
      value={binding.value()}
      onChange={binding.onChange}
      showLabel={false}
      matchUi={preferences.theme()}
      surface="syntax"
      systemMode={themeStore.systemMode()}
      label="Syntax theme"
    />
  )
}
