import type { MessageInitShape } from '@bufbuild/protobuf'
import type { WatchEventsResponse } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { UseWatchEventsStreamsOpts } from '~/hooks/useWatchEventsStreams'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentActivityState, AgentStatus, WatchReplayMode } from '~/generated/proto/leapmux/v1/agent_pb'
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
    // The tail-reconcile effect asks for the newest page of every agent tab.
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

/**
 * Capture the real stream hook's options so each test sends frames through the dispatcher.
 * Direct AgentActivityStore and handler tests do not verify which handler that dispatcher selects.
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

/**
 * Two agent tabs on one worker, with the activity store and the alert exposed.
 */
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
  // a2 is the active tab, so a1 is off screen and can receive a notification badge.
  // A prompt on the visible tab adds no badge.
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
      quakeStore: createTestQuakeStore(),
      getActiveQuakeKeyId: () => null,
      settingsLoading: createLoadingSignal(),
      getActiveWorkspaceId: () => WS,
      onAgentSettled: (id: string) => settled.push(id),
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
    activity,
    settled,
    controlStore,
    tabHasNotification: (id: string) => view.getAgentTab(id)?.hasNotification,
  }
}

/** The handler retains an unreadable control request and its decoding failure. This fixture supplies JSON bytes. */
function controlPayload(): Uint8Array {
  return new TextEncoder().encode(JSON.stringify({ tool_name: 'Bash' }))
}

function agentEvent(agentId: string, event: NonNullable<MessageInitShape<typeof AgentEventSchema>['event']>, replay = false): WatchEventsResponse {
  return create(WatchEventsResponseSchema, {
    event: { case: 'agentEvent', value: create(AgentEventSchema, { agentId, event, replay, replayId: replay ? 1n : 0n, replayAgentId: replay ? agentId : '' }) },
  })
}

/**
 * Verify catchUpStart through handleAgentEvent.
 * Applying a transition handler to this baseline alerts once for each agent that settled while the client was disconnected.
 * The baseline handler restores the level without creating those alerts.
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
    // The baseline carries the published state.
    // A held settle therefore restores WORKING and retains its later transition.
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
 * Verify the frame's explicit replay flag. The worker registers live interest before replay
 * starts. Live and replay frames share one stream. Arrival order cannot distinguish them.
 * Treating a live prompt as replay loses its notification badge.
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
