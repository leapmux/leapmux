import type { JSX } from 'solid-js'
import type { DropdownTriggerProps } from './DropdownMenu'
import MoreHorizontal from 'lucide-solid/icons/more-horizontal'
import { menuTrigger } from '~/components/tree/sidebarActions.css'
import { IconButton } from './IconButton'

export interface MoreHorizontalTriggerOptions {
  'class'?: string
  'data-testid'?: string
}

// Builds the DropdownMenu `trigger` render-prop for the standard
// "three-dot" affordance: a small IconButton that swallows the row's
// click/pointerdown so the popover opens without selecting the row.
// The pointerdown/click toggle dance (capturing wasOpenOnPointerDown
// before light-dismiss) lives in DropdownMenu's internal handlers —
// see handleTriggerPointerDown / handleTriggerClick there — so this
// helper just spreads the triggerProps it receives.
export function moreHorizontalTrigger(
  opts: MoreHorizontalTriggerOptions = {},
): (triggerProps: DropdownTriggerProps) => JSX.Element {
  return triggerProps => (
    <IconButton
      icon={MoreHorizontal}
      size="sm"
      class={opts.class}
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

// rowContextMenuTrigger is the standard "three-dot, styled with the
// sidebar's row-menu CSS class" trigger used by every per-row context
// menu (BranchContextMenu, WorkspaceContextMenu, WorkerContextMenu,
// TunnelContextMenu). Bakes in `class: menuTrigger` so each call site
// reduces to `<DropdownMenu trigger={rowContextMenuTrigger()}>` — when
// the visual changes (kebab icon, different size, renamed CSS class),
// every row menu picks it up uniformly instead of one of four
// hand-written sites drifting. Callers that need a non-row trigger
// (titlebar, dialog headers) still use `moreHorizontalTrigger` with
// their own class.
export function rowContextMenuTrigger(
  opts: { 'data-testid'?: string } = {},
): (triggerProps: DropdownTriggerProps) => JSX.Element {
  return moreHorizontalTrigger({ 'class': menuTrigger, 'data-testid': opts['data-testid'] })
}
