import type { Component } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { Show } from 'solid-js'
import { DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { useAuth } from '~/context/AuthContext'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import { requestDesktopSwitchMode } from '~/lib/soloStopConfirm'
import { isAutoAuthenticated, isDesktopApp } from '~/lib/systemInfo'
import { dangerMenuItem } from '~/styles/shared.css'
import { openPreferences, setShowAboutDialog } from './UserMenuState'

export const AppAboutMenuItem: Component = () => (
  <button role="menuitem" onClick={() => setShowAboutDialog(true)}>
    {isDesktopApp()
      ? 'About LeapMux Desktop...'
      : 'About...'}
  </button>
)

export const UserMenuItems: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()
  // Hoisted so the spread narrows; `detail?: string` takes no undefined.
  const preferencesHint = getShortcutHintsText('app.openPreferences')

  const handleLogout = async () => {
    await auth.logout()
    navigate('/login', { replace: true })
  }

  const handleSwitchMode = () => {
    void requestDesktopSwitchMode()
  }

  return (
    <>
      <AppAboutMenuItem />
      <button role="menuitem" onClick={() => openPreferences()}>
        <DropdownMenuItemContent
          label="Preferences..."
          {...(preferencesHint !== undefined
            ? { detail: preferencesHint }
            : {})}
        />
      </button>

      {/*
        * isAutoAuthenticated, not isSoloMode. Log out ends a SESSION, and a
        * credential-free connection holds none: the desktop app's local socket,
        * or a solo hub whose account has no password yet. Signing out there
        * lands on /login, which sends the visitor straight back.
        *
        * A solo hub whose account HOLDS a password is the other case, and it
        * needs the item: its network callers sign in with a real session, and
        * without this they could never end one.
        */}
      <Show when={!isAutoAuthenticated()}>
        <hr />
        <button role="menuitem" class={dangerMenuItem} onClick={() => handleLogout()}>
          Log out
        </button>
      </Show>

      <Show when={isDesktopApp()}>
        <hr />
        <button role="menuitem" onClick={handleSwitchMode}>
          Switch mode...
        </button>
      </Show>
    </>
  )
}
