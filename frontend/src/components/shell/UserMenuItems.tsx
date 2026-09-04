import type { Component } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { Show } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { useAuth } from '~/context/AuthContext'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import { isAutoAuthenticated, isDesktopApp } from '~/lib/systemInfo'
import { dangerMenuItem } from '~/styles/shared.css'
import { motion } from '~/styles/tokens'
import { openPreferences, setShowAboutDialog } from './UserMenuState'

export const AppAboutMenuItem: Component = () => (
  <button role="menuitem" onClick={() => setShowAboutDialog(true)}>
    {isDesktopApp() ? 'About LeapMux Desktop...' : 'About...'}
  </button>
)

export const UserMenuItems: Component = () => {
  const auth = useAuth()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await auth.logout()
    navigate('/login', { replace: true })
  }

  const handleSwitchMode = async () => {
    const overlay = document.createElement('div')
    const bg = getComputedStyle(document.documentElement).getPropertyValue('--background').trim() || '#000'
    overlay.style.cssText = `position:fixed;inset:0;z-index:2147483647;background:${bg};opacity:0;transition:opacity var(--transition)`
    document.body.appendChild(overlay)
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        overlay.style.opacity = '1'
      })
      const fallback = setTimeout(resolve, motion.medium + 100)
      overlay.addEventListener('transitionend', () => {
        clearTimeout(fallback)
        resolve()
      }, { once: true })
    })

    try {
      await platformBridge.switchMode()
    }
    finally {
      overlay.remove()
    }
    window.__leapmux_disconnectDesktop?.()
  }

  return (
    <>
      <AppAboutMenuItem />
      <button role="menuitem" onClick={() => openPreferences()}>
        <DropdownMenuItemContent label="Preferences..." detail={getShortcutHintsText('app.openPreferences')} />
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
