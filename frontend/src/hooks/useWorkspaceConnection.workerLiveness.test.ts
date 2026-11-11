import type { MessageInitShape } from '@bufbuild/protobuf'
import type { TerminalEventSchema, WatchEventsResponse } from '~/generated/proto/leapmux/v1/watch_pb'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { workerTerminal } from '~/api/workerRpc'
import { useTabHydrators } from '~/components/shell/useTabHydrators'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
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
import { createRepoGitStore } from '~/stores/repoGit.store'
import { terminalDisplayStatus } from '~/stores/tab.helpers'
import { TERMINAL_DISCONNECTED } from '~/stores/tab.types'
import { emitAddTab } from '~/stores/tabOps'
import { createWorkerOnlineStore } from '~/stores/workerOnline.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestQuakeStore, createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    workerTerminal: {
      ...actual.workerTerminal,
      listTerminals: vi.fn(),
    },
    workerAgent: {
      ...actual.workerAgent,
      listAgents: vi.fn(async () => ({ agents: [], verdicts: [] })),
      // The tail-reconcile effect asks for the newest page of every agent tab.
      listAgentMessages: vi.fn().mockResolvedValue({ messages: [], hasMore: false }),
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

// Capture the stream hook's options so each test reports the worker's
// liveness and sends frames itself.
const streams = vi.hoisted(() => ({ opts: undefined as unknown }))

vi.mock('~/hooks/useWatchEventsStreams', () => ({
  useWatchEventsStreams: (opts: unknown) => {
    streams.opts = opts
    return {
      abortSignalFor: () => undefined,
    }
  },
}))

/** The xterm instances, which a test fills by hand. */
const terminalView = vi.hoisted(() => ({
  instances: new Map<string, object>(),
}))

vi.mock('~/components/terminal/TerminalView', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/terminal/TerminalView')>()
  return {
    ...actual,
    getTerminalInstance: (id: string) => terminalView.instances.get(id),
  }
})

const listTerminals = vi.mocked(workerTerminal.listTerminals)

beforeEach(() => {
  streams.opts = undefined
  terminalView.instances.clear()
  listTerminals.mockReset()
  listTerminals.mockResolvedValue({ terminals: [], verdicts: [] } as never)
})

function streamOpts(): UseWatchEventsStreamsOpts {
  if (!streams.opts)
    throw new Error('useWorkspaceConnection did not open its watch streams')
  return streams.opts as UseWatchEventsStreamsOpts
}

const WS = 'ws-worker-liveness'
const WORKER = 'w-1'
const OTHER_WORKER = 'w-2'

const flush = () => new Promise<void>(resolve => setTimeout(resolve, 0))

/**
 * Mount the connection hook and the hydrators, wired as AppShell wires them,
 * over the tabs that `place` lists.
 */
function mount(place: { type: TabType, id: string, workerId: string, meta: Record<string, unknown> }[]) {
  const harness = installTestBridge({ workspaceId: WS })
  const stores = createTestTabStores(WS)
  const agentActivityStore = createAgentActivityStore()
  const forget = vi.spyOn(agentActivityStore, 'forget')
  const onWorkerReconnected = vi.fn()
  const workerOnline = createWorkerOnlineStore()
  let position = 0
  for (const tab of place) {
    position += 1
    emitAddTab({
      type: tab.type,
      id: tab.id,
      tileId: harness.rootTileId,
      position: `p${position}`,
      workerId: tab.workerId,
    })
    stores.metadata.patch(tab.id, tab.meta)
  }
  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    const repoGitStore = createRepoGitStore()
    const hydrators = useTabHydrators({
      userId: () => harness.userId,
      view: stores.view,
      metadata: stores.metadata,
      repoGitStore,
      agentActivityStore,
    })
    useWorkspaceConnection({
      workerOnline,
      chatStore: createChatStore(),
      agentInputQueueStore: createAgentInputQueueStore(),
      view: stores.view,
      metadata: stores.metadata,
      selection: stores.selection,
      controlStore: createControlStore(),
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore,
      repoGitStore,
      quakeStore: createTestQuakeStore(),
      getActiveQuakeKeyId: () => null,
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
      onWorkerReconnected: (workerId) => {
        onWorkerReconnected(workerId)
        hydrators.reaskWorkerTerminals(workerId)
      },
    })
  })
  return {
    ...stores,
    dispose,
    forget,
    onWorkerReconnected,
    workerOnline,
    /** What a surface shows for the terminal `id`. */
    shown: (id: string) => terminalDisplayStatus(stores.view.getTerminalTab(id)!, workerOnline.isOnline),
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

function terminalReply(terminals: { id: string, status: TerminalStatus }[]) {
  return {
    terminals: terminals.map(t => create(TerminalInfoSchema, { terminalId: t.id, status: t.status, workingDir: '/repo' })),
    verdicts: [],
  } as never
}

describe('useWorkspaceConnection worker liveness', () => {
  it('reports a worker that comes back once, with its id', () => {
    const s = mount([])
    streamOpts().onWorkerOnline(WORKER, false)
    streamOpts().onWorkerOnline(WORKER, true)
    streamOpts().onWorkerOnline(WORKER, true)

    expect(s.onWorkerReconnected).toHaveBeenCalledExactlyOnceWith(WORKER)
    s.dispose()
  })

  it('reports no reconnect for a worker that was never offline', () => {
    const s = mount([])
    streamOpts().onWorkerOnline(WORKER, true)

    expect(s.onWorkerReconnected).not.toHaveBeenCalled()
    s.dispose()
  })

  it('reports no reconnect for an empty worker id', () => {
    const s = mount([])
    streamOpts().onWorkerOnline('', false)
    streamOpts().onWorkerOnline('', true)

    expect(s.onWorkerReconnected).not.toHaveBeenCalled()
    s.dispose()
  })

  // Every live agent event and every agent status reports its worker online.
  // A report that changes nothing must not run the offline sweep again, or each
  // event re-walks every tab while any worker is offline.
  it('does not run the offline sweep again for a report that changes nothing', () => {
    const s = mount([
      {
        type: TabType.AGENT,
        id: 'a-offline',
        workerId: OTHER_WORKER,
        meta: { agentStatus: AgentStatus.ACTIVE },
      },
    ])
    streamOpts().onWorkerOnline(OTHER_WORKER, false)
    expect(s.forget, 'the sweep clears the agent of the offline worker').toHaveBeenCalledExactlyOnceWith('a-offline')

    streamOpts().onWorkerOnline(WORKER, true)
    streamOpts().onWorkerOnline(OTHER_WORKER, false)

    expect(s.forget, 'neither report changed which workers are offline').toHaveBeenCalledOnce()
    s.dispose()
  })

  it('runs the offline sweep for a second worker that goes offline', () => {
    const s = mount([
      {
        type: TabType.AGENT,
        id: 'a-1',
        workerId: WORKER,
        meta: { agentStatus: AgentStatus.ACTIVE },
      },
      {
        type: TabType.AGENT,
        id: 'a-2',
        workerId: OTHER_WORKER,
        meta: { agentStatus: AgentStatus.ACTIVE },
      },
    ])
    streamOpts().onWorkerOnline(OTHER_WORKER, false)
    streamOpts().onWorkerOnline(WORKER, false)

    expect(s.view.getAgentTab('a-1')?.agentStatus).toBe(AgentStatus.INACTIVE)
    expect(s.view.getAgentTab('a-2')?.agentStatus).toBe(AgentStatus.INACTIVE)
    s.dispose()
  })

  // Another client restarted the shell while this one could not reach the
  // worker. The worker replays a status only for a terminal in FULL mode, so a
  // background tab hears nothing on the new stream. The re-ask heals it.
  it('heals a background EXITED terminal whose shell another client restarted during an outage', async () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-exited',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.EXITED },
      },
    ])
    await flush()
    expect(listTerminals, 'a hydrated EXITED tab asks for nothing on its own').not.toHaveBeenCalled()

    streamOpts().onWorkerOnline(WORKER, false)
    expect(s.shown('t-exited'), 'only a READY terminal shows DISCONNECTED').toBe(TerminalStatus.EXITED)

    listTerminals.mockResolvedValue(terminalReply([
      { id: 't-exited', status: TerminalStatus.READY },
    ]))
    streamOpts().onWorkerOnline(WORKER, true)
    await flush()
    await flush()

    expect(listTerminals).toHaveBeenCalledOnce()
    expect(listTerminals.mock.calls[0]?.[0]).toBe(WORKER)
    expect(s.view.getTerminalTab('t-exited')?.status).toBe(TerminalStatus.READY)
    s.dispose()
  })

  // READY states that the shell runs, so it applies from any status. A replay on
  // a reopened stream can report a shell that another client restarted. A READY
  // that an older reply overtook does no lasting harm either, because the status
  // queue of the worker sends `closed` behind it.
  it('shows READY for a READY after EXITED, and a following closed moves the tab to EXITED', () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-exited',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.EXITED },
      },
    ])

    streamOpts().onEvent(WORKER, terminalEvent('t-exited', {
      case: 'statusChange',
      value: { status: TerminalStatus.READY },
    }))
    expect(s.shown('t-exited')).toBe(TerminalStatus.READY)

    streamOpts().onEvent(WORKER, terminalEvent('t-exited', {
      case: 'closed',
      value: { exitCode: 0 },
    }))
    expect(s.shown('t-exited')).toBe(TerminalStatus.EXITED)
    s.dispose()
  })

  // The reply of the re-ask and the replayed delta race. A mounted xterm never
  // paints the screen of a later reply, so a reply that moved the cursor made
  // `applyTerminalData` drop the delta as already applied, and the output of the
  // outage never reached the view.
  it('shows the replayed output when the reply of the re-ask lands first', async () => {
    const written: number[][] = []
    terminalView.instances.set('t-live', {
      terminal: {
        write: (data: Uint8Array, onParsed: () => void) => {
          written.push([...data])
          onParsed()
        },
      },
    })
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-live',
        workerId: WORKER,
        meta: {
          hydrated: true,
          contentReady: true,
          terminalStatus: TerminalStatus.READY,
          lastOffset: 4,
        },
      },
    ])
    streamOpts().onWorkerOnline(WORKER, false)
    listTerminals.mockResolvedValue({
      terminals: [
        create(TerminalInfoSchema, {
          terminalId: 't-live',
          status: TerminalStatus.READY,
          screen: new Uint8Array([1, 2, 3, 4, 5, 6]),
          screenEndOffset: 6n,
        }),
      ],
      verdicts: [],
    } as never)

    streamOpts().onWorkerOnline(WORKER, true)
    await flush()
    await flush()
    expect(listTerminals).toHaveBeenCalledOnce()

    streamOpts().onEvent(WORKER, terminalEvent('t-live', {
      case: 'data',
      value: {
        data: new Uint8Array([5, 6]),
        isSnapshot: false,
        endOffset: 6n,
      },
    }))

    expect(written).toEqual([
      [5, 6],
    ])
    expect(s.metadata.get('t-live')?.lastOffset).toBe(6)
    s.dispose()
  })

  it('asks again for every terminal of the worker that came back, and for no other', async () => {
    listTerminals.mockRejectedValue(new Error('the worker is unreachable'))
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-exited',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.EXITED },
      },
      {
        type: TabType.TERMINAL,
        id: 't-failed',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.STARTUP_FAILED },
      },
      {
        type: TabType.TERMINAL,
        id: 't-ready',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.READY },
      },
      {
        type: TabType.TERMINAL,
        id: 't-other',
        workerId: OTHER_WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.EXITED },
      },
    ])
    streamOpts().onWorkerOnline(WORKER, false)
    expect(s.shown('t-ready')).toBe(TERMINAL_DISCONNECTED)
    await flush()

    listTerminals.mockReset()
    listTerminals.mockResolvedValue(terminalReply([
      { id: 't-exited', status: TerminalStatus.READY },
      { id: 't-failed', status: TerminalStatus.READY },
      { id: 't-ready', status: TerminalStatus.READY },
    ]))
    streamOpts().onWorkerOnline(WORKER, true)
    await flush()
    await flush()

    const asked = listTerminals.mock.calls.flatMap(([workerId, req]) =>
      (req as { tabIds: string[] }).tabIds.map(id => `${workerId}/${id}`))
    expect(asked.sort()).toEqual([`${WORKER}/t-exited`, `${WORKER}/t-failed`, `${WORKER}/t-ready`])
    expect(s.view.getTerminalTab('t-exited')?.status).toBe(TerminalStatus.READY)
    expect(s.view.getTerminalTab('t-failed')?.status).toBe(TerminalStatus.READY)
    expect(s.view.getTerminalTab('t-ready')?.status).toBe(TerminalStatus.READY)
    expect(s.view.getTerminalTab('t-other')?.status, 'a terminal of another worker keeps its status').toBe(TerminalStatus.EXITED)
    s.dispose()
  })

  it('stores the status of the worker over an outage, for a READY terminal too', () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-ready',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.READY },
      },
      {
        type: TabType.TERMINAL,
        id: 't-starting',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.STARTING },
      },
    ])

    streamOpts().onWorkerOnline(WORKER, false)

    expect(s.view.getTerminalTab('t-ready')?.status).toBe(TerminalStatus.READY)
    expect(s.metadata.get('t-ready')?.terminalStatus).toBe(TerminalStatus.READY)
    expect(s.view.getTerminalTab('t-starting')?.status).toBe(TerminalStatus.STARTING)
    s.dispose()
  })

  it('shows a READY terminal as DISCONNECTED while its worker is offline, and no longer', () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-ready',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.READY },
      },
      {
        type: TabType.TERMINAL,
        id: 't-other',
        workerId: OTHER_WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.READY },
      },
    ])
    expect(s.shown('t-ready')).toBe(TerminalStatus.READY)

    streamOpts().onWorkerOnline(WORKER, false)
    expect(s.shown('t-ready')).toBe(TERMINAL_DISCONNECTED)
    expect(s.shown('t-other'), 'another worker keeps its terminals').toBe(TerminalStatus.READY)

    // Before any reply: the worker reporting back is the whole of the transition.
    streamOpts().onWorkerOnline(WORKER, true)
    expect(s.shown('t-ready')).toBe(TerminalStatus.READY)
    s.dispose()
  })

  it('shows a STARTING terminal of an offline worker as STARTING', () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-starting',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.STARTING },
      },
    ])

    streamOpts().onWorkerOnline(WORKER, false)

    expect(s.shown('t-starting')).toBe(TerminalStatus.STARTING)
    s.dispose()
  })

  it('asks the worker nothing about its terminals until it comes back', async () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-ready',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.READY },
      },
    ])

    streamOpts().onWorkerOnline(WORKER, false)
    await flush()
    await flush()
    expect(listTerminals, 'an offline worker cannot answer').not.toHaveBeenCalled()

    listTerminals.mockResolvedValue(terminalReply([
      { id: 't-ready', status: TerminalStatus.READY },
    ]))
    streamOpts().onWorkerOnline(WORKER, true)
    await flush()
    await flush()
    expect(listTerminals, 'the reconnect still re-asks').toHaveBeenCalledOnce()
    s.dispose()
  })

  it('moves a terminal whose shell exited during the outage to EXITED on the re-ask reply', async () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-ready',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.READY },
      },
    ])
    streamOpts().onWorkerOnline(WORKER, false)
    expect(s.shown('t-ready')).toBe(TERMINAL_DISCONNECTED)

    listTerminals.mockResolvedValue(terminalReply([
      { id: 't-ready', status: TerminalStatus.EXITED },
    ]))
    streamOpts().onWorkerOnline(WORKER, true)
    expect(s.shown('t-ready'), 'the reply has not landed yet').toBe(TerminalStatus.READY)
    await flush()
    await flush()

    expect(s.shown('t-ready')).toBe(TerminalStatus.EXITED)
    s.dispose()
  })

  it('asks once per reconnect, so a later report of the online worker asks nothing', async () => {
    const s = mount([
      {
        type: TabType.TERMINAL,
        id: 't-exited',
        workerId: WORKER,
        meta: { hydrated: true, terminalStatus: TerminalStatus.EXITED },
      },
    ])
    listTerminals.mockResolvedValue(terminalReply([
      { id: 't-exited', status: TerminalStatus.EXITED },
    ]))
    streamOpts().onWorkerOnline(WORKER, false)
    streamOpts().onWorkerOnline(WORKER, true)
    await flush()
    await flush()
    expect(listTerminals).toHaveBeenCalledOnce()

    streamOpts().onWorkerOnline(WORKER, true)
    await flush()
    await flush()

    expect(listTerminals, 'the re-ask mark goes with the answer').toHaveBeenCalledOnce()
    expect(s.view.getTerminalTab('t-exited')?.status).toBe(TerminalStatus.EXITED)
    s.dispose()
  })
})
