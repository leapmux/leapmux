import type { Component } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { dangerMenuItem } from '~/styles/shared.css'

interface BranchContextMenuProps {
  onChangeBranch: () => void
  onDeleteBranch: () => void
  /**
   * Why both items are unusable, or undefined when they are usable. Both
   * actions need the Worker -- one to read the branch state, the other to
   * mutate it -- so they are gated together rather than item by item.
   *
   * The reason is the whole point of disabling rather than hiding: a row that
   * silently loses its menu reads as a bug, whereas a dimmed item with a title
   * says which machine to bring back. Dimming and the not-allowed cursor come
   * from oat's global `:disabled` rule, so `disabled` is the whole styling
   * contract here.
   *
   * Known limitation, accepted deliberately: a natively disabled button is out
   * of the focus order, so a keyboard or screen-reader user cannot reach the
   * title and gets no explanation. `aria-disabled` plus a click guard would keep
   * it focusable, but every other menu in the app uses native `disabled` and
   * takes its styling from that global rule -- so switching this one alone would
   * split the pattern and need its own `[aria-disabled]` styling. If this is
   * revisited, move all the menus together.
   */
  disabledReason?: string
}

// Per-row trigger+children wrapper around DropdownMenu. The menu items
// close over their row's branch data via the calling component's
// closure, so there's no shared overlay state to thread.
export const BranchContextMenu: Component<BranchContextMenuProps> = props => (
  <DropdownMenu trigger={rowContextMenuTrigger()}>
    <button
      role="menuitem"
      disabled={Boolean(props.disabledReason)}
      title={props.disabledReason}
      onClick={() => props.onChangeBranch()}
    >
      Change branch...
    </button>
    <hr />
    <button
      role="menuitem"
      class={dangerMenuItem}
      disabled={Boolean(props.disabledReason)}
      title={props.disabledReason}
      onClick={() => props.onDeleteBranch()}
    >
      Delete branch...
    </button>
  </DropdownMenu>
)
