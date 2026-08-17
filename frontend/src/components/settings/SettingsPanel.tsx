import type { Component } from 'solid-js'
import type { SettingRowModel } from './types'
import { For, Show } from 'solid-js'
import { Alert } from '~/components/common/Alert'
import { SettingRow } from './SettingRow'
import * as styles from './SettingsPanel.css'

export interface SettingsPanelProps {
  /**
   * This category's rows, already built, filtered and visible.
   *
   * ONE list, whichever source each row came from. Two lists meant two
   * `<For>` blocks over two prop shapes, and the registry half arrived as
   * descriptors joined to their bindings by an id-keyed record — a lookup
   * that yields `undefined` for an id the record does not hold.
   */
  rows: readonly SettingRowModel[]
  /**
   * Whether this group holds a restart-class setting the user can SEE. The
   * dialog derives it, because the nav marks the same groups from the same
   * rule — one statement, so the badge in the list and the warning in the
   * panel cannot disagree.
   */
  restartGroup: boolean
  /** The owning store's most recent failed write, or null. */
  writeError: { key: string, message: string } | null
}

/**
 * One category's rows.
 *
 * The panel RENDERS; it does not decide what to render. Rows arrive built
 * and filtered from the dialog, which is also what the navigation and the
 * search index are computed from — so a row can no longer be searchable,
 * or make a nav group occupied, while this panel drops it. Building them
 * here as well was a second construction path over the same descriptors,
 * and the two filters had already diverged.
 */
export const SettingsPanel: Component<SettingsPanelProps> = (props) => {
  const storeError = (id: string): string | null => {
    const writeError = props.writeError
    // The store keys errors by the proto key (the RPC target). A scalar row's
    // id is that key; an object-shaped setting renders one row per field as
    // `${key}.${field}`. Prefix-matching would paint every sibling field with
    // one field's validation error (queue_budget.relay_bytes failing also
    // marked worker_bytes and userevents_bytes). Object-field errors surface
    // from SettingRow's own catch of the failed set.
    //
    // A registry row cannot collide with a hub key here: the dialog passes
    // a `writeError` only while the active group is an admin one, and only
    // the hub scope has a store that records one.
    return writeError !== null && id === writeError.key ? writeError.message : null
  }

  return (
    <div role="tabpanel" id="preferences-panel" class={styles.panel}>
      <Show when={props.restartGroup}>
        <Alert variant="warning">Changes in this group apply after a hub restart.</Alert>
      </Show>
      <For each={props.rows}>
        {row => (
          <SettingRow descriptor={row.descriptor} binding={row.binding} error={storeError(row.descriptor.id)} />
        )}
      </For>
    </div>
  )
}
