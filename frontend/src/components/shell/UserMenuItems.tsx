import type { Component } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { Show } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { useAuth } from '~/context/AuthContext'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import { isDesktopApp, isSoloMode } from '~/lib/systemInfo'
import { dangerMenuItem } from '~/styles/shared.css'
import { motion } from '~/styles/tokens'
import {
  setShowAboutDialog,
  setShowPreferencesDialog,
  setShowProfileDialog,
} from './UserMenuState'

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
      <Show when={!isSoloMode()}>
        <button role="menuitem" onClick={() => setShowProfileDialog(true)}>
          Profile...
        </button>
      </Show>
      <AppAboutMenuItem />
      <button role="menuitem" onClick={() => setShowPreferencesDialog(true)}>
        <DropdownMenuItemContent label="Preferences..." shortcut={getShortcutHintsText('app.openPreferences')} />
      </button>

      <Show when={!isSoloMode()}>
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
