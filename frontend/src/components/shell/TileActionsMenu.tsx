import type { Component } from 'solid-js'
import type { SplitOrientation, TileCloseMode } from '~/stores/layout.store'
import Columns2 from 'lucide-solid/icons/columns-2'
import LayoutGrid from 'lucide-solid/icons/layout-grid'
import Rows2 from 'lucide-solid/icons/rows-2'
import X from 'lucide-solid/icons/x'
import { For, Show } from 'solid-js'
import { DropdownMenuItemContent } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import { getShortcutHintsText } from '~/lib/shortcuts/display'
import { closeAffordance } from '~/stores/layout.store'

export interface TileActions {
  canSplit: boolean
  canMakeGrid: boolean
  closeMode: TileCloseMode
  onSplit: (direction: SplitOrientation) => void
  onMakeGrid: (rows: number, cols: number) => void
  onClose: () => void
}

export interface SplitActionDef {
  direction: SplitOrientation
  // Lucide icon component shown beside (or as) the action button.
  icon: typeof Columns2
  label: string
  shortcutId: string
  testId: string
}

/**
 * Single source of truth for the split-action label/icon/shortcut/test-id
 * mapping. Both the dropdown menu (this file) and the inline IconButton row
 * (`Tile.tsx`) iterate over this so the labels can't drift independently.
 *
 * direction names the divider line: 'vertical' makes a `|` divider with two
 * side-by-side panes (icon: Columns2); 'horizontal' makes a `-` divider with
 * two stacked panes (icon: Rows2).
 */
export const SPLIT_ACTIONS: readonly SplitActionDef[] = [
  {
    direction: 'vertical',
    icon: Columns2,
    label: 'Split vertically',
    shortcutId: 'app.splitTileVertical',
    testId: 'split-vertical',
  },
  {
    direction: 'horizontal',
    icon: Rows2,
    label: 'Split horizontally',
    shortcutId: 'app.splitTileHorizontal',
    testId: 'split-horizontal',
  },
]

/**
 * Tile's pop affordance: move the active tab into a floating window
 * ("pop out") or reattach it to the main layout ("pop in"). The producer
 * (`createTileRenderer`) picks `label`/`testId` to match the direction;
 * absent when the tile cannot pop in either direction (workspace
 * archived, no active tab, etc.).
 */
export interface TilePopAction {
  label: string
  testId: string
  onClick: () => void
}

interface TileActionsMenuProps {
  actions: TileActions
  /** Show icons before labels (TabBar overflow) vs plain (Tile tiny menu). */
  withIcons?: boolean
  /** What "Make grid" should do. TabBar wants a fixed 2×2; Tile defers to a popover. */
  onMakeGridClick: () => void
  makeGridLabel: string
  /** Pop out / pop in — only Tile's tiny menu sets this; TabBar omits it. */
  pop?: TilePopAction
}

/**
 * Renders the tile-action menu items as a fragment of <button role="menuitem">
 * elements. The caller composes this inside their own <DropdownMenu> and is
 * responsible for any section headers (e.g. TabBar's "Tile" header) since
 * those vary per surface.
 */
export const TileActionsMenu: Component<TileActionsMenuProps> = (props) => {
  const close = () => closeAffordance(props.actions.closeMode, 'menu')

  return (
    <>
      <Show when={props.pop}>
        {pop => (
          <button
            role="menuitem"
            onClick={() => pop().onClick()}
          >
            <DropdownMenuItemContent
              label={pop().label}
              shortcut={getShortcutHintsText('app.toggleFloatingTab')}
            />
          </button>
        )}
      </Show>
      <Show when={props.actions.canSplit}>
        <For each={SPLIT_ACTIONS}>
          {action => (
            <button
              role="menuitem"
              onClick={() => props.actions.onSplit(action.direction)}
            >
              <Show when={props.withIcons}><Icon icon={action.icon} size="sm" /></Show>
              <DropdownMenuItemContent
                label={action.label}
                shortcut={getShortcutHintsText(action.shortcutId)}
              />
            </button>
          )}
        </For>
      </Show>
      <Show when={props.actions.canMakeGrid}>
        <button
          role="menuitem"
          data-testid="make-grid-menu-item"
          onClick={() => props.onMakeGridClick()}
        >
          <Show when={props.withIcons}><Icon icon={LayoutGrid} size="sm" /></Show>
          <DropdownMenuItemContent label={props.makeGridLabel} />
        </button>
      </Show>
      <Show when={props.actions.closeMode.kind !== 'none'}>
        <button
          role="menuitem"
          data-testid={close().testId}
          onClick={() => props.actions.onClose()}
        >
          <Show when={props.withIcons}><Icon icon={X} size="sm" /></Show>
          <DropdownMenuItemContent label={close().label} />
        </button>
      </Show>
    </>
  )
}
