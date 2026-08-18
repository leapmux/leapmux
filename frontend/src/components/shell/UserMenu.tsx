import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { PreferencesDialog } from '~/components/settings/PreferencesDialog'
import { closePreferences, preferencesOpenSeq, showPreferencesDialog } from './UserMenuState'

/**
 * Renders dialogs triggered from the menu. Mount once in a stable parent so
 * open dialogs survive menu instance recreation (e.g. after a sidebar
 * re-render triggered by `auth.refreshUser()`).
 */
export const UserMenuDialogs: Component = () => (
  <Show when={showPreferencesDialog()}>
    {category => (
      <PreferencesDialog category={category()} openSeq={preferencesOpenSeq()} onClose={closePreferences} />
    )}
  </Show>
)
