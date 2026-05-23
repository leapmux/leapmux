/// <reference types="vitest/globals" />
import type { LastTabCloseChoice, LastTabConfirmState } from './LastTabCloseDialog'
import type { InspectLastTabCloseResponse } from '~/generated/leapmux/v1/git_pb'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { LastTabCloseTarget } from '~/generated/leapmux/v1/git_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { LastTabCloseDialog } from './LastTabCloseDialog'

vi.mock('~/api/workerRpc', () => ({
  pushBranch: vi.fn(),
  inspectLastTabClose: vi.fn(),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
  showErrorToast: vi.fn(),
}))

type GitStateFlat = Partial<{
  diffAdded: number
  diffDeleted: number
  diffUntracked: number
  unpushedCommitCount: number
  hasUncommittedChanges: boolean
  upstreamExists: boolean
  remoteBranchMissing: boolean
  originExists: boolean
  canPush: boolean
}>

function makeState(overrides: Partial<LastTabConfirmState> & GitStateFlat = {}): LastTabConfirmState {
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
    ...rest
  } = overrides
  return {
    $typeName: 'leapmux.v1.InspectLastTabCloseResponse',
    target: LastTabCloseTarget.BRANCH,
    shouldPrompt: true,
    repoRoot: '/repo',
    worktreePath: '',
    worktreeId: '',
    branchName: 'feature',
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
    } as LastTabConfirmState['gitState']),
    workerId: 'w1',
    tabId: 't1',
    tabType: TabType.AGENT,
    resolve: vi.fn(),
    ...rest,
  } as LastTabConfirmState
}

function renderDialog(
  state: LastTabConfirmState,
  onDismiss = vi.fn(),
  onStatusRefreshed?: (s: InspectLastTabCloseResponse) => void,
) {
  render(() => (
    <LastTabCloseDialog
      state={state}
      onDismiss={onDismiss}
      onStatusRefreshed={onStatusRefreshed}
    />
  ))
  return { onDismiss, onStatusRefreshed }
}

describe('lastTabCloseDialog', () => {
  it('renders the worktree variant with worktree path and Delete button', () => {
    renderDialog(makeState({
      target: LastTabCloseTarget.WORKTREE,
      worktreePath: '/tmp/wt',
      branchName: 'wt-branch',
    }))
    expect(screen.getByText(/closing the last tab for worktree/)).toBeInTheDocument()
    // The path appears twice: once in the header sentence and once in
    // BranchStatusInfo's "Worktree:" line.
    expect(screen.getAllByText('/tmp/wt').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
  })

  it('worktree variant intro paragraph wraps the path in <code> and omits the branch sentence', () => {
    // Pin the Show/fallback split: rendering the worktree intro must not
    // also render the branch intro, and the path must live inside a
    // <code> element so it picks up monospace styling.
    renderDialog(makeState({
      target: LastTabCloseTarget.WORKTREE,
      worktreePath: '/tmp/wt',
      branchName: 'wt-branch',
    }))
    const intro = screen.getByText(/closing the last tab for worktree/).closest('p')
    expect(intro).not.toBeNull()
    const code = intro!.querySelector('code')
    expect(code?.textContent).toBe('/tmp/wt')
    // The branch-variant copy must be absent — the Show fallback only
    // renders for the non-worktree target.
    expect(screen.queryByText(/closing the last non-worktree tab for branch/)).toBeNull()
  })

  it('renders the branch variant without a Delete button', () => {
    renderDialog(makeState({
      target: LastTabCloseTarget.BRANCH,
      hasUncommittedChanges: true,
      diffAdded: 3,
    }))
    expect(screen.getByText(/closing the last non-worktree tab for branch/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument()
  })

  it('branch variant intro paragraph wraps the branch name in <code> and omits the worktree sentence', () => {
    renderDialog(makeState({
      target: LastTabCloseTarget.BRANCH,
      branchName: 'release/v1',
      worktreePath: '',
    }))
    const intro = screen.getByText(/closing the last non-worktree tab for branch/).closest('p')
    expect(intro).not.toBeNull()
    const code = intro!.querySelector('code')
    expect(code?.textContent).toBe('release/v1')
    expect(screen.queryByText(/closing the last tab for worktree/)).toBeNull()
  })

  it('renders BranchStatusInfo with agentCount=1 when tabType is AGENT', () => {
    renderDialog(makeState({
      target: LastTabCloseTarget.WORKTREE,
      worktreePath: '/tmp/wt',
      tabType: TabType.AGENT,
    }))
    expect(screen.getByText('1 agent will be stopped.')).toBeInTheDocument()
  })

  it('renders BranchStatusInfo with terminalCount=1 when tabType is TERMINAL', () => {
    renderDialog(makeState({
      target: LastTabCloseTarget.WORKTREE,
      worktreePath: '/tmp/wt',
      tabType: TabType.TERMINAL,
    }))
    expect(screen.getByText('1 terminal will be stopped.')).toBeInTheDocument()
  })

  it('renders BranchStatusInfo with fileCount=1 and a Delete button when tabType is FILE', () => {
    // Regression guard for the original bug: a FILE tab being the
    // last tab on a worktree must surface the worktree-variant dialog
    // (Delete button + "will be closed" verb), not silently skip the
    // confirmation entirely.
    renderDialog(makeState({
      target: LastTabCloseTarget.WORKTREE,
      worktreePath: '/tmp/wt',
      tabType: TabType.FILE,
    }))
    expect(screen.getByText('1 file will be closed.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Delete' })).toBeInTheDocument()
    // FILE closes stop no running process, so the agent/terminal verb
    // must not appear at all.
    expect(screen.queryByText(/will be stopped/)).toBeNull()
    expect(screen.queryByText(/will keep running/)).toBeNull()
  })

  it('cancel resolves cancel and dismisses', () => {
    const resolve = vi.fn<(c: LastTabCloseChoice) => void>()
    const { onDismiss } = renderDialog(makeState({ resolve }))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(resolve).toHaveBeenCalledWith('cancel')
    expect(onDismiss).toHaveBeenCalled()
  })

  it('close anyway → arms then resolves close-anyway', async () => {
    const resolve = vi.fn<(c: LastTabCloseChoice) => void>()
    const { onDismiss } = renderDialog(makeState({ resolve }))
    fireEvent.click(screen.getByRole('button', { name: 'Close anyway' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
    expect(resolve).toHaveBeenCalledWith('close-anyway')
    expect(onDismiss).toHaveBeenCalled()
  })

  it('worktree Delete → arms then resolves schedule-delete', async () => {
    const resolve = vi.fn<(c: LastTabCloseChoice) => void>()
    const { onDismiss } = renderDialog(makeState({
      target: LastTabCloseTarget.WORKTREE,
      worktreePath: '/tmp/wt',
      resolve,
    }))
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    fireEvent.click(screen.getByRole('button', { name: 'Confirm?' }))
    expect(resolve).toHaveBeenCalledWith('schedule-delete')
    expect(onDismiss).toHaveBeenCalled()
  })

  it('push button is "Commit and Push" when uncommitted changes exist', () => {
    renderDialog(makeState({ canPush: true, hasUncommittedChanges: true, diffAdded: 1 }))
    expect(screen.getByRole('button', { name: /Commit and Push/ })).toBeInTheDocument()
  })

  it('push button is "Push" when only unpushed commits exist (no uncommitted)', () => {
    renderDialog(makeState({ canPush: true, unpushedCommitCount: 2 }))
    expect(screen.getByRole('button', { name: /^Push/ })).toBeInTheDocument()
  })

  it('hides Push entirely when canPush is false', () => {
    renderDialog(makeState({ canPush: false }))
    expect(screen.queryByRole('button', { name: /Push/ })).not.toBeInTheDocument()
  })

  it('hides Push when canPush is true but the branch is already clean', () => {
    // canPush is a capability check (origin exists, valid branch name),
    // not "there's something to push". A clean tree against an existing
    // upstream has no work to do, so the button must not render even
    // though the capability is there.
    renderDialog(makeState({
      canPush: true,
      hasUncommittedChanges: false,
      unpushedCommitCount: 0,
      upstreamExists: true,
      remoteBranchMissing: false,
    }))
    // Exact-name matchers — `/Push/` also matches "Pushed" etc., which
    // would mask a regression where copy changes leave the button visible.
    expect(screen.queryByRole('button', { name: 'Push' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Commit and Push' })).not.toBeInTheDocument()
  })

  it('shows Push when only upstream is missing (no other pending work)', () => {
    // The first push sets the upstream — the button must remain visible
    // in that case even though there are zero unpushed commits relative
    // to a missing upstream. Isolated to the upstreamExists trigger
    // (remoteBranchMissing left at default false) so a regression that
    // drops the !upstreamExists branch of hasPushableWork fails this test.
    renderDialog(makeState({
      canPush: true,
      upstreamExists: false,
      remoteBranchMissing: false,
    }))
    expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument()
  })

  it('shows Push when only the remote branch is missing (upstream still set)', () => {
    // Pins the remoteBranchMissing trigger of hasPushableWork in isolation:
    // upstream is configured (e.g. tracking origin/<name>) but origin
    // doesn't actually have the ref yet — a pre-push state where the
    // upstream metadata exists but no commits have been pushed.
    renderDialog(makeState({
      canPush: true,
      upstreamExists: true,
      remoteBranchMissing: true,
      hasUncommittedChanges: false,
      unpushedCommitCount: 0,
    }))
    expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument()
  })

  it('pushes via workerRpc and re-inspects when Push is clicked', async () => {
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    vi.mocked(workerRpc.inspectLastTabClose).mockResolvedValueOnce(makeState({ shouldPrompt: true }) as never)
    const state = makeState({
      canPush: true,
      unpushedCommitCount: 1,
      workerId: 'w7',
      tabId: 't9',
      tabType: TabType.AGENT,
    })
    renderDialog(state)
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => {
      // The worker always re-probes pushStatus to avoid acting on a
      // stale snapshot, so no hint rides the request.
      expect(workerRpc.pushBranch).toHaveBeenCalledWith('w7', {
        tabType: TabType.AGENT,
        tabId: 't9',
      })
      expect(workerRpc.inspectLastTabClose).toHaveBeenCalledWith('w7', { tabType: TabType.AGENT, tabId: 't9' })
    })
  })

  it('invokes onStatusRefreshed with the refreshed response after Push', async () => {
    // Regression guard for the Q#7 refactor: the dialog no longer holds
    // a local status shadow; the parent owns `state` and must be notified
    // so it can merge the refreshed fields back in.
    const refreshed = makeState({ shouldPrompt: false })
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    vi.mocked(workerRpc.inspectLastTabClose).mockResolvedValueOnce(refreshed as never)
    const onStatusRefreshed = vi.fn<(s: InspectLastTabCloseResponse) => void>()
    renderDialog(makeState({ canPush: true, hasUncommittedChanges: true, diffAdded: 1 }), vi.fn(), onStatusRefreshed)
    fireEvent.click(screen.getByRole('button', { name: /Commit and Push/ }))
    await waitFor(() => {
      expect(onStatusRefreshed).toHaveBeenCalledTimes(1)
      expect(onStatusRefreshed).toHaveBeenCalledWith(refreshed)
    })
  })

  it('does not throw when Push completes and onStatusRefreshed is not provided', async () => {
    // onStatusRefreshed is optional — the legacy call shape (without it)
    // must still complete a Push cleanly. Guards against a future
    // refactor accidentally requiring the callback.
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    vi.mocked(workerRpc.inspectLastTabClose).mockResolvedValueOnce(makeState({ shouldPrompt: false }) as never)
    renderDialog(makeState({ canPush: true, unpushedCommitCount: 1 }))
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.inspectLastTabClose).toHaveBeenCalled())
  })

  it('post-push inspect failure: success toast still fires, no "Failed to push" warn toast', async () => {
    // Regression guard: refreshStatus is invoked from PushBranchButton
    // inside useDialogSubmit.run, so any rejection would route into the
    // submit's onError → showWarnToast('Failed to push branch') even
    // though the push itself succeeded, and the success toast (queued
    // AFTER the await) would never fire. The fix swallows the inspect
    // rejection so the user-visible signal matches the actual outcome
    // (push succeeded; refresh failed silently).
    const { showInfoToast, showWarnToast } = await import('~/components/common/Toast')
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    vi.mocked(workerRpc.inspectLastTabClose).mockRejectedValueOnce(new Error('worker hiccup'))
    const onStatusRefreshed = vi.fn<(s: InspectLastTabCloseResponse) => void>()
    renderDialog(makeState({ canPush: true, unpushedCommitCount: 1 }), vi.fn(), onStatusRefreshed)
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(showInfoToast).toHaveBeenCalledWith('Branch pushed successfully'))
    expect(showWarnToast).not.toHaveBeenCalled()
    // The refresh failed silently, so the parent is not notified — the
    // pre-push status it already holds is the most-recent good copy.
    expect(onStatusRefreshed).not.toHaveBeenCalled()
  })

  it('renders both uncommitted-change and unpushed-commit lines when both apply', () => {
    renderDialog(makeState({
      hasUncommittedChanges: true,
      diffAdded: 5,
      diffDeleted: 2,
      diffUntracked: 1,
      unpushedCommitCount: 3,
      canPush: true,
    }))
    expect(screen.getByText(/Uncommitted changes:/)).toBeInTheDocument()
    expect(screen.getByText(/3 commits/)).toBeInTheDocument()
  })

  it('shows a "Branch not pushed to remote" line when remoteBranchMissing is true', () => {
    renderDialog(makeState({
      remoteBranchMissing: true,
      upstreamExists: true,
      canPush: true,
    }))
    expect(screen.getByText('Branch not pushed to remote.')).toBeInTheDocument()
  })

  it('shows the "no changes" line when the branch is clean', () => {
    renderDialog(makeState())
    expect(screen.getByText('No uncommitted changes or unpushed commits.')).toBeInTheDocument()
  })
})
