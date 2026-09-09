import type { JSX } from 'solid-js'
import type { DropdownTriggerProps } from './DropdownMenu'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { menuTrigger } from '~/components/tree/sidebarActions.css'
import { IconButton } from './IconButton'

export interface MoreHorizontalTriggerOptions {
  'class'?: string
  'data-testid'?: string
  /**
   * Tooltip text, which `IconButton` renders as a `<Tooltip ariaLabel>` -- so
   * it is also the button's ACCESSIBLE NAME.
   *
   * A row menu leaves it unset: the row beside it already says what the menu
   * acts on, and every row would otherwise announce the same word. A trigger
   * that stands alone (a section header, a titlebar) must set it, or it
   * announces as an unnamed button.
   */
  'title'?: string
}

// Creates the standard three-dot trigger for a DropdownMenu.
// The trigger uses a 24px IconButton and keeps the default 14px icon.
// It stops row events so the popover opens without selecting the row.
// DropdownMenu owns the pointer-down and click state changes.
// Its `handleTriggerPointerDown` and `handleTriggerClick` capture the open
// state before light dismiss changes it.
// This helper forwards the trigger properties to those handlers.
export function moreHorizontalTrigger(
  opts: MoreHorizontalTriggerOptions = {},
): (triggerProps: DropdownTriggerProps) => JSX.Element {
  return triggerProps => (
    <IconButton
      icon={MoreHorizontal}
      size="md"
      class={opts.class}
      title={opts.title}
      ref={triggerProps.ref}
      aria-expanded={triggerProps['aria-expanded']}
      data-testid={opts['data-testid']}
      onPointerDown={(e: PointerEvent) => {
        e.stopPropagation()
        triggerProps.onPointerDown()
      }}
      onClick={(e: MouseEvent) => {
        e.stopPropagation()
        triggerProps.onClick()
      }}
    />
  )
}

// Creates the standard three-dot trigger for each row context menu.
// It supplies the sidebar's `menuTrigger` class to every row menu.
// Thus, one visual change updates all row menus.
// EVERY per-row context menu uses this trigger, and no other trigger belongs on
// a sidebar row: the row height reserves this button's size, so a row menu that
// picks a different one breaks the rhythm of the list.
// A trigger outside a row uses `moreHorizontalTrigger` with its own class.
// Run `rg 'rowContextMenuTrigger' frontend/src` for the current call sites,
// rather than a list here that drifts as menus are added.
export function rowContextMenuTrigger(
  opts: { 'data-testid'?: string } = {},
): (triggerProps: DropdownTriggerProps) => JSX.Element {
  return moreHorizontalTrigger({ 'class': menuTrigger, 'data-testid': opts['data-testid'] })
}
