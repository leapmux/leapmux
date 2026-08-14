import type { Component } from 'solid-js'
import type { FileSortDirection, FileSortKey, FileSortOrder } from '~/lib/fileSort'
import ArrowDownWideNarrow from 'lucide-solid/icons/arrow-down-wide-narrow'
import ArrowUpNarrowWide from 'lucide-solid/icons/arrow-up-narrow-wide'
import { For } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { IconButton } from '~/components/common/IconButton'
import {
  FILE_SORT_DIRECTIONS,
  FILE_SORT_KEYS,
  sortDirectionLabel,
  sortKeyLabel,
} from '~/lib/fileSort'
import { menuSectionHeader } from '~/styles/shared.css'

export interface FilesSortMenuProps {
  sortOrder: () => FileSortOrder
  onChange: (order: FileSortOrder) => void
}

/**
 * The Files section's "Sort by" control: a criterion group and a direction
 * group in one popover.
 */
export const FilesSortMenu: Component<FilesSortMenuProps> = (props) => {
  const setKey = (key: FileSortKey) => props.onChange({ ...props.sortOrder(), key })
  const setDirection = (direction: FileSortDirection) => props.onChange({ ...props.sortOrder(), direction })

  return (
    <DropdownMenu
      // `div`, not the default `menu`: the popover carries TWO independent
      // settings, and a menu dismisses on every click inside it, so choosing a
      // criterion would close the panel before the user can reach the order.
      // Escape and an outside click still dismiss it.
      as="div"
      data-testid="files-sort-menu"
      trigger={triggerProps => (
        <IconButton
          {...triggerProps}
          icon={props.sortOrder().direction === 'desc' ? ArrowDownWideNarrow : ArrowUpNarrowWide}
          iconSize="sm"
          size="sm"
          title={`Sort by ${sortKeyLabel(props.sortOrder().key).toLowerCase()} (${sortDirectionLabel(props.sortOrder().key, props.sortOrder().direction)})`}
          data-testid="files-sort-toggle"
        />
      )}
    >
      {/*
        The popover is a plain `div`, so the menu role lives here — without it
        the `menuitemradio` items below would have no menu to belong to. Each
        group carries its own name, because one label on the popover cannot
        name two groups and six consecutive radios would otherwise announce as
        one six-option group.
      */}
      <div role="menu" aria-label="Sort files">
        <div role="group" aria-label="Sort by">
          <div class={menuSectionHeader} aria-hidden="true">Sort by</div>
          <For each={FILE_SORT_KEYS}>
            {key => (
              <DropdownMenuCheckableItem
                kind="radio"
                label={sortKeyLabel(key)}
                checked={props.sortOrder().key === key}
                data-testid={`files-sort-key-${key}`}
                onSelect={() => setKey(key)}
              />
            )}
          </For>
        </div>
        <hr />
        <div role="group" aria-label="Order">
          <div class={menuSectionHeader} aria-hidden="true">Order</div>
          <For each={FILE_SORT_DIRECTIONS}>
            {direction => (
              <DropdownMenuCheckableItem
                kind="radio"
                label={sortDirectionLabel(props.sortOrder().key, direction)}
                checked={props.sortOrder().direction === direction}
                data-testid={`files-sort-direction-${direction}`}
                onSelect={() => setDirection(direction)}
              />
            )}
          </For>
        </div>
      </div>
    </DropdownMenu>
  )
}
