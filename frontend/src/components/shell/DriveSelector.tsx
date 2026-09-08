import type { Component } from 'solid-js'
import ChevronDown from 'lucide-solid/icons/chevron-down'
import HardDrive from 'lucide-solid/icons/hard-drive'
import { createMemo, For } from 'solid-js'
import { DropdownMenu, DropdownMenuCheckableItem } from '~/components/common/DropdownMenu'
import { Icon } from '~/components/common/Icon'
import * as styles from './DriveSelector.css'

export interface DriveSelectorProps {
  /** The root the tree is currently on, e.g. `C:\` or `\\srv\share\`. */
  value: string
  /** The roots the worker reported. May not contain `value`. */
  roots: readonly string[]
  /** Called with the chosen root. The caller makes it the selected path. */
  onSelect: (root: string) => void
}

/**
 * The drive half of the directory picker's location bar, left of the path box.
 *
 * WINDOWS ONLY, and the CALLER decides that -- this component never asks about
 * the OS. `DirectorySelector` shows it only when the worker's reported flavor
 * is win32, which is also what excludes WSL and Docker workers: both report
 * `linux`, and both really do have a single `/`.
 *
 * A `DropdownMenu` of radio items, never a native `<select>`. A drive list is
 * dynamic and unbounded -- mapped network drives and removable media come and
 * go -- so it is the second of the two shapes this project allows.
 */
export const DriveSelector: Component<DriveSelectorProps> = (props) => {
  // This renders for a Windows worker alone, so a case-insensitive comparison
  // is the CORRECT one here, not a shortcut.
  const sameRoot = (a: string, b: string) => a.toLowerCase() === b.toLowerCase()

  const options = createMemo(() => {
    // The current root may not be in the reported list: Windows does not
    // enumerate a UNC share as a logical drive, and a drive may be mapped
    // after the fetch. Leading with it keeps the trigger's own value
    // selectable; without it every row is unchecked and the current location
    // becomes unreachable after one stray click.
    const roots = props.roots
    return roots.some(r => sameRoot(r, props.value)) ? [...roots] : [props.value, ...roots]
  })

  // A backslash and a colon are awkward to address in a selector, and the
  // letter already identifies the drive.
  const optionTestId = (root: string) => `drive-option-${root.replace(/[^a-z0-9]/gi, '').toLowerCase()}`

  return (
    <DropdownMenu
      class={styles.menu}
      // A <menu> of radio items carries no name of its own, and neither does
      // the trigger: its text is a bare drive letter.
      aria-label="Drive"
      data-testid="drive-selector-menu"
      trigger={triggerProps => (
        <button
          type="button"
          class={styles.trigger}
          aria-label="Drive"
          aria-expanded={triggerProps['aria-expanded']}
          data-testid="drive-selector-trigger"
          ref={triggerProps.ref}
          onPointerDown={triggerProps.onPointerDown}
          onClick={triggerProps.onClick}
        >
          <Icon icon={HardDrive} size="sm" class={styles.triggerIcon} />
          <span class={styles.triggerValue}>{props.value}</span>
          <ChevronDown size={16} class={styles.triggerChevron} />
        </button>
      )}
    >
      <For each={options()}>
        {root => (
          <DropdownMenuCheckableItem
            kind="radio"
            label={root}
            checked={sameRoot(root, props.value)}
            data-testid={optionTestId(root)}
            onSelect={() => props.onSelect(root)}
          />
        )}
      </For>
    </DropdownMenu>
  )
}
