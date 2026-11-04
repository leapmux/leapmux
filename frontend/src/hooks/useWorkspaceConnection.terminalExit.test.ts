import type { MessageInitShape } from '@bufbuild/protobuf'
import type { TerminalEventSchema, WatchEventsResponse } from '~/generated/proto/leapmux/v1/watch_pb'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import type { QuakeTerminalStore } from '~/stores/quakeTerminal.store'
import type { Tab } from '~/stores/tab.types'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { TerminalInfoSchema, TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { WatchEventsResponseSchema } from '~/generated/proto/leapmux/v1/watch_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { createAgentActivityStore } from '~/stores/agentActivity.store'
import { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createQuakeTerminalStore, quakeKeyId } from '~/stores/quakeTerminal.store'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { emitAddTab, emitRemoveTab } from '~/stores/tabOps'
import { createWorkerOnlineStore } from '~/stores/workerOnline.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

const WS = 'ws-shell-exit'
const WORKER = 'w-1'
const QUAKE_TERMINAL = 'q1'
const TERMINAL_TAB = 't1'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    workerTerminal: {
      ...actual.workerTerminal,
      // The worker already runs the quake terminal, so the panel adopts it.
      listTerminals: vi.fn(async () => ({
        terminals: [
          create(TerminalInfoSchema, {
            terminalId: 'q1',
            quake: true,
            status: TerminalStatus.READY,
            workingDir: '/repo',
          }),
        ],
        verdicts: [],
      })),
    },
    channelManager: {
      getOrOpenChannel: vi.fn().mockResolvedValue('ch-1'),
      hasOpenChannelForWorker: vi.fn().mockReturnValue(true),
      fatalCloseInfo: vi.fn(() => null),
    },
  }
})

vi.mock('~/components/common/Toast', () => ({
  showWarnToast: vi.fn(),
  showInfoToast: vi.fn(),
  showWarnToastUnlessDisconnected: vi.fn(),
}))

/**
 * The xterm instances, replaced by a registry that a test fills by hand. A
 * test mounts an instance after the frames arrive, which is the window in
 * which the hook queues them.
 */
const terminalView = vi.hoisted(() => ({
  instances: new Map<string, object>(),
  readyListeners: new Set<(id: string) => void>(),
  disposed: [] as string[],
}))

vi.mock('~/components/terminal/TerminalView', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/terminal/TerminalView')>()
  return {
    ...actual,
    getTerminalInstance: (id: string) => terminalView.instances.get(id),
    addTerminalInstanceReadyListener: (listener: (id: string) => void) => {
      terminalView.readyListeners.add(listener)
      return () => terminalView.readyListeners.delete(listener)
    },
    disposeTerminalInstance: (id: string) => {
      terminalView.disposed.push(id)
      terminalView.instances.delete(id)
    },
  }
})

/** Each frame that reached an xterm instance, in arrival order. */
const written = vi.hoisted(() => [] as { kind: string, data: number[] }[])

vi.mock('~/lib/terminal', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/lib/terminal')>()
  return {
    ...actual,
    applyTerminalData: (_instance: unknown, payload: { kind: string, data: Uint8Array, endOffset: number }) => {
      written.push({
        kind: payload.kind,
        data: [...payload.data],
      })
      return payload.endOffset
    },
    bufferHasVisibleContent: () => true,
  }
})

// Capture the stream hook's options so each test sends frames through the dispatcher.
const streams = vi.hoisted(() => ({ opts: undefined as unknown }))

vi.mock('~/hooks/useWatchEventsStreams', () => ({
  useWatchEventsStreams: (opts: unknown) => {
    streams.opts = opts
    return {
      abortSignalFor: () => undefined,
    }
  },
}))

beforeEach(() => {
  streams.opts = undefined
  terminalView.instances.clear()
  terminalView.readyListeners.clear()
  terminalView.disposed.length = 0
  written.length = 0
})

function streamOpts(): UseWatchEventsStreamsOpts {
  if (!streams.opts)
    throw new Error('useWorkspaceConnection did not open its watch streams')
  return streams.opts as UseWatchEventsStreamsOpts
}

/** The tab whose working directory the quake panel belongs to. */
const AGENT_TAB: Tab = {
  type: TabType.AGENT,
  id: 'a1',
  workspaceId: WS,
  workerId: WORKER,
  workingDir: '/repo',
}

/**
 * Mount the hook beside a real quake store that shares its metadata, as the
 * shell wires them, and place one terminal tab.
 */
async function mount() {
  const harness = installTestBridge({ workspaceId: WS })
  const [agentTabOpen, setAgentTabOpen] = createSignal(true)
  let dispose!: () => void
  const mounted = createRoot((d) => {
    dispose = d
    // The tab view reads the quake terminals of the store, as the shell's view
    // does, so the hook queues their frames like a tab's. A signal, because the
    // store needs the metadata that the view's bundle creates.
    const [quakeStoreRef, setQuakeStoreRef] = createSignal<QuakeTerminalStore>()
    const stores = createTestTabStores(WS, () => quakeStoreRef()?.detachedTerminals() ?? [])
    const quakeStore = createQuakeTerminalStore({
      metadata: stores.metadata,
      tabForKey: () => agentTabOpen()
        ? AGENT_TAB
        : undefined,
      closeDelayMs: () => 0,
      isWorkspaceMutatable: () => true,
    })
    setQuakeStoreRef(() => quakeStore)
    emitAddTab({
      type: TabType.TERMINAL,
      id: TERMINAL_TAB,
      tileId: harness.rootTileId,
      position: 'p1',
      workerId: WORKER,
    })
    stores.metadata.patch(TERMINAL_TAB, { terminalStatus: TerminalStatus.READY })
    useWorkspaceConnection({
      workerOnline: createWorkerOnlineStore(),
      chatStore: createChatStore(),
      agentInputQueueStore: createAgentInputQueueStore(),
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      controlStore: createControlStore(),
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore: createAgentActivityStore(),
      repoGitStore: createRepoGitStore(),
      quakeStore,
      getActiveQuakeKeyId: () => quakeKeyId(AGENT_TAB as { workerId: string, workingDir: string }),
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
    })
    return { ...stores, quakeStore, rootTileId: harness.rootTileId }
  })
  // Open the panel. The worker answers with the shell it already runs.
  await mounted.quakeStore.open(AGENT_TAB)
  return {
    ...mounted,
    dispose,
    /** Close the last tab in the directory of the quake panel. */
    closeLastTabOfQuakeDirectory: () => {
      setAgentTabOpen(false)
      mounted.quakeStore.retireStaleKeys()
    },
  }
}

function terminalEvent(terminalId: string, event: NonNullable<MessageInitShape<typeof TerminalEventSchema>['event']>): WatchEventsResponse {
  return create(WatchEventsResponseSchema, {
    event: {
      case: 'terminalEvent',
      value: { terminalId, event },
    },
  })
}

/** A frame the worker sends before the xterm instance of its terminal mounts. */
function snapshotFrame(terminalId: string, bytes: number[]): WatchEventsResponse {
  return terminalEvent(terminalId, {
    case: 'data',
    value: {
      data: new Uint8Array(bytes),
      isSnapshot: true,
      endOffset: BigInt(bytes.length),
    },
  })
}

const replayedExit = {
  case: 'statusChange',
  value: { status: TerminalStatus.EXITED },
} as const
const liveClose = {
  case: 'closed',
  value: { exitCode: 0 },
} as const

/** Mount an xterm instance for `terminalId`, the way TerminalView announces one. */
function mountInstance(terminalId: string): void {
  terminalView.instances.set(terminalId, {})
  for (const listener of terminalView.readyListeners)
    listener(terminalId)
}

/**
 * A shell exit reaches the client in two shapes: a live `closed` event, and a
 * replayed EXITED status for a shell that exited before the watch began. Both
 * must take the same transition.
 */
describe('useWorkspaceConnection shell exit', () => {
  for (const [shape, event] of [
    ['a replayed EXITED status', replayedExit],
    ['a live close', liveClose],
  ] as const) {
    // The worker writes no exit notice into a quake terminal, so a panel that
    // stayed open showed a dead shell that swallowed every key.
    it(`retracts the open quake panel on ${shape}`, async () => {
      const s = await mount()
      const keyId = quakeKeyId(AGENT_TAB as { workerId: string, workingDir: string })
      expect(s.quakeStore.entryFor(keyId)?.open, 'the panel is open before the exit').toBe(true)

      streamOpts().onEvent(WORKER, terminalEvent(QUAKE_TERMINAL, event))

      expect(s.quakeStore.entryFor(keyId), 'the panel retracts and lets go of the dead shell').toBeUndefined()
      expect(terminalView.disposed).toEqual([QUAKE_TERMINAL])
      expect(s.metadata.get(QUAKE_TERMINAL), 'no EXITED row is left to paint an exit notice').toBeUndefined()
      s.dispose()
    })

    it(`drops the queued frames of a quake terminal on ${shape}`, async () => {
      const s = await mount()
      expect(s.view.getTerminalTab(QUAKE_TERMINAL), 'the hook queues frames only for a terminal the view holds').toBeDefined()
      streamOpts().onEvent(WORKER, snapshotFrame(QUAKE_TERMINAL, [1, 2]))

      streamOpts().onEvent(WORKER, terminalEvent(QUAKE_TERMINAL, event))
      mountInstance(QUAKE_TERMINAL)

      expect(written, 'the store released the instance, so nothing may read the frames').toEqual([])
      s.dispose()
    })

    it(`marks a terminal tab EXITED on ${shape}`, async () => {
      const s = await mount()

      streamOpts().onEvent(WORKER, terminalEvent(TERMINAL_TAB, event))

      expect(s.view.getTerminalTab(TERMINAL_TAB)?.status).toBe(TerminalStatus.EXITED)
      expect(terminalView.disposed, 'a terminal tab keeps its instance and offers a restart').toEqual([])
      s.dispose()
    })

    // A tab that left the view mounts no instance, so its queued frames have
    // no reader.
    it(`drops the queued frames of a terminal tab that left the view on ${shape}`, async () => {
      const s = await mount()
      streamOpts().onEvent(WORKER, snapshotFrame(TERMINAL_TAB, [4, 5]))
      emitRemoveTab(TabType.TERMINAL, TERMINAL_TAB)
      expect(s.view.getTerminalTab(TERMINAL_TAB)).toBeUndefined()

      streamOpts().onEvent(WORKER, terminalEvent(TERMINAL_TAB, event))
      mountInstance(TERMINAL_TAB)

      expect(written).toEqual([])
      s.dispose()
    })

    // No data frame follows an exit and no instance mounts for a closed tab, so
    // only the departure of the tab itself can release the frames.
    it(`releases the queued frames when the tab closes after ${shape}`, async () => {
      const s = await mount()
      streamOpts().onEvent(WORKER, snapshotFrame(TERMINAL_TAB, [4, 5]))
      streamOpts().onEvent(WORKER, terminalEvent(TERMINAL_TAB, event))

      emitRemoveTab(TabType.TERMINAL, TERMINAL_TAB)
      mountInstance(TERMINAL_TAB)

      expect(written).toEqual([])
      s.dispose()
    })

    // The worker replays the screen of a terminal just before its status, so
    // the frame queued in front of a replayed EXITED is the last screen. The
    // tab stays, and its instance shows that screen when it mounts.
    it(`keeps the queued screen of a terminal tab on ${shape}`, async () => {
      const s = await mount()
      streamOpts().onEvent(WORKER, snapshotFrame(TERMINAL_TAB, [7, 8, 9]))

      streamOpts().onEvent(WORKER, terminalEvent(TERMINAL_TAB, event))
      mountInstance(TERMINAL_TAB)

      expect(written).toEqual([
        {
          kind: 'snapshot',
          data: [7, 8, 9],
        },
      ])
      s.dispose()
    })
  }

  // The watch plan drops a closed tab, so a live shell sends no later frame
  // either.
  it('releases the queued frames of a live terminal whose tab closes', async () => {
    const s = await mount()
    streamOpts().onEvent(WORKER, snapshotFrame(TERMINAL_TAB, [4, 5]))

    emitRemoveTab(TabType.TERMINAL, TERMINAL_TAB)
    mountInstance(TERMINAL_TAB)

    expect(written).toEqual([])
    s.dispose()
  })

  it('releases the queued frames of a quake terminal whose last tab closes', async () => {
    const s = await mount()
    streamOpts().onEvent(WORKER, snapshotFrame(QUAKE_TERMINAL, [1, 2]))

    s.closeLastTabOfQuakeDirectory()
    expect(s.view.getTerminalTab(QUAKE_TERMINAL), 'the panel let go of the shell').toBeUndefined()
    mountInstance(QUAKE_TERMINAL)

    expect(written).toEqual([])
    s.dispose()
  })

  it('keeps the queued frames of a terminal when another tab closes', async () => {
    const s = await mount()
    streamOpts().onEvent(WORKER, snapshotFrame(TERMINAL_TAB, [7, 8]))

    emitAddTab({
      type: TabType.TERMINAL,
      id: 't-other',
      tileId: s.rootTileId,
      position: 'p2',
      workerId: WORKER,
    })
    emitRemoveTab(TabType.TERMINAL, 't-other')
    mountInstance(TERMINAL_TAB)

    expect(written).toEqual([
      {
        kind: 'snapshot',
        data: [7, 8],
      },
    ])
    s.dispose()
  })
})
