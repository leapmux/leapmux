import type { Component } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { For, Show } from 'solid-js'
import { platformBridge } from '~/api/platformBridge'
import { DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { getShortcutHint } from '~/lib/shortcuts/display'
import { isDesktopApp, isSoloMode } from '~/lib/systemInfo'
import { dangerMenuItem, menuSectionHeader } from '~/styles/shared.css'
import * as styles from './UserMenuItems.css'
import {
  setShowAboutDialog,
  setShowPreferencesDialog,
  setShowProfileDialog,
} from './UserMenuState'

interface UserMenuItemsProps {
  aboutLabel?: string
}

export const UserMenuItems: Component<UserMenuItemsProps> = (props) => {
  const auth = useAuth()
  const org = useOrg()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await auth.logout()
    navigate('/login', { replace: true })
  }

  const handleSwitchMode = async () => {
    const overlay = document.createElement('div')
    const bg = getComputedStyle(document.documentElement).getPropertyValue('--background').trim() || '#000'
    overlay.style.cssText = `position:fixed;inset:0;z-index:2147483647;background:${bg};opacity:0;transition:opacity .3s ease`
    document.body.appendChild(overlay)
    await new Promise<void>((resolve) => {
      requestAnimationFrame(() => {
        overlay.style.opacity = '1'
      })
      overlay.addEventListener('transitionend', () => resolve(), { once: true })
      setTimeout(resolve, 400)
    })

    await platformBridge.switchMode()

    overlay.remove()
    ;(window as any).__leapmux_disconnectDesktop?.()
  }

  return (
    <>
      <li class={menuSectionHeader}>App</li>
      <Show when={!isSoloMode()}>
        <button role="menuitem" onClick={() => setShowProfileDialog(true)}>
          Profile...
        </button>
      </Show>
      <button role="menuitem" onClick={() => setShowAboutDialog(true)}>
        {props.aboutLabel ?? 'About...'}
      </button>
      <button role="menuitem" onClick={() => setShowPreferencesDialog(true)}>
        <DropdownMenuItemContent label="Preferences..." shortcut={getShortcutHint('app.openPreferences')} />
      </button>

      <Show when={!isSoloMode()}>
        <hr />
        <li class={menuSectionHeader}>Switch Organization</li>
        <div class={styles.orgList}>
          <For each={org.orgs()}>
            {o => (
              <button
                role="menuitem"
                class={styles.orgItem}
                data-active={o.name === org.slug() ? '' : undefined}
                onClick={() => navigate(`/o/${o.name}`)}
              >
                {o.name}
                <Show when={o.isPersonal}>
                  <span class={styles.personalTag}>(personal)</span>
                </Show>
              </button>
            )}
          </For>
        </div>
        <button role="menuitem" class={dangerMenuItem} onClick={() => handleLogout()}>
          Log out
        </button>
      </Show>

      <Show when={isDesktopApp()}>
        <hr />
        <li class={menuSectionHeader}>Desktop</li>
        <button role="menuitem" onClick={handleSwitchMode}>
          Switch mode...
        </button>
      </Show>
    </>
  )
}
