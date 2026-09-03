import type { JSX } from 'solid-js'
import type { WorkingTreeInfo } from '~/components/common/WorkingTree'
import type { BranchMenuActions } from '~/components/workspace/branchActions'
import { Show } from 'solid-js'
import { WorkingTreeIcon, WorkingTreeTooltip } from '~/components/common/WorkingTree'
import { BranchContextMenu } from '~/components/workspace/BranchContextMenu'
import * as styles from './composer.css'

/**
 * Props for the working-tree chip in the composer status bar.
 */
export interface WorkingTreeChipProps {
  /**
   * The checkout to name. REQUIRED, and required whole: the chip renders the
   * kind's glyph, and a caller that could omit the kind would silently paint a
   * worktree as a branch — the defect this component exists to remove.
   *
   * The chip is hidden when `name` is empty, which is a tab with no git status
   * yet.
   */
  workingTree: WorkingTreeInfo
  /** Why every branch action is unusable, or undefined when usable. */
  disabledReason?: string
  /**
   * Every menu action, already bound to this branch. Undefined hides the chip:
   * a branch name that opens a menu of dead items is worse than no chip.
   */
  actions?: BranchMenuActions
  /** The Worker the branch is checked out on, for the menu's own lists. */
  workerId: string
}

/**
 * A status-bar chip that renders the current branch name and opens the
 * existing `BranchContextMenu` (the same one the sidebar uses) from a
 * branch-name button trigger. Reuses the same items, guards, and callbacks as
 * the workspace list's per-row menu — no parallel branch-action surface.
 *
 * The icon and the tooltip body come from `~/components/common/WorkingTree`,
 * which is also what the sidebar's branch row renders. One component for both,
 * so the chip cannot start calling a worktree a branch while the sidebar row
 * beside it does not.
 */
export function WorkingTreeChip(props: WorkingTreeChipProps): JSX.Element {
  // Both facts or no chip. The chip states a branch NAME and opens a MENU, so a
  // guard per fact would leave either a nameless chip or a trigger whose menu
  // has nothing to run.
  const ready = () => {
    const { name } = props.workingTree
    const { actions } = props
    return name && actions ? { name, actions } : undefined
  }
  return (
    <Show when={ready()}>
      {chip => (
        <BranchContextMenu
          isWorktree={props.workingTree.isWorktree}
          workerId={props.workerId}
          actions={chip().actions}
          disabledReason={props.disabledReason}
          data-testid="composer-branch-popover"
          trigger={triggerProps => (
            // The branch name, not an action: the menu holds every action, and
            // the label is ellipsized, so this is where the full name shows --
            // alongside the kind of checkout and its directory, which the chip
            // has no room for. When the actions are unusable it carries the
            // reason instead, which otherwise only reaches a user who opens the
            // menu.
            <WorkingTreeTooltip info={props.workingTree} disabledReason={props.disabledReason}>
              <button
                class={styles.axisChip}
                data-testid="composer-branch-trigger"
                {...triggerProps}
              >
                {/* No `label`: the tooltip states the kind in words, and this
                    trigger is a real button, so a keyboard user opens it. */}
                <WorkingTreeIcon isWorktree={props.workingTree.isWorktree} size="xs" />
                <span class={styles.axisChipLabel}>{chip().name}</span>
              </button>
            </WorkingTreeTooltip>
          )}
        />
      )}
    </Show>
  )
}
