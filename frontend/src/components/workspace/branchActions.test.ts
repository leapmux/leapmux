import type { Tab } from '~/stores/tab.types'
import { describe, expect, it } from 'vitest'
import { focusedBranchAction, WORKER_OFFLINE_BRANCH_REASON } from './branchActions'

/**
 * A tab whose flat git mirror and nested `agentGitStatus` can be set
 * independently, which is what the store actually does: `stampBranchOnTabs` and
 * `applyGitStatusToTabs` write the flat fields alone.
 */
function tab(overrides: {
  id?: string
  workerId?: string
  gitToplevel?: string
  gitBranch?: string
  gitIsWorktree?: boolean
  /** The nested copy, deliberately allowed to disagree with the flat mirror. */
  staleBranch?: string
} = {}): Tab {
  return {
    id: overrides.id ?? 't1',
    workerId: overrides.workerId ?? 'w1',
    gitToplevel: overrides.gitToplevel ?? '/repo',
    gitBranch: overrides.gitBranch ?? 'main',
    gitIsWorktree: overrides.gitIsWorktree ?? false,
    agentGitStatus: {
      branch: overrides.staleBranch ?? overrides.gitBranch ?? 'main',
      toplevel: overrides.gitToplevel ?? '/repo',
      isWorktree: overrides.gitIsWorktree ?? false,
    },
  } as unknown as Tab
}

function action(t: Tab | undefined, isOnline?: (workerId: string) => boolean, tabs: Tab[] = []) {
  return focusedBranchAction({
    tab: t,
    workspaceId: 'ws1',
    workspaceTabs: () => (tabs.length > 0 ? tabs : t ? [t] : []),
    isWorkerKnownOnline: isOnline,
  })
}

describe('focusedBranchAction gating', () => {
  it('disables the actions when the agent has no Worker yet', () => {
    expect(action(tab({ workerId: '' }), () => true).disabledReason).toBeDefined()
    expect(action(undefined, () => true).disabledReason).toBeDefined()
  })

  it('reports the offline Worker ahead of the missing repository root', () => {
    expect(action(tab({ gitToplevel: '' }), () => false).disabledReason).toBe(WORKER_OFFLINE_BRANCH_REASON)
  })

  it('disables the actions when the repository root is unknown', () => {
    // The branch comes from `git status` and the root from a separate
    // `git rev-parse`, so a status can carry a branch with no root.
    expect(action(tab({ gitToplevel: '' }), () => true).disabledReason).toBeDefined()
  })

  it('disables the actions when the branch is unknown', () => {
    expect(action(tab({ gitBranch: '' }), () => true).disabledReason).toBeDefined()
  })

  it('treats an absent liveness check as "not offline"', () => {
    // The shell omits the check only when it has no worker list to consult;
    // failing closed there would disable the menu for every agent.
    expect(action(tab()).disabledReason).toBeUndefined()
  })

  it('never returns both a reason and a builder', () => {
    // The two are mutually exclusive by construction: an enabled menu item that
    // resolves to no ref is the failure this shape exists to prevent.
    const disabled = action(tab({ workerId: '' }), () => true)
    expect(disabled.buildRef).toBeUndefined()

    const enabled = action(tab(), () => true)
    expect(enabled.disabledReason).toBeUndefined()
    expect(enabled.buildRef).toBeDefined()
  })

  it('does not build the ref while only gating', () => {
    // Gating is read on every reactive tick; building walks every tab of the
    // workspace, so it must stay lazy.
    let walked = 0
    const result = focusedBranchAction({
      tab: tab(),
      workspaceId: 'ws1',
      workspaceTabs: () => {
        walked++
        return [tab()]
      },
      isWorkerKnownOnline: () => true,
    })
    expect(result.disabledReason).toBeUndefined()
    expect(walked).toBe(0)

    result.buildRef!()
    expect(walked).toBe(1)
  })
})

describe('focusedBranchAction ref', () => {
  it('carries the repo identity the dialogs need', () => {
    const ref = action(tab({ gitIsWorktree: true }), () => true).buildRef!()
    expect(ref).toMatchObject({
      workspaceId: 'ws1',
      workerId: 'w1',
      gitToplevel: '/repo',
      branchName: 'main',
      isWorktree: true,
    })
  })

  it('collects only the tabs in the same branch group', () => {
    const focused = tab({ id: 'a' })
    const sameBranch = tab({ id: 'b' })
    const otherBranch = tab({ id: 'c', gitBranch: 'feature' })
    const otherWorker = tab({ id: 'd', workerId: 'w2' })
    const otherRepo = tab({ id: 'e', gitToplevel: '/other' })

    const ref = action(focused, () => true, [focused, sameBranch, otherBranch, otherWorker, otherRepo]).buildRef!()

    expect(ref.tabs.map(t => t.id)).toEqual(['a', 'b'])
  })

  it('identifies the branch from the same source it counts the tabs by', () => {
    // The regression this shape exists to prevent. `stampBranchOnTabs` rewrites
    // the flat `gitBranch` after a branch change and leaves `agentGitStatus`
    // alone, and the worker re-broadcasts the nested copy only at turn end -- so
    // on an idle agent the nested branch stays stale. Reading the label from the
    // nested copy while counting the tabs by the flat one would put the OLD
    // branch name on a dialog listing tabs already stamped with the NEW one.
    const focused = tab({ id: 'a', gitBranch: 'renamed', staleBranch: 'main' })
    const alsoRenamed = tab({ id: 'b', gitBranch: 'renamed', staleBranch: 'main' })
    const stillOnMain = tab({ id: 'c', gitBranch: 'main', staleBranch: 'main' })

    const ref = action(focused, () => true, [focused, alsoRenamed, stillOnMain]).buildRef!()

    expect(ref.branchName).toBe('renamed')
    expect(ref.tabs.map(t => t.id)).toEqual(['a', 'b'])
  })
})
