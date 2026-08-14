import type { Component } from 'solid-js'
import type { ContextMenuTargetProps } from '~/components/common/DropdownMenu'
import { Show } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { dangerMenuItem } from '~/styles/shared.css'

/**
 * The pop-out / pop-in affordance, when the surface offers one. Declared here in
 * the common layer so this component does not depend on the shell; the
 * tile-level affordance is its subtype -- see `TilePopAction` in
 * ~/components/shell/TileActionsMenu.tsx, which adds the `testId` its own
 * trigger carries.
 */
export interface TabPopAction {
  label: string
  onClick: () => void
}

export interface TabContextMenuProps extends ContextMenuTargetProps {
  /** Start the inline rename. Omit to hide the item. */
  'onRename'?: () => void
  /** Close the tab. Omit to hide the item -- a tab the surface refuses to close has no Close. */
  'onClose'?: () => void
  /** A close is already in flight. The item stays visible, so the row's menu does not reshape mid-click. */
  'isClosing'?: boolean
  'pop'?: TabPopAction
  'data-testid'?: string
}

/**
 * The right-click / long-press menu for a tab, shared by the sidebar's tab leaf
 * and the tile tab bar's tab.
 *
 * Every item is an action the row ALREADY performs through some other input --
 * double-click to rename, the hover X or a middle click to close -- promoted to a
 * place a finger and a secondary button can reach. Nothing new is invented here:
 * "close others", "close to the right", "duplicate" and "pin" have no
 * implementation anywhere in the app, and adding them is a different piece of work
 * from making the existing actions reachable.
 *
 * Neither tab row gets a kebab. Both already spend their one hover-revealed action
 * slot on the close X, and a second button beside it would widen every row and
 * duplicate Close.
 *
 * Renders nothing when no item would appear. A menu that opens empty is worse than
 * a right-click that falls through to the browser's own.
 */
export const TabContextMenu: Component<TabContextMenuProps> = (props) => {
  const hasAnyItem = () => Boolean(props.onRename || props.onClose || props.pop)

  return (
    <Show when={hasAnyItem()}>
      <DropdownMenu contextMenuFor={props.contextMenuFor} data-testid={props['data-testid']}>
        <Show when={props.onRename}>
          <button role="menuitem" data-testid="tab-menu-rename" onClick={() => props.onRename!()}>
            Rename
          </button>
        </Show>

        <Show when={props.pop}>
          {pop => (
            <button role="menuitem" data-testid="tab-menu-pop" onClick={() => pop().onClick()}>
              {pop().label}
            </button>
          )}
        </Show>

        <Show when={props.onClose}>
          {/* Only when something precedes it, so a Close-only menu has no leading rule. */}
          <Show when={props.onRename || props.pop}>
            <hr />
          </Show>
          <button
            role="menuitem"
            class={dangerMenuItem}
            data-testid="tab-menu-close"
            disabled={props.isClosing}
            onClick={() => {
              if (!props.isClosing)
                props.onClose!()
            }}
          >
            Close
          </button>
        </Show>
      </DropdownMenu>
    </Show>
  )
}
