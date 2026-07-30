/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { useWorkerPrivateStreams } from './useWorkerPrivateStreams'

interface OpenedStream {
  workerId: string
  onTabRenamed: (evt: { tabId: string, title: string }) => void
  onFileTabPathRegistered: (evt: { tabId: string, filePath: string, workingDir: string }) => void
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
    return {
      harness,
      ...stores,
      run: () => useWorkerPrivateStreams({ view: stores.view, metadata: stores.metadata }),
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
      opened[0].onFileTabPathRegistered({ tabId: 'f1', filePath: '/repo/nested/new.ts', workingDir: '/repo' })
      expect(s.metadata.get('f1')?.hydrated, 'the event is a worker answer, so the hydrator has nothing left to ask').toBe(true)
      dispose()
    })
  })

  /**
   * A tab this client learns about from the stream — a peer opened it, or this
   * client joined afterwards — has to land in the same branch group as it did
   * for the client that opened it. That grouping keys on `workingDir`, so the
   * event's copy is mirrored onto the tab alongside the path; deriving one from
   * the file's own directory would answer for a different repo whenever the
   * file was opened from another checkout.
   */
  it('mirrors an unfilled tab\'s path AND working dir from the stream', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.FILE, id: 'f1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()

      opened[0].onFileTabPathRegistered({ tabId: 'f1', filePath: '/repo/nested/new.ts', workingDir: '/repo' })

      expect(s.metadata.get('f1')).toMatchObject({
        filePath: '/repo/nested/new.ts',
        workingDir: '/repo',
      })
      dispose()
    })
  })

  /**
   * The event and the CRDT row that describes it travel different routes --
   * worker->client and worker->hub->client -- so the event can win. Dropping the
   * payload when the row has not landed used to be permanent: a second cache
   * still recorded the path, which is exactly what the FILE hydrator treats as
   * "already answered", so nothing asked again and the tab kept an empty path
   * for the life of the page.
   */
  it('mirrors a path that arrives BEFORE the tab\'s CRDT row', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      // A worker to subscribe to, but no `f-early` row yet.
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()

      opened[0].onFileTabPathRegistered({ tabId: 'f-early', filePath: '/repo/early.ts', workingDir: '/repo' })

      emitAddTab({ type: TabType.FILE, id: 'f-early', tileId: s.harness.rootTileId, position: 'b', workerId: 'w1' })
      s.run()
      await flush()

      expect(s.metadata.get('f-early'), 'the row must pick up what arrived before it').toMatchObject({
        filePath: '/repo/early.ts',
        workingDir: '/repo',
      })
      dispose()
    })
  })

  /**
   * The local open path seeds `workingDir` from `getCurrentTabContext()`, which
   * is empty until worker hydration lands, and then marks the tab hydrated -- so
   * the worker's echo is the only thing left that can supply it. Riding on
   * `!filePath` meant a tab that got its path locally could never get its dir,
   * and it stayed ungrouped in the sidebar while the worker answered its
   * close/push questions from a directory the client did not know.
   */
  it('fills a missing working dir on a tab that already has its path', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.FILE, id: 'f2', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()
      // What `handleFileOpen` writes when the context has not hydrated yet.
      s.metadata.patch('f2', { filePath: '/repo/nested/new.ts', workingDir: '' })

      opened[0].onFileTabPathRegistered({ tabId: 'f2', filePath: '/repo/nested/new.ts', workingDir: '/repo' })

      expect(s.metadata.get('f2')).toMatchObject({
        filePath: '/repo/nested/new.ts',
        workingDir: '/repo',
      })
      dispose()
    })
  })

  /**
   * The worker is the resolver, so its answer REPLACES a local guess rather
   * than deferring to it. The local open path seeds `workingDir` from
   * `getCurrentTabContext()` and marks the tab hydrated, so nothing else will
   * ever revisit it -- a gate that wrote only missing fields could by
   * construction never correct a value that was present but wrong, and that
   * value is what every branch-context operation resolves the tab through.
   */
  it('replaces a local guess with the worker-resolved values', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.FILE, id: 'f3', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()
      s.metadata.patch('f3', { filePath: '/guess/a.ts', workingDir: '/guess' })

      opened[0].onFileTabPathRegistered({ tabId: 'f3', filePath: '/repo/a.ts', workingDir: '/repo/wt' })

      expect(s.metadata.get('f3')).toMatchObject({
        filePath: '/repo/a.ts',
        workingDir: '/repo/wt',
      })
      dispose()
    })
  })

  /**
   * Replacing is not the same as clearing. These fields arrive as proto3
   * strings, so "the worker sent nothing" and "the worker sent empty" are the
   * same bytes -- and `mergeDefined` treats a real `''` as a clearing write.
   * An event that carries no working dir must leave the one already there.
   */
  it('does not let an empty event field clear a known value', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({ type: TabType.FILE, id: 'f4', tileId: s.harness.rootTileId, position: 'a', workerId: 'w1' })
      s.run()
      await flush()
      s.metadata.patch('f4', { filePath: '/repo/a.ts', workingDir: '/repo' })

      opened[0].onFileTabPathRegistered({ tabId: 'f4', filePath: '', workingDir: '' })

      expect(s.metadata.get('f4')).toMatchObject({
        filePath: '/repo/a.ts',
        workingDir: '/repo',
      })
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
