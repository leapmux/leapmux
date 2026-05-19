import type { BranchGitState } from '~/generated/leapmux/v1/git_pb'
import { fireEvent, render, screen, waitFor } from '@solidjs/testing-library'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { showInfoToast, showWarnToast } from '~/components/common/Toast'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { PushBranchButton } from './PushBranchButton'

vi.mock('~/api/workerRpc', () => ({
  pushBranch: vi.fn(),
}))

vi.mock('~/components/common/Toast', () => ({
  showInfoToast: vi.fn(),
  showWarnToast: vi.fn(),
}))

function gitState(overrides: Partial<BranchGitState> = {}): BranchGitState {
  return {
    $typeName: 'leapmux.v1.BranchGitState',
    diffAdded: 0,
    diffDeleted: 0,
    diffUntracked: 0,
    unpushedCommitCount: 0,
    hasUncommittedChanges: false,
    upstreamExists: true,
    remoteBranchMissing: false,
    originExists: true,
    canPush: true,
    ...overrides,
  } as BranchGitState
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('pushBranchButton', () => {
  it('labels "Push" when gitState has no uncommitted changes', () => {
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState({ hasUncommittedChanges: false })}
        onPushed={vi.fn()}
      />
    ))
    expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Commit/ })).toBeNull()
  })

  it('labels "Commit and Push" when gitState reports uncommitted changes', () => {
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState({ hasUncommittedChanges: true })}
        onPushed={vi.fn()}
      />
    ))
    expect(screen.getByRole('button', { name: /Commit and Push/ })).toBeInTheDocument()
  })

  it('labels "Push" when gitState is undefined (no snapshot available)', () => {
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={undefined}
        onPushed={vi.fn()}
      />
    ))
    expect(screen.getByRole('button', { name: 'Push' })).toBeInTheDocument()
  })

  it('sends only tabType + tabId on click (gitState is not forwarded to the worker)', async () => {
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    const onPushed = vi.fn()
    const snapshot = gitState({ hasUncommittedChanges: true, upstreamExists: false })
    render(() => (
      <PushBranchButton
        workerId="w7"
        tab={{ type: TabType.TERMINAL, id: 't9' }}
        gitState={snapshot}
        onPushed={onPushed}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: /Commit and Push/ }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    // The worker always re-probes pushStatus, so the snapshot must NOT
    // ride along — including it would let a stale snapshot make the
    // dialog disagree with what the server actually pushes.
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0]).toEqual([
      'w7',
      {
        tabType: TabType.TERMINAL,
        tabId: 't9',
      },
    ])
    await waitFor(() => expect(onPushed).toHaveBeenCalledTimes(1))
  })

  it('still sends only tabType + tabId when gitState is undefined', async () => {
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={undefined}
        onPushed={vi.fn()}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(workerRpc.pushBranch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(workerRpc.pushBranch).mock.calls[0][1]).toEqual({
      tabType: TabType.AGENT,
      tabId: 'a1',
    })
  })

  it('shows the success toast when push resolves', async () => {
    vi.mocked(workerRpc.pushBranch).mockResolvedValueOnce({} as never)
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState()}
        onPushed={vi.fn()}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(showInfoToast).toHaveBeenCalledWith('Branch pushed successfully'))
    expect(showWarnToast).not.toHaveBeenCalled()
  })

  it('shows the warn toast and does not call onPushed when push rejects', async () => {
    const failure = new Error('remote unreachable')
    vi.mocked(workerRpc.pushBranch).mockRejectedValueOnce(failure)
    const onPushed = vi.fn()
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState()}
        onPushed={onPushed}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(showWarnToast).toHaveBeenCalledWith('Failed to push branch', failure))
    expect(onPushed).not.toHaveBeenCalled()
    expect(showInfoToast).not.toHaveBeenCalled()
  })

  it('respects the disabled prop even when not pushing', () => {
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState()}
        onPushed={vi.fn()}
        disabled
      />
    ))
    expect(screen.getByRole('button', { name: 'Push' })).toBeDisabled()
  })

  it('disables the button while the RPC is in flight', async () => {
    // Verifies useDialogSubmit's loading signal is wired into the
    // button's disabled prop. Regression guard for the
    // createLoadingSignal -> useDialogSubmit refactor: a previous
    // hand-rolled version had a chance of leaving the button enabled
    // mid-flight if the spinner state and disabled flag diverged.
    let resolvePush!: () => void
    const pushPromise = new Promise<unknown>((res) => {
      resolvePush = () => res({})
    })
    vi.mocked(workerRpc.pushBranch).mockReturnValueOnce(pushPromise as never)
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState()}
        onPushed={vi.fn()}
      />
    ))
    const button = screen.getByRole('button', { name: 'Push' })
    expect(button).not.toBeDisabled()
    fireEvent.click(button)
    await waitFor(() => expect(button).toBeDisabled())
    resolvePush()
    await waitFor(() => expect(showInfoToast).toHaveBeenCalled())
  })

  it('routes rejection through useDialogSubmit.onError (raw err forwarded to toast)', async () => {
    // Regression guard for the refactor: onError must receive the raw
    // err object so showWarnToast can extract its own message. A naive
    // adoption of `setError(formatError(err))` would have lost the err
    // and passed a string-only sink to the toast helper.
    const failure = new Error('remote unreachable')
    vi.mocked(workerRpc.pushBranch).mockRejectedValueOnce(failure)
    render(() => (
      <PushBranchButton
        workerId="w1"
        tab={{ type: TabType.AGENT, id: 'a1' }}
        gitState={gitState()}
        onPushed={vi.fn()}
      />
    ))
    fireEvent.click(screen.getByRole('button', { name: 'Push' }))
    await waitFor(() => expect(showWarnToast).toHaveBeenCalledTimes(1))
    // Second arg MUST be the original Error instance, not a string.
    const [msg, errArg] = vi.mocked(showWarnToast).mock.calls[0]
    expect(msg).toBe('Failed to push branch')
    expect(errArg).toBe(failure)
  })
})
