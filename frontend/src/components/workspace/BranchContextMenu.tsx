import type { Component, JSX } from 'solid-js'
import type { BranchMenuActions } from './branchActions'
import type { ContextMenuTargetProps, DropdownTriggerProps } from '~/components/common/DropdownMenu'
import type { ChangeBranchMode } from '~/hooks/useGitModeState'
import { createSignal } from 'solid-js'
import { DropdownMenu } from '~/components/common/DropdownMenu'
import { rowContextMenuTrigger } from '~/components/common/moreHorizontalTrigger'
import { NewTabMenuItems } from '~/components/common/NewTabMenuItems'
import { Tooltip } from '~/components/common/Tooltip'
import { workingTreeDeleteLabel } from '~/components/common/WorkingTree'
import { useAvailableProviders } from '~/hooks/useAvailableProviders'
import { useAvailableShells } from '~/hooks/useAvailableShells'
import { GitMode } from '~/hooks/useGitModeState'
import { dangerMenuItem } from '~/styles/shared.css'

interface BranchContextMenuProps extends ContextMenuTargetProps {
  /** Every action of this menu, already bound to one branch. */
  'actions': BranchMenuActions
  /**
   * The Worker the branch is checked out on. The menu lists THIS Worker's agent
   * providers and shells, which is the whole point of asking for it: the lists
   * the app already holds belong to whichever Worker the active tab sits on,
   * and that is a different machine whenever the row is on another one.
   */
  'workerId': string
  /**
   * True iff the row's checkout is a linked worktree. It names the DELETE item
   * only: deleting a worktree removes a whole directory, and calling that
   * "Delete branch..." is how a user destroys a directory they meant to keep.
   *
   * The three CHANGE items keep their names either way, because a worktree has
   * a branch checked out and the dialog still changes that branch.
   */
  'isWorktree': boolean
  /**
   * Why EVERY item is unusable, or undefined when they are usable. Each action
   * runs on the Worker the repository is on -- one reads the branch state,
   * another mutates it, and the rest start an agent or a terminal there -- so
   * one reason disables the whole menu rather than one item at a time.
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
// close over their row's branch data via the bound `actions` bundle, so
// there's no shared overlay state to thread.
export const BranchContextMenu: Component<BranchContextMenuProps> = (props) => {
  // The two worker lists are fetched ON OPEN, never on mount. The sidebar
  // mounts one of these menus per branch row of every workspace, so a
  // mount-time fetch would scan every Worker in the fleet to populate menus
  // nobody opened. Both hooks keep their cached answer once the menu closes and
  // re-fetch only on a workerId change, so re-opening the same row is free.
  const [menuOpen, setMenuOpen] = createSignal(false)
  const listSource = () => menuOpen() && props.workerId ? { workerId: props.workerId } : null
  const { providers } = useAvailableProviders(listSource)
  const { shells, defaultShell } = useAvailableShells(listSource)

  /** One change item. The three differ only in their label and their mode. */
  const changeItem = (label: string, mode: ChangeBranchMode) => (
    // The reason goes through <Tooltip>, which works on a disabled control and
    // leaves the item its own name. A `title` this long BECOMES the accessible
    // name, so a screen reader announced the reason in place of the label.
    <Tooltip text={props.disabledReason}>
      <button
        role="menuitem"
        disabled={Boolean(props.disabledReason)}
        onClick={() => props.actions.onChangeBranch(mode)}
      >
        {label}
      </button>
    </Tooltip>
  )

  return (
    <DropdownMenu
      trigger={props.trigger ?? rowContextMenuTrigger()}
      contextMenuFor={props.contextMenuFor}
      onToggle={setMenuOpen}
      data-testid={props['data-testid']}
    >
      {/* The three modes the Change branch dialog offers, each opening it with
          its own radio already selected. One item per mode, because a single
          "Change branch..." made the user open the dialog to discover that
          "Create new worktree" lived inside it. The labels are the dialog's own
          radio labels, so the item the user picks states what they then see. */}
      {changeItem('Switch to branch...', GitMode.SwitchBranch)}
      {changeItem('Create new branch...', GitMode.CreateBranch)}
      {changeItem('Create new worktree...', GitMode.CreateWorktree)}
      <hr />
      <Tooltip text={props.disabledReason}>
        <button
          role="menuitem"
          class={dangerMenuItem}
          disabled={Boolean(props.disabledReason)}
          onClick={() => props.actions.onDeleteBranch()}
        >
          {`${workingTreeDeleteLabel(props.isWorktree)}...`}
        </button>
      </Tooltip>
      <hr />
      {/* The same block the tab bar's + menu renders, WITHOUT its shortcut
          hints: those keys open a tab at the current tab's working directory,
          and these items open one at this branch's checkout. */}
      <NewTabMenuItems
        availableProviders={providers()}
        availableShells={shells()}
        defaultShell={defaultShell()}
        disabledReason={props.disabledReason}
        onNewAgent={provider => props.actions.onNewAgent(provider)}
        onNewAgentAdvanced={() => props.actions.onNewAgentAdvanced()}
        onNewTerminalWithShell={shell => props.actions.onNewTerminalWithShell(shell)}
        onNewTerminalAdvanced={() => props.actions.onNewTerminalAdvanced()}
      />
    </DropdownMenu>
  )
}
