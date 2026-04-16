import type { Component, JSX } from 'solid-js'
import { Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { PreferencesDialog } from '~/components/settings/PreferencesDialog'
import { ProfileDialog } from '~/components/settings/ProfileDialog'
import { isDesktopApp, isSoloMode } from '~/lib/systemInfo'
import { UserMenuItems } from './UserMenuItems'
import { showAboutDialog, showPreferencesDialog, showProfileDialog, setShowPreferencesDialog, setShowProfileDialog } from './UserMenuState'
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
/** Renders dialogs triggered by UserMenu. Mount once in a stable parent. */
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

export const UserMenu: Component<UserMenuProps> = (props) => {
  return (
    <>
      <Show
        when={props.trigger}
        fallback={(
          <div class={styles.container}>
            <DropdownMenu
              trigger={triggerProps => (
                <button class={styles.trigger} data-testid="user-menu-trigger" {...triggerProps}>
                  {isSoloMode() ? 'Preferences' : 'Menu'}
                </button>
              )}
            >
              <UserMenuItems />
            </DropdownMenu>
          </div>
        )}
      >
        <DropdownMenu trigger={props.trigger}>
          <UserMenuItems />
        </DropdownMenu>
      </Show>
    </>
  )
}
