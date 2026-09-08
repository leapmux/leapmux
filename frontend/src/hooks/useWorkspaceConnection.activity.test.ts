import type { WatchEventsResponse } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentStatus } from '~/generated/proto/leapmux/v1/agent_pb'
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
import { createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    // The tail-reconcile effect asks for the newest page of every agent tab.
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

/**
 * The real stream hook dials a channel. Replaced with a capture, so a test
 * delivers frames to the dispatcher itself.
 *
 * This is what makes the `catchUpStart` wiring testable at all. The unit tests
 * beside it drive `AgentActivityStore` and the extracted handlers directly, so
 * every one of them passes whichever of the two the switch happens to call.
 */
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

const WS = 'ws-activity-dispatch'
const WORKER = 'w-1'

/** One agent tab on one worker, with the activity store and the alert exposed. */
function mountConnection() {
  const harness = installTestBridge({ workspaceId: WS })
  const { view, metadata, selection } = createTestTabStores(WS)
  const activity = createAgentActivityStore()
  const controlStore = createControlStore()
  const settled: string[] = []

  emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'p1', workerId: WORKER })
  emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'p2', workerId: WORKER })
  metadata.patch('a1', { agentStatus: AgentStatus.ACTIVE })
  metadata.patch('a2', { agentStatus: AgentStatus.ACTIVE })
  // a2 is the tile's active tab, so a1 is OFF screen -- which is the tab a
  // badge is for. A prompt on the tab the user already looks at badges nothing.
  selection.setActiveById(TabType.AGENT, 'a2')

  let dispose!: () => void
  createRoot((d) => {
    dispose = d
    useWorkspaceConnection({
      chatStore: createChatStore(),
      agentInputQueueStore: createAgentInputQueueStore(),
      view,
      metadata,
      selection,
      controlStore,
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore: activity,
      repoGitStore: createRepoGitStore(),
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
      onAgentSettled: (id: string) => settled.push(id),
    })
  })
  return {
    dispose,
    activity,
    settled,
    controlStore,
    tabHasNotification: (id: string) => view.getAgentTab(id)?.hasNotification,
  }
}

/** A control request's payload is JSON, and the handler drops one it cannot parse. */
function controlPayload(): Uint8Array {
  return new TextEncoder().encode(JSON.stringify({ tool_name: 'Bash' }))
}

function agentEvent(agentId: string, event: unknown, replay = false): WatchEventsResponse {
  return { event: { case: 'agentEvent', value: { agentId, event, replay } } } as unknown as WatchEventsResponse
}

/**
 * The `catchUpStart` branch of handleAgentEvent, which no other test reaches.
 *
 * Swapping that one line to `handleActivityChanged` makes every reconnect ring
 * once for each agent that settled while the client was away -- the burst the
 * level/transition split exists to prevent -- and the whole suite stays green
 * without these cases.
 */
describe('useWorkspaceConnection catchUpStart activity', () => {
  it('seeds the replay baseline instead of ringing for it', () => {
    const { dispose, activity, settled } = mountConnection()
    // The client watched this turn start.
    activity.apply('a1', AgentActivityState.WORKING)

    streamOpts().onEvent(WORKER, agentEvent('a1', {
      case: 'catchUpStart',
      value: { activityState: AgentActivityState.IDLE },
    }))

    expect(activity.isBusy('a1'), 'the level still lands').toBe(false)
    expect(settled, 'a level is not news the user asked for').toEqual([])
    dispose()
  })

  it('paints the spinner from the replay baseline before the burst', () => {
    const { dispose, activity, settled } = mountConnection()

    streamOpts().onEvent(WORKER, agentEvent('a1', {
      case: 'catchUpStart',
      value: { activityState: AgentActivityState.WORKING },
    }))

    expect(activity.isBusy('a1'), 'the tab knows it is busy before the messages arrive').toBe(true)
    expect(settled).toEqual([])
    dispose()
  })

  it('still rings for the settle that follows the baseline', () => {
    // The baseline carries the PUBLISHED state, so a settle the Worker is still
    // holding in its debounce window reads WORKING here and keeps its edge.
    const { dispose, activity, settled } = mountConnection()
    activity.apply('a1', AgentActivityState.WORKING)

    streamOpts().onEvent(WORKER, agentEvent('a1', {
      case: 'catchUpStart',
      value: { activityState: AgentActivityState.WORKING },
    }))
    streamOpts().onEvent(WORKER, agentEvent('a1', {
      case: 'activityChanged',
      value: { state: AgentActivityState.IDLE },
    }))

    expect(settled, 'the turn that ended while the tab replayed still rings').toEqual(['a1'])
    dispose()
  })
})

/**
 * The replay/live split, now that the FRAME says which it is.
 *
 * A client registers its live watch before the replay burst runs and both write
 * the same stream, so arrival order cannot tell them apart. Guessing from it
 * dropped a live permission prompt's badge whenever it raced a replay.
 */
describe('useWorkspaceConnection replay marking', () => {
  it('badges an off-screen tab for a live control request that races a replay', () => {
    const { dispose, controlStore, tabHasNotification } = mountConnection()

    streamOpts().onEvent(WORKER, agentEvent('a1', {
      case: 'controlRequest',
      value: { agentId: 'a1', requestId: 'req-1', payload: controlPayload() },
    }))

    expect(controlStore.getRequests('a1').length, 'the prompt is recorded either way').toBe(1)
    expect(tabHasNotification('a1'), 'and a live prompt badges the tab it arrived for').toBe(true)
    dispose()
  })

  it('does not badge for the same request replayed', () => {
    const { dispose, controlStore, tabHasNotification } = mountConnection()

    streamOpts().onEvent(WORKER, agentEvent('a1', {
      case: 'controlRequest',
      value: { agentId: 'a1', requestId: 'req-1', payload: controlPayload() },
    }, true))

    expect(controlStore.getRequests('a1').length, 'the replay still hydrates the prompt').toBe(1)
    expect(tabHasNotification('a1'), 'but a replay is not news the user asked for').not.toBe(true)
    dispose()
  })
})
