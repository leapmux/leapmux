/// <reference types="vitest/globals" />
import type { TabPayloadView } from '~/lib/tabPayload'
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { QuakePanelAction } from '~/generated/proto/leapmux/v1/worker_private_pb'
import { setStorageAccountForTests } from '~/lib/browserStorage'
import { getCRDTBridge, setCRDTBridge, setTabWorkerId, tombstoneTab } from '~/lib/crdt'
import { useMetadataSweep } from '~/stores/tabMetadata.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge, seedWorkspace, TEST_USER_ID } from '~/test-support/crdtBridge'
import { createCanonicalTabPublisher } from '~/test-support/tabPublication'
import { createTestQuakeStore, createTestTabStores } from '~/test-support/tabStores'
import { useWorkerPrivateStreams } from './useWorkerPrivateStreams'

interface OpenedStream {
  workerId: string
  onTabRenamed: (evt: { tabId: string, tabType: TabType, title: string }) => void
  onTabPayloadRegistered: (evt: { tabId: string, payload: TabPayloadView }) => void
  onTabPayloadRevoked: (evt: { tabId: string }) => void
  onQuakePanelCommand: (evt: { workingDir: string, action: QuakePanelAction }) => void
  closed: boolean
}

const opened: OpenedStream[] = []

/**
 * The stream a test drives. The mock push happens during `run()`, and a run
 * that opened nothing must fail loudly here rather than pass silently, so
 * the throw doubles as the type-level guard.
 */
function firstOpened(): OpenedStream {
  const stream = opened[0]
  if (stream === undefined)
    throw new Error('expected a worker private stream to be opened')
  return stream
}

const mockOpen = vi.fn((o: Record<string, unknown>) => {
  const rec: OpenedStream = {
    workerId: o.workerId as string,
    onTabRenamed: o.onTabRenamed as OpenedStream['onTabRenamed'],
    onTabPayloadRegistered: o.onTabPayloadRegistered as OpenedStream['onTabPayloadRegistered'],
    onTabPayloadRevoked: o.onTabPayloadRevoked as OpenedStream['onTabPayloadRevoked'],
    onQuakePanelCommand: o.onQuakePanelCommand as OpenedStream['onQuakePanelCommand'],
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
// The directory the quake panel commands address. A quake terminal belongs to
// one, so a tab without it addresses no panel at all.
const QUAKE_DIR = '/repo'

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
  function mount(acceptsEarlyMetadata = (_id: string, _type: TabType, _worker: string) => false) {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    const quakeStore = createTestQuakeStore(stores.view)
    return {
      harness,
      ...stores,
      quakeStore,
      run: () => useWorkerPrivateStreams({
        acceptsEarlyMetadata,
        state: () => getCRDTBridge()?.speculativeState() ?? null,
        view: stores.view,
        metadata: stores.metadata,
        quakeStore,
      }),
    }
  }

  it('opens a stream for a worker hosting tabs in a NON-active workspace', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      seedWorkspace(s.harness, 'ws-other', 'other-root')
      emitAddTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      emitAddTab({
        type: TabType.AGENT,
        id: 'a2',
        tileId: 'other-root',
        position: 'a',
        workerId: 'w2',
      })
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
      emitAddTab({
        type: TabType.AGENT,
        id: 'a2',
        tileId: 'other-root',
        position: 'a',
        workerId: 'w2',
      })
      s.run()
      await flush()

      const stream = opened.find(o => o.workerId === 'w2')
      expect(stream, 'the off-screen workspace must have a stream at all').toBeDefined()
      stream!.onTabRenamed({ tabId: 'a2', tabType: TabType.AGENT, title: 'Renamed elsewhere' })

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
      emitAddTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      emitAddTab({
        type: TabType.AGENT,
        id: 'a2',
        tileId: s.harness.rootTileId,
        position: 'b',
        workerId: 'w1',
      })
      emitAddTab({
        type: TabType.AGENT,
        id: 'a3',
        tileId: 'other-root',
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()

      expect(opened).toHaveLength(1)
      expect(firstOpened().workerId).toBe('w1')
      dispose()
    })
  })

  /**
   * The subscription must not depend on the workspace already holding a tab.
   * A worker that hosts a tab ANYWHERE is subscribed, so the first tab opened
   * in a brand-new (or so-far empty) workspace — by the `leapmux control` CLI,
   * by another session — is delivered on the stream that is already up.
   */
  it('keeps the worker stream up for a workspace that holds no tabs yet', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      // An empty second workspace: nothing in it, so a (workspace, worker) key
      // would have produced no stream for it at all.
      seedWorkspace(s.harness, 'ws-empty', 'empty-root')
      emitAddTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()

      expect(opened).toHaveLength(1)
      const before = opened.length

      // The first tab lands in the empty workspace on the SAME worker. No new
      // stream is needed, and none is opened — the live one already covers it.
      emitAddTab({
        type: TabType.FILE,
        id: 'f1',
        tileId: 'empty-root',
        position: 'a',
        workerId: 'w1',
      })
      await flush()

      expect(opened).toHaveLength(before)
      firstOpened().onTabPayloadRegistered({
        tabId: 'f1',
        payload: { kind: 'file', filePath: '/repo/nested/new.ts', workingDir: '/repo' },
      })
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
      emitAddTab({
        type: TabType.FILE,
        id: 'f1',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()

      firstOpened().onTabPayloadRegistered({
        tabId: 'f1',
        payload: { kind: 'file', filePath: '/repo/nested/new.ts', workingDir: '/repo' },
      })

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
      const s = mount((id, type, worker) => id === 'f-early' && type === TabType.FILE && worker === 'w1')
      // A worker to subscribe to, but no `f-early` row yet.
      emitAddTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()

      firstOpened().onTabPayloadRegistered({
        tabId: 'f-early',
        payload: { kind: 'file', filePath: '/repo/early.ts', workingDir: '/repo' },
      })

      emitAddTab({
        type: TabType.FILE,
        id: 'f-early',
        tileId: s.harness.rootTileId,
        position: 'b',
        workerId: 'w1',
      })
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
      emitAddTab({
        type: TabType.FILE,
        id: 'f2',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()
      // What `handleFileOpen` writes when the context has not hydrated yet.
      s.metadata.patch('f2', { filePath: '/repo/nested/new.ts', workingDir: '' })

      firstOpened().onTabPayloadRegistered({
        tabId: 'f2',
        payload: { kind: 'file', filePath: '/repo/nested/new.ts', workingDir: '/repo' },
      })

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
      emitAddTab({
        type: TabType.FILE,
        id: 'f3',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()
      s.metadata.patch('f3', { filePath: '/guess/a.ts', workingDir: '/guess' })

      firstOpened().onTabPayloadRegistered({
        tabId: 'f3',
        payload: { kind: 'file', filePath: '/repo/a.ts', workingDir: '/repo/wt' },
      })

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
      emitAddTab({
        type: TabType.FILE,
        id: 'f4',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()
      s.metadata.patch('f4', { filePath: '/repo/a.ts', workingDir: '/repo' })

      firstOpened().onTabPayloadRegistered({
        tabId: 'f4',
        payload: { kind: 'file', filePath: '', workingDir: '' },
      })

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
      emitAddTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: s.harness.rootTileId,
        position: 'a',
      })
      s.run()
      await flush()

      expect(opened).toHaveLength(0)
      dispose()
    })
  })

  // The Control CLI's only route into the UI. It stores nothing, so a client
  // that does not run simply never hears it -- which is the right outcome for
  // what is effectively a remote keystroke.
  describe('the quake panel command', () => {
    // The command addresses a DIRECTORY, so the tab needs one: the hook resolves
    // it back to a tab, which is what supplies the workspace a cold open is
    // refused in.
    async function withCommand(fn: (s: ReturnType<typeof mount>) => void) {
      await createRoot(async (dispose) => {
        const s = mount()
        emitAddTab({
          type: TabType.AGENT,
          id: 'a1',
          tileId: s.harness.rootTileId,
          position: 'a',
          workerId: 'w1',
        })
        s.metadata.patch('a1', { workingDir: QUAKE_DIR })
        s.run()
        await flush()
        fn(s)
        dispose()
      })
    }

    it('opens the panel of the directory it gives', async () => {
      await withCommand((s) => {
        const open = vi.spyOn(s.quakeStore, 'open')
        firstOpened().onQuakePanelCommand({ workingDir: QUAKE_DIR, action: QuakePanelAction.OPEN })
        expect(open).toHaveBeenCalledWith(expect.objectContaining({ id: 'a1' }))
      })
    })

    it('closes the panel of the directory it gives', async () => {
      await withCommand((s) => {
        const close = vi.spyOn(s.quakeStore, 'close')
        firstOpened().onQuakePanelCommand({ workingDir: QUAKE_DIR, action: QuakePanelAction.CLOSE })
        expect(close).toHaveBeenCalledWith({ workerId: 'w1', workingDir: QUAKE_DIR })
      })
    })

    it('toggles the panel of the directory it gives', async () => {
      await withCommand((s) => {
        const toggle = vi.spyOn(s.quakeStore, 'toggle')
        firstOpened().onQuakePanelCommand({ workingDir: QUAKE_DIR, action: QuakePanelAction.TOGGLE })
        expect(toggle).toHaveBeenCalledWith(expect.objectContaining({ id: 'a1' }))
      })
    })

    // The command reaches every frontend of the account, and another one may be
    // looking at a workspace this client is not.
    it('ignores a directory this client has no tab in', async () => {
      await withCommand((s) => {
        const toggle = vi.spyOn(s.quakeStore, 'toggle')
        firstOpened().onQuakePanelCommand({ workingDir: '/somewhere-else', action: QuakePanelAction.TOGGLE })
        expect(toggle).not.toHaveBeenCalled()
      })
    })

    // CLOSE is the one of the three that needs only the KEY, so it must not be
    // refused for want of a tab. It is also the only route to dismissing a
    // panel on a phone, where there is no chord -- and the panel covers the
    // whole centre area, so a refused close strands the user until a reload.
    it('closes the panel of a directory this client has no tab in', async () => {
      await withCommand((s) => {
        const close = vi.spyOn(s.quakeStore, 'close')
        firstOpened().onQuakePanelCommand({ workingDir: '/somewhere-else', action: QuakePanelAction.CLOSE })
        expect(close).toHaveBeenCalledWith({ workerId: 'w1', workingDir: '/somewhere-else' })
      })
    })

    // An action a newer CLI knows and this build does not. Guessing between
    // show and hide would be worse than doing nothing.
    it('ignores an action it does not recognise', async () => {
      await withCommand((s) => {
        const toggle = vi.spyOn(s.quakeStore, 'toggle')
        const open = vi.spyOn(s.quakeStore, 'open')
        const close = vi.spyOn(s.quakeStore, 'close')
        firstOpened().onQuakePanelCommand({ workingDir: QUAKE_DIR, action: QuakePanelAction.UNSPECIFIED })
        expect(toggle).not.toHaveBeenCalled()
        expect(open).not.toHaveBeenCalled()
        expect(close).not.toHaveBeenCalled()
      })
    })
  })

  it('closes every stream when the owner disposes', async () => {
    await createRoot(async (dispose) => {
      const s = mount()
      emitAddTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: s.harness.rootTileId,
        position: 'a',
        workerId: 'w1',
      })
      s.run()
      await flush()
      expect(opened).toHaveLength(1)
      dispose()
      expect(firstOpened().closed, 'streams must not outlive the AppShell that opened them').toBe(true)
    })
  })
})

describe('useWorkerPrivateStreams retired tabs', () => {
  it.each(['payload', 'rename'] as const)('ignores a delayed %s event after canonical retirement', async (kind) => {
    await createRoot(async (dispose) => {
      try {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        const publisher = createCanonicalTabPublisher(harness)
        publisher.publish(TabType.AGENT, 'sibling', { workerId: 'w1' })
        publisher.publish(TabType.FILE, 'closed-file', { workerId: 'w1' })
        stores.metadata.patch('closed-file', { filePath: '/repo/a', hydrated: true })
        useMetadataSweep(() => getCRDTBridge()?.speculativeState() ?? null, stores.metadata)
        useWorkerPrivateStreams({
          acceptsEarlyMetadata: () => false,
          state: () => getCRDTBridge()?.speculativeState() ?? null,
          view: stores.view,
          metadata: stores.metadata,
          quakeStore: createTestQuakeStore(stores.view),
        })
        await flush()
        const stream = firstOpened()
        publisher.deliver([
          tombstoneTab(publisher.context, TabType.FILE, 'closed-file'),
        ])
        await flush()
        expect(stream.closed).toBe(false)
        expect(stores.metadata.get('closed-file')).toBeUndefined()
        if (kind === 'payload') {
          stream.onTabPayloadRegistered({
            tabId: 'closed-file',
            payload: { kind: 'file', filePath: '/repo/a', workingDir: '/repo' },
          })
        }
        else {
          stream.onTabRenamed({ tabId: 'closed-file', tabType: TabType.FILE, title: 'Late rename' })
        }
        expect(stores.metadata.get('closed-file')).toBeUndefined()
      }
      finally { dispose() }
    })
  })
})

describe('useWorkerPrivateStreams cleared Worker register', () => {
  it.each(['payload', 'rename'] as const)('ignores an old Worker %s event after hosting is explicitly cleared', async (kind) => {
    await createRoot(async (dispose) => {
      try {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        const publisher = createCanonicalTabPublisher(harness)
        publisher.publish(TabType.AGENT, 'sibling', { workerId: 'w1' })
        publisher.publish(TabType.FILE, 'unhosted', { workerId: 'w1' })
        stores.metadata.patch('unhosted', { title: 'Current title', filePath: '/current' })
        useWorkerPrivateStreams({
          acceptsEarlyMetadata: () => false,
          state: () => getCRDTBridge()?.speculativeState() ?? null,
          view: stores.view,
          metadata: stores.metadata,
          quakeStore: createTestQuakeStore(stores.view),
        })
        await flush()
        const stream = firstOpened()
        publisher.deliver([
          setTabWorkerId(publisher.context, TabType.FILE, 'unhosted', ''),
        ])
        await flush()
        expect(harness.pending.state.speculativeState.tabs.unhosted?.workerId?.value).toBe('')
        expect(stream.closed).toBe(false)
        const patch = vi.spyOn(stores.metadata, 'patch')
        if (kind === 'payload') {
          stream.onTabPayloadRegistered({
            tabId: 'unhosted',
            payload: { kind: 'file', filePath: '/obsolete', workingDir: '/repo' },
          })
        }
        else {
          stream.onTabRenamed({ tabId: 'unhosted', tabType: TabType.FILE, title: 'Obsolete title' })
        }
        expect(patch).not.toHaveBeenCalled()
        expect(stores.metadata.get('unhosted')).toMatchObject({ title: 'Current title', filePath: '/current' })
      }
      finally { dispose() }
    })
  })
})

describe('useWorkerPrivateStreams creation ownership', () => {
  it.each(['payload', 'rename'] as const)('ignores an unknown %s event without allocating private metadata', async (kind) => {
    await createRoot(async (dispose) => {
      try {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        createCanonicalTabPublisher(harness).publish(TabType.AGENT, 'sibling', { workerId: 'w1' })
        useWorkerPrivateStreams({
          acceptsEarlyMetadata: () => false,
          state: () => getCRDTBridge()?.speculativeState() ?? null,
          view: stores.view,
          metadata: stores.metadata,
          quakeStore: createTestQuakeStore(stores.view),
        })
        await flush()
        if (kind === 'payload') {
          firstOpened().onTabPayloadRegistered({
            tabId: 'unknown',
            payload: { kind: 'file', filePath: '/private', workingDir: '/repo' },
          })
        }
        else {
          firstOpened().onTabRenamed({ tabId: 'unknown', tabType: TabType.AGENT, title: 'Private title' })
        }
        expect(stores.metadata.get('unknown')).toBeUndefined()
      }
      finally { dispose() }
    })
  })

  it('ignores callbacks retained after stream disposal', async () => {
    await createRoot(async (dispose) => {
      const harness = installTestBridge({ workspaceId: WS })
      const stores = createTestTabStores(WS)
      createCanonicalTabPublisher(harness).publish(TabType.FILE, 'file', { workerId: 'w1' })
      stores.metadata.patch('file', { title: 'Current', filePath: '/current' })
      useWorkerPrivateStreams({
        acceptsEarlyMetadata: () => false,
        state: () => getCRDTBridge()?.speculativeState() ?? null,
        view: stores.view,
        metadata: stores.metadata,
        quakeStore: createTestQuakeStore(stores.view),
      })
      await flush()
      const stream = firstOpened()
      dispose()
      stream.onTabRenamed({ tabId: 'file', tabType: TabType.FILE, title: 'Late title' })
      stream.onTabPayloadRegistered({
        tabId: 'file',
        payload: { kind: 'file', filePath: '/late', workingDir: '/repo' },
      })
      expect(stores.metadata.get('file')).toMatchObject({ title: 'Current', filePath: '/current' })
    })
  })

  it('reclaims a revoked unpublished payload while preserving every canonical live tab', async () => {
    await createRoot(async (dispose) => {
      try {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        const publisher = createCanonicalTabPublisher(harness)
        publisher.publish(TabType.FILE, 'live', { workerId: 'w1' })
        publisher.publish(TabType.FILE, 'reassigned', { workerId: 'w2' })
        for (const id of ['unpublished', 'live', 'reassigned'])
          stores.metadata.patch(id, { filePath: '/private', hydrated: true })
        useWorkerPrivateStreams({
          acceptsEarlyMetadata: () => false,
          state: () => getCRDTBridge()?.speculativeState() ?? null,
          view: stores.view,
          metadata: stores.metadata,
          quakeStore: createTestQuakeStore(stores.view),
        })
        await flush()
        const stream = opened.find(stream => stream.workerId === 'w1')!
        for (const id of ['unpublished', 'live', 'reassigned'])
          stream.onTabPayloadRevoked({ tabId: id })
        expect(stores.metadata.get('unpublished')).toBeUndefined()
        expect(stores.metadata.get('live')).toMatchObject({ filePath: '/private' })
        expect(stores.metadata.get('reassigned')).toMatchObject({ filePath: '/private' })
      }
      finally { dispose() }
    })
  })
})

describe('useWorkerPrivateStreams account generation', () => {
  it('reopens the same Worker stream and rejects prior callbacks after switching away and back', async () => {
    await createRoot(async (dispose) => {
      try {
        const harness = installTestBridge({ workspaceId: WS })
        const stores = createTestTabStores(WS)
        createCanonicalTabPublisher(harness).publish(TabType.FILE, 'file', { workerId: 'w1' })
        useWorkerPrivateStreams({
          acceptsEarlyMetadata: () => false,
          state: () => getCRDTBridge()?.speculativeState() ?? null,
          view: stores.view,
          metadata: stores.metadata,
          quakeStore: createTestQuakeStore(stores.view),
        })
        await flush()
        const original = firstOpened()
        let previous = original
        for (const account of ['account-b', TEST_USER_ID]) {
          const replacement = installTestBridge({ userId: account, workspaceId: WS })
          createCanonicalTabPublisher(replacement).publish(TabType.FILE, 'file', { workerId: 'w1' })
          setStorageAccountForTests(account)
          await flush()
          const current = opened.at(-1)!
          expect(previous.closed).toBe(true)
          expect(current).not.toBe(previous)
          previous = current
          current.onTabRenamed({ tabId: 'file', tabType: TabType.FILE, title: account })
          original.onTabRenamed({ tabId: 'file', tabType: TabType.FILE, title: 'Obsolete' })
          original.onTabPayloadRevoked({ tabId: 'file' })
          expect(stores.metadata.get('file')?.title).toBe(account)
        }
        expect(opened.filter(stream => !stream.closed)).toHaveLength(1)
      }
      finally {
        dispose()
        setStorageAccountForTests(TEST_USER_ID)
      }
    })
  })
})
