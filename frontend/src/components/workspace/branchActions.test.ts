import type { Tab } from '~/stores/tab.types'
import { describe, expect, it } from 'vitest'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { focusedBranchAction, WORKER_OFFLINE_BRANCH_REASON } from './branchActions'

function tab(overrides: {
  id?: string
  workerId?: string
  gitToplevel?: string
  branch?: string
  isWorktree?: boolean
} = {}): Tab {
  return {
    id: overrides.id ?? 't1',
    workerId: overrides.workerId ?? 'w1',
    gitToplevel: overrides.gitToplevel ?? '/repo',
  } as unknown as Tab
}

function seedRepo(
  store: ReturnType<typeof createRepoGitStore>,
  t: Tab,
  git: { branch?: string, isWorktree?: boolean },
) {
  const workerId = t.workerId ?? 'w1'
  const toplevel = t.gitToplevel ?? '/repo'
  store.upsert(repoKey(workerId, toplevel), {
    workerId,
    toplevel,
    branch: git.branch ?? 'main',
    isWorktree: git.isWorktree ?? false,
  })
}

function action(
  t: Tab | undefined,
  store: ReturnType<typeof createRepoGitStore>,
  isOnline?: (workerId: string) => boolean,
  tabs: Tab[] = [],
) {
  return focusedBranchAction({
    tab: t,
    workspaceId: 'ws1',
    workspaceTabs: () => (tabs.length > 0 ? tabs : t ? [t] : []),
    repoGitStore: store,
    isWorkerKnownOnline: isOnline,
  })
}

describe('focusedBranchAction gating', () => {
  it('disables the actions when the agent has no Worker yet', () => {
    const store = createRepoGitStore()
    expect(action(tab({ workerId: '' }), store, () => true).disabledReason).toBeDefined()
    expect(action(undefined, store, () => true).disabledReason).toBeDefined()
  })

  it('reports the offline Worker ahead of the missing repository root', () => {
    const store = createRepoGitStore()
    expect(action(tab({ gitToplevel: '' }), store, () => false).disabledReason).toBe(WORKER_OFFLINE_BRANCH_REASON)
  })

  it('disables the actions when the repository root is unknown', () => {
    const store = createRepoGitStore()
    expect(action(tab({ gitToplevel: '' }), store, () => true).disabledReason).toBeDefined()
  })

  it('disables the actions when the branch is unknown', () => {
    const store = createRepoGitStore()
    const t = tab()
    store.upsert(repoKey('w1', '/repo'), { workerId: 'w1', toplevel: '/repo', branch: '' })
    expect(action(t, store, () => true).disabledReason).toBeDefined()
  })

  it('treats an absent liveness check as "not offline"', () => {
    const store = createRepoGitStore()
    const t = tab()
    seedRepo(store, t, { branch: 'main' })
    expect(action(t, store).disabledReason).toBeUndefined()
  })

  it('never returns both a reason and a builder', () => {
    const store = createRepoGitStore()
    const disabled = action(tab({ workerId: '' }), store, () => true)
    expect(disabled.buildRef).toBeUndefined()

    const t = tab()
    seedRepo(store, t, { branch: 'main' })
    const enabled = action(t, store, () => true)
    expect(enabled.disabledReason).toBeUndefined()
    expect(enabled.buildRef).toBeDefined()
  })

  it('does not build the ref while only gating', () => {
    const store = createRepoGitStore()
    const t = tab()
    seedRepo(store, t, { branch: 'main' })
    let walked = 0
    const result = focusedBranchAction({
      tab: t,
      workspaceId: 'ws1',
      workspaceTabs: () => {
        walked++
        return [t]
      },
      repoGitStore: store,
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
    const store = createRepoGitStore()
    const t = tab({ isWorktree: true })
    seedRepo(store, t, { branch: 'main', isWorktree: true })

    const ref = action(t, store, () => true).buildRef!()
    expect(ref).toMatchObject({
      workspaceId: 'ws1',
      workerId: 'w1',
      gitToplevel: '/repo',
      branchName: 'main',
      isWorktree: true,
    })
  })

  it('collects only the tabs in the same branch group', () => {
    const store = createRepoGitStore()
    const focused = tab({ id: 'a', gitToplevel: '/repo-main' })
    const sameBranch = tab({ id: 'b', gitToplevel: '/repo-main' })
    const otherBranch = tab({ id: 'c', gitToplevel: '/repo-feature' })
    const otherWorker = tab({ id: 'd', workerId: 'w2', gitToplevel: '/repo-main' })
    const otherRepo = tab({ id: 'e', gitToplevel: '/other' })

    seedRepo(store, focused, { branch: 'main' })
    seedRepo(store, sameBranch, { branch: 'main' })
    seedRepo(store, otherBranch, { branch: 'feature' })
    seedRepo(store, otherWorker, { branch: 'main' })
    seedRepo(store, otherRepo, { branch: 'main' })

    const ref = action(focused, store, () => true, [focused, sameBranch, otherBranch, otherWorker, otherRepo]).buildRef!()

    expect(ref.tabs.map(t => t.id)).toEqual(['a', 'b'])
  })

  it('identifies the branch from the repo-keyed store', () => {
    const store = createRepoGitStore()
    const focused = tab({ id: 'a', gitToplevel: '/repo-renamed' })
    const alsoRenamed = tab({ id: 'b', gitToplevel: '/repo-renamed' })
    const stillOnMain = tab({ id: 'c', gitToplevel: '/repo-main' })

    seedRepo(store, focused, { branch: 'renamed' })
    seedRepo(store, alsoRenamed, { branch: 'renamed' })
    seedRepo(store, stillOnMain, { branch: 'main' })

    const ref = action(focused, store, () => true, [focused, alsoRenamed, stillOnMain]).buildRef!()

    expect(ref.branchName).toBe('renamed')
    expect(ref.tabs.map(t => t.id)).toEqual(['a', 'b'])
  })
})
