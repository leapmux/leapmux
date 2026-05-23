/// <reference types="vitest/globals" />
import type { GitBranchEntry, InspectBranchDeletionResponse } from '~/generated/leapmux/v1/git_pb'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { DeleteBranchDialog } from './DeleteBranchDialog'

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/workerRpc', () => ({
  inspectBranchDeletion: vi.fn(),
  deleteBranch: vi.fn(),
  pushBranch: vi.fn(),
  forceRemoveWorktree: vi.fn(),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

function makeBranches(names: string[]): GitBranchEntry[] {
  return names.map(name => ({
    $typeName: 'leapmux.v1.GitBranchEntry',
    name,
    isRemote: false,
  } as GitBranchEntry))
}

function makeInspectResp(overrides: Partial<InspectBranchDeletionResponse> & Partial<{
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
  unpushedCommitCount: number
  hasUncommittedChanges: boolean
  upstreamExists: boolean
  remoteBranchMissing: boolean
  originExists: boolean
  canPush: boolean
  /** Convenience: pass branch names; converted to GitBranchEntry rows. */
  branchNames: string[]
}> = {}): InspectBranchDeletionResponse {
  const {
    diffAdded = 0,
    diffDeleted = 0,
    diffUntracked = 0,
    unpushedCommitCount = 0,
    hasUncommittedChanges = false,
    upstreamExists = true,
    remoteBranchMissing = false,
    originExists = true,
    canPush = false,
    gitState,
    branchNames,
    branches,
    ...rest
  } = overrides
  // Default non-worktree responses include a picker list (the doomed
  // branch is in there too — the dialog filters it out). The worktree
  // path leaves `branches` empty to mirror the worker's contract.
  const isWorktree = rest.isWorktree ?? false
  const defaultBranches: GitBranchEntry[] = isWorktree ? [] : makeBranches(['main', 'doomed'])
  return {
    $typeName: 'leapmux.v1.InspectBranchDeletionResponse',
    isWorktree,
    worktreePath: '',
    // Worktree responses thread the DB row id so the dialog can call
    // ForceRemoveWorktree directly; non-worktree leaves it empty.
    worktreeId: isWorktree ? 'wt-1' : '',
    branchName: 'doomed',
    branches: branches ?? (branchNames ? makeBranches(branchNames) : defaultBranches),
    gitState: gitState ?? ({
      $typeName: 'leapmux.v1.BranchGitState',
      diffAdded,
      diffDeleted,
      diffUntracked,
      unpushedCommitCount,
      hasUncommittedChanges,
      upstreamExists,
      remoteBranchMissing,
      originExists,
      canPush,
    } as InspectBranchDeletionResponse['gitState']),
    ...rest,
  } as InspectBranchDeletionResponse
}

function makeAgentTab(id: string): Tab {
  return {
    type: TabType.AGENT,
    id,
    title: id,
    tileId: 'tile-1',
    position: '0',
    workerId: 'w1',
    workingDir: '/repo',
  } as Tab
}

function makeTerminalTab(id: string): Tab {
  return {
    type: TabType.TERMINAL,
    id,
    title: id,
    tileId: 'tile-1',
    position: '0',
    workerId: 'w1',
    workingDir: '/repo',
  } as Tab
}

function makeFileTab(id: string): Tab {
  return {
    type: TabType.FILE,
    id,
    title: id,
    tileId: 'tile-1',
    position: '0',
    workerId: 'w1',
    workingDir: '/repo',
    filePath: `/repo/${id}.ts`,
  } as Tab
}

function renderDialog(props: Partial<Parameters<typeof DeleteBranchDialog>[0]> = {}) {
  const tabs = props.tabs ?? [makeAgentTab('a1')]
  const defaults = {
    workerId: 'w1',
    gitToplevel: '/repo',
    branchName: 'doomed',
    tabs,
    closeTab: vi.fn(),
    onClose: vi.fn(),
  }
  const merged = { ...defaults, ...props }
  render(() => <DeleteBranchDialog {...merged} />)
  return merged
}

async function clickDelete() {
  // ConfirmButton arms on first click, fires on the second.
  fireEvent.click(screen.getByRole('button', { name: 'Delete branch' }))
  await waitFor(() => expect(screen.getByRole('button', { name: 'Confirm?' })).toBeInTheDocument())
  fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
}

describe('deleteBranchDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(workerRpc.deleteBranch).mockResolvedValue({ $typeName: 'leapmux.v1.DeleteBranchResponse' })
    vi.mocked(workerRpc.pushBranch).mockResolvedValue({ $typeName: 'leapmux.v1.PushBranchResponse' })
  })

  it('shows a loader while inspecting branch state', async () => {
    let resolve: (r: InspectBranchDeletionResponse) => void = () => {}
    vi.mocked(workerRpc.inspectBranchDeletion).mockReturnValue(
      new Promise<InspectBranchDeletionResponse>((r) => { resolve = r }),
    )
    renderDialog()
    expect(screen.getByText(/Inspecting branch state/)).toBeInTheDocument()
    // Unblock the resource so the dialog doesn't leak the promise.
    resolve(makeInspectResp({ isWorktree: true, worktreePath: '/wt' }))
  })

  it('worktree variant closes every tab with KEEP and fires ForceRemoveWorktree', async () => {
    // Pins the post-refactor flow: tab closes are UI-only (KEEP so the
    // worker doesn't race ForceRemoveWorktree by also trying to remove
    // the worktree from each AGENT/TERMINAL's last-close pipeline), and
    // ForceRemoveWorktree against the worktree DB row is the
    // authoritative removal. This decouples removal from tab existence
    // — a branch group with zero AGENT/TERMINAL tabs (or only FILE
    // tabs) used to be a silent no-op because the FILE close path
    // doesn't ref-count the worktree.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.forceRemoveWorktree).mockResolvedValue({
      $typeName: 'leapmux.v1.ForceRemoveWorktreeResponse',
    })
    const closeTab = vi.fn()
    const tabs = [makeAgentTab('a1'), makeAgentTab('a2'), makeTerminalTab('t1')]
    const props = renderDialog({ tabs, closeTab })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(closeTab).toHaveBeenCalledTimes(3))
    expect(closeTab).toHaveBeenNthCalledWith(1, tabs[0], WorktreeAction.KEEP)
    expect(closeTab).toHaveBeenNthCalledWith(2, tabs[1], WorktreeAction.KEEP)
    expect(closeTab).toHaveBeenNthCalledWith(3, tabs[2], WorktreeAction.KEEP)
    await waitFor(() => expect(workerRpc.forceRemoveWorktree).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.forceRemoveWorktree).mock.calls[0]).toEqual([
      'w1',
      { worktreeId: 'wt-1' },
    ])
    await waitFor(() => expect(props.onClose).toHaveBeenCalled())
  })

  it('worktree variant removes the worktree even when the group has only FILE tabs', async () => {
    // The defect this guards: a FILE-only branch group used to slip
    // past the closeTabWithAction REMOVE pipeline because the FILE
    // branch explicitly ignores worktreeAction, so the worktree stayed
    // on disk while the dialog toasted success. ForceRemoveWorktree is
    // the source of truth now; the dialog must call it regardless of
    // the tab-type mix.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.forceRemoveWorktree).mockResolvedValue({
      $typeName: 'leapmux.v1.ForceRemoveWorktreeResponse',
    })
    const closeTab = vi.fn()
    const tabs = [makeFileTab('f1'), makeFileTab('f2')]
    const props = renderDialog({ tabs, closeTab })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(workerRpc.forceRemoveWorktree).toHaveBeenCalledTimes(1))
    expect(closeTab).toHaveBeenCalledTimes(2)
    expect(closeTab).toHaveBeenNthCalledWith(1, tabs[0], WorktreeAction.KEEP)
    expect(closeTab).toHaveBeenNthCalledWith(2, tabs[1], WorktreeAction.KEEP)
    await waitFor(() => expect(props.onClose).toHaveBeenCalled())
  })

  it('worktree variant: forceRemoveWorktree failure must NOT close any tabs', async () => {
    // Pins the order of operations: ForceRemoveWorktree is awaited
    // BEFORE the tab closes fire. Closing tabs first and then handling
    // a worktree-remove failure would leave the user staring at a
    // "Delete failed" banner above an already-empty branch group with
    // no UI path to retry — the tabs are gone, the worktree (and
    // therefore the branch group) is still on disk.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.forceRemoveWorktree).mockRejectedValue(new Error('worker said no'))
    const closeTab = vi.fn()
    const tabs = [makeAgentTab('a1'), makeAgentTab('a2')]
    const props = renderDialog({ tabs, closeTab })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(workerRpc.forceRemoveWorktree).toHaveBeenCalledTimes(1))
    expect(closeTab).not.toHaveBeenCalled()
    expect(props.onClose).not.toHaveBeenCalled()
    // Error banner must surface so the user knows the action failed
    // and the tabs are still there to retry against.
    await waitFor(() => expect(screen.getByText(/worker said no/)).toBeInTheDocument())
  })

  it('worktree variant falls back to per-tab REMOVE when the worker reports the worktree is untracked', async () => {
    // worktreeId === '' is the worker's documented signal that the
    // worktree dir exists on disk but no DB row backs it (commonly: the
    // user created the worktree from a terminal via `git worktree add`
    // before opening any LeapMux tab inside it). The proto contract on
    // InspectBranchDeletionResponse.worktree_id explicitly says the
    // dialog "falls back to closing tabs through their own pipeline"
    // in this case — hard-failing here used to strand the user with
    // no UI path to clean up the worktree the worker can clearly see.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt', worktreeId: '' }),
    )
    const closeTab = vi.fn()
    const props = renderDialog({
      closeTab,
      tabs: [makeAgentTab('a1'), makeAgentTab('a2')],
    })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    // ForceRemoveWorktree must NOT fire — we have no worktree_id to
    // pass and a hard call would 404 on the worker.
    expect(workerRpc.forceRemoveWorktree).not.toHaveBeenCalled()
    // Each tab gets a per-tab REMOVE so its own close pipeline drives
    // whatever cleanup it can (FILE revoke, AGENT/TERMINAL close).
    expect(closeTab).toHaveBeenCalledTimes(2)
    expect(closeTab).toHaveBeenCalledWith(expect.objectContaining({ id: 'a1' }), WorktreeAction.REMOVE)
    expect(closeTab).toHaveBeenCalledWith(expect.objectContaining({ id: 'a2' }), WorktreeAction.REMOVE)
  })

  it('non-worktree variant: Delete disabled until switch-to is chosen', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    const del = screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement
    expect(del.disabled).toBe(true)

    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })
    await waitFor(() => expect(del.disabled).toBe(false))
  })

  it('non-worktree variant: fires onBranchChanged with the chosen switch-to branch', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const onBranchChanged = vi.fn()
    renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()
    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('main')
  })

  it('non-worktree variant: stamps the local name when switching to a remote-tracking ref', async () => {
    // The worker's deleteBranchInDir routes through checkoutBranchInDir,
    // which resolves 'origin/foo' to the local branch 'foo' before
    // deleting. The sidebar shows the local name, so onBranchChanged
    // must too. Regression for a bug where the raw remote ref was
    // stamped onto every tab in the branch group, leaving the sidebar
    // label disagreeing with HEAD until something else triggered a
    // refresh.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        branches: [
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'main', isRemote: false },
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'origin/foo', isRemote: true },
        ] as GitBranchEntry[],
      }),
    )
    const onBranchChanged = vi.fn()
    renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'origin/foo' } })

    await clickDelete()
    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('foo')
  })

  it('non-worktree variant: keeps a local-branch name verbatim even when it contains "/"', async () => {
    // A legitimate local branch like `feature/auth` must NOT have its
    // prefix stripped — otherwise the sidebar would stamp `auth` on
    // every tab in the group while HEAD is actually on `feature/auth`.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        branches: [
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'feature/auth', isRemote: false },
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'doomed', isRemote: false },
        ] as GitBranchEntry[],
      }),
    )
    const onBranchChanged = vi.fn()
    renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'feature/auth' } })

    await clickDelete()
    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('feature/auth')
  })

  it('mounts with exactly one inspectBranchDeletion call (no separate list-branches RPC)', async () => {
    // The inspect RPC carries the branch picker list inline (worker-side
    // fan-out), so the dialog needs only one round trip at open time.
    // A regression that re-introduces a second fetch would either fail
    // this count assertion or trip the mock (no `listGitBranches` is
    // declared in the workerRpc mock above).
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(1)
  })

  it('forwards props.branchName as branchNameHint on inspectBranchDeletion', async () => {
    // The caller already has the branch label (it's the row that opened
    // the menu). Passing it as a hint lets the worker parallelize the
    // queryGitPathInfo and pushStatusForPath forks; pin the wire
    // contract so a future refactor can't silently drop it.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog({ branchName: 'doomed' })
    await waitFor(() => expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(1))
    const [, req] = vi.mocked(workerRpc.inspectBranchDeletion).mock.calls[0]
    expect(req).toMatchObject({ path: '/repo', branchNameHint: 'doomed' })
  })

  it('sends empty branchNameHint for the sidebar "(no branch)" group', async () => {
    // `branchName: null` (no current branch on the row, e.g. detached
    // HEAD or freshly-initialised repo) must surface as an empty hint —
    // the wire field is a `string`, not optional — so the worker falls
    // back to the no-hint path.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog({ branchName: null })
    await waitFor(() => expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(1))
    const [, req] = vi.mocked(workerRpc.inspectBranchDeletion).mock.calls[0]
    expect(req).toMatchObject({ branchNameHint: '' })
  })

  it('refreshInspect after push re-runs inspect once and re-populates the branch list', async () => {
    // Post-push refresh re-issues only the inspect RPC. The worker
    // repopulates `branches` in the new response, so a new ref (e.g.
    // `origin/<doomed>` created by the push) lands in the picker without
    // a second RPC. This pins the consolidated single-RPC contract.
    const second = makeInspectResp({
      canPush: false,
      unpushedCommitCount: 0,
      branchNames: ['main', 'feature', 'doomed', 'origin/doomed'],
    })
    vi.mocked(workerRpc.inspectBranchDeletion)
      .mockResolvedValueOnce(makeInspectResp({ canPush: true, unpushedCommitCount: 1 }))
      .mockResolvedValueOnce(second)

    renderDialog({ tabs: [makeTerminalTab('t1')] })
    await waitFor(() => expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(1))

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(2))
    // The newly-introduced remote ref is now in the picker.
    await waitFor(() => {
      const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
      const optionValues = Array.from(select.options).map(o => o.value)
      // doomed is filtered (it's the current branch); the rest survive in input order.
      expect(optionValues).toEqual(['', 'main', 'feature', 'origin/doomed'])
    })
  })

  it('worktree response carries no branch list (worker contract: empty when isWorktree)', async () => {
    // The worker leaves InspectBranchDeletionResponse.branches empty
    // when isWorktree is true, since the dialog renders no picker.
    // Pin the contract so a regression that always populates the list
    // (wasting bytes on every worktree open) trips this test.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    // No <select> rendered for the worktree variant.
    expect(screen.queryByText(/Switch this working directory to:/)).toBeNull()
  })

  it('worktree variant does NOT call onBranchChanged', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.forceRemoveWorktree).mockResolvedValue({
      $typeName: 'leapmux.v1.ForceRemoveWorktreeResponse',
    })
    const onBranchChanged = vi.fn()
    const closeTab = vi.fn()
    renderDialog({ onBranchChanged, closeTab })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(closeTab).toHaveBeenCalled())
    expect(onBranchChanged).not.toHaveBeenCalled()
  })

  it('non-worktree variant fires deleteBranch with the chosen switch target', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()

    await waitFor(() => expect(workerRpc.deleteBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.deleteBranch).mock.calls[0][1]).toMatchObject({
      branchToDelete: 'doomed',
      switchToBranch: 'main',
      path: '/repo',
    })
  })

  it('non-worktree variant filters out the current branch from the switch-to list', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ branchNames: ['doomed', 'main', 'feature'] }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    const optionValues = Array.from(select.options).map(o => o.value)
    // Exact set: showPrompt prepends an empty "" option, the
    // doomed/current branch is filtered out, the rest preserve order.
    expect(optionValues).toEqual(['', 'main', 'feature'])
  })

  it('only-branch case disables Delete and shows the explanatory copy', async () => {
    // Only the doomed branch in the candidate list ⇒ after filtering,
    // the picker has no candidates ⇒ isOnlyBranch ⇒ disabled.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ branchNames: ['doomed'] }),
    )
    renderDialog()
    await waitFor(() =>
      expect(screen.getByText(/Cannot delete the only branch/)).toBeInTheDocument(),
    )
    const del = screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement
    expect(del.disabled).toBe(true)
  })

  it('only-branch case hides the affected-tabs line (delete cannot proceed)', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ branchNames: ['doomed'] }),
    )
    renderDialog({ tabs: [makeAgentTab('a1'), makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByText(/Cannot delete the only branch/)).toBeInTheDocument())
    // Neither "stopped" nor "kept-running" wording should appear when
    // the delete can't go through.
    expect(screen.queryByText(/will be stopped/)).toBeNull()
    expect(screen.queryByText(/will keep running/)).toBeNull()
  })

  it('shows the Commit and Push button when canPush is true', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ canPush: true, hasUncommittedChanges: true, diffAdded: 1 }),
    )
    renderDialog()
    await waitFor(() => {
      expect(screen.getByRole('button', { name: /Commit and Push/ })).toBeInTheDocument()
    })
  })

  it('hides the Commit and Push button when canPush is false', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ canPush: false, hasUncommittedChanges: false, unpushedCommitCount: 0 }),
    )
    renderDialog()
    // Wait for the dialog to settle into the non-worktree layout.
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /Commit and Push/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull()
  })

  it('hides the Push button when canPush is true but the branch is already clean', async () => {
    // canPush is a capability check (origin exists, valid branch name).
    // A clean tree against an existing upstream has nothing to push, so
    // the button must not render even though the capability is there.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        canPush: true,
        hasUncommittedChanges: false,
        unpushedCommitCount: 0,
        upstreamExists: true,
        remoteBranchMissing: false,
      }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    expect(screen.getByText(/No uncommitted changes or unpushed commits/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Commit and Push/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Push' })).toBeNull()
  })

  it('shows the Push button when only upstream is missing (no other pending work)', async () => {
    // Pins the !upstreamExists trigger of hasPushableWork in isolation —
    // remoteBranchMissing is left at its default false so a regression
    // that drops the upstream branch of the predicate fails here.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        canPush: true,
        hasUncommittedChanges: false,
        unpushedCommitCount: 0,
        upstreamExists: false,
        remoteBranchMissing: false,
      }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())
  })

  it('shows the Push button when only the remote branch is missing (upstream still set)', async () => {
    // Pre-push state: upstream metadata exists (e.g. tracking origin/<name>)
    // but origin doesn't actually have the ref yet. Pins the
    // remoteBranchMissing trigger of hasPushableWork in isolation.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        canPush: true,
        hasUncommittedChanges: false,
        unpushedCommitCount: 0,
        upstreamExists: true,
        remoteBranchMissing: true,
      }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())
  })

  it('push button picks the first AGENT/TERMINAL tab when a FILE tab is in slot 0', async () => {
    // Regression: PushBranchButton used to take props.tabs[0] without
    // filtering tab type. A branch group whose sidebar sort placed a
    // FILE tab first would route push at that FILE id, which the
    // worker's getTabWorkingDir rejects as 'unsupported tab type'.
    // The dialog must walk past FILE tabs to find one whose
    // loadTabGitContext can actually resolve a working directory.
    const inspectResp = makeInspectResp({ canPush: true, unpushedCommitCount: 1 })
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(inspectResp)
    renderDialog({ tabs: [makeFileTab('f1'), makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0][1]).toEqual({
      tabType: TabType.TERMINAL,
      tabId: 't1',
    })
  })

  it('hides the push button entirely when the only tabs in the group are FILE tabs', async () => {
    // No AGENT/TERMINAL tab means the worker has nowhere to anchor
    // pushBranch. Rather than render a button that always fails, the
    // dialog must hide it. The branch is still deletable through the
    // switch-to picker — the push affordance is just unavailable.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ canPush: true, unpushedCommitCount: 1 }),
    )
    renderDialog({ tabs: [makeFileTab('f1'), makeFileTab('f2')] })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /Commit and Push|^Push$/ })).toBeNull()
  })

  it('worktree variant: closeTab is called for every tab including FILE', async () => {
    // The worktree-variant delete loop must hand every tab in the
    // branch group to closeTabWithAction so the helper can remove FILE
    // tabs locally (worktree path is about to vanish; orphaning a
    // FILE tab there would point it at a deleted dir). The dialog's
    // contract is "hand each tab to props.closeTab"; the
    // FILE-handling lives inside closeTabWithAction. Pin both: every
    // tab gets a call, FILE included.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.forceRemoveWorktree).mockResolvedValue({
      $typeName: 'leapmux.v1.ForceRemoveWorktreeResponse',
    })
    const closeTab = vi.fn()
    const tabs = [makeAgentTab('a1'), makeFileTab('f1'), makeTerminalTab('t1')]
    renderDialog({ tabs, closeTab })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(closeTab).toHaveBeenCalledTimes(3))
    // Specifically: the FILE tab is NOT skipped by the dialog.
    expect(closeTab.mock.calls.map(c => c[0].id)).toEqual(['a1', 'f1', 't1'])
  })

  it('push button uses a tab from the group to call pushBranch', async () => {
    const inspectResp = makeInspectResp({ canPush: true, unpushedCommitCount: 2 })
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(inspectResp)
    renderDialog({ tabs: [makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    // The worker always re-probes pushStatus to avoid acting on a
    // stale snapshot, so no hint rides the request.
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0][1]).toEqual({
      tabType: TabType.TERMINAL,
      tabId: 't1',
    })
  })

  it('cancel closes without firing any worker RPC', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const closeTab = vi.fn()
    renderDialog({ closeTab })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(workerRpc.deleteBranch).not.toHaveBeenCalled()
    expect(closeTab).not.toHaveBeenCalled()
  })

  it('renders affected-tab counts based on the tabs prop', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    renderDialog({
      tabs: [makeAgentTab('a1'), makeAgentTab('a2'), makeTerminalTab('t1')],
    })
    await waitFor(() =>
      expect(screen.getByText('2 agents and 1 terminal will be stopped.')).toBeInTheDocument(),
    )
  })

  it('renders the affected-tab counts derived from the tabs snapshot', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    renderDialog({
      tabs: [
        makeAgentTab('a1'),
        makeAgentTab('a2'),
        makeAgentTab('a3'),
        makeAgentTab('a4'),
        makeAgentTab('a5'),
        makeTerminalTab('t1'),
        makeTerminalTab('t2'),
        makeTerminalTab('t3'),
        makeTerminalTab('t4'),
      ],
    })
    await waitFor(() =>
      expect(screen.getByText('5 agents and 4 terminals will be stopped.')).toBeInTheDocument(),
    )
  })

  it('hides the affected-tab line entirely when the snapshot has zero agents/terminals', async () => {
    // The worktree variant counts every tab (agents + terminals) as
    // "will be stopped"; zero counts must hide the line entirely so we
    // don't render "0 agents will be stopped" copy.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    renderDialog({ tabs: [] })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    expect(screen.queryByText(/will be stopped/)).toBeNull()
  })

  it('inspect failure surfaces an error in the dialog and disables Delete', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockRejectedValue(new Error('worker offline'))
    renderDialog()
    await waitFor(() => expect(screen.getByText('worker offline')).toBeInTheDocument())
    const del = screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement
    expect(del.disabled).toBe(true)
  })

  it('keeps remote branches in the non-worktree switch-to picker', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        branches: [
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'main', isRemote: false },
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'doomed', isRemote: false },
          { $typeName: 'leapmux.v1.GitBranchEntry', name: 'origin/release', isRemote: true },
        ] as GitBranchEntry[],
      }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    const optionValues = Array.from(select.options).map(o => o.value)
    // Exact set: showPrompt prepends the empty option, the doomed/current
    // local branch is dropped, the remote branch survives.
    expect(optionValues).toEqual(['', 'main', 'origin/release'])
  })

  it('non-worktree variant says tabs will keep running, not stopped', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog({ tabs: [makeAgentTab('a1'), makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    expect(screen.getByText('1 agent and 1 terminal will keep running.')).toBeInTheDocument()
  })

  it('hides the worktree path when the branch is non-worktree', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: false, worktreePath: '' }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    expect(screen.queryByText(/Worktree:/)).toBeNull()
  })

  it('surfaces a deleteBranch failure message in the dialog', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    vi.mocked(workerRpc.deleteBranch).mockRejectedValue(new Error('branch in use'))

    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()

    await waitFor(() => expect(screen.getByText('branch in use')).toBeInTheDocument())
  })

  // Regression: a successful post-push refreshInspect replaces info()
  // with a new truthy value. Solid's <Show> render-prop callback only
  // fires on truthy/falsy boundaries, so any code that captured a
  // const data = i() snapshot would have rendered stale gitState in
  // the body even though the footer's `info()?.gitState?.canPush`
  // accessor saw the refresh. Verify the body's "N commits not pushed"
  // line flips with the refreshed payload.
  it('body re-renders when refreshInspect replaces info() with a new truthy value', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion)
      .mockResolvedValueOnce(makeInspectResp({ canPush: true, unpushedCommitCount: 2 }))
      .mockResolvedValueOnce(makeInspectResp({ canPush: false, unpushedCommitCount: 0 }))
    vi.mocked(workerRpc.pushBranch).mockResolvedValue({ $typeName: 'leapmux.v1.PushBranchResponse' })

    renderDialog({ tabs: [makeTerminalTab('t1')] })
    // First inspect — push needed, two commits ahead.
    await waitFor(() => expect(screen.getByText(/2 commits not pushed/)).toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument()

    // Push fires the second inspect; after it resolves, BranchStatusInfo
    // must re-read the refreshed gitState.
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))

    // The clean copy is the canonical "everything is fine" line that
    // BranchStatusInfo renders when neither uncommitted nor unpushed
    // remain — so its presence implies the refreshed gitState flowed
    // through.
    await waitFor(() => expect(screen.getByText(/No uncommitted changes or unpushed commits/)).toBeInTheDocument())
    expect(screen.queryByText(/2 commits not pushed/)).toBeNull()
    // The Push button disappears too (canPush flipped to false), which
    // the footer's `info()?.gitState?.canPush` already read reactively
    // — pin it so we don't regress the second reactive surface.
    expect(screen.queryByRole('button', { name: /Commit and Push|^Push$/ })).toBeNull()
  })

  // Regression: PushBranchButton used to read props.tabs[0].type without
  // an empty-array guard, so a hasPushableWork=true response combined
  // with an empty tabs snapshot crashed the dialog render. The Show
  // gate must include tabs.length > 0 so the typed Tab[] contract is
  // honored even at the empty edge.
  it('does not render the Push button when tabs is empty even if canPush is true', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        isWorktree: true,
        worktreePath: '/wt',
        canPush: true,
        unpushedCommitCount: 2,
      }),
    )
    renderDialog({ tabs: [] })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: /Commit and Push|^Push$/ })).toBeNull()
  })

  // Regression: the dialog used to show stale pre-push BranchStatusInfo
  // during the post-push inspect.refresh() because the spinner gate
  // depended only on !info() && !error(). The refresh indicator must
  // appear when inspect.loading() is true even after the first inspect
  // landed.
  it('shows a refresh indicator while inspect.refresh() is in flight after a successful push', async () => {
    let resolveSecond!: (r: InspectBranchDeletionResponse) => void
    vi.mocked(workerRpc.inspectBranchDeletion)
      .mockResolvedValueOnce(makeInspectResp({ canPush: true, unpushedCommitCount: 1 }))
      .mockReturnValueOnce(new Promise<InspectBranchDeletionResponse>((r) => { resolveSecond = r }))
    vi.mocked(workerRpc.pushBranch).mockResolvedValue({ $typeName: 'leapmux.v1.PushBranchResponse' })

    renderDialog({ tabs: [makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(screen.getByTestId('delete-branch-refresh-indicator')).toBeInTheDocument())
    // Body stays visible; the indicator is additive, not a replacement.
    expect(screen.getByText(/Branch:/)).toBeInTheDocument()
    // Unblock the refresh so the test doesn't leak the promise.
    resolveSecond(makeInspectResp({ canPush: false, unpushedCommitCount: 0 }))
  })

  // Regression: the dialog's error block lives outside the body's
  // <Show when={info()}> gate, so a refresh that fails AFTER the
  // initial inspect succeeded must surface the error alongside the
  // still-rendered body — not replace it. The earlier Switch+Match
  // structure made one of the two cases (info-set, error-set)
  // unreachable when both were truthy at the same time.
  it('renders error AND body together when a post-inspect refresh fails', async () => {
    // Initial inspect succeeds with canPush:true so the Push button renders.
    // The post-Push refresh fires inspectBranchDeletion again — fail it.
    vi.mocked(workerRpc.inspectBranchDeletion)
      .mockResolvedValueOnce(makeInspectResp({ canPush: true, unpushedCommitCount: 1 }))
      .mockRejectedValueOnce(new Error('worker offline'))
    vi.mocked(workerRpc.pushBranch).mockResolvedValue({ $typeName: 'leapmux.v1.PushBranchResponse' })

    renderDialog({ tabs: [makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    // Body rendered: "Branch:" line and the switch-to picker visible.
    expect(screen.getByText(/Branch:/)).toBeInTheDocument()

    // Push triggers handlePushed → refreshInspect → reject.
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(screen.getByText('worker offline')).toBeInTheDocument())
    // The body must still be visible — error never replaces it.
    expect(screen.getByText(/Branch:/)).toBeInTheDocument()
    expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument()
  })
})
