import type { WatchEventsResponse } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
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
    // Answered with an empty page so the hook's own promise chains settle.
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

// The real stream hook dials a channel. Replaced with a capture, so a test
// delivers frames to the dispatcher itself.
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
  return {
    dispose,
    status: () => view.getAgentTab('a1')?.agentStatus,
    epoch: () => metadata.liveStatusEpoch('a1'),
  }
}

function agentEvent(event: unknown, replay = false): WatchEventsResponse {
  return { event: { case: 'agentEvent', value: { agentId: 'a1', event, replay } } } as unknown as WatchEventsResponse
}

function controlRequest(): unknown {
  return {
    case: 'controlRequest',
    value: { agentId: 'a1', requestId: 'req-1', payload: new TextEncoder().encode(JSON.stringify({ tool_name: 'Bash' })) },
  }
}

/**
 * The live writers of an agent's status, through the real hook.
 *
 * A `ListAgents` reply that is in flight holds an older answer than every status
 * that the live stream writes meanwhile, and it compares
 * `TabMetadataStore.liveStatusEpoch` across the call to learn whether one landed
 * (see `useTabHydrators`). A writer that bypasses the count lets the older reply
 * replace its status.
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
