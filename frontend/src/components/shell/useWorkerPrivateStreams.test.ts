/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { createFileTabPathsStore } from '~/lib/fileTabPaths'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { useWorkerPrivateStreams } from './useWorkerPrivateStreams'

interface OpenedStream {
  workerId: string
  onTabRenamed: (evt: { tabId: string, title: string }) => void
  onFileTabPathRegistered: (evt: { tabId: string, filePath: string }) => void
  closed: boolean
}

const opened: OpenedStream[] = []
const mockOpen = vi.fn((o: Record<string, unknown>) => {
  const rec: OpenedStream = {
    workerId: o.workerId as string,
    onTabRenamed: o.onTabRenamed as OpenedStream['onTabRenamed'],
    onFileTabPathRegistered: o.onFileTabPathRegistered as OpenedStream['onFileTabPathRegistered'],
    closed: false,
  }
  opened.push(rec)
  return () => {
    rec.closed = true
  }
})

vi.mock('~/lib/workerPrivateEvents', () => ({
  openWorkerPrivateEventStream: (o: Record<string, unknown>) => mockOpen(o),
}))

beforeEach(() => {
  opened.length = 0
  mockOpen.mockClear()
})
afterEach(() => setCRDTBridge(null))

const WS = 'ws-active'

const flush = () => new Promise<void>(queueMicrotask)

/**
 * These streams carry the only `TabRenamed` this client ever sees, and they
 * write into `tabMetadata` — one flat map spanning every workspace. Scoping the
 * subscriptions to the ACTIVE workspace therefore left a peer's rename
 * invisible for every workspace off screen, permanently: `useTabHydrators` will
 * not re-ask (`hydrated` is write-once) and the stream bootstrap carries file
 * paths, not titles.
 */
describe('useWorkerPrivateStreams', () => {
  function mount() {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    const fileTabPaths = createFileTabPathsStore()
    return {
      harness,
      ...stores,
      fileTabPaths,
      run: () => useWorkerPrivateStreams({ view: stores.view, metadata: stores.metadata, fileTabPaths }),
    }
  }

  it('opens a stream for a worker hosting tabs in a NON-active workspace', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      seedWorkspace(s.harness, 'ws-other', 'other-root')
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: 'other-root', position: 'a', workerId: 'w2' })
      s.run()
      await flush()

      expect(opened.map(o => o.workerId).sort()).toEqual(['w1', 'w2'])
      dispose()
    })
  })

  it('delivers a rename for an off-screen workspace into metadata', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      seedWorkspace(s.harness, 'ws-other', 'other-root')
      emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: 'other-root', position: 'a', workerId: 'w2' })
      s.run()
      await flush()

      const stream = opened.find(o => o.workerId === 'w2')
      expect(stream, 'the off-screen workspace must have a stream at all').toBeDefined()
      stream!.onTabRenamed({ tabId: 'a2', title: 'Renamed elsewhere' })

      expect(s.metadata.get('a2')?.title).toBe('Renamed elsewhere')
      dispose()
    })
  })

  /**
   * ONE stream per worker, whatever the workspace spread. The pair-keyed shape
   * this replaced opened N of them for the same channel and — worse — derived
   * the pair set from tabs that already existed, so a workspace with no tabs
   * yet had no subscription at all.
   */
  it('opens one stream per worker regardless of how many workspaces it hosts tabs in', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      seedWorkspace(s.harness, 'ws-other', 'other-root')
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: s.harness.rootTileId, position: 'b', workerId: 'w1' })
      emitAddTab({ type: TabType.AGENT, id: 'a3', tileId: 'other-root', position: 'a', workerId: 'w1' })
      s.run()
      await flush()

      expect(opened).toHaveLength(1)
      expect(opened[0].workerId).toBe('w1')
      dispose()
    })
  })

  /**
   * The subscription must not depend on the workspace already holding a tab.
   * A worker that hosts a tab ANYWHERE is subscribed, so the first tab opened
   * in a brand-new (or so-far empty) workspace — by the `leapmux remote` CLI,
   * by another session — is delivered on the stream that is already up.
   */
  it('keeps the worker stream up for a workspace that holds no tabs yet', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      // An empty second workspace: nothing in it, so a (workspace, worker) key
      // would have produced no stream for it at all.
      seedWorkspace(s.harness, 'ws-empty', 'empty-root')
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()

      expect(opened).toHaveLength(1)
      const before = opened.length

      // The first tab lands in the empty workspace on the SAME worker. No new
      // stream is needed, and none is opened — the live one already covers it.
      emitAddTab({ type: TabType.FILE, id: 'f1', tileId: 'empty-root', position: 'a', workerId: 'w1' })
      await flush()

      expect(opened).toHaveLength(before)
      opened[0].onFileTabPathRegistered({ tabId: 'f1', filePath: '/repo/new.ts' })
      expect(s.fileTabPaths.pathFor('f1')).toBe('/repo/new.ts')
      dispose()
    })
  })

  it('skips a tab with no worker to subscribe to', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a' })
      s.run()
      await flush()

      expect(opened).toHaveLength(0)
      dispose()
    })
  })

  it('closes every stream when the owner disposes', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()
      expect(opened).toHaveLength(1)
      dispose()
      expect(opened[0].closed, 'streams must not outlive the AppShell that opened them').toBe(true)
    })
  })
})
