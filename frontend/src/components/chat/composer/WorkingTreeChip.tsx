import type { JSX } from 'solid-js'
import type { DiffStats } from '~/stores/repoGit'
import { Show } from 'solid-js'
import { Tooltip } from '~/components/common/Tooltip'
import { WorkingTreeIcon, WorkingTreeRows } from '~/components/common/WorkingTree'
import { BranchContextMenu } from '~/components/workspace/BranchContextMenu'
import * as styles from './composer.css'

/**
 * Props for the working-tree chip in the composer status bar.
 */
export interface WorkingTreeChipProps {
  /** Current branch name. The chip is hidden when this is empty. */
  branchName: string | undefined
  /** True iff the agent's checkout is a linked worktree. */
  isWorktree: boolean
  /** Absolute working-tree root, shown tilde-compressed in the tooltip. */
  directory: string
  /** The worker's home directory, for the tilde compression. */
  homeDir?: string
  /** Diff stats for the badge in the tooltip. The badge self-hides at all-zero. */
  stats?: DiffStats | null
  /** Why both branch actions are unusable, or undefined when usable. */
  disabledReason?: string
  /** Open the "Change branch..." dialog. */
  onChangeBranch: () => void
  /** Open the "Delete branch..." / "Delete worktree..." dialog. */
  onDeleteBranch: () => void
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
  return (
    <Show when={props.branchName}>
      {branch => (
        <BranchContextMenu
          isWorktree={props.isWorktree}
          onChangeBranch={props.onChangeBranch}
          onDeleteBranch={props.onDeleteBranch}
          disabledReason={props.disabledReason}
          data-testid="composer-branch-popover"
          trigger={triggerProps => (
            // The branch name, not an action: the menu holds two, and the label
            // is ellipsized, so this is where the full name shows -- alongside
            // the kind of checkout and its directory, which the chip has no
            // room for. When the actions are unusable it carries the reason
            // instead, which otherwise only reaches a user who opens the menu.
            <Tooltip
              text={props.disabledReason}
              content={props.disabledReason
                ? undefined
                : (
                    <WorkingTreeRows
                      isWorktree={props.isWorktree}
                      name={branch()}
                      directory={props.directory}
                      homeDir={props.homeDir}
                      stats={props.stats}
                    />
                  )}
            >
              <button
                class={styles.axisChip}
                data-testid="composer-branch-trigger"
                {...triggerProps}
              >
                <WorkingTreeIcon isWorktree={props.isWorktree} size="xs" />
                <span class={styles.axisChipLabel}>{branch()}</span>
              </button>
            </Tooltip>
          )}
        />
      )}
    </Show>
  )
}
