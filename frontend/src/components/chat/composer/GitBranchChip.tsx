import type { JSX } from 'solid-js'
import GitBranch from 'lucide-solid/icons/git-branch'
import { Show } from 'solid-js'
import { Icon } from '~/components/common/Icon'
import { Tooltip } from '~/components/common/Tooltip'
import { BranchContextMenu } from '~/components/workspace/BranchContextMenu'
import * as styles from './composer.css'

/**
 * Props for the branch chip in the composer status bar.
 */
export interface GitBranchChipProps {
  /** Current branch name. The chip is hidden when this is empty. */
  branchName: string | undefined
  /** Why both branch actions are unusable, or undefined when usable. */
  disabledReason?: string
  /** Open the "Change branch..." dialog. */
  onChangeBranch: () => void
  /** Open the "Delete branch..." dialog. */
  onDeleteBranch: () => void
}

/**
 * A status-bar chip that renders the current git branch name and opens the
 * existing `BranchContextMenu` (the same one the sidebar uses) from a
 * branch-name button trigger. Reuses the same items, guards, and callbacks as
 * the workspace list's per-row menu — no parallel branch-action surface.
 */
export function GitBranchChip(props: GitBranchChipProps): JSX.Element {
  return (
    <Show when={props.branchName}>
      <BranchContextMenu
        onChangeBranch={props.onChangeBranch}
        onDeleteBranch={props.onDeleteBranch}
        disabledReason={props.disabledReason}
        data-testid="composer-branch-popover"
        trigger={triggerProps => (
          // The branch name, not an action: the menu holds two, and the label is
          // ellipsized, so this is where the full name shows. When the actions
          // are unusable it carries the reason instead, which otherwise only
          // reaches a user who opens the menu.
          <Tooltip text={props.disabledReason ?? props.branchName}>
            <button
              class={styles.axisChip}
              data-testid="composer-branch-trigger"
              {...triggerProps}
            >
              <Icon icon={GitBranch} size="xs" />
              <span class={styles.axisChipLabel}>{props.branchName}</span>
            </button>
          </Tooltip>
        )}
      />
    </Show>
  )
}
