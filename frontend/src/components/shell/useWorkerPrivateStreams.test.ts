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
  workspaceId: string
  workerId: string
  onTabRenamed: (evt: { tabId: string, title: string }) => void
  closed: boolean
}

const opened: OpenedStream[] = []
const mockOpen = vi.fn((o: Record<string, unknown>) => {
  const rec: OpenedStream = {
    workspaceId: o.workspaceId as string,
    workerId: o.workerId as string,
    onTabRenamed: o.onTabRenamed as OpenedStream['onTabRenamed'],
    closed: false,
  }
  opened.push(rec)
  return () => {
    rec.closed = true
  }
})

vi.mock('~/lib/workspacePrivateEvents', () => ({
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

      const pairs = opened.map(o => `${o.workspaceId}::${o.workerId}`).sort()
      expect(pairs).toEqual([`${WS}::w1`, 'ws-other::w2'])
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

      const stream = opened.find(o => o.workspaceId === 'ws-other')
      expect(stream, 'the off-screen workspace must have a stream at all').toBeDefined()
      stream!.onTabRenamed({ tabId: 'a2', title: 'Renamed elsewhere' })

      expect(s.metadata.get('a2')?.title).toBe('Renamed elsewhere')
      dispose()
    })
  })

  it('opens one stream per (workspace, worker) pair, not per tab', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: s.harness.rootTileId, position: 'b', workerId: 'w1' })
      s.run()
      await flush()

      expect(opened).toHaveLength(1)
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
