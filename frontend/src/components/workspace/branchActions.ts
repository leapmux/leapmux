import type { BranchRef } from './WorkspaceTabTree'
import type { Tab } from '~/stores/tab.types'
import { tabBranchKey } from './branchKeys'

/**
 * Why the branch actions are unusable when the Worker is unreachable.
 *
 * Both surfaces that offer Change/Delete branch -- the sidebar's per-branch row
 * and the composer's branch chip -- show this same sentence, because both
 * actions run on the machine the repository is on. One constant so the two
 * cannot tell the user two different reasons.
 */
export const WORKER_OFFLINE_BRANCH_REASON
  = 'This Worker is offline. Branch actions need the machine the repository is on.'

/**
 * What the composer's branch chip can do for the focused agent: either a reason
 * the actions are unusable, or a builder for the {@link BranchRef} the dialogs
 * take.
 *
 * ONE function answers both questions, so the guard and the ref cannot
 * disagree. Two hand-maintained precondition lists let the menu enable an item
 * whose builder then returns nothing, and the call site drops that click with no
 * dialog, no error, and no log.
 */
export type FocusedBranchAction
  = | { disabledReason: string, buildRef?: undefined }
    | { disabledReason?: undefined, buildRef: () => BranchRef }

/**
 * Resolve the branch action for `tab`.
 *
 * Every field is read from the FLAT git mirror (`gitBranch`, `gitToplevel`,
 * `gitIsWorktree`) rather than from the nested `agentGitStatus`. The two are
 * written together on hydration, but `stampBranchOnTabs` (after a branch change)
 * and `applyGitStatusToTabs` (after a checkout in a terminal) write the flat
 * fields ALONE, and the worker re-broadcasts `agentGitStatus` only at turn end.
 * On an idle agent the nested branch therefore stays stale for as long as the
 * agent is idle. Since `tabBranchKey` -- the membership test the sidebar groups
 * by -- reads the flat fields, mixing the two sources would name the OLD branch
 * in the delete dialog while listing the tabs already stamped with the NEW one:
 * a wrong tab set on a destructive confirmation.
 *
 * `buildRef` is lazy. The guard is read reactively on every tick, and building
 * the ref walks every tab of the workspace.
 */
export function focusedBranchAction(opts: {
  tab: Tab | undefined
  workspaceId: string
  /** Every tab of the active workspace, for the affected-tab set. */
  workspaceTabs: () => Tab[]
  isWorkerKnownOnline?: (workerId: string) => boolean
}): FocusedBranchAction {
  const { tab } = opts
  if (!tab?.workerId)
    return { disabledReason: 'This agent is not attached to a Worker yet.' }
  if (opts.isWorkerKnownOnline && !opts.isWorkerKnownOnline(tab.workerId))
    return { disabledReason: WORKER_OFFLINE_BRANCH_REASON }
  if (!tab.gitToplevel)
    return { disabledReason: 'The repository root for this agent is not known yet. Branch actions need it.' }
  if (!tab.gitBranch)
    return { disabledReason: 'The branch for this agent is not known yet. Branch actions need it.' }

  const workerId = tab.workerId
  const gitToplevel = tab.gitToplevel
  const branchName = tab.gitBranch
  return {
    buildRef: () => {
      const key = tabBranchKey(tab)
      return {
        workspaceId: opts.workspaceId,
        workerId,
        gitToplevel,
        isWorktree: !!tab.gitIsWorktree,
        branchName,
        tabs: opts.workspaceTabs().filter(t => tabBranchKey(t) === key),
      }
    },
  }
}
