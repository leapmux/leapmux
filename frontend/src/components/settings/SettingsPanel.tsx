import type { Component } from 'solid-js'
import type { SettingRowModel } from './types'
import { For, Show } from 'solid-js'
import { Alert } from '~/components/common/Alert'
import { ElevationStatus } from './ElevationStatus'
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
  /**
   * Whether this group holds a row the hub refuses without a recently proven
   * factor. Derived by the dialog from the same rows, by the same rule that
   * derives `restartGroup`.
   */
  elevationGroup: boolean
  /**
   * The failed writes to place on their rows, empty when there are none.
   *
   * A LIST because the desktop shell can refuse two INDEPENDENT choices in one
   * push. An admin store has only ever one failed write, so that caller passes
   * a list of at most one.
   */
  writeErrors: readonly { key: string, message: string }[]
}

/**
 * One category's rows.
 *
 * The panel RENDERS; it does not decide what to render. Rows arrive built
 * and filtered from the dialog, which is also the source of the navigation
 * and the search index — so a row can no longer be searchable, or make a nav
 * group occupied, while this panel drops it. Building them here as well was a
 * second construction path over the same descriptors, and the two filters
 * already diverged.
 */
export const SettingsPanel: Component<SettingsPanelProps> = (props) => {
  const storeError = (id: string): string | null => {
    // The store keys errors by the proto key (the RPC target). A scalar row's
    // id is that key; an object-shaped setting renders one row per field as
    // `${key}.${field}`. Prefix-matching would paint every sibling field with
    // one field's validation error (queue_budget.relay_bytes failing also
    // marked worker_bytes and userevents_bytes). Object-field errors surface
    // from SettingRow's own catch of the failed set.
    //
    // A registry row cannot collide with a hub key here, although the dialog
    // passes errors for a USER group too (the desktop shell's refusals, on
    // `desktop.trayEnabled` or `desktop.startOnLogin`). A registry id is
    // dotted and a hub key is a bare snake_case name, so the two id spaces do
    // not meet -- and only one of the two sources can be in play at a time,
    // because a group is either admin or not.
    return props.writeErrors.find(e => e.key === id)?.message ?? null
  }

  return (
    <div role="tabpanel" id="preferences-panel" class={styles.panel}>
      {/*
        The verification state sits ABOVE the rows it explains, in every group
        that holds one the hub refuses without a proven factor -- the Account
        group and every ADMINISTRATION group, since the hub requires the same
        window for every settings write.

        It lived inside ONE account editor before, which put it half way down
        the Account panel under a Save button it had nothing to do with, and
        left every admin panel without it although the same window governs
        them. The panel is the layer that knows which group is on screen, so
        the panel is where it belongs.

        `ElevationStatus` renders nothing while the session is not verified,
        so a group that needs one does not carry an empty box.
      */}
      <Show when={props.elevationGroup}>
        <ElevationStatus />
      </Show>
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
