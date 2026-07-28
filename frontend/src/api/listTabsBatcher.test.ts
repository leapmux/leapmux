import type { WorkspaceTab } from '~/generated/leapmux/v1/workspace_pb'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { listTabsForWorkspace } from './listTabsBatcher'

const mockListTabs = vi.fn<(req: { workspaceIds: string[] }) => Promise<{ tabs: WorkspaceTab[] }>>()

vi.mock('./clients', () => ({
  workspaceClient: {
    listTabs: (req: { workspaceIds: string[] }) => mockListTabs(req),
  },
}))

function tab(workspaceId: string, tabId: string): WorkspaceTab {
  return {
    tabType: 1,
    tabId,
    position: '',
    tileId: '',
    workerId: '',
    workspaceId,
  } as WorkspaceTab
}

beforeEach(() => {
  mockListTabs.mockReset()
})

describe('listTabsForWorkspace', () => {
  it('coalesces concurrent calls for different workspace IDs into one RPC', async () => {
    mockListTabs.mockImplementation(async ({ workspaceIds }) => ({
      tabs: workspaceIds.flatMap(id => [tab(id, `${id}-a`), tab(id, `${id}-b`)]),
    }))

    const [a, b, c] = await Promise.all([
      listTabsForWorkspace('ws-1'),
      listTabsForWorkspace('ws-2'),
      listTabsForWorkspace('ws-3'),
    ])

    expect(mockListTabs).toHaveBeenCalledTimes(1)
    expect(mockListTabs.mock.calls[0][0].workspaceIds.sort()).toEqual(['ws-1', 'ws-2', 'ws-3'])
    expect(a.tabs.map(t => t.tabId)).toEqual(['ws-1-a', 'ws-1-b'])
    expect(b.tabs.map(t => t.tabId)).toEqual(['ws-2-a', 'ws-2-b'])
    expect(c.tabs.map(t => t.tabId)).toEqual(['ws-3-a', 'ws-3-b'])
  })

  it('dedupes concurrent calls for the same workspace ID', async () => {
    mockListTabs.mockImplementation(async ({ workspaceIds }) => ({
      tabs: workspaceIds.map(id => tab(id, `${id}-x`)),
    }))

    const [a, b] = await Promise.all([
      listTabsForWorkspace('ws-1'),
      listTabsForWorkspace('ws-1'),
    ])

    expect(mockListTabs).toHaveBeenCalledTimes(1)
    expect(mockListTabs.mock.calls[0][0].workspaceIds).toEqual(['ws-1'])
    expect(a.tabs).toEqual(b.tabs)
    expect(a.tabs.map(t => t.tabId)).toEqual(['ws-1-x'])
  })

  it('returns an empty tabs list when the server omits a requested workspace', async () => {
    mockListTabs.mockImplementation(async () => ({
      tabs: [tab('ws-1', 'a')], // ws-2 silently dropped
    }))

    const [a, b] = await Promise.all([
      listTabsForWorkspace('ws-1'),
      listTabsForWorkspace('ws-2'),
    ])

    expect(a.tabs.map(t => t.tabId)).toEqual(['a'])
    expect(b.tabs).toEqual([])
  })

  it('rejects every waiter with the same error instance when the RPC fails', async () => {
    // Asserting the identity, not just the rejected status: a `catch` that
    // swallows and re-wraps would still leave every waiter rejected, so a
    // status-only assertion cannot tell the two apart.
    const boom = new Error('boom')
    mockListTabs.mockRejectedValue(boom)

    // Start both before awaiting so they share one batch.
    const p1 = listTabsForWorkspace('ws-1')
    const p2 = listTabsForWorkspace('ws-2')

    await expect(p1).rejects.toBe(boom)
    await expect(p2).rejects.toBe(boom)
  })

  it('starts a new batch after the previous microtask has flushed', async () => {
    // `createBatch` clears `pendingBatch` BEFORE awaiting the RPC, so a call
    // arriving after the flush opens a fresh batch instead of attaching to the
    // spent one. Without that reset (or with its identity check inverted), the
    // second call's resolver is added to a batch whose `flushBatch` has already
    // returned -- so its promise never settles, and because `inflightCache`
    // only deletes its key in a `finally`, every retry dedupes onto the same
    // dead promise. The user sees a workspace's tab tree spin forever with no
    // request in flight and no error.
    mockListTabs.mockImplementation(async ({ workspaceIds }) => ({
      tabs: workspaceIds.map((id: string) => tab(id, 'x')),
    }))

    await listTabsForWorkspace('ws-1')
    await listTabsForWorkspace('ws-2')

    expect(mockListTabs).toHaveBeenCalledTimes(2)
    expect(mockListTabs.mock.calls[0][0].workspaceIds).toEqual(['ws-1'])
    expect(mockListTabs.mock.calls[1][0].workspaceIds).toEqual(['ws-2'])
  })
})
