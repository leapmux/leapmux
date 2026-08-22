import type { Component, JSX } from 'solid-js'
import { For, Show } from 'solid-js'
import { showInfoToast } from '~/components/common/Toast'
import { copyTextToClipboard } from '~/lib/clipboard'
import * as styles from './MenuInfoRows.css'

export interface MenuInfoRow {
  /** Row label, including its trailing colon. */
  label: string
  value: JSX.Element
}

export interface MenuInfoRowsProps {
  rows: MenuInfoRow[]
}

/**
 * A label/value grid for the informational block at the top of a context menu.
 *
 * Presentation only, and normally reached through {@link MenuInfoButton} rather
 * than directly -- that wrapper is what makes the block behave inside a menu.
 */
export const MenuInfoRows: Component<MenuInfoRowsProps> = props => (
  <span class={styles.infoGrid}>
    <For each={props.rows}>
      {row => (
        <>
          <span>{row.label}</span>
          <span>{row.value}</span>
        </>
      )}
    </For>
  </span>
)

export interface MenuInfoButtonProps {
  'rows': MenuInfoRow[]
  /** Text copied to the clipboard when the block is activated. */
  'copyText': () => string
  /** Toast shown after a successful copy. */
  'toastMessage': string
  'data-testid'?: string
}

/**
 * The informational block at the top of a context menu: a label/value grid that
 * copies its values when activated.
 *
 * It is a real `role="menuitem"` button, not inert markup, and that is
 * structural rather than decorative. A `DropdownMenu` in its default `menu`
 * mode dismisses on every click inside it, so a press on plain text would start
 * a selection and the release would close the menu under the pointer. Giving
 * the block an action of its own makes that dismissal correct instead of
 * surprising.
 *
 * The button lives here rather than at each call site so that rule is enforced
 * by construction. Both callers used to hand-write the wrapper, the class, the
 * copy and the toast, and they had already drifted apart on the test id.
 *
 * Renders nothing when there are no rows, so a caller can pass a possibly-empty
 * list without guarding.
 */
export const MenuInfoButton: Component<MenuInfoButtonProps> = (props) => {
  // Routed through `copyTextToClipboard`, which falls back to `execCommand`
  // where the platform exposes no `navigator.clipboard` -- any non-secure
  // origin -- and names the cause on screen when neither path works. The
  // success toast below waits for the write to land rather than assuming it did.
  const copy = async () => {
    if (await copyTextToClipboard(props.copyText()))
      showInfoToast(props.toastMessage)
  }

  return (
    <Show when={props.rows.length > 0}>
      <button
        role="menuitem"
        class={styles.infoButton}
        data-testid={props['data-testid']}
        onClick={() => void copy()}
      >
        <MenuInfoRows rows={props.rows} />
      </button>
    </Show>
  )
}
