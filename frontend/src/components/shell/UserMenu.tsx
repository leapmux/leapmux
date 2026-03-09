import type { Component, JSX } from 'solid-js'
import { useNavigate } from '@solidjs/router'
import { createSignal, For, Show } from 'solid-js'
import { AdminDialog } from '~/components/admin/AdminDialog'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { PreferencesDialog } from '~/components/settings/PreferencesDialog'
import { useAuth } from '~/context/AuthContext'
import { useOrg } from '~/context/OrgContext'
import { dangerMenuItem, menuSectionHeader } from '~/styles/shared.css'
import * as styles from './UserMenu.css'

interface UserMenuProps {
  /** Custom trigger element. When provided, the default container and trigger are replaced. */
  trigger?: JSX.Element
}

export const UserMenu: Component<UserMenuProps> = (props) => {
  const auth = useAuth()
  const org = useOrg()
  const navigate = useNavigate()

  const [showPreferencesDialog, setShowPreferencesDialog] = createSignal(false)
  const [showAdminDialog, setShowAdminDialog] = createSignal(false)

  const handleLogout = async () => {
    await auth.logout()
    navigate('/login', { replace: true })
  }

  const renderMenuItems = () => (
    <>
      <button role="menuitem" onClick={() => navigate(`/o/${org.slug()}/workers`)}>
        Workers
      </button>
      <button role="menuitem" onClick={() => setShowPreferencesDialog(true)}>
        Preferences
      </button>
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
      <Show when={auth.user()?.isAdmin}>
        <button role="menuitem" onClick={() => setShowAdminDialog(true)}>
          Administration
        </button>
      </Show>
      <button role="menuitem" class={dangerMenuItem} onClick={() => handleLogout()}>
        Log out
      </button>
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
                  {auth.user()?.username ?? '...'}
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
      <Show when={showPreferencesDialog()}>
        <PreferencesDialog onClose={() => setShowPreferencesDialog(false)} />
      </Show>
      <Show when={showAdminDialog()}>
        <AdminDialog onClose={() => setShowAdminDialog(false)} />
      </Show>
    </>
  )
}
