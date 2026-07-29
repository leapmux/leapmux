/// <reference types="vitest/globals" />
import type { TabStampTarget } from './syncGitStatusToTabs'
import type { Tab } from '~/stores/tab.types'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { handleBranchChanged } from './handleBranchChanged'

const mockGetGitFileStatus = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  getGitFileStatus: (...a: unknown[]) => mockGetGitFileStatus(...a),
}))

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

function agent(over: Partial<Extract<Tab, { type: TabType.AGENT }>> = {}): Tab {
  return { type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1', ...over } as Tab
}

/**
 * A recording {@link TabStampTarget}. This is exactly the seam the interface
 * exists for: the whole handler runs with no CRDT bridge and no reactive root.
 */
function recordingTarget(tabs: Tab[]) {
  const writes: Array<{ ids: string[], fields: Record<string, unknown> }> = []
  const target: TabStampTarget = {
    get tabs() {
      return tabs
    },
    update: (tabIds, fields) => {
      writes.push({ ids: tabs.filter(t => tabIds.has(t.id)).map(t => t.id), fields: fields as Record<string, unknown> })
    },
  }
  return { target, writes }
}

function fakeGitStore(state: Record<string, unknown>, refresh = vi.fn().mockResolvedValue(undefined)) {
  return { state, refresh } as never
}

beforeEach(() => {
  mockGetGitFileStatus.mockReset()
  mockGetGitFileStatus.mockResolvedValue({
    toplevel: '/repo',
    originUrl: 'o',
    currentBranch: 'feature',
    files: [],
  })
})

/**
 * What has to happen after a branch change, extracted from a 60-line closure
 * that lived inside a JSX prop and was reachable only by rendering
 * `AppShellDialogs`.
 *
 * The fork is the part worth pinning: the ACTIVE repo refreshes the file-status
 * singleton (so the file tree follows), while any OTHER repo is fetched
 * directly and must NOT touch that singleton — it tracks the focused repo's
 * tree, and refreshing it for a background repo would swing the tree to a repo
 * the user is not looking at.
 */
describe('handleBranchChanged', () => {
  it('stamps the new branch on every tab in the repo, across workspaces', async () => {
    const tabs = [
      agent({ id: 'here', workerId: 'w1', gitToplevel: '/repo' }),
      agent({ id: 'elsewhere', workspaceId: 'ws-2', workerId: 'w1', gitToplevel: '/repo' }),
      agent({ id: 'other-repo', workerId: 'w1', gitToplevel: '/other' }),
      agent({ id: 'other-worker', workerId: 'w2', gitToplevel: '/repo' }),
    ]
    const { target, writes } = recordingTarget(tabs)

    handleBranchChanged(
      { target, gitFileStatusStore: fakeGitStore({}), getCurrentTabContext: () => ({} as never) },
      'w1',
      '/repo',
      'feature',
    )

    expect(writes[0].fields).toEqual({ gitBranch: 'feature' })
    expect(writes[0].ids, 'reaches other workspaces, and only this repo on this worker')
      .toEqual(['here', 'elsewhere'])
    await flush()
  })

  it('refreshes the singleton for the ACTIVE repo and reuses its state', async () => {
    const refresh = vi.fn().mockResolvedValue(undefined)
    const store = fakeGitStore({
      workerId: 'w1',
      toplevel: '/repo',
      originUrl: 'o',
      currentBranch: 'feature',
      files: [],
    }, refresh)
    const { target, writes } = recordingTarget([agent({ id: 'a', workerId: 'w1', gitToplevel: '/repo', workingDir: '/repo/sub' })])

    handleBranchChanged(
      { target, gitFileStatusStore: store, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/repo' } as never) },
      'w1',
      '/repo',
      'feature',
    )
    await flush()

    expect(refresh).toHaveBeenCalledWith('w1', '/repo')
    expect(mockGetGitFileStatus, 'no second RPC — the singleton was just refreshed').not.toHaveBeenCalled()
    expect(writes.length, 'branch stamp, then the diff-stat stamp').toBe(2)
  })

  /**
   * `refresh` swallows its own RPC failure, but the continuation does not: it
   * walks every tab in the account and writes metadata. An unhandled rejection
   * there gave no toast and no diagnosis, while the identical failure on the
   * non-active arm was logged -- two arms of one function answering the same
   * question differently.
   */
  it('survives a throwing continuation on the ACTIVE repo', async () => {
    const store = fakeGitStore({ workerId: 'w1', toplevel: '/repo', originUrl: 'o', currentBranch: 'feature', files: [] })
    const tabs = [agent({ id: 'a', workerId: 'w1', gitToplevel: '/repo', workingDir: '/repo/sub' })]
    const target: TabStampTarget = {
      get tabs() {
        return tabs
      },
      // The diff-stat stamp throws; the branch stamp before it must still land,
      // and nothing may escape as an unhandled rejection.
      update: vi.fn()
        .mockImplementationOnce(() => {})
        .mockImplementation(() => { throw new Error('metadata row reclaimed') }),
    }

    expect(() => handleBranchChanged(
      { target, gitFileStatusStore: store, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/repo' } as never) },
      'w1',
      '/repo',
      'feature',
    )).not.toThrow()
    await flush()

    expect(target.update, 'the branch stamp landed before the failure').toHaveBeenCalled()
  })

  it('does NOT touch the singleton for a non-active repo', async () => {
    const refresh = vi.fn().mockResolvedValue(undefined)
    const store = fakeGitStore({}, refresh)
    const { target, writes } = recordingTarget([agent({ id: 'a', workerId: 'w1', gitToplevel: '/repo', workingDir: '/repo/sub' })])

    handleBranchChanged(
      // The user is looking at a DIFFERENT repo.
      { target, gitFileStatusStore: store, getCurrentTabContext: () => ({ workerId: 'w1', gitToplevel: '/elsewhere' } as never) },
      'w1',
      '/repo',
      'feature',
    )
    await flush()

    expect(refresh, 'refreshing it would swing the file tree to a repo the user is not viewing').not.toHaveBeenCalled()
    expect(mockGetGitFileStatus).toHaveBeenCalledWith('w1', { workerId: 'w1', path: '/repo' })
    expect(writes.length, 'the tabs are still stamped from the direct fetch').toBe(2)
  })

  it('stamps nothing when the repo path never resolved', async () => {
    // `isSameRepo` refuses an empty toplevel, so an unresolved path must stamp
    // nothing rather than every un-stamped tab on the worker — the stamp now
    // spans the whole account, so that would be a cross-repo leak.
    const { target, writes } = recordingTarget([agent({ id: 'a', workerId: 'w1' })])

    handleBranchChanged(
      { target, gitFileStatusStore: fakeGitStore({}), getCurrentTabContext: () => ({} as never) },
      'w1',
      '',
      'feature',
    )
    await flush()

    expect(writes).toHaveLength(0)
  })

  it('survives a failed background fetch without throwing', async () => {
    mockGetGitFileStatus.mockRejectedValue(new Error('worker unreachable'))
    const { target } = recordingTarget([agent({ id: 'a', workerId: 'w1', gitToplevel: '/repo', workingDir: '/repo/sub' })])

    expect(() => handleBranchChanged(
      { target, gitFileStatusStore: fakeGitStore({}), getCurrentTabContext: () => ({} as never) },
      'w1',
      '/repo',
      'feature',
    )).not.toThrow()
    await flush()
  })
})
