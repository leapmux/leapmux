/// <reference types="vitest/globals" />
import type { DeleteBranchResponse, GitBranchEntry, InspectBranchDeletionResponse } from '~/generated/leapmux/v1/git_pb'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { DeleteBranchDialog, worktreeRemovalToast } from './DeleteBranchDialog'

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/workerRpc', () => ({
  inspectBranchDeletion: vi.fn(),
  deleteBranch: vi.fn(),
  pushBranch: vi.fn(),
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
    // Worktree responses thread the DB row id so the dialog can tell a
    // tracked worktree from an untracked one; non-worktree leaves it empty.
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

// Default worktree-close outcome: the happy path where the last tab's
// REMOVE removed the worktree. Tests that exercise still-referenced /
// failed / untracked paths override `closeWorktreeTabs`.
function makeCloseWorktreeTabs(outcome: Partial<{ removed: boolean, failed: boolean, stillReferenced: boolean, unknown: boolean }> = {}) {
  return vi.fn().mockResolvedValue({ removed: true, failed: false, stillReferenced: false, unknown: false, ...outcome })
}

function renderDialog(props: Partial<Parameters<typeof DeleteBranchDialog>[0]> = {}) {
  const tabs = props.tabs ?? [makeAgentTab('a1')]
  const defaults = {
    workerId: 'w1',
    gitToplevel: '/repo',
    branchName: 'doomed',
    tabs,
    closeWorktreeTabs: makeCloseWorktreeTabs(),
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

  it('worktree variant hands the whole tab group to closeWorktreeTabs and toasts the removed outcome', async () => {
    // Worktree removal is coupled to the tab closes: the dialog passes the
    // snapshot group to closeWorktreeTabs (which closes each tab with
    // WorktreeAction.REMOVE on the worker and folds the per-close
    // outcomes), awaits the verdict, and — on a REMOVED outcome — toasts
    // past-tense success. It no longer optimistically promises removal.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: true })
    const tabs = [makeAgentTab('a1'), makeAgentTab('a2'), makeTerminalTab('t1')]
    const props = renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledTimes(1))
    expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs)
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Worktree removed')
  })

  it('worktree variant closes a FILE-only group through closeWorktreeTabs', async () => {
    // A FILE-only branch group is removed identically: the worker
    // ref-counts worktree_tabs type-agnostically (FILE rows count the same
    // as AGENT/TERMINAL). The dialog does not special-case tab type — it
    // hands the whole group to closeWorktreeTabs regardless.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: true })
    const tabs = [makeFileTab('f1'), makeFileTab('f2')]
    const props = renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs))
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Worktree removed')
  })

  it('worktree variant: still-referenced outcome toasts honestly instead of claiming removal', async () => {
    // Closing this group's tabs did not bring the worktree ref-count to
    // zero — sibling tabs in another branch group (or a stale snapshot)
    // still reference it, so the worker kept the worktree. The dialog must
    // say so rather than claim a removal that did not happen (S1/S10).
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: false, stillReferenced: true })
    const props = renderDialog({ tabs: [makeAgentTab('a1')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Tabs closed; worktree still in use elsewhere')
  })

  it('worktree variant: tracked worktree with no removal toasts "not removed", not "still in use"', async () => {
    // Every close degraded to KEEP because its worktree link was already
    // gone (a startup-race strand the worker GC will reclaim): nothing was
    // removed and no close reported the worktree still referenced. The
    // dialog must say it was not removed rather than implying another tab
    // holds it (S5) — only a genuine STILL_REFERENCED earns that message.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: false, failed: false, stillReferenced: false })
    const props = renderDialog({ tabs: [makeAgentTab('a1')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Tabs closed; worktree not removed')
    expect(showInfoToast).not.toHaveBeenCalledWith('Tabs closed; worktree still in use elsewhere')
  })

  it('worktree variant: removal failure shows no success toast (close pipeline already warned)', async () => {
    // On a FAILED outcome the close pipeline already warn-toasted the git
    // error + worktree path for manual cleanup, so the dialog must NOT add
    // a success toast (S2/S8). It still closes — the tabs are gone.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: false, failed: true })
    const props = renderDialog({ tabs: [makeAgentTab('a1')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).not.toHaveBeenCalled()
  })

  it('worktree variant: untracked worktree (empty worktreeId) toasts honestly', async () => {
    // worktreeId === '' is the worker's signal that the worktree dir
    // exists on disk with no DB row backing it (created outside LeapMux
    // via `git worktree add`). Closing with REMOVE is still correct — the
    // worker's GetWorktreeForTab finds no association and degrades REMOVE
    // to KEEP, leaving the dir in place — but the toast must say so rather
    // than promise a removal that won't happen.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt', worktreeId: '' }),
    )
    const tabs = [makeAgentTab('a1'), makeAgentTab('a2')]
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: false })
    const props = renderDialog({ closeWorktreeTabs, tabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs)
    expect(showInfoToast).toHaveBeenCalledWith('Tabs closed (worktree was not tracked)')
  })

  it('worktree variant: a REMOVED outcome wins over a sibling close failure', async () => {
    // A concurrent sibling close can hit a transient partial failure
    // (e.g. its own DB lookup) and report FAILED while the last-reference
    // close still removes the worktree (REMOVED). The dir IS gone, so the
    // dialog must toast "Worktree removed" — `removed` outranks `failed`,
    // whose own detail was already warn-toasted by the close pipeline.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: true, failed: true })
    const props = renderDialog({ tabs: [makeAgentTab('a1'), makeAgentTab('a2')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Worktree removed')
  })

  it('worktree variant: a real removal wins over a stale empty worktreeId snapshot', async () => {
    // worktreeId is captured at inspect time. If the worktree is adopted
    // (gains a DB row) between inspect and confirm, the worker actually
    // removes it (REMOVED) even though the snapshot's worktreeId is still
    // empty. The dialog must report the ground-truth removal, not the
    // stale "not tracked" snapshot.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt', worktreeId: '' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: true })
    const props = renderDialog({ tabs: [makeAgentTab('a1')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Worktree removed')
    expect(showInfoToast).not.toHaveBeenCalledWith('Tabs closed (worktree was not tracked)')
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
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    renderDialog({ onBranchChanged, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()
    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('main')
    // The branch path switches the working dir and leaves the tabs running
    // on the new branch — unlike the worktree path, it must NOT close any
    // tab. Pins the worktree-vs-branch behavioral split from the branch side.
    expect(closeWorktreeTabs).not.toHaveBeenCalled()
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
    const onBranchChanged = vi.fn()
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    renderDialog({ onBranchChanged, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalled())
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

  it('worktree variant: hands every tab including FILE to closeWorktreeTabs', async () => {
    // The dialog must hand the WHOLE branch group to closeWorktreeTabs,
    // FILE tabs included — the worker ref-counts FILE rows the same as
    // AGENT/TERMINAL, and orphaning a FILE tab on a worktree that's about
    // to vanish would point its editor at a deleted dir. The per-tab
    // dispatch + FILE handling lives inside closeWorktreeTabs; the
    // dialog's contract is just "pass the full group, don't filter".
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: true })
    const tabs = [makeAgentTab('a1'), makeFileTab('f1'), makeTerminalTab('t1')]
    renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledTimes(1))
    // Specifically: the FILE tab is NOT skipped by the dialog.
    expect(closeWorktreeTabs.mock.calls[0][0].map((t: { id: string }) => t.id)).toEqual(['a1', 'f1', 't1'])
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
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    renderDialog({ closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(workerRpc.deleteBranch).not.toHaveBeenCalled()
    expect(closeWorktreeTabs).not.toHaveBeenCalled()
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

  it('non-worktree variant: a deleteBranch failure surfaces inline and keeps the dialog open', async () => {
    // Branch deletion is synchronous: the dialog holds open under the busy
    // overlay until DeleteBranch settles, so a failure renders the worker's
    // message in the dialog's inline error row (not a toast) and onClose is
    // never called — the user can pick a different switch-to or retry.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    vi.mocked(workerRpc.deleteBranch).mockRejectedValue(new Error('branch in use'))
    const onBranchChanged = vi.fn()

    const props = renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()

    await waitFor(() => expect(screen.getByText('branch in use')).toBeInTheDocument())
    expect(props.onClose).not.toHaveBeenCalled()
    expect(onBranchChanged).not.toHaveBeenCalled()
    expect(showInfoToast).not.toHaveBeenCalled()
  })

  it('non-worktree variant: holds the dialog open until deleteBranch resolves, then stamps and closes', async () => {
    // Branch deletion blocks: while DeleteBranch is in flight the dialog
    // stays open (onClose not called) and the stamp/toast hold; once it
    // resolves, onBranchChanged stamps the local name, the success toast
    // fires, and the dialog closes.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    let resolveDelete!: (r: DeleteBranchResponse) => void
    vi.mocked(workerRpc.deleteBranch).mockReturnValue(
      new Promise<DeleteBranchResponse>((r) => { resolveDelete = r }),
    )
    const onBranchChanged = vi.fn()
    const props = renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()

    // DeleteBranch is still pending: dialog stays open, nothing stamped.
    await waitFor(() => expect(workerRpc.deleteBranch).toHaveBeenCalledTimes(1))
    expect(props.onClose).not.toHaveBeenCalled()
    expect(onBranchChanged).not.toHaveBeenCalled()
    expect(showInfoToast).not.toHaveBeenCalled()

    resolveDelete({ $typeName: 'leapmux.v1.DeleteBranchResponse' })
    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledWith('main'))
    await waitFor(() => expect(showInfoToast).toHaveBeenCalledWith('Branch deleted'))
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
  })

  it('non-worktree variant: a throwing onBranchChanged does not masquerade as a delete failure', async () => {
    // The delete succeeded on the worker; onBranchChanged only stamps the
    // sidebar label. A throw from it must NOT propagate into the dialog's
    // error sink and show "Delete failed" for an op that worked — success
    // is committed (toast + onClose) before the isolated stamp, and the
    // stamp failure surfaces as its own warn toast. (S11)
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const onBranchChanged = vi.fn(() => {
      throw new Error('stamp boom')
    })

    const props = renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    const select = screen.getAllByRole('combobox')[0] as HTMLSelectElement
    fireEvent.change(select, { target: { value: 'main' } })

    await clickDelete()

    await waitFor(() => expect(showInfoToast).toHaveBeenCalledWith('Branch deleted'))
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('main')
    // The stamp failure is surfaced as a warn toast, not the inline
    // "Delete failed" fallback.
    expect(showWarnToast).toHaveBeenCalledWith('Branch deleted, but failed to update the sidebar label', expect.any(Error))
    expect(screen.queryByText('Delete failed')).toBeNull()
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

  it('worktree variant: a still-referenced outcome wins over a stale empty worktreeId snapshot', async () => {
    // worktreeId is captured at inspect time. If the worktree is adopted
    // (gains a DB row) AND keeps a sibling between inspect and confirm, the
    // worker reports STILL_REFERENCED even though the snapshot's worktreeId
    // is still empty. Only a tracked worktree can report STILL_REFERENCED,
    // so the dialog must say "still in use elsewhere", NOT "not tracked".
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt', worktreeId: '' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: false, stillReferenced: true })
    const props = renderDialog({ tabs: [makeAgentTab('a1')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Tabs closed; worktree still in use elsewhere')
    expect(showInfoToast).not.toHaveBeenCalledWith('Tabs closed (worktree was not tracked)')
  })

  it('worktree variant: an unknown outcome toasts "could not confirm" instead of "not removed"', async () => {
    // A close RPC rejected (worker dropped mid-call) so closeWorktreeTabs
    // couldn't get a definitive verdict. The worker may have removed the
    // worktree under bgCtx anyway, so the dialog must not claim a clean
    // "not removed" — it says it couldn't confirm. (S3)
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs({ removed: false, unknown: true })
    const props = renderDialog({ tabs: [makeAgentTab('a1')], closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Tabs closed; could not confirm worktree removal')
    expect(showInfoToast).not.toHaveBeenCalledWith('Tabs closed; worktree not removed')
  })
})

describe('worktreeRemovalToast', () => {
  // Fill the summary defaults so each case sets only the flags it exercises.
  const s = (o: Partial<{ removed: boolean, failed: boolean, stillReferenced: boolean, unknown: boolean }> = {}) =>
    ({ removed: false, failed: false, stillReferenced: false, unknown: false, ...o })

  it('removed wins over everything, including a stale untracked snapshot and a sibling failure', () => {
    expect(worktreeRemovalToast(s({ removed: true, failed: true, stillReferenced: true, unknown: true }), false)).toBe('Worktree removed')
    expect(worktreeRemovalToast(s({ removed: true }), true)).toBe('Worktree removed')
  })

  it('failed stays silent — the close pipeline already warn-toasted its detail', () => {
    expect(worktreeRemovalToast(s({ failed: true }), true)).toBeNull()
    // failed outranks still-referenced, unknown, and the untracked snapshot.
    expect(worktreeRemovalToast(s({ failed: true, stillReferenced: true, unknown: true }), false)).toBeNull()
  })

  it('still-referenced wins over unknown and a stale untracked snapshot (only a tracked worktree can report it)', () => {
    expect(worktreeRemovalToast(s({ stillReferenced: true, unknown: true }), false))
      .toBe('Tabs closed; worktree still in use elsewhere')
    expect(worktreeRemovalToast(s({ stillReferenced: true }), true))
      .toBe('Tabs closed; worktree still in use elsewhere')
  })

  it('unknown (RPC rejected / unreachable / threw) reports "could not confirm", outranking the inspect snapshot', () => {
    // No definitive verdict from any tab: don't claim removed OR not-removed.
    // Wins over both the untracked snapshot and the tracked "not removed".
    expect(worktreeRemovalToast(s({ unknown: true }), false))
      .toBe('Tabs closed; could not confirm worktree removal')
    expect(worktreeRemovalToast(s({ unknown: true }), true))
      .toBe('Tabs closed; could not confirm worktree removal')
  })

  it('untracked snapshot with no other outcome reports "not tracked"', () => {
    expect(worktreeRemovalToast(s(), false))
      .toBe('Tabs closed (worktree was not tracked)')
  })

  it('tracked but nothing removed reports "not removed" (e.g. a startup-race strand the GC will reclaim)', () => {
    expect(worktreeRemovalToast(s(), true))
      .toBe('Tabs closed; worktree not removed')
  })
})
