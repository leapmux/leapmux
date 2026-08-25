import type { Component } from 'solid-js'
import { Show } from 'solid-js'
import { PreferencesDialog } from '~/components/settings/PreferencesDialog'
import { useAuth } from '~/context/AuthContext'
import { closePreferences, preferencesOpenSeq, showPreferencesDialog } from './UserMenuState'

/**
 * Renders dialogs triggered from the menu. Mount once in a stable parent so
 * open dialogs survive menu instance recreation (e.g. after a sidebar
 * re-render triggered by `auth.refreshUser()`).
 *
 * IT NEEDS AN IDENTITY, and the guard is here rather than at each caller. The
 * dialog edits one account's preferences: the device tier is stored under that
 * account, so with none set every browser-only row throws on its first write and
 * the key-pin section throws at render. This is the ONE mount of the dialog, so
 * one guard here covers every way it opens -- including the macOS Apple-menu
 * item, which `app.tsx` registers for the whole desktop session and which is
 * therefore live on `/login` and `/setup`. The in-shell callers already sit
 * inside `AuthGuard`, so the guard changes nothing for them.
 */
export const UserMenuDialogs: Component = () => {
  const auth = useAuth()
  return (
    <Show when={auth.isAuthenticated() && showPreferencesDialog()}>
      {category => (
        <PreferencesDialog category={category()} openSeq={preferencesOpenSeq()} onClose={closePreferences} />
      )}
    </Show>
  )
}
