import type { Component, JSX } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { createSignal, For, Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { PreferencesDialog } from '~/components/settings/PreferencesDialog'
import { ProfileDialog } from '~/components/settings/ProfileDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { isDesktopApp, isSoloMode } from '~/lib/systemInfo'
import { dangerMenuItem, menuSectionHeader } from '~/styles/shared.css'
import { AboutDialog } from './AboutDialog'
import { NoticeDialog } from './NoticeDialog'
import * as styles from './UserMenu.css'

interface UserMenuProps {
  /** Custom trigger element. When provided, the default container and trigger are replaced. */
  trigger?: JSX.Element
}

/**
 * Dialog signals live outside UserMenu so they survive component recreation.
 * UserMenu instances may be destroyed and recreated when the sidebar
 * re-renders (e.g. after auth.refreshUser()), but open dialogs must persist.
 */
const [showProfileDialog, setShowProfileDialog] = createSignal(false)
const [showPreferencesDialog, setShowPreferencesDialog] = createSignal(false)
const [showAboutDialog, setShowAboutDialog] = createSignal(false)
const [showNoticeDialog, setShowNoticeDialog] = createSignal(false)

/** Renders dialogs triggered by UserMenu. Mount once in a stable parent. */
export const UserMenuDialogs: Component = () => (
  <>
    <Show when={showProfileDialog()}>
      <ProfileDialog onClose={() => setShowProfileDialog(false)} />
    </Show>
    <Show when={showPreferencesDialog()}>
      <PreferencesDialog onClose={() => setShowPreferencesDialog(false)} />
    </Show>
    <Show when={showAboutDialog()}>
      <AboutDialog onClose={() => setShowAboutDialog(false)} onShowNotice={() => setShowNoticeDialog(true)} />
    </Show>
    <Show when={showNoticeDialog()}>
      <NoticeDialog onClose={() => setShowNoticeDialog(false)} />
    </Show>
  </>
)

export const UserMenu: Component<UserMenuProps> = (props) => {
  const auth = useAuth()
  const org = useOrg()
  const navigate = useNavigate()

  const handleLogout = async () => {
    await auth.logout()
    navigate('/login', { replace: true })
  }

  const handleSwitchMode = async () => {
    // Fade out the page content.
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

    await window.go?.main?.App?.SwitchMode()
    // Remove the overlay before switching views — the LauncherView
    // handles its own fade-in.
    overlay.remove()
    // Reset SPA state in-place. Wails doesn't re-inject window.go
    // after page reload, so we switch without reloading.
    ;(window as any).__leapmux_disconnectDesktop?.()
  }

  const renderMenuItems = () => (
    <>
      <Show when={!isSoloMode()}>
        <button role="menuitem" onClick={() => setShowProfileDialog(true)}>
          Profile...
        </button>
      </Show>
      <button role="menuitem" onClick={() => setShowPreferencesDialog(true)}>
        Preferences...
      </button>
      <button role="menuitem" onClick={() => setShowAboutDialog(true)}>
        About...
      </button>
      <Show when={!isSoloMode()}>
        <hr />
        <li class={menuSectionHeader}>Switch organization</li>
        <div class={styles.orgList}>
          <For each={org.orgs()}>
            {o => (
              <button
                role="menuitem"
                class={o.name === org.slug() ? styles.orgItemActive : styles.orgItem}
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

  return (
    <>
      <Show
        when={props.trigger}
        fallback={(
          <div class={styles.container}>
            <DropdownMenu
              trigger={triggerProps => (
                <button class={styles.trigger} data-testid="user-menu-trigger" {...triggerProps}>
                  {isSoloMode() ? 'Preferences' : (auth.user()?.username ?? '...')}
                </button>
              )}
            >
              {renderMenuItems()}
            </DropdownMenu>
          </div>
        )}
      >
        <DropdownMenu trigger={props.trigger}>
          {renderMenuItems()}
        </DropdownMenu>
      </Show>
    </>
  )
}
