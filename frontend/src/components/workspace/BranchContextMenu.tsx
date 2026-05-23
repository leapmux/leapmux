import type { Component } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { dangerMenuItem } from '~/styles/shared.css'

interface BranchContextMenuProps {
  onChangeBranch: () => void
  onDeleteBranch: () => void
}

// Per-row trigger+children wrapper around DropdownMenu. The menu items
// close over their row's branch data via the calling component's
// closure, so there's no shared overlay state to thread.
export const BranchContextMenu: Component<BranchContextMenuProps> = props => (
  <DropdownMenu trigger={rowContextMenuTrigger()}>
    <button role="menuitem" onClick={() => props.onChangeBranch()}>
      Change branch...
    </button>
    <hr />
    <button role="menuitem" class={dangerMenuItem} onClick={() => props.onDeleteBranch()}>
      Delete branch...
    </button>
  </DropdownMenu>
)
