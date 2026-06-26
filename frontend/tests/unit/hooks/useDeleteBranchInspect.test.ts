/// <reference types="vitest/globals" />
import type { InspectBranchDeletionResponse } from '~/generated/leapmux/v1/git_pb'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { useDeleteBranchInspect } from '~/hooks/useDeleteBranchInspect'

vi.mock('~/context/OrgContext', () => ({
  useOrg: () => ({ orgId: () => 'org-1', slug: () => 'admin' }),
}))

vi.mock('~/api/workerRpc', () => ({
  inspectBranchDeletion: vi.fn(),
}))

function makeResp(overrides: Partial<InspectBranchDeletionResponse> = {}): InspectBranchDeletionResponse {
  return {
    $typeName: 'leapmux.v1.InspectBranchDeletionResponse',
    isWorktree: false,
    worktreePath: '',
    branchName: 'doomed',
    gitState: undefined,
    branches: [],
    ...overrides,
  } as InspectBranchDeletionResponse
}

async function flushMicrotasks() {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

describe('useDeleteBranchInspect', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('issues one inspect RPC on mount and exposes the response on info()', async () => {
    const resp = makeResp({
      branches: [
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'main', isRemote: false },
        { $typeName: 'leapmux.v1.GitBranchEntry', name: 'feature', isRemote: false },
      ],
    })
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(resp)
    await createRoot(async (dispose) => {
      const inspect = useDeleteBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'doomed',
        onError: () => {},
      })
      expect(inspect.info()).toBeNull()
      await flushMicrotasks()
      expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(1)
      expect(inspect.info()).toBe(resp)
      expect(inspect.loading()).toBe(false)
      dispose()
    })
  })

  it('forwards props.branchName as the wire hint', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeResp())
    await createRoot(async (dispose) => {
      useDeleteBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'doomed',
        onError: () => {},
      })
      await flushMicrotasks()
      const [, req] = vi.mocked(workerRpc.inspectBranchDeletion).mock.calls[0]
      expect(req).toMatchObject({
        path: '/repo',
        branchNameHint: 'doomed',
      })
      dispose()
    })
  })

  it('sends an empty branchNameHint when branchName is null (sidebar "(no branch)" group)', async () => {
    vi.mocked(workerRpc.inspectBranchDeletion).mockResolvedValue(makeResp())
    await createRoot(async (dispose) => {
      useDeleteBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: null,
        onError: () => {},
      })
      await flushMicrotasks()
      const [, req] = vi.mocked(workerRpc.inspectBranchDeletion).mock.calls[0]
      expect(req).toMatchObject({ branchNameHint: '' })
      dispose()
    })
  })

  it('refresh() re-issues the RPC and surfaces the new response', async () => {
    const first = makeResp()
    const second = makeResp({ branchName: 'doomed-after-push' })
    vi.mocked(workerRpc.inspectBranchDeletion)
      .mockResolvedValueOnce(first)
      .mockResolvedValueOnce(second)
    await createRoot(async (dispose) => {
      const inspect = useDeleteBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'doomed',
        onError: () => {},
      })
      await flushMicrotasks()
      expect(inspect.info()).toBe(first)
      await inspect.refresh()
      await flushMicrotasks()
      expect(workerRpc.inspectBranchDeletion).toHaveBeenCalledTimes(2)
      expect(inspect.info()).toBe(second)
      dispose()
    })
  })

  it('forwards RPC failures to onError and clears loading', async () => {
    const onError = vi.fn()
    vi.mocked(workerRpc.inspectBranchDeletion).mockRejectedValue(new Error('boom'))
    await createRoot(async (dispose) => {
      const inspect = useDeleteBranchInspect({
        workerId: 'w1',
        gitToplevel: '/repo',
        branchName: 'doomed',
        onError,
      })
      await flushMicrotasks()
      expect(onError).toHaveBeenCalledTimes(1)
      expect(onError).toHaveBeenCalledWith(expect.any(Error))
      expect(inspect.info()).toBeNull()
      expect(inspect.loading()).toBe(false)
      dispose()
    })
  })

  it('refuses to RPC with an empty gitToplevel (defense against unstamped branch rows)', async () => {
    // Regression: WorkspaceTabTree.buildBranchRef coerces an unstamped
    // tab.gitToplevel to '' and used to forward that empty path to the
    // worker — SanitizePath then rejected the request as
    // permission-denied. The dialog opens stuck on an auth-style error
    // for what is really a "tab hasn't been git-stamped yet" UI bug.
    // The hook now no-ops the RPC and surfaces a clear error.
    const onError = vi.fn()
    await createRoot(async (dispose) => {
      const inspect = useDeleteBranchInspect({
        workerId: 'w1',
        gitToplevel: '',
        branchName: 'doomed',
        onError,
      })
      await flushMicrotasks()
      expect(workerRpc.inspectBranchDeletion).not.toHaveBeenCalled()
      expect(onError).toHaveBeenCalledTimes(1)
      expect(onError.mock.calls[0][0]).toBeInstanceOf(Error)
      expect((onError.mock.calls[0][0] as Error).message).toMatch(/no resolved repo path/)
      expect(inspect.info()).toBeNull()
      dispose()
    })
  })
})
