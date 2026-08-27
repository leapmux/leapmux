import type { Component, JSX } from 'solid-js'
import type { ContextMenuTargetProps, DropdownTriggerProps } from '~/components/common/DropdownMenu'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { Tooltip } from '~/components/common/Tooltip'
import { dangerMenuItem } from '~/styles/shared.css'

interface BranchContextMenuProps extends ContextMenuTargetProps {
  'onChangeBranch': () => void
  'onDeleteBranch': () => void
  /**
   * Why both items are unusable, or undefined when they are usable. Both
   * actions need the Worker -- one to read the branch state, the other to
   * mutate it -- so one reason disables both rather than one item at a time.
   *
   * The reason is the whole point of disabling rather than hiding: a row that
   * silently loses its menu reads as a bug, whereas a dimmed item that states a
   * reason says which machine to bring back. Dimming and the not-allowed cursor
   * come from oat's global `:disabled` rule, so `disabled` is the whole styling
   * contract here.
   *
   * Known limitation, accepted deliberately: a natively disabled button is out
   * of the focus order, so a keyboard user cannot open the tooltip from the
   * keyboard. `<Tooltip>` still leaves the reason in `aria-describedby` for as
   * long as the control is disabled, which is the route a screen reader takes.
   * `aria-disabled` plus a click guard would keep it focusable, but every other
   * menu in the app uses native `disabled` and takes its styling from that
   * global rule -- so a switch here alone would split the pattern and need its
   * own `[aria-disabled]` styling. Move all the menus together instead.
   */
  'disabledReason'?: string
  /**
   * DropdownMenu `trigger` render-prop for the open affordance. Defaults to the
   * sidebar's kebab (`rowContextMenuTrigger()`); the composer's branch chip
   * supplies a branch-name button here so the same menu items surface from a
   * different trigger.
   */
  'trigger'?: (triggerProps: DropdownTriggerProps) => JSX.Element
  /** data-testid applied to the popover element. */
  'data-testid'?: string
}

// Per-row trigger+children wrapper around DropdownMenu. The menu items
// close over their row's branch data via the calling component's
// closure, so there's no shared overlay state to thread.
export const BranchContextMenu: Component<BranchContextMenuProps> = props => (
  <DropdownMenu
    trigger={props.trigger ?? rowContextMenuTrigger()}
    contextMenuFor={props.contextMenuFor}
    data-testid={props['data-testid']}
  >
    {/*
      The reason goes through <Tooltip>, which works on a disabled control and
      leaves the item its own name. A `title` this long BECOMES the accessible
      name, so a screen reader announced the reason in place of "Change
      branch...".
    */}
    <Tooltip text={props.disabledReason}>
      <button
        role="menuitem"
        disabled={Boolean(props.disabledReason)}
        onClick={() => props.onChangeBranch()}
      >
        Change branch...
      </button>
    </Tooltip>
    <hr />
    <Tooltip text={props.disabledReason}>
      <button
        role="menuitem"
        class={dangerMenuItem}
        disabled={Boolean(props.disabledReason)}
        onClick={() => props.onDeleteBranch()}
      >
        Delete branch...
      </button>
    </Tooltip>
  </DropdownMenu>
)
