/// <reference types="vitest/globals" />
import type { DeleteBranchResponse, GitBranchEntry, InspectBranchDeletionResponse, InspectWorktreeRemovalResponse } from '~/generated/leapmux/v1/git_pb'
import type { Tab } from '~/stores/tab.types'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { WorktreeAction } from '~/generated/leapmux/v1/common_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { makeInspectResp, makeWorktreeRemovalResp } from '~/test-support/gitBranchFixtures'
import { menuOptionValues, pickMenuValue } from '~/test-support/menu'
import { DeleteBranchDialog } from './DeleteBranchDialog'

vi.mock('~/api/workerRpc', () => ({
  inspectBranchDeletion: vi.fn(),
  inspectWorktreeRemoval: vi.fn(),
  deleteBranch: vi.fn(),
  pushBranch: vi.fn(),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

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

// The close hand-off. It reports the outcome itself (see
// closeWorktreeTabsAndReport in useTabOperations), so the dialog neither
// awaits it nor reads a summary from it -- these tests assert WHAT the dialog
// hands off, and the outcome mapping is pinned in closeResultToast.test.ts.
function makeCloseWorktreeTabs() {
  return vi.fn()
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
    // Default: git refuses nothing, so the worktree path proceeds. The
    // blocked cases set their own reason.
    vi.mocked(workerRpc.inspectWorktreeRemoval).mockResolvedValue(makeWorktreeRemovalResp())
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

  it('worktree variant hands the whole tab group off with REMOVE', async () => {
    // Worktree removal is coupled to the tab closes: the dialog passes the
    // snapshot group, and the close pipeline runs each tab with
    // WorktreeAction.REMOVE on the worker and reports the folded outcome.
    // `trackedAtInspect` rides along, because only the dialog holds the
    // inspect-time snapshot the reporter ranks below the worker's verdict.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt', worktreeId: 'wt-1' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const tabs = [makeAgentTab('a1'), makeAgentTab('a2'), makeTerminalTab('t1')]
    const props = renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledTimes(1))
    expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs, WorktreeAction.REMOVE, true)
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
  })

  it('worktree variant hands off trackedAtInspect=false for an untracked worktree', async () => {
    // The mirror. An untracked worktree has no DB row, so REMOVE degrades to
    // KEEP server-side and the directory stays. The reporter needs to know,
    // or it reports "not removed" as if something had gone wrong.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt', worktreeId: '' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    renderDialog({ closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledWith(expect.anything(), WorktreeAction.REMOVE, false))
  })

  it('worktree variant: closes WITHOUT waiting for the tab closes to settle', async () => {
    // The behaviour this preflight pays for. The removal behind these
    // closes stops each agent subprocess, which takes a 3-second grace
    // before the kill, and then deletes the whole worktree directory. It
    // asks the user nothing, so the dialog hands it off and dismisses.
    //
    // The hand-off returns nothing at all, which is what makes the await
    // impossible to reintroduce by accident: the dialog has no promise to
    // hold. A resolved onClose proves it dismissed on its own.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const props = renderDialog({ closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(closeWorktreeTabs).toHaveBeenCalledTimes(1)
    expect(showInfoToast).not.toHaveBeenCalled()
    // The preflight must ask about THIS worktree. Every other assertion in
    // this file accepts whatever path the dialog sends, because the mock
    // ignores its arguments. A preflight aimed at the wrong directory then
    // answers for a repository nobody deletes, and reads as a clean "no
    // refusal" every time.
    expect(workerRpc.inspectWorktreeRemoval).toHaveBeenCalledWith('w1', {
      workerId: 'w1',
      path: '/repo',
    })
  })

  it('worktree variant: a blocked preflight keeps the dialog open and closes nothing', async () => {
    // The refusals git states up front are raised while the dialog is still
    // open, because a closed dialog can render none of them. Nothing may be
    // destroyed on this path: the tabs are the user's only route back to
    // the worktree.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.inspectWorktreeRemoval).mockResolvedValue(
      makeWorktreeRemovalResp('This worktree is locked (held for review). Unlock it with `git worktree unlock` first.'),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const props = renderDialog({ closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(screen.getAllByText(/held for review/).length).toBeGreaterThan(0))
    expect(closeWorktreeTabs).not.toHaveBeenCalled()
    expect(props.onClose).not.toHaveBeenCalled()
    expect(showInfoToast).not.toHaveBeenCalled()
  })

  it('worktree variant: a rejected preflight hands the closes off anyway', async () => {
    // A rejection is NOT a refusal. The worker returns an error when its
    // probe cannot answer, and it fails open on exactly that error --
    // offering the removal unqualified rather than hiding one the user can
    // still have. This surface must agree, or one worker state refuses the
    // delete here and allows it in the last-tab dialog. The close then
    // reports the real outcome, which is what it did before the preflight
    // existed.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    vi.mocked(workerRpc.inspectWorktreeRemoval).mockRejectedValue(new Error('git worktree list: exit status 128'))
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const props = renderDialog({ closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(closeWorktreeTabs).toHaveBeenCalledTimes(1)
    // And the failure is not dressed up as a refusal: nothing renders the
    // probe's error text at the user, because it says nothing about whether
    // the removal succeeds.
    expect(screen.queryByText(/exit status 128/)).not.toBeInTheDocument()
  })

  it('worktree variant: a blocked reason on the OPEN inspect disables Delete up front', async () => {
    // The user must learn the refusal BEFORE arming a destructive two-click
    // confirm, not after firing it. The verdict rides on the same open-time
    // inspect that populated the dialog, so it costs no extra round trip --
    // exactly what the last-tab dialog already does with the same verdict.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        isWorktree: true,
        worktreePath: '/wt',
        worktreeRemovalBlockedReason: 'This worktree is locked (held for review). Unlock it with `git worktree unlock` first.',
      }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    renderDialog({ closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    const del = screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement
    expect(del.disabled).toBe(true)
    expect(del.title).toContain('held for review')
    // Visible text, not the `title` alone: a greyed-out destructive option
    // with no stated reason looks like a defect.
    expect(screen.getAllByText(/held for review/).length).toBeGreaterThan(0)
    // Nothing was armed, so nothing reached the confirm-time re-check either.
    expect(workerRpc.inspectWorktreeRemoval).not.toHaveBeenCalled()
    expect(closeWorktreeTabs).not.toHaveBeenCalled()
  })

  it('worktree variant: a blocked reason offers the close-tabs escape hatch', async () => {
    // A refused removal must not leave Cancel as the dialog's one enabled
    // action. The tabs are still closable -- only the removal is refused --
    // so this is the counterpart of the last-tab dialog's "Close anyway".
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        isWorktree: true,
        worktreePath: '/wt',
        worktreeRemovalBlockedReason: 'This worktree is locked. Unlock it with `git worktree unlock` first.',
      }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const tabs = [makeAgentTab('a1')]
    const props = renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Close tabs, keep worktree' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))

    // KEEP, not REMOVE: the whole point is that git refuses the removal.
    expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs, WorktreeAction.KEEP, false)
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
  })

  it('worktree variant: no blocked reason leaves Delete enabled and offers no escape hatch', async () => {
    // The mirror, and the case that matters most: a field read that goes
    // wrong here disables Delete on every worktree branch.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    const del = screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement
    expect(del.disabled).toBe(false)
    expect(del.title).toBe('')
    expect(screen.queryByRole('button', { name: 'Close tabs, keep worktree' })).not.toBeInTheDocument()
  })

  it('worktree variant: states that the removal check was skipped', async () => {
    // An empty blocked reason is also what a clean worktree sends, so a
    // skipped check and an accepted removal look identical without this.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({
        isWorktree: true,
        worktreePath: '/wt',
        errorHint: 'could not check whether git will remove this worktree',
      }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    expect(screen.getByText(/could not check whether git will remove this worktree/)).toBeInTheDocument()
    // A skipped check is not a refusal: the removal stays on offer.
    expect((screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('non-worktree variant: a stray blocked reason cannot disable the in-place delete', async () => {
    // The in-place delete removes no worktree, so the removal verdict must
    // not restrict it. The worker sets the field only for a worktree, and
    // the dialog restates that gate so the two cannot disagree.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: false, worktreeRemovalBlockedReason: 'This worktree is locked.' }),
    )
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

    expect(screen.queryByText(/This worktree is locked/)).not.toBeInTheDocument()
    expect((screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('worktree variant: the confirm button spins while the preflight runs', async () => {
    // The preflight is a round trip, so the button states that it runs —
    // this dialog's custom footer cannot use DialogFormFooter's own
    // spinner-in-submit pattern.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    let release: (r: InspectWorktreeRemovalResponse) => void = () => {}
    vi.mocked(workerRpc.inspectWorktreeRemoval).mockReturnValue(new Promise((r) => {
      release = r
    }))
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(screen.getByRole('button', { name: /Checking/ })).toBeInTheDocument())
    release(makeWorktreeRemovalResp())
  })

  it('non-worktree variant: runs no removal preflight', async () => {
    // The in-place delete removes no worktree, so the preflight has nothing
    // to answer — and it must not restrict a path it cannot describe.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

    await clickDelete()

    await waitFor(() => expect(workerRpc.deleteBranch).toHaveBeenCalledTimes(1))
    expect(workerRpc.inspectWorktreeRemoval).not.toHaveBeenCalled()
  })

  it('worktree variant closes a FILE-only group through closeWorktreeTabs', async () => {
    // A FILE-only branch group is removed identically: the worker
    // ref-counts worktree_tabs type-agnostically (FILE rows count the same
    // as AGENT/TERMINAL). The dialog does not special-case tab type — it
    // hands the whole group to closeWorktreeTabs regardless.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ isWorktree: true, worktreePath: '/wt' }),
    )
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const tabs = [makeFileTab('f1'), makeFileTab('f2')]
    const props = renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())

    await clickDelete()

    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledWith(tabs, WorktreeAction.REMOVE, true))
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
  })

  it('non-worktree variant: Delete disabled until switch-to is chosen', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())

    const del = screen.getByRole('button', { name: 'Delete branch' }) as HTMLButtonElement
    expect(del.disabled).toBe(true)

    pickMenuValue('branch-select-menu', 'main')
    await waitFor(() => expect(del.disabled).toBe(false))
  })

  it('non-worktree variant: fires onBranchChanged with the chosen switch-to branch', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const onBranchChanged = vi.fn()
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    renderDialog({ onBranchChanged, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

    await clickDelete()
    await waitFor(() => expect(onBranchChanged).toHaveBeenCalledTimes(1))
    expect(onBranchChanged).toHaveBeenCalledWith('main')
    // The branch path switches the working dir and leaves the tabs running
    // on the new branch — unlike the worktree path, it must NOT close any
    // tab. Pins the worktree-vs-branch behavioral split from the branch side.
    expect(closeWorktreeTabs).not.toHaveBeenCalled()
  })

  it('non-worktree variant: notifies onBranchChanged BEFORE onClose', async () => {
    // The order changes the behavior; it is not cosmetic. `onClose` tears
    // down the subtree that owns this dialog in AppShellDialogs. A parent
    // callback that runs after the close can read disposed state, and Solid
    // answers such a read with a thrown string. The close ran first, so it
    // swallowed the sidebar stamp and the git-status refresh. The deleted
    // branch's label stayed on screen until a reload.
    //
    // The dialog's own try/catch — not this order — is what keeps a
    // callback throw out of `run`'s "Delete failed" banner.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const calls: string[] = []
    const onBranchChanged = vi.fn(() => {
      calls.push('onBranchChanged')
    })
    const onClose = vi.fn(() => {
      calls.push('onClose')
    })
    renderDialog({ onBranchChanged, onClose })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

    await clickDelete()
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(calls).toEqual(['onBranchChanged', 'onClose'])
  })

  it('non-worktree variant: closes even when no onBranchChanged is supplied', async () => {
    // `onBranchChanged` is optional, and the close now runs AFTER it. An
    // absent callback must therefore still reach `props.onClose()`. The
    // optional call must not drop the close.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const props = renderDialog({ onBranchChanged: undefined })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

    await clickDelete()
    await waitFor(() => expect(props.onClose).toHaveBeenCalledTimes(1))
    expect(showInfoToast).toHaveBeenCalledWith('Branch deleted')
    expect(showWarnToast).not.toHaveBeenCalled()
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
    pickMenuValue('branch-select-menu', 'origin/foo')

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
    pickMenuValue('branch-select-menu', 'feature/auth')

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
      const optionValues = menuOptionValues('branch-select-menu')
      // doomed is filtered (it's the current branch); the rest survive in input order.
      expect(optionValues).toEqual(['main', 'feature', 'origin/doomed'])
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

    pickMenuValue('branch-select-menu', 'main')

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
    const optionValues = menuOptionValues('branch-select-menu')
    // Exact set: the
    // doomed/current branch is filtered out, the rest preserve order.
    expect(optionValues).toEqual(['main', 'feature'])
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

  it('pushes from the group\'s dir whatever kinds of tab it holds', async () => {
    // No anchor tab, and so no kind preference to get wrong. The push names a
    // DIRECTORY, which every tab in a branch group shares by construction and
    // which is client-side metadata on the joined tab -- so it needs no
    // worker-side row. Anchoring on a tab used to mean a FILE tab could be
    // chosen whose `worker_file_tabs` row a peer's close had hard-deleted, and
    // the push failed with "file tab path not found" while a healthy terminal
    // sat beside it.
    const inspectResp = makeInspectResp({ canPush: true, unpushedCommitCount: 1 })
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(inspectResp)
    renderDialog({ tabs: [makeFileTab('f1'), makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0][1]).toEqual({ workingDir: '/repo' })
  })

  it('offers push for a group made only of FILE tabs', async () => {
    // This group had no push affordance at all while FILE tabs could not
    // anchor one -- the user had to open a terminal in the branch just to push
    // it before deleting. There is nothing special about the group: its tabs
    // name a working dir, so the worker can commit and push from it.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ canPush: true, unpushedCommitCount: 1 }),
    )
    renderDialog({ tabs: [makeFileTab('f1'), makeFileTab('f2')] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0][1]).toEqual({ workingDir: '/repo' })
  })

  it('hides the push button when no tab in the group names a directory', async () => {
    // The edge the Show gate covers: a branch row whose tabs all closed while
    // the dialog was open has no dir to push from, and a button that sends an
    // empty one is worse than no button.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(
      makeInspectResp({ canPush: true, unpushedCommitCount: 1 }),
    )
    renderDialog({ tabs: [] })
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
    const closeWorktreeTabs = makeCloseWorktreeTabs()
    const tabs = [makeAgentTab('a1'), makeFileTab('f1'), makeTerminalTab('t1')]
    renderDialog({ tabs, closeWorktreeTabs })
    await waitFor(() => expect(screen.getByText(/Worktree:/)).toBeInTheDocument())
    await clickDelete()
    await waitFor(() => expect(closeWorktreeTabs).toHaveBeenCalledTimes(1))
    // Specifically: the FILE tab is NOT skipped by the dialog.
    expect(closeWorktreeTabs.mock.calls[0][0].map((t: { id: string }) => t.id)).toEqual(['a1', 'f1', 't1'])
  })

  it('push button sends the group\'s working dir to pushBranch', async () => {
    const inspectResp = makeInspectResp({ canPush: true, unpushedCommitCount: 2 })
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(inspectResp)
    renderDialog({ tabs: [makeTerminalTab('t1')] })
    await waitFor(() => expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument())

    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    // The worker always re-probes pushStatus to avoid acting on a
    // stale snapshot, so no hint rides the request.
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0][1]).toEqual({ workingDir: '/repo' })
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

  it('disables Cancel while DeleteBranch is in flight', async () => {
    // The Dialog `busy` flag gates Escape, the backdrop click, and the X
    // button, but it never reaches a custom footer button. Without an
    // explicit `disabled` the user can dismiss the dialog mid-delete: the
    // RPC keeps running, and a failure then calls setError on a disposed
    // dialog, so the user sees no error at all for a delete that did not
    // happen. DialogFormFooter's own Cancel carries the same gate.
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    // Never resolves: holds the dialog in its submitting state.
    vi.mocked(workerRpc.deleteBranch).mockReturnValue(new Promise<DeleteBranchResponse>(() => {}))
    const props = renderDialog()
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

    await clickDelete()
    await waitFor(() => expect(workerRpc.deleteBranch).toHaveBeenCalledTimes(1))

    const cancel = screen.getByRole('button', { name: 'Cancel' }) as HTMLButtonElement
    expect(cancel.disabled).toBe(true)
    fireEvent.click(cancel)
    expect(props.onClose).not.toHaveBeenCalled()
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
    const optionValues = menuOptionValues('branch-select-menu')
    // Exact set: the doomed/current
    // local branch is dropped, the remote branch survives.
    expect(optionValues).toEqual(['main', 'origin/release'])
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
    pickMenuValue('branch-select-menu', 'main')

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
    pickMenuValue('branch-select-menu', 'main')

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
    // error sink and show "Delete failed" for a delete that worked. The
    // try/catch around the stamp is what guarantees that, and the stamp
    // failure surfaces as its own warn toast. (S11)
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeInspectResp())
    const onBranchChanged = vi.fn(() => {
      throw new Error('stamp boom')
    })

    const props = renderDialog({ onBranchChanged })
    await waitFor(() => expect(screen.getByText(/Switch this working directory to:/)).toBeInTheDocument())
    pickMenuValue('branch-select-menu', 'main')

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
})
