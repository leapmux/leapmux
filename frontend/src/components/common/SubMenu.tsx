import type { Component, JSX } from 'solid-js'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { createSignal, Show } from 'solid-js'
import { menuSubTrigger, menuSubTriggerLabel } from '~/styles/shared.css'
import { DropdownMenu } from './DropdownMenu'
import { Icon } from './Icon'

export interface SubMenuProps {
  /** The trigger row's label. */
  'label': JSX.Element
  /** What the popover IS. `div` for a panel that must survive a click inside it. */
  'as'?: 'menu' | 'div'
  /** data-testid on the trigger row. */
  'data-testid'?: string
  /** data-testid on the popover. */
  'popoverTestId'?: string
  /** Accessible name for the popover. */
  'aria-label'?: string
  'children': JSX.Element
}

/**
 * A menu item that opens a nested menu.
 *
 * It exists for the `<Show>`, which is not cosmetic. `DropdownMenu` renders its
 * children eagerly, and the parent's keyboard navigation queries
 * `[role=menuitem]` over the WHOLE popover subtree -- so a closed submenu's
 * items are still matched. ArrowDown then calls `.focus()` on a `display:none`
 * element, which is a silent no-op: the focus stalls, and type-ahead matches
 * text nobody can see. Mounting the children only while the submenu is open
 * removes the phantom items and the eager render cost together.
 *
 * Every nested menu in the app goes through this, so no call site can forget
 * the gate. The trigger's chevron and its layout come with it.
 *
 * Known gap: `ArrowRight` does not open a submenu. `DropdownMenu` handles
 * Escape, Up, Down, Home, End and type-ahead only, and the roving focus lands
 * on the trigger, from which Enter opens the popover.
 */
export const SubMenu: Component<SubMenuProps> = (props) => {
  const [open, setOpen] = createSignal(false)
  return (
    <DropdownMenu
      as={props.as}
      onToggle={setOpen}
      data-testid={props.popoverTestId}
      aria-label={props['aria-label']}
      trigger={triggerProps => (
        <button
          type="button"
          role="menuitem"
          class={menuSubTrigger}
          data-testid={props['data-testid']}
          {...triggerProps}
        >
          <span class={menuSubTriggerLabel}>{props.label}</span>
          <Icon icon={ChevronRight} size="xs" />
        </button>
      )}
    >
      <Show when={open()}>{props.children}</Show>
    </DropdownMenu>
  )
}
