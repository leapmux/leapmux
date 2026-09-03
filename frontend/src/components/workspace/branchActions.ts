import type { BranchRef } from './WorkspaceTabTree'
import type { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ChangeBranchMode } from '~/hooks/useGitModeState'
import type { createRepoGitStore } from '~/stores/repoGit.store'
import type { Tab } from '~/stores/tab.types'
import { repoGitView } from '~/stores/repoGit'
import { tabBranchKey, tabGitToplevelForKey } from './branchKeys'

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
 * Everything a branch context menu can do, already bound to ONE branch.
 *
 * The menu takes this form, so no item has to hold a branch of its own. Both
 * surfaces that render the menu -- the sidebar's per-branch row and the
 * composer's branch chip -- supply it, and both reach the same handlers in
 * `AppShell`, so the two menus cannot offer different actions.
 *
 * The four new-tab actions carry NO working directory or worker: the binding
 * supplies both from the branch, so a caller cannot open an agent at the wrong
 * checkout by forgetting to override the current tab's context.
 */
export interface BranchMenuActions {
  /** Open the Change branch dialog with `mode` already selected. */
  onChangeBranch: (mode: ChangeBranchMode) => void
  /** Open the Delete branch / Delete worktree dialog. */
  onDeleteBranch: () => void
  /** Open an agent with this provider on the branch, without a dialog. */
  onNewAgent: (provider: AgentProvider) => void
  /** Open the New agent dialog, pre-filled with the branch's checkout. */
  onNewAgentAdvanced: () => void
  /** Open a terminal with this shell on the branch, without a dialog. */
  onNewTerminalWithShell: (shell: string) => void
  /** Open the New terminal dialog, pre-filled with the branch's checkout. */
  onNewTerminalAdvanced: () => void
}

/**
 * The same actions before they know which branch they act on.
 *
 * This is the form the shell hands to the sidebar, which holds many branch rows
 * and one set of handlers. Each row binds it with {@link bindBranchActions}.
 */
export type BranchRefActions = {
  [K in keyof BranchMenuActions]: BranchMenuActions[K] extends (...args: infer A) => void
    ? (ref: BranchRef, ...args: A) => void
    : never
}

/**
 * Bind every action to one branch.
 *
 * `buildRef` stays LAZY and is called once per invoked action: building a ref
 * walks every tab of the workspace, and a sidebar with many branch rows would
 * otherwise pay that walk for every row on every render.
 *
 * It may ANSWER undefined, which is the composer's refused case: the same
 * {@link focusedBranchAction} call that withheld the builder supplied the
 * `disabledReason` that disables every item of the menu, so a bound action can
 * only fire there if that guard is broken. It does nothing rather than throw.
 */
export function bindBranchActions(
  actions: BranchRefActions,
  buildRef: () => BranchRef | undefined,
): BranchMenuActions {
  const run = (fn: (ref: BranchRef) => void) => {
    const ref = buildRef()
    if (ref)
      fn(ref)
  }
  return {
    onChangeBranch: mode => run(ref => actions.onChangeBranch(ref, mode)),
    onDeleteBranch: () => run(ref => actions.onDeleteBranch(ref)),
    onNewAgent: provider => run(ref => actions.onNewAgent(ref, provider)),
    onNewAgentAdvanced: () => run(ref => actions.onNewAgentAdvanced(ref)),
    onNewTerminalWithShell: shell => run(ref => actions.onNewTerminalWithShell(ref, shell)),
    onNewTerminalAdvanced: () => run(ref => actions.onNewTerminalAdvanced(ref)),
  }
}

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
  = | { disabledReason: string, buildRef?: undefined, workerId?: undefined }
    | {
      disabledReason?: undefined
      buildRef: () => BranchRef
      /**
       * The branch's Worker. Given beside `buildRef` rather than inside it,
       * because the menu needs it on every render to list that Worker's agent
       * providers and shells -- and `buildRef` walks every tab of the
       * workspace, which is far too much work for one id it already holds.
       */
      workerId: string
    }

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
  const git = repoGitView(tab, opts.repoGitStore)
  const gitToplevel = tabGitToplevelForKey(tab, opts.repoGitStore)
  if (!gitToplevel)
    return { disabledReason: 'The repository root for this agent is not known yet. Branch actions need it.' }
  if (!git.branchLabel)
    return { disabledReason: 'The branch for this agent is not known yet. Branch actions need it.' }

  const workerId = tab.workerId
  const branchName = git.branchLabel
  return {
    workerId,
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
