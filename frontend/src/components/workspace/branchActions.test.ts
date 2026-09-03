import type { BranchRef } from './WorkspaceTabTree'
import type { Tab } from '~/stores/tab.types'
import { describe, expect, it, vi } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { GitMode } from '~/hooks/useGitModeState'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { stubBranchRefActions } from '~/test-support/branchMenu'
import { bindBranchActions, focusedBranchAction, WORKER_OFFLINE_BRANCH_REASON } from './branchActions'

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

  it('enables branch actions from store toplevel when tab gitToplevel is absent', () => {
    const store = createRepoGitStore()
    const t = { id: 't1', workerId: 'w1', workingDir: '/repo/pkg' } as Tab
    store.upsert(repoKey('w1', '/repo'), { workerId: 'w1', toplevel: '/repo', branch: 'main' })
    expect(action(t, store, () => true).disabledReason).toBeUndefined()
    expect(action(t, store, () => true).buildRef!().gitToplevel).toBe('/repo')
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

  // The menu lists the BRANCH Worker's providers and shells on every render, so
  // the Worker has to arrive beside `buildRef` rather than inside it. Reading it
  // through the ref would walk every tab of the workspace once per render, for
  // one id this call already holds.
  it('states the Worker beside the builder, without building the ref', () => {
    const store = createRepoGitStore()
    const t = tab({ workerId: 'w-branch' })
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
    expect(result.workerId).toBe('w-branch')
    expect(walked).toBe(0)
  })

  // The refused variant carries no Worker at all, so a menu that reads it while
  // disabled asks no Worker for a provider list.
  it('states no Worker when the actions are refused', () => {
    const store = createRepoGitStore()
    expect(action(tab({ workerId: '' }), store, () => true).workerId).toBeUndefined()
    const offline = tab()
    seedRepo(store, offline, { branch: 'main' })
    expect(action(offline, store, () => false).workerId).toBeUndefined()
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

describe('bindBranchActions', () => {
  const ref = { workspaceId: 'ws1', workerId: 'w1', gitToplevel: '/repo', isWorktree: false, branchName: 'main', tabs: [] } as BranchRef

  it('passes the ref first and the action\'s own arguments after it', () => {
    const actions = stubBranchRefActions()
    const bound = bindBranchActions(actions, () => ref)

    bound.onChangeBranch(GitMode.CreateWorktree)
    bound.onDeleteBranch()
    bound.onNewAgent(AgentProvider.CODEX)
    bound.onNewAgentAdvanced()
    bound.onNewTerminalWithShell('/bin/zsh')
    bound.onNewTerminalAdvanced()

    expect(actions.onChangeBranch).toHaveBeenCalledWith(ref, GitMode.CreateWorktree)
    expect(actions.onDeleteBranch).toHaveBeenCalledWith(ref)
    expect(actions.onNewAgent).toHaveBeenCalledWith(ref, AgentProvider.CODEX)
    expect(actions.onNewAgentAdvanced).toHaveBeenCalledWith(ref)
    expect(actions.onNewTerminalWithShell).toHaveBeenCalledWith(ref, '/bin/zsh')
    expect(actions.onNewTerminalAdvanced).toHaveBeenCalledWith(ref)
  })

  // Building a ref walks every tab of the workspace, and the sidebar mounts one
  // menu per branch row. Binding must cost nothing until an item is clicked.
  it('builds no ref until an action runs', () => {
    const buildRef = vi.fn(() => ref)
    const bound = bindBranchActions(stubBranchRefActions(), buildRef)
    expect(buildRef).not.toHaveBeenCalled()
    bound.onDeleteBranch()
    expect(buildRef).toHaveBeenCalledTimes(1)
  })

  // A row survives a tree rebuild that swaps its branch object, so the ref has
  // to come from the branch the row shows at CLICK time.
  it('reads the builder at click time, not at bind time', () => {
    const actions = stubBranchRefActions()
    let current = ref
    const bound = bindBranchActions(actions, () => current)
    const moved = { ...ref, branchName: 'feature' } as BranchRef
    current = moved
    bound.onDeleteBranch()
    expect(actions.onDeleteBranch).toHaveBeenCalledWith(moved)
  })

  // The composer's refused case: focusedBranchAction withheld the builder and
  // supplied the reason that disables every item, so nothing should reach the
  // handlers if an item somehow fires anyway.
  it('does nothing when the builder answers undefined', () => {
    const actions = stubBranchRefActions()
    const bound = bindBranchActions(actions, () => undefined)
    bound.onChangeBranch(GitMode.SwitchBranch)
    bound.onDeleteBranch()
    bound.onNewAgent(AgentProvider.CODEX)
    bound.onNewAgentAdvanced()
    bound.onNewTerminalWithShell('/bin/zsh')
    bound.onNewTerminalAdvanced()
    for (const fn of Object.values(actions))
      expect(fn).not.toHaveBeenCalled()
  })
})
