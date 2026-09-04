import type { Component, JSX } from 'solid-js'
import type { DropdownMenuProps } from './DropdownMenu'
import ChevronRight from 'lucide-solid/icons/chevron-right'
import { createSignal, Show } from 'solid-js'
import { menuSubTrigger, menuSubTriggerLabel } from '~/styles/shared.css'
import { DropdownMenu } from './DropdownMenu'
import { Icon } from './Icon'

/**
 * The `DropdownMenu` props a nested menu may set for itself.
 *
 * `as` carries all three variants, not just `menu` and `div`: the composer's
 * "Agent info" panel is `card`, and a wrapper that cannot express it forces
 * that call site back to a hand-rolled `DropdownMenu` -- which is the per-site
 * drift this component exists to end.
 *
 * `open`, `popoverRef` and `contextMenuFor` are deliberately NOT here. This
 * component owns the open state, because that is what the `<Show>` below reads.
 */
type ForwardedMenuProps = Pick<
  DropdownMenuProps,
  'as' | 'class' | 'placement' | 'matchTriggerWidth' | 'aria-label'
>

export interface SubMenuProps extends ForwardedMenuProps {
  /** The trigger row's label. */
  'label': JSX.Element
  /** data-testid on the trigger row. */
  'data-testid'?: string
  /** data-testid on the popover. */
  'popoverTestId'?: string
  /**
   * Told when this submenu opens or closes.
   *
   * Forwarded rather than consumed: the `<Show>` below is driven by this
   * component's own signal, so a caller that needs to reset state or start a
   * fetch scoped to THIS submenu can still hear about it. Without that, such a
   * caller had to re-implement the whole open gate and lose the fix.
   */
  'onToggle'?: (open: boolean) => void
  'children': JSX.Element
}

/**
 * A menu item that opens a nested menu.
 *
 * It bundles the trigger row -- its chevron and its layout -- with a `<Show>`
 * that mounts the children only while the submenu is open, so a nested menu
 * costs nothing until someone opens it.
 *
 * The `<Show>` is an OPTIMIZATION, not the keyboard fix. `DropdownMenu` renders
 * its children eagerly and its roving focus queries `[role=menuitem]` over the
 * whole popover subtree, but it now skips an item inside a nested popover that
 * is closed -- so a submenu built without this component is still correct for
 * ArrowDown and type-ahead. That is why the composer's three nested menus,
 * which cannot use this component (one needs `as="card"`, one supplies its own
 * trigger, one is a whole component of its own), are safe as they are.
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
      class={props.class}
      placement={props.placement}
      matchTriggerWidth={props.matchTriggerWidth}
      onToggle={(open) => {
        setOpen(open)
        props.onToggle?.(open)
      }}
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
