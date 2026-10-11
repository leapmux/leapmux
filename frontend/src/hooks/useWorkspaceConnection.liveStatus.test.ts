import type { MessageInitShape } from '@bufbuild/protobuf'
import type { TerminalEventSchema, WatchEventsResponse } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus, AvailableOptionGroupSchema, WatchReplayMode } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { AgentEventSchema, TabType, WatchAgentEntrySchema, WatchEventsResponseSchema, WatchMode } from '~/generated/proto/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { createAgentActivityStore } from '~/stores/agentActivity.store'
import { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestQuakeStore, createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    // Return an empty page so the hook completes its history request.
    listAgentMessages: vi.fn().mockResolvedValue({ messages: [], hasMore: false }),
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

// Capture the stream hook's options so each test sends frames through the dispatcher.
const streams = vi.hoisted(() => ({ opts: undefined as unknown }))

vi.mock('~/hooks/useWatchEventsStreams', () => ({
  useWatchEventsStreams: (opts: unknown) => {
    streams.opts = opts
    return { abortSignalFor: () => undefined }
  },
}))

beforeEach(() => {
  streams.opts = undefined
})

function streamOpts(): UseWatchEventsStreamsOpts {
  if (!streams.opts)
    throw new Error('useWorkspaceConnection did not open its watch streams')
  return streams.opts as UseWatchEventsStreamsOpts
}

const WS = 'ws-live-status'
const WORKER = 'w-1'

function mountConnection(initial: AgentStatus) {
  const harness = installTestBridge({ workspaceId: WS })
  const { view, metadata, selection } = createTestTabStores(WS)
  emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'p1', workerId: WORKER })
  metadata.patch('a1', { agentStatus: initial })

  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    useWorkspaceConnection({
      chatStore: createChatStore(),
      agentInputQueueStore: createAgentInputQueueStore(),
      view,
      metadata,
      selection,
      controlStore: createControlStore(),
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore: createAgentActivityStore(),
      repoGitStore: createRepoGitStore(),
      quakeStore: createTestQuakeStore(),
      getActiveQuakeKeyId: () => null,
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
    })
  })
  streamOpts().onReplayRequested?.(WORKER, 1n, [create(WatchAgentEntrySchema, {
    agentId: 'a1',
    mode: WatchMode.FULL,
    replay: WatchReplayMode.LATEST,
    cursorSeq: 0n,
    replayId: 1n,
  })])
  return {
    dispose,
    status: () => view.getAgentTab('a1')?.agentStatus,
    epoch: () => metadata.liveStatusEpoch('a1'),
    catalogEpoch: () => metadata.liveCatalogEpoch('a1'),
  }
}

function agentEvent(event: NonNullable<MessageInitShape<typeof AgentEventSchema>['event']>, replay = false): WatchEventsResponse {
  return create(WatchEventsResponseSchema, {
    event: { case: 'agentEvent', value: create(AgentEventSchema, { agentId: 'a1', event, replay, replayId: replay ? 1n : 0n, replayAgentId: replay ? 'a1' : '' }) },
  })
}

function controlRequest(): NonNullable<MessageInitShape<typeof AgentEventSchema>['event']> {
  return {
    case: 'controlRequest',
    value: { agentId: 'a1', requestId: 'req-1', payload: new TextEncoder().encode(JSON.stringify({ tool_name: 'Bash' })) },
  }
}

/**
 * Verify live status writes through the real hook.
 * A pending ListAgents request can return a status older than a live event.
 * useTabHydrators compares TabMetadataStore.liveStatusEpoch across that request to detect an intervening write.
 * A writer that omits the count lets the older reply replace its status.
 */
describe('useWorkspaceConnection live status writers', () => {
  it('counts a statusChange event', () => {
    const { dispose, status, epoch } = mountConnection(AgentStatus.STARTING)

    streamOpts().onEvent(WORKER, agentEvent({
      case: 'statusChange',
      value: { agentId: 'a1', status: AgentStatus.ACTIVE, workerOnline: true, optionGroups: [] },
    }))

    expect(status()).toBe(AgentStatus.ACTIVE)
    expect(epoch()).toBe(1)
    dispose()
  })

  it('counts the live event that revives an INACTIVE tab', () => {
    const { dispose, status, epoch } = mountConnection(AgentStatus.INACTIVE)

    streamOpts().onEvent(WORKER, agentEvent(controlRequest()))

    expect(status(), 'a live request proves that the agent runs').toBe(AgentStatus.ACTIVE)
    expect(epoch()).toBe(1)
    dispose()
  })

  it('does not count a live event that writes no status', () => {
    const { dispose, status, epoch } = mountConnection(AgentStatus.ACTIVE)

    streamOpts().onEvent(WORKER, agentEvent(controlRequest()))

    expect(status()).toBe(AgentStatus.ACTIVE)
    expect(epoch(), 'the tab was ACTIVE already, so the event wrote none').toBe(0)
    dispose()
  })

  it('does not revive an INACTIVE tab from a replayed event, and does not count it', () => {
    const { dispose, status, epoch } = mountConnection(AgentStatus.INACTIVE)

    streamOpts().onEvent(WORKER, agentEvent(controlRequest(), true))

    expect(status()).toBe(AgentStatus.INACTIVE)
    expect(epoch()).toBe(0)
    dispose()
  })
})

/**
 * Verify live catalog writes through the real hook.
 * A pending ListAgents request can return a catalog older than a live event.
 * useTabHydrators compares TabMetadataStore.liveCatalogEpoch across that request to detect an intervening write.
 * Catalog and status have separate counts because one event can change either or both.
 */
describe('useWorkspaceConnection live catalog writers', () => {
  const catalog = () => [create(AvailableOptionGroupSchema, { id: 'model', currentValue: 'opus' })]

  it('counts a catalog and a status that arrive in one event, each on its own count', () => {
    const { dispose, status, epoch, catalogEpoch } = mountConnection(AgentStatus.STARTING)

    streamOpts().onEvent(WORKER, agentEvent({
      case: 'statusChange',
      value: { agentId: 'a1', status: AgentStatus.ACTIVE, workerOnline: true, optionGroups: catalog() },
    }))

    expect(status()).toBe(AgentStatus.ACTIVE)
    expect(epoch()).toBe(1)
    expect(catalogEpoch()).toBe(1)
    dispose()
  })

  it('counts a settings refresh that has no status as a catalog and not as a status', () => {
    const { dispose, status, epoch, catalogEpoch } = mountConnection(AgentStatus.ACTIVE)

    streamOpts().onEvent(WORKER, agentEvent({
      case: 'statusChange',
      value: { agentId: 'a1', status: AgentStatus.UNSPECIFIED, workerOnline: true, optionGroups: catalog() },
    }))

    expect(status()).toBe(AgentStatus.ACTIVE)
    expect(epoch(), 'the event says nothing about the lifecycle').toBe(0)
    expect(catalogEpoch()).toBe(1)
    dispose()
  })

  it('counts a status event that carries no catalog as a status and not as a catalog', () => {
    const { dispose, epoch, catalogEpoch } = mountConnection(AgentStatus.ACTIVE)

    streamOpts().onEvent(WORKER, agentEvent({
      case: 'statusChange',
      value: { agentId: 'a1', status: AgentStatus.INACTIVE, workerOnline: true, optionGroups: [] },
    }))

    expect(epoch()).toBe(1)
    expect(catalogEpoch(), 'an empty catalog means "unchanged"').toBe(0)
    dispose()
  })

  it('counts a git-only event as neither', () => {
    const { dispose, epoch, catalogEpoch } = mountConnection(AgentStatus.ACTIVE)

    streamOpts().onEvent(WORKER, agentEvent({
      case: 'statusChange',
      value: {
        agentId: 'a1',
        status: AgentStatus.UNSPECIFIED,
        workerOnline: true,
        optionGroups: [],
        gitStatus: { toplevel: '/repo', branch: 'main' },
      },
    }))

    expect(epoch()).toBe(0)
    expect(catalogEpoch()).toBe(0)
    dispose()
  })
})

function mountTerminalConnection(initial: TerminalStatus | undefined) {
  const harness = installTestBridge({ workspaceId: WS })
  const { view, metadata, selection } = createTestTabStores(WS)
  emitAddTab({ type: TabType.TERMINAL, id: 't1', tileId: harness.rootTileId, position: 'p1', workerId: WORKER })
  if (initial !== undefined)
    metadata.patch('t1', { terminalStatus: initial })

  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    useWorkspaceConnection({
      chatStore: createChatStore(),
      agentInputQueueStore: createAgentInputQueueStore(),
      view,
      metadata,
      selection,
      controlStore: createControlStore(),
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore: createAgentActivityStore(),
      repoGitStore: createRepoGitStore(),
      quakeStore: createTestQuakeStore(),
      getActiveQuakeKeyId: () => null,
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
    })
  })
  return {
    dispose,
    status: () => view.getTerminalTab('t1')?.status,
    epoch: () => metadata.liveStatusEpoch('t1'),
  }
}

function terminalEvent(event: NonNullable<MessageInitShape<typeof TerminalEventSchema>['event']>): WatchEventsResponse {
  return create(WatchEventsResponseSchema, {
    event: { case: 'terminalEvent', value: { terminalId: 't1', event } },
  })
}

/**
 * Verify live terminal status writes through the real hook.
 * A pending ListTerminals request can return a status older than a live event.
 * useTabHydrators compares TabMetadataStore.liveStatusEpoch across that request to detect an intervening write.
 * A writer that omits the count lets the older reply replace its status.
 */
describe('useWorkspaceConnection live terminal status writers', () => {
  it('counts a statusChange event', () => {
    const { dispose, status, epoch } = mountTerminalConnection(TerminalStatus.STARTING)

    streamOpts().onEvent(WORKER, terminalEvent({
      case: 'statusChange',
      value: { terminalId: 't1', status: TerminalStatus.READY },
    }))

    expect(status()).toBe(TerminalStatus.READY)
    expect(epoch()).toBe(1)
    dispose()
  })

  it('counts the close of a shell', () => {
    const { dispose, status, epoch } = mountTerminalConnection(TerminalStatus.READY)

    streamOpts().onEvent(WORKER, terminalEvent({ case: 'closed', value: { exitCode: 0 } }))

    expect(status()).toBe(TerminalStatus.EXITED)
    expect(epoch()).toBe(1)
    dispose()
  })

  it('does not count a statusChange event that the tab refuses', () => {
    const { dispose, status, epoch } = mountTerminalConnection(TerminalStatus.DISCONNECTED)

    streamOpts().onEvent(WORKER, terminalEvent({
      case: 'statusChange',
      value: { terminalId: 't1', status: TerminalStatus.READY },
    }))

    expect(status()).toBe(TerminalStatus.DISCONNECTED)
    expect(epoch()).toBe(0)
    dispose()
  })

  it('does not count a bell', () => {
    const { dispose, epoch } = mountTerminalConnection(TerminalStatus.READY)

    streamOpts().onEvent(WORKER, terminalEvent({ case: 'bell', value: {} }))

    expect(epoch()).toBe(0)
    dispose()
  })
})
