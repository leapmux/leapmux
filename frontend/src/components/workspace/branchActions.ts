import type { BranchRef } from './WorkspaceTabTree'
import type { Tab } from '~/stores/tab.types'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import { repoGitView } from '~/stores/repoGit'
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
 * Branch name and worktree disposition are read from the repo-keyed git store
 * via {@link repoGitView}, not from per-tab fields or nested `agentGitStatus`.
 * `tabBranchKey` -- the membership test the sidebar groups by -- uses the same
 * store, so the delete dialog's tab set always matches the tree.
 *
 * `buildRef` is lazy. The guard is read reactively on every tick, and building
 * the ref walks every tab of the workspace.
 */
export function focusedBranchAction(opts: {
  tab: Tab | undefined
  workspaceId: string
  /** Every tab of the active workspace, for the affected-tab set. */
  workspaceTabs: () => Tab[]
  repoGitStore: ReturnType<typeof createRepoGitStore>
  isWorkerKnownOnline?: (workerId: string) => boolean
}): FocusedBranchAction {
  const { tab } = opts
  if (!tab?.workerId)
    return { disabledReason: 'This agent is not attached to a Worker yet.' }
  if (opts.isWorkerKnownOnline && !opts.isWorkerKnownOnline(tab.workerId))
    return { disabledReason: WORKER_OFFLINE_BRANCH_REASON }
  if (!tab.gitToplevel)
    return { disabledReason: 'The repository root for this agent is not known yet. Branch actions need it.' }

  const git = repoGitView(tab, opts.repoGitStore)
  if (!git.branchLabel)
    return { disabledReason: 'The branch for this agent is not known yet. Branch actions need it.' }

  const workerId = tab.workerId
  const gitToplevel = tab.gitToplevel
  const branchName = git.branchLabel
  return {
    buildRef: () => {
      const key = tabBranchKey(tab, opts.repoGitStore)
      return {
        workspaceId: opts.workspaceId,
        workerId,
        gitToplevel,
        isWorktree: !!git.isWorktree,
        branchName,
        tabs: opts.workspaceTabs().filter(t => tabBranchKey(t, opts.repoGitStore) === key),
      }
    },
  }
}
