import type { WorkspaceTab } from '~/generated/leapmux/v1/workspace_pb'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { listTabsForWorkspace } from './listTabsBatcher'

const mockListTabs = vi.fn<(req: { orgId: string, workspaceIds: string[] }) => Promise<{ tabs: WorkspaceTab[] }>>()

vi.mock('./clients', () => ({
  workspaceClient: {
    listTabs: (req: { orgId: string, workspaceIds: string[] }) => mockListTabs(req),
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
      listTabsForWorkspace('org', 'ws-1'),
      listTabsForWorkspace('org', 'ws-2'),
      listTabsForWorkspace('org', 'ws-3'),
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
      listTabsForWorkspace('org', 'ws-1'),
      listTabsForWorkspace('org', 'ws-1'),
    ])

    expect(mockListTabs).toHaveBeenCalledTimes(1)
    expect(mockListTabs.mock.calls[0][0].workspaceIds).toEqual(['ws-1'])
    expect(a.tabs).toEqual(b.tabs)
    expect(a.tabs.map(t => t.tabId)).toEqual(['ws-1-x'])
  })

  it('keeps batches separate across orgs', async () => {
    mockListTabs.mockImplementation(async ({ orgId, workspaceIds }) => ({
      tabs: workspaceIds.map(id => tab(id, `${orgId}:${id}`)),
    }))

    await Promise.all([
      listTabsForWorkspace('orgA', 'ws-1'),
      listTabsForWorkspace('orgB', 'ws-1'),
    ])

    expect(mockListTabs).toHaveBeenCalledTimes(2)
    const calledOrgs = mockListTabs.mock.calls.map(c => c[0].orgId).sort()
    expect(calledOrgs).toEqual(['orgA', 'orgB'])
  })

  it('returns an empty tabs list when the server omits a requested workspace', async () => {
    mockListTabs.mockImplementation(async () => ({
      tabs: [tab('ws-1', 'a')], // ws-2 silently dropped
    }))

    const [one, two] = await Promise.all([
      listTabsForWorkspace('org', 'ws-1'),
      listTabsForWorkspace('org', 'ws-2'),
    ])

    expect(one.tabs.map(t => t.tabId)).toEqual(['a'])
    expect(two.tabs).toEqual([])
  })

  it('rejects every waiter when the RPC fails', async () => {
    const boom = new Error('boom')
    mockListTabs.mockImplementation(async () => {
      throw boom
    })

    const p1 = listTabsForWorkspace('org', 'ws-1')
    const p2 = listTabsForWorkspace('org', 'ws-2')

    await expect(p1).rejects.toBe(boom)
    await expect(p2).rejects.toBe(boom)
  })

  it('starts a new batch after the previous microtask has flushed', async () => {
    mockListTabs.mockImplementation(async ({ workspaceIds }) => ({
      tabs: workspaceIds.map(id => tab(id, `${id}-x`)),
    }))

    await listTabsForWorkspace('org', 'ws-1')
    await listTabsForWorkspace('org', 'ws-2')

    expect(mockListTabs).toHaveBeenCalledTimes(2)
    expect(mockListTabs.mock.calls[0][0].workspaceIds).toEqual(['ws-1'])
    expect(mockListTabs.mock.calls[1][0].workspaceIds).toEqual(['ws-2'])
  })
})
