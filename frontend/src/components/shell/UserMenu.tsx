import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { PreferencesDialog } from '~/components/settings/PreferencesDialog'
import { ProfileDialog } from '~/components/settings/ProfileDialog'
import { setShowPreferencesDialog, setShowProfileDialog, showPreferencesDialog, showProfileDialog } from './UserMenuState'

/**
 * Renders dialogs triggered from the menu. Mount once in a stable parent so
 * open dialogs survive menu instance recreation (e.g. after a sidebar
 * re-render triggered by `auth.refreshUser()`).
 */
export const UserMenuDialogs: Component = () => (
  <>
    <Show when={showProfileDialog()}>
      <ProfileDialog onClose={() => setShowProfileDialog(false)} />
    </Show>
    <Show when={showPreferencesDialog()}>
      <PreferencesDialog onClose={() => setShowPreferencesDialog(false)} />
    </Show>
  </>
)
