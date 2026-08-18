import type { JSX } from 'solid-js'
import { PreferencesProvider } from '~/context/PreferencesContext'

/**
 * Wrap a component-test render tree in a PreferencesProvider, for components
 * that read preferences through `usePreferences`.
 *
 * The provider's mount-time `listUserSettings` load rejects quietly in jsdom
 * (no hub) and is caught by the provider itself, so tests that do not mock
 * `~/api/clients` still render against the default account values.
 */
export function withPreferences(ui: () => JSX.Element): () => JSX.Element {
  return () => <PreferencesProvider>{ui()}</PreferencesProvider>
}
