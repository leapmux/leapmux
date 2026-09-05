import type { Component } from 'solid-js'
import type { ExternalApp } from '~/api/platformBridge'
import Check from 'lucide-solid/icons/check'
import RefreshCw from 'lucide-solid/icons/refresh-cw'
import { createMemo, For, Show } from 'solid-js'
import { ExternalAppIcon } from '~/components/common/ExternalAppIcons'
import { isFileManager } from '~/lib/externalApps'
import { spinner } from '~/styles/animations.css'
import * as styles from './ExternalAppMenuItems.css'

export interface ExternalAppMenuItemsProps {
  /** Every detected application, already sorted by display name. */
  apps: () => ExternalApp[]
  /** The remembered application, which carries the check mark. */
  preferredId: () => string | undefined
  /** Picking a row. Every surface both launches and remembers. */
  onSelect: (id: string) => void
  onRefresh: () => void
  refreshing: () => boolean
  /**
   * Prefix for each row's test id, so the four surfaces that render this list
   * address their own rows rather than whichever copy the DOM holds first.
   */
  testIdPrefix: string
}

/**
 * The "Open in ..." application list: the file manager, then the editors, then
 * a way to re-probe the machine.
 *
 * Rendered identically by the title bar's split button and by every context
 * menu's `Open in ...` submenu, so a user who learns the list once has learned
 * it everywhere.
 *
 * The file manager leads and keeps its own group. It is a different kind of
 * target from an editor, and sorting it in among them by name would file
 * "Finder" between "Cursor" and "Visual Studio Code" for no reason a reader
 * could see. The grouping asks the KIND the sidecar sent -- never an id -- so
 * a platform whose file manager is called something else still groups right.
 */
export const ExternalAppMenuItems: Component<ExternalAppMenuItemsProps> = (props) => {
  const fileManagers = createMemo(() => props.apps().filter(isFileManager))
  const editors = createMemo(() => props.apps().filter(a => !isFileManager(a)))

  const row = (app: ExternalApp) => (
    <button
      type="button"
      role="menuitem"
      class={`${styles.menuItem}${app.id === props.preferredId() ? ` ${styles.menuItemSelected}` : ''}`}
      onClick={() => props.onSelect(app.id)}
      data-testid={`${props.testIdPrefix}-item-${app.id}`}
    >
      <span class={styles.menuItemValue}>
        <ExternalAppIcon id={app.id} size={16} />
        <span>{app.displayName}</span>
      </span>
      <Show when={app.id === props.preferredId()}>
        <Check size={14} class={styles.check} />
      </Show>
    </button>
  )

  return (
    <>
      <For each={fileManagers()}>{row}</For>
      {/* Only between two groups that both have rows: a rule above an empty
          list reads as a menu that lost an item. */}
      <Show when={fileManagers().length > 0 && editors().length > 0}>
        <hr class={styles.menuSeparator} />
      </Show>
      <For each={editors()}>{row}</For>
      <hr class={styles.menuSeparator} />
      <button
        type="button"
        role="menuitem"
        class={styles.menuItem}
        onClick={() => props.onRefresh()}
        disabled={props.refreshing()}
        data-testid={`${props.testIdPrefix}-refresh`}
      >
        <span class={styles.menuItemValue}>
          <RefreshCw size={14} class={props.refreshing() ? spinner : undefined} />
          <span>Refresh app list</span>
        </span>
      </button>
    </>
  )
}
