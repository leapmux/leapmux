import { create } from '@bufbuild/protobuf'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType, WorktreeAction, WorktreeRemovalOutcome } from '~/generated/proto/leapmux/v1/common_pb'
import { CloseTabResponseSchema } from '~/generated/proto/leapmux/v1/worker_tab_pb'
import { closeTabOnWorker } from './closeTabOnWorker'

const closeTab = vi.hoisted(() => vi.fn())
vi.mock('~/api/workerRpc', () => ({
  workerTab: {
    closeTab,
  },
}))

const showWarnToast = vi.hoisted(() => vi.fn())
vi.mock('~/components/common/Toast', () => ({ showWarnToast }))

beforeEach(() => {
  closeTab.mockReset()
  showWarnToast.mockReset()
})

describe('closeTabOnWorker', () => {
  it('sends the tab and the worktree action to the worker, and resolves with its response', async () => {
    const response = create(CloseTabResponseSchema, { worktreeRemoval: WorktreeRemovalOutcome.REMOVED })
    closeTab.mockResolvedValue(response)

    const result = await closeTabOnWorker('w-1', { type: TabType.FILE, id: 'f-1' }, WorktreeAction.REMOVE, 'Failed to close tab')

    expect(closeTab).toHaveBeenCalledExactlyOnceWith('w-1', {
      tab: { tabType: TabType.FILE, tabId: 'f-1' },
      worktreeAction: WorktreeAction.REMOVE,
    })
    expect(result).toBe(response)
    expect(showWarnToast).not.toHaveBeenCalled()
  })

  it('shows a partial failure that the worker reports, and still resolves with the response', async () => {
    const response = create(CloseTabResponseSchema, { failureMessage: 'Could not remove the worktree', failureDetail: 'locked' })
    closeTab.mockResolvedValue(response)

    const result = await closeTabOnWorker('w-1', { type: TabType.AGENT, id: 'a-1' }, WorktreeAction.REMOVE, 'Failed to close agent')

    expect(result).toBe(response)
    expect(showWarnToast).toHaveBeenCalledExactlyOnceWith('Could not remove the worktree: locked')
  })

  it('shows the failure label and resolves undefined when the RPC rejects', async () => {
    const error = new Error('worker unreachable')
    closeTab.mockRejectedValue(error)

    const result = await closeTabOnWorker('w-1', { type: TabType.TERMINAL, id: 't-1' }, WorktreeAction.KEEP, 'Failed to close terminal')

    expect(result).toBeUndefined()
    expect(showWarnToast).toHaveBeenCalledExactlyOnceWith('Failed to close terminal', error)
  })

  it('sends nothing without a worker, and warns that a REMOVE cannot reach the worktree', async () => {
    const result = await closeTabOnWorker('', { type: TabType.AGENT, id: 'a-1' }, WorktreeAction.REMOVE, 'Failed to close agent')

    expect(result).toBeUndefined()
    expect(closeTab).not.toHaveBeenCalled()
    expect(showWarnToast).toHaveBeenCalledExactlyOnceWith('Closed the tab, but could not remove its worktree (no worker connection).')
  })

  it('sends nothing and warns about nothing without a worker for a KEEP', async () => {
    const result = await closeTabOnWorker('', { type: TabType.AGENT, id: 'a-1' }, WorktreeAction.KEEP, 'Failed to close agent')

    expect(result).toBeUndefined()
    expect(closeTab).not.toHaveBeenCalled()
    expect(showWarnToast).not.toHaveBeenCalled()
  })
})
