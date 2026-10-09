import type { MessageInitShape } from '@bufbuild/protobuf'
import type { AgentChatMessage, AgentControlRequest, AgentStatusChange, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TerminalStatusChange } from '~/generated/proto/leapmux/v1/terminal_pb'
import type { AgentEvent, WatchEventsRequest, WatchEventsResponse } from '~/generated/proto/leapmux/v1/workspace_pb'
import type { AgentTab, FileTab, Tab, TerminalTab } from '~/stores/tab.types'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { createRoot, mapArray } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { CATCH_UP_GAP_LIMIT } from '~/generated/contracts/chat-history'
import { AgentActivityState, AgentChatMessageSchema, AgentControlCancelRequestSchema, AgentControlRequestSchema, AgentGoalAction, AgentGoalSchema, AgentGoalStatus, AgentProvider, AgentStatus, AgentStatusChangeSchema, BackgroundTaskItemSchema, BackgroundTaskKind, BackgroundTaskStatus, ContentCompression, ControlResponseState, ListAgentMessagesResponseSchema, ListMessageMarksResponseSchema, MessageSource, TodoItemSchema, TodoStatus } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus, TerminalStatusChangeSchema } from '~/generated/proto/leapmux/v1/terminal_pb'
import { AgentEventSchema, TabType, WatchEventsRequestSchema, WatchEventsResponseSchema, WatchMode, WatchRejectionReason } from '~/generated/proto/leapmux/v1/workspace_pb'
import { applyNotificationMetadata, applyPendingAxisSuppression, buildAgentStatusTabUpdate, handleActivityChanged, handleAgentInactive, handleAgentMessage, handleAgentSessionInfo, handleAgentSettled, handleAgentStatusChange, handleControlCancellation, handleControlRequest, handleResultDivider, resolveSettingsTabFields, wireRateLimitUpdateFromSessionInfo, wireSessionInfoToUpdates } from '~/hooks/agentEvents'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { applyTerminalStatusChange, handleTerminalBell, handleTerminalNotification, handleTerminalProgress, handleTerminalTitleChanged, markTerminalExited } from '~/hooks/terminalEvents'
import { clearOfflineAgentState, collectWorkerOfflineTargets, enqueuePendingTerminalData, MAX_PENDING_TERMINAL_FRAMES, reconcileLaggingTails, useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { ChannelError, channelNotOpenError } from '~/lib/channelError'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentActivityStore } from '~/stores/agentActivity.store'
import { createAgentInputQueueStore } from '~/stores/agentInputQueue.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { LIVE_STATUS_FIELDS, TERMINAL_LIVE_STATUS_FIELDS } from '~/stores/tab.helpers'
import { createTabMetadataStore } from '~/stores/tabMetadata.store'
import { emitAddTab, emitRemoveTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestQuakeStore, createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    watchEventsViaChannel: vi.fn(),
    // The active agent triggers a history request. Stub that request so each test controls its
    // failure without opening a real channel.

    listAgentMessages: vi.fn(),
    // This module calls only getOrOpenChannel. Its fake must not attempt a real handshake.
    channelManager: {
      getOrOpenChannel: vi.fn().mockResolvedValue('ch-1'),
      hasOpenChannelForWorker: vi.fn().mockReturnValue(true),
      // useWatchEventsStreams checks the relay before it schedules a reconnect. Return null unless a
      // test supplies a fatal refusal.

      fatalCloseInfo: vi.fn(() => null),
    },
  }
})

/** Retain every user notification, regardless of the helper that supplies it. */
const mockShowWarnToast = vi.fn()
const mockToastHost = vi.fn((element: HTMLElement) => {
  const mounted = element.cloneNode(true) as HTMLElement
  document.body.appendChild(mounted)
  return mounted
})
let previousToastHost: Window['ot'] | undefined

vi.mock('~/components/common/Toast', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/components/common/Toast')>()
  return {
    ...actual,
    showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
    showInfoToast: vi.fn(),
    showWarnToastUnlessDisconnected: (message: string, err: unknown) => {
      const previousCalls = mockToastHost.mock.calls.length
      actual.showWarnToastUnlessDisconnected(message, err)
      if (mockToastHost.mock.calls.length > previousCalls)
        mockShowWarnToast(message, err)
    },
  }
})

beforeEach(() => {
  previousToastHost = window.ot
  mockToastHost.mockClear()
  window.ot = {
    toast: Object.assign(() => {}, { el: mockToastHost, clear: () => {} }),
  }
})

afterEach(() => {
  if (previousToastHost === undefined)
    Reflect.deleteProperty(window, 'ot')
  else
    window.ot = previousToastHost
  for (const result of mockToastHost.mock.results) {
    if (result.type === 'return')
      result.value.remove()
  }
})

const WS = 'ws-test'

/** The tool-progress tests address two tool_use rows from one provider session. */
const TOOL_A = { spanId: 'toolu_A', agentSessionId: 'sess-1' }
const TOOL_B = { spanId: 'toolu_B', agentSessionId: 'sess-1' }

let nextPosition = 0

/**
 * A joined tab requires placement and metadata. addAgent creates its placement and writes its
 * metadata. The tests read the joined view and update worker fields through metadata.
 */
function makeTabStores(workspaceId = WS) {
  const harness = installTestBridge({ workspaceId })
  const stores = createTestTabStores(workspaceId)
  return {
    ...stores,
    rootTileId: harness.rootTileId,
    /** Place an agent and write its metadata. Select its tab unless activate is false. */
    addAgent(id: string, meta: Record<string, unknown> = {}, opts: { tileId?: string, activate?: boolean } = {}) {
      nextPosition += 1
      emitAddTab({
        type: TabType.AGENT,
        id,
        tileId: opts.tileId ?? harness.rootTileId,
        position: `p${nextPosition}`,
        workerId: (meta.workerId as string | undefined) ?? '',
      })
      if (Object.keys(meta).length > 0)
        stores.metadata.patch(id, meta)
      if (opts.activate !== false)
        stores.selection.setActiveById(TabType.AGENT, id)
    },
    /** Place a terminal and write its metadata. Select its tab. */
    addTerminal(id: string, meta: Record<string, unknown> = {}) {
      nextPosition += 1
      emitAddTab({ type: TabType.TERMINAL, id, tileId: harness.rootTileId, position: `p${nextPosition}`, workerId: '' })
      if (Object.keys(meta).length > 0)
        stores.metadata.patch(id, meta)
    },
  }
}

type TabStores = ReturnType<typeof makeTabStores>

/** Supply actual stores to each direct event handler. */
function handlerStores(tabs: TabStores, agentSessionStore = createAgentSessionStore()) {
  return {
    ...tabs,
    agentSessionStore,
    chatStore: createChatStore(),
    controlStore: createControlStore(),
    repoGitStore: createRepoGitStore(),
    getActiveWorkspaceId: () => WS,
  }
}

/** Return the runtime phase union for direct handler tests. */
function simulatePhase(phase: 'catchingUp' | 'live'): 'catchingUp' | 'live' {
  return phase
}

describe('background agent history trimming', () => {
  /**
   * Exercise the actual message handler. A tile can have a selected key that belongs to another
   * tab. Compare the exact agent key so background history still receives its size limit.
   */
  function makeTrimStores() {
    const tabs = makeTabStores()
    return {
      stores: {
        controlStore: createControlStore(),
        quakeStore: createTestQuakeStore(),
        getActiveQuakeKeyId: () => null,
        agentSessionStore: createAgentSessionStore(),
        agentActivityStore: createAgentActivityStore(),
        chatStore: createChatStore(),
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        getActiveWorkspaceId: () => WS,
      },
      tabs,
    }
  }

  function makeUserMessage(id: string, seq: bigint) {
    return {
      id,
      source: MessageSource.USER,
      content: new TextEncoder().encode(JSON.stringify({ type: 'user', content: 'test' })),
      contentCompression: ContentCompression.NONE,
      seq,
      agentProvider: AgentProvider.CLAUDE_CODE,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  /**
   * Fill the history to its limit. Deliver one additional row through the actual handler so it
   * decides whether to trim.
   */
  function overflowThroughHandler(
    stores: ReturnType<typeof makeTrimStores>['stores'],
    agentId: string,
    cap: number,
  ) {
    stores.chatStore.setMessages(agentId, Array.from({ length: cap }, (_, i) =>
      makeUserMessage(`m${i + 1}`, BigInt(i + 1))))
    handleAgentMessage(agentId, makeUserMessage(`m${cap + 1}`, BigInt(cap + 1)) as never, stores as never, 'live')
  }

  it('trims an agent the user is not looking at', () => {
    createRoot((dispose) => {
      const { stores, tabs } = makeTrimStores()
      tabs.addAgent('active-agent')
      tabs.addAgent('background-agent', {}, { activate: false })
      // The user selects active-agent. Both agents share the root tile.
      tabs.selection.setActiveById(TabType.AGENT, 'active-agent')

      overflowThroughHandler(stores, 'background-agent', MAX_BACKGROUND_CHAT_MESSAGES)

      const messages = stores.chatStore.getMessages('background-agent')
      expect(messages, 'the cap must actually bound the backlog').toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(messages[0]?.seq).toBe(2n)
      expect(messages.at(-1)?.seq).toBe(BigInt(MAX_BACKGROUND_CHAT_MESSAGES + 1))
      expect(stores.chatStore.hasOlderMessages('background-agent')).toBe(true)
      dispose()
    })
  })

  it('does not trim the agent that is active on its own tile', () => {
    createRoot((dispose) => {
      const { stores, tabs } = makeTrimStores()
      tabs.addAgent('active-agent')
      tabs.selection.setActiveById(TabType.AGENT, 'active-agent')

      overflowThroughHandler(stores, 'active-agent', MAX_BACKGROUND_CHAT_MESSAGES)

      const messages = stores.chatStore.getMessages('active-agent')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 1)
      expect(messages[0]?.seq).toBe(1n)
      dispose()
    })
  })

  it('does not trim a tab that is tile-active while another tab is workspace-active', () => {
    createRoot((dispose) => {
      const { stores, tabs } = makeTrimStores()
      // Use a second tile. Its selected tab must differ from the workspace's focused tab.

      const secondTile = tabs.layoutStore.splitTile(tabs.rootTileId, 'horizontal')!
      tabs.addAgent('active-agent')
      tabs.addAgent('visible-agent', {}, { tileId: secondTile, activate: false })
      tabs.selection.setActiveById(TabType.AGENT, 'visible-agent')
      tabs.selection.setActiveById(TabType.AGENT, 'active-agent')

      overflowThroughHandler(stores, 'visible-agent', MAX_BACKGROUND_CHAT_MESSAGES)

      const messages = stores.chatStore.getMessages('visible-agent')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 1)
      expect(messages[0]?.seq).toBe(1n)
      dispose()
    })
  })
})

describe('agent tab notification keys', () => {
  it('does not notify the active agent tab when key formats match store keys', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-1')
      const controlStore = createControlStore()
      handleControlRequest('agent-1', create(AgentControlRequestSchema, {
        agentId: 'agent-1',
        requestId: 'active-tab-request',
        payload: new TextEncoder().encode('{}'),
      }), 'live', {
        ...tabs,
        agentSessionStore: createAgentSessionStore(),
        chatStore: createChatStore(),
        controlStore,
        getActiveWorkspaceId: () => WS,
      })
      expect(tabs.view.getAgentTab('agent-1')).toBeDefined()
      expect(tabs.view.getAgentTab('agent-1')?.hasNotification).not.toBe(true)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  // Call the real control handler. A live request sets a badge only when the agent tab is off
  // screen.

  function applyControlRequestNotification(
    tabs: TabStores,
    agentId: string,
    catchUpPhase: 'catchingUp' | 'live',
  ) {
    handleControlRequest(agentId, create(AgentControlRequestSchema, {
      agentId,
      requestId: 'notification-request',
      payload: new TextEncoder().encode('{}'),
    }), catchUpPhase, handlerStores(tabs))
  }

  it('badges a background tab when a live control request arrives', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-A')
      tabs.addAgent('agent-B', {}, { activate: false })
      tabs.selection.setActiveById(TabType.AGENT, 'agent-A')

      applyControlRequestNotification(tabs, 'agent-B', 'live')

      const tabB = tabs.view.getAgentTab('agent-B')
      const tabA = tabs.view.getAgentTab('agent-A')
      expect(tabB?.hasNotification).toBe(true)
      expect(tabA?.hasNotification).not.toBe(true)
      dispose()
    })
  })

  it('does not badge the focused tab when its own control request arrives', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-A')
      tabs.selection.setActiveById(TabType.AGENT, 'agent-A')

      applyControlRequestNotification(tabs, 'agent-A', 'live')

      expect(tabs.view.getAgentTab('agent-A')?.hasNotification).not.toBe(true)
      dispose()
    })
  })

  // Reload replays pending control requests. Replay must retain the prompt without creating a
  // new badge.

  it('does not badge during catch-up replay', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-A')
      tabs.addAgent('agent-B', {}, { activate: false })
      tabs.selection.setActiveById(TabType.AGENT, 'agent-A')

      applyControlRequestNotification(tabs, 'agent-B', 'catchingUp')

      const tabB = tabs.view.getAgentTab('agent-B')
      expect(tabB?.hasNotification).not.toBe(true)
      dispose()
    })
  })
})

describe('context usage refresh on compaction boundary', () => {
  // Call the real notification handler. A completed compaction replaces stale token components
  // and retains the known context window.

  function applyCompaction(
    sessionStore: ReturnType<typeof createAgentSessionStore>,
    agentId: string,
    content: unknown,
  ) {
    const tabs = makeTabStores()
    const msg = create(AgentChatMessageSchema, {
      id: 'compact-1',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify(content)),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      agentProvider: AgentProvider.CLAUDE_CODE,
    })
    applyNotificationMetadata(agentId, msg, parseMessageContent(msg), handlerStores(tabs, sessionStore), 'live')
  }

  const compactBoundary = (meta: Record<string, unknown>) => ({ type: 'system', subtype: 'compact_boundary', compact_metadata: meta })

  // Use a separate agent ID for each case because the store persists through browser storage.
  // A shared ID could carry one case's contextWindow into the next case.
  it('drops the grid to the post-compaction size and preserves the context window', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // Seed the input and cache usage from the previous turn.
      store.updateInfo('compact-drop', {
        contextUsage: {
          inputTokens: 50000,
          cacheCreationInputTokens: 40000,
          cacheReadInputTokens: 60000,
          contextWindow: 200000,
        },
      })

      applyCompaction(store, 'compact-drop', compactBoundary({ trigger: 'auto', pre_tokens: 150000, post_tokens: 12000 }))

      expect(store.getInfo('compact-drop').contextUsage).toEqual({
        inputTokens: 0,
        cacheCreationInputTokens: 0,
        cacheReadInputTokens: 0,
        contextTokens: 12000,
        contextWindow: 200000,
      })
      dispose()
    })
  })

  it('derives the post size from pre minus tokens_saved when post_tokens is absent', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      store.updateInfo('compact-derive', {
        contextUsage: { inputTokens: 100000, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, contextWindow: 200000 },
      })

      applyCompaction(store, 'compact-derive', compactBoundary({ pre_tokens: 100000, tokens_saved: 70000 }))

      expect(store.getInfo('compact-derive').contextUsage?.contextTokens).toBe(30000)
      dispose()
    })
  })

  it('leaves the existing usage untouched when the boundary carries no resolvable post', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      const before = { inputTokens: 50000, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, contextWindow: 200000 }
      store.updateInfo('compact-noop', { contextUsage: { ...before } })

      // pre_tokens alone supplies no post-compaction count.
      applyCompaction(store, 'compact-noop', compactBoundary({ trigger: 'auto', pre_tokens: 150000 }))

      expect(store.getInfo('compact-noop').contextUsage).toEqual(before)
      dispose()
    })
  })

  it('sets contextTokens even when no prior context window is known', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      applyCompaction(store, 'compact-nowindow', compactBoundary({ pre_tokens: 100000, post_tokens: 8000 }))

      expect(store.getInfo('compact-nowindow').contextUsage).toEqual({
        inputTokens: 0,
        cacheCreationInputTokens: 0,
        cacheReadInputTokens: 0,
        contextTokens: 8000,
      })
      dispose()
    })
  })
})

describe('applyNotificationMetadata usage folding', () => {
  // The neutral extractor reads cost and normalized usage. The provider's session hook reads its
  // native usage shape.

  function stores() {
    const { view, metadata, selection } = makeTabStores()
    return { agentSessionStore: createAgentSessionStore(), agentActivityStore: createAgentActivityStore(), chatStore: createChatStore(), view, metadata, selection, getActiveWorkspaceId: () => WS }
  }
  function msgOf(content: unknown, agentProvider: AgentProvider) {
    return {
      id: 'm1',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify(content)),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      agentProvider,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  // Use a separate agent ID for each case because the session store persists through browser storage.
  // A shared ID could carry one case's usage into the next case.

  it('applies a plan auto-title from a LIVE plan_updated', () => {
    createRoot((dispose) => {
      const s = stores()
      s.metadata.patch('u-plan-live', { title: 'Agent' })
      const msg = msgOf({ type: 'plan_updated', plan_title: 'Dummy plan', update_agent_title: true }, AgentProvider.CLAUDE_CODE)
      applyNotificationMetadata('u-plan-live', msg, parseMessageContent(msg), s, 'live')
      expect(s.metadata.get('u-plan-live')?.title).toBe('Dummy plan')
      dispose()
    })
  })

  it('does NOT re-apply a plan auto-title while catching up', () => {
    createRoot((dispose) => {
      const s = stores()
      // Replay restores planFilePath through its metadata receipt. Replay preserves a manual tab
      // title and omits live notification effects.

      s.metadata.patch('u-plan-catchup', { title: 'My Custom Name' })
      const msg = msgOf({ type: 'plan_updated', plan_title: 'Dummy plan', update_agent_title: true }, AgentProvider.CLAUDE_CODE)
      applyNotificationMetadata('u-plan-catchup', msg, parseMessageContent(msg), s, 'catchingUp')
      expect(s.metadata.get('u-plan-catchup')?.title).toBe('My Custom Name')
      dispose()
    })
  })

  it('still restores planFilePath while catching up', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = msgOf({ type: 'plan_updated', plan_file_path: '/repo/PLAN.md', plan_title: 'Dummy plan', update_agent_title: true }, AgentProvider.CLAUDE_CODE)
      applyNotificationMetadata('u-plan-path', msg, parseMessageContent(msg), s, 'catchingUp')
      expect(s.agentSessionStore.getInfo('u-plan-path').planFilePath).toBe('/repo/PLAN.md')
      dispose()
    })
  })

  it('folds a Claude assistant message.usage + cost into session info', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = msgOf({ type: 'assistant', total_cost_usd: 0.05, message: { usage: { input_tokens: 1000, cache_read_input_tokens: 200 } } }, AgentProvider.CLAUDE_CODE)
      applyNotificationMetadata('u-claude', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-claude').contextUsage).toEqual({ inputTokens: 1000, cacheCreationInputTokens: 0, cacheReadInputTokens: 200 })
      expect(s.agentSessionStore.getInfo('u-claude').totalCostUsd).toBe(0.05)
      dispose()
    })
  })

  it('folds a Codex thread/tokenUsage/updated notification into session info', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = msgOf({ method: 'thread/tokenUsage/updated', params: { tokenUsage: { last: { inputTokens: 10, cachedInputTokens: 5 }, modelContextWindow: 4096 } } }, AgentProvider.CODEX)
      applyNotificationMetadata('u-codex', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-codex').contextUsage).toEqual({ inputTokens: 5, cacheCreationInputTokens: 0, cacheReadInputTokens: 5, contextWindow: 4096 })
      dispose()
    })
  })

  it('folds a Pi message_end message.usage into session info', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = msgOf({ type: 'message_end', message: { usage: { input: 100, output: 10, cacheRead: 20, cacheWrite: 5, totalTokens: 130 } } }, AgentProvider.PI)
      applyNotificationMetadata('u-pi', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-pi').contextUsage).toEqual({ inputTokens: 100, cacheCreationInputTokens: 5, cacheReadInputTokens: 20, outputTokens: 10, contextTokens: 130 })
      dispose()
    })
  })

  it('does NOT fold a subagent message (parent_tool_use_id) — the neutral skip guard survives the moved call site', () => {
    createRoot((dispose) => {
      const s = stores()
      const msg = msgOf({ type: 'assistant', parent_tool_use_id: 'toolu_x', total_cost_usd: 0.03, message: { usage: { input_tokens: 500 } } }, AgentProvider.CLAUDE_CODE)
      applyNotificationMetadata('u-subagent', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-subagent').contextUsage).toBeUndefined()
      expect(s.agentSessionStore.getInfo('u-subagent').totalCostUsd).toBeUndefined()
      dispose()
    })
  })

  it('prefers a backend-normalized context_usage over the raw message.usage fallback', () => {
    createRoot((dispose) => {
      const s = stores()
      // Supply normalized context usage and native usage together. The neutral extractor must use
      // the normalized value without calling the provider hook.

      const msg = msgOf({ type: 'message_end', context_usage: { input_tokens: 100, cache_read_input_tokens: 20 }, message: { usage: { input: 999 } } }, AgentProvider.PI)
      applyNotificationMetadata('u-normalized', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-normalized').contextUsage).toEqual({ inputTokens: 100, cacheCreationInputTokens: 0, cacheReadInputTokens: 20 })
      dispose()
    })
  })

  it('does NOT fold usage/cost from a non-AGENT (LEAPMUX) row — the source gate survives the moved call site', () => {
    // This branch requires AGENT source. A USER or LEAPMUX row with similar usage fields must not
    // change agent usage.

    createRoot((dispose) => {
      const s = stores()
      const msg = { ...msgOf({ type: 'assistant', total_cost_usd: 0.05, context_usage: { input_tokens: 100 }, message: { usage: { input_tokens: 1000 } } }, AgentProvider.CLAUDE_CODE), source: MessageSource.LEAPMUX }
      applyNotificationMetadata('u-leapmux', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-leapmux').contextUsage).toBeUndefined()
      expect(s.agentSessionStore.getInfo('u-leapmux').totalCostUsd).toBeUndefined()
      dispose()
    })
  })
})

describe('startupMessage handling in agent statusChange', () => {
  function applyStatusChange(
    tabs: TabStores,
    sc: { agentId: string, status: AgentStatus, startupMessage?: string },
  ) {
    handleAgentStatusChange(sc.agentId, create(AgentStatusChangeSchema, sc), 'live', handlerStores(tabs), createLoadingSignal(), () => {})
  }

  it('stores startupMessage while STARTING so the startup panel can render the phase label', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-1', { agentStatus: AgentStatus.STARTING })

      applyStatusChange(tabs, {
        agentId: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      })
      expect(tabs.view.getAgentTab('agent-1')?.startupMessage).toBe('Checking Git status…')

      applyStatusChange(tabs, {
        agentId: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Starting Claude Code…',
      })
      expect(tabs.view.getAgentTab('agent-1')?.startupMessage).toBe('Starting Claude Code…')
      dispose()
    })
  })

  it('clears startupMessage on ACTIVE so the label does not linger after startup succeeds', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-1', { agentStatus: AgentStatus.STARTING, startupMessage: 'Starting Claude Code…' })

      applyStatusChange(tabs, { agentId: 'agent-1', status: AgentStatus.ACTIVE })

      expect(tabs.view.getAgentTab('agent-1')?.startupMessage).toBe('')
      dispose()
    })
  })

  it('clears startupMessage on STARTUP_FAILED so the error banner replaces the phase label', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-1', { agentStatus: AgentStatus.STARTING, startupMessage: 'Checking Git status…' })

      applyStatusChange(tabs, { agentId: 'agent-1', status: AgentStatus.STARTUP_FAILED })

      expect(tabs.view.getAgentTab('agent-1')?.startupMessage).toBe('')
      dispose()
    })
  })

  it('leaves startupMessage alone on status-less events (UNSPECIFIED) so catchUp sentinels do not wipe live phases', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-1', { agentStatus: AgentStatus.STARTING, startupMessage: 'Checking Git status…' })

      applyStatusChange(tabs, { agentId: 'agent-1', status: AgentStatus.UNSPECIFIED })

      expect(tabs.view.getAgentTab('agent-1')?.startupMessage).toBe('Checking Git status…')
      dispose()
    })
  })
})

describe('per-axis optimistic suppression in agent statusChange', () => {
  // Call the actual pending-axis helper with createLoadingSignal. Retain each pending optimistic
  // value. A pending absent key represents a local clear and must stay absent.

  it('keeps the pending axis optimistic while applying a server change to an unrelated axis', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      // The tab already holds the user's optimistic model value.
      s.start('agent-1', ['model'])
      const prev = { model: 'opus', permissionMode: 'default' }
      // The server still reports the old model. It also reports a new permission mode.

      const serverValues = { model: 'sonnet', permissionMode: 'plan' }

      const merged = applyPendingAxisSuppression(serverValues, prev, s.pendingAxes('agent-1'))
      expect(merged.model).toBe('opus') // Retain the pending model value. Apply the unrelated permission-mode value.
      expect(merged.permissionMode).toBe('plan')
      dispose()
    })
  })

  it('keeps a pending CLEARED axis absent rather than re-absorbing the server value', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      // The local clear removes permissionMode before the axis becomes pending. The previous values
      // therefore contain no permissionMode key.

      s.start('agent-1', ['permissionMode'])
      const prev = { model: 'opus' }
      // The server still reports permissionMode from before the local clear.
      const serverValues = { model: 'opus', permissionMode: 'plan' }

      const merged = applyPendingAxisSuppression(serverValues, prev, s.pendingAxes('agent-1'))
      // Preserve the pending clear and the unrelated axis.
      expect('permissionMode' in merged).toBe(false)
      expect(merged.model).toBe('opus')
      dispose()
    })
  })

  it('applies all server values once the pending change settles', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      s.start('agent-1', ['model'])
      s.stop('agent-1', ['model']) // The RPC completed.
      const prev = { model: 'opus', permissionMode: 'default' }
      const serverValues = { model: 'sonnet', permissionMode: 'plan' }

      const merged = applyPendingAxisSuppression(serverValues, prev, s.pendingAxes('agent-1'))
      // The server value applies after the pending change ends.
      expect(merged.model).toBe('sonnet')
      expect(merged.permissionMode).toBe('plan')
      dispose()
    })
  })

  it('returns the server values unchanged (same reference) when nothing is pending', () => {
    const serverValues = { model: 'sonnet', permissionMode: 'plan' }
    // With no pending axis, return the server object unchanged. The write boundary can then reuse
    // its existing value.

    expect(applyPendingAxisSuppression(serverValues, { model: 'opus' }, new Set())).toBe(serverValues)
  })
})

describe('resolveSettingsTabFields', () => {
  // deriveOptionGroupTabFields reads only id and currentValue from these option groups.
  const group = (id: string, currentValue: string): AvailableOptionGroup =>
    ({ id, label: id, currentValue, options: [] }) as unknown as AvailableOptionGroup

  it('returns {} for an empty option-group push, leaving the previously-derived fields untouched', () => {
    expect(resolveSettingsTabFields(undefined, [], new Set())).toEqual({})
  })

  /**
   * The metadata write boundary suppresses unchanged option values. Test that boundary so every
   * producer receives the same rule. The producer still returns its derived record.
   */
  it('lets the write point drop a re-broadcast that changes no current value', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('a1', { optionValues: { model: 'opus' } })
    const stored = metadata.get('a1')!.optionValues

    const prev: AgentTab = { type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1', optionValues: { model: 'opus' } }
    const fields = resolveSettingsTabFields(prev, [group('model', 'opus')], new Set())
    metadata.patch('a1', fields)

    // Equal content preserves the stored reference.
    // For then retains its rows instead of disposing and creating them again.
    expect(metadata.get('a1')!.optionValues).toBe(stored)
  })

  it('keeps the optimistic value for a pending axis while applying the server value elsewhere', () => {
    const prev: AgentTab = { type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1', optionValues: { model: 'opus', permissionMode: 'default' } }
    const fields = resolveSettingsTabFields(
      prev,
      [group('model', 'sonnet'), group('permissionMode', 'plan')],
      new Set(['model']),
    )
    expect(fields.optionValues).toEqual({ model: 'opus', permissionMode: 'plan' })
  })
})

describe('buildAgentStatusTabUpdate', () => {
  const settings = { optionValues: { model: 'opus' } } as Partial<AgentTab>

  it('omits status/sessionId for a status-less (git-only) push so a default cannot overwrite valid state', () => {
    const sc = { status: AgentStatus.UNSPECIFIED, agentSessionId: 's1', startupError: '', startupMessage: '' } as unknown as AgentStatusChange
    const update = buildAgentStatusTabUpdate(sc, false, settings)
    expect('agentStatus' in update).toBe(false)
    expect('agentSessionId' in update).toBe(false)
    // The catalog update still applies.
    expect(update.optionValues).toEqual({ model: 'opus' })
  })

  it('carries status, clears startupError/startupMessage on ACTIVE, and merges settings', () => {
    const sc = { status: AgentStatus.ACTIVE, agentSessionId: 's1', startupError: 'stale', startupMessage: 'stale', supportsSteering: true } as unknown as AgentStatusChange
    const update = buildAgentStatusTabUpdate(sc, true, settings)
    expect(update.agentStatus).toBe(AgentStatus.ACTIVE)
    expect(update.agentSessionId).toBe('s1')
    expect(update.startupError).toBe('')
    expect(update.startupMessage).toBe('')
    expect(update.supportsSteering).toBe(true)
    expect(update.optionValues).toEqual({ model: 'opus' })
  })

  it('carries the phase label while STARTING and the server error on STARTUP_FAILED', () => {
    const starting = buildAgentStatusTabUpdate(
      { status: AgentStatus.STARTING, startupMessage: 'Starting Claude Code…', startupError: '' } as unknown as AgentStatusChange,
      true,
      {},
    )
    expect(starting.startupMessage).toBe('Starting Claude Code…')
    const failed = buildAgentStatusTabUpdate(
      { status: AgentStatus.STARTUP_FAILED, startupError: 'spawn failed', startupMessage: '' } as unknown as AgentStatusChange,
      true,
      {},
    )
    expect(failed.startupError).toBe('spawn failed')
  })

  // withoutLiveStatusFields removes LIVE_STATUS_FIELDS from an older ListAgents reply after a live event.
  // Every live status field must belong to that list, or the older reply could replace it.
  it('writes exactly the fields in LIVE_STATUS_FIELDS beside the status', () => {
    const written = new Set<string>()
    for (const status of [AgentStatus.ACTIVE, AgentStatus.INACTIVE, AgentStatus.STARTING, AgentStatus.STARTUP_FAILED]) {
      const sc = { status, agentSessionId: 's1', startupError: 'e', startupMessage: 'm', supportsSteering: true, supportsPreemption: true } as unknown as AgentStatusChange
      for (const key of Object.keys(buildAgentStatusTabUpdate(sc, true, {})))
        written.add(key)
    }
    expect([...written].sort()).toEqual([...LIVE_STATUS_FIELDS].sort())
  })

  it('derives repo identity from a gitStatus payload (a git-only push)', () => {
    const sc = {
      status: AgentStatus.UNSPECIFIED,
      gitStatus: { branch: 'main', originUrl: 'git@x:y.git', toplevel: '/repo', isWorktree: true },
    } as unknown as AgentStatusChange
    const update = buildAgentStatusTabUpdate(sc, false, {})
    expect(update.gitToplevel).toBe('/repo')
    expect('gitBranch' in update).toBe(false)
    expect('agentGitStatus' in update).toBe(false)
  })

  it('carries gitToplevel whenever the push has a toplevel', () => {
    const gs = { branch: 'main', originUrl: 'git@x:y.git', toplevel: '/repo', ahead: 2, modified: true }
    const sc = { status: AgentStatus.UNSPECIFIED, gitStatus: gs } as unknown as AgentStatusChange

    const update = buildAgentStatusTabUpdate(sc, false, {})
    expect(update.gitToplevel).toBe('/repo')
  })

  it('applies a changed git toplevel', () => {
    const gs = { branch: 'main', originUrl: 'git@x:y.git', toplevel: '/repo', ahead: 2 }
    const sc = { status: AgentStatus.UNSPECIFIED, gitStatus: { ...gs, toplevel: '/other' } } as unknown as AgentStatusChange

    const update = buildAgentStatusTabUpdate(sc, false, {})
    expect(update.gitToplevel).toBe('/other')
  })

  it('leaves git identity alone when a status-only push carries none', () => {
    const sc = { status: AgentStatus.INACTIVE, agentSessionId: 's1' } as unknown as AgentStatusChange

    const update = buildAgentStatusTabUpdate(sc, true, {})
    expect('gitToplevel' in update).toBe(false)
    expect(update.agentStatus).toBe(AgentStatus.INACTIVE)
  })

  // Check the joined tab identity and its For row. An unchanged git update must preserve both.

  it('keeps the agent tab object (and its <For> row) across a no-op git-status push', () => {
    createRoot((dispose) => {
      const s = makeTabStores()
      s.addAgent('a1', { workerId: 'wkr-1' })
      const push = () => {
        const sc = {
          status: AgentStatus.UNSPECIFIED,
          gitStatus: { branch: 'main', originUrl: 'git@x:y.git', toplevel: '/repo', ahead: 2 },
        } as unknown as AgentStatusChange
        s.metadata.patch('a1', buildAgentStatusTabUpdate(sc, false, {}))
      }
      push()
      const before = s.view.getAgentTab('a1')!

      // For uses mapArray. A repeated row-body call therefore identifies a remount.

      let mounts = 0
      const rows = mapArray(() => s.view.forTile(s.rootTileId), (tab) => {
        mounts += 1
        return tab
      })
      rows()
      expect(mounts).toBe(1)

      push()
      rows()
      expect(s.view.getAgentTab('a1'), 'the tab keeps its object').toBe(before)
      expect(mounts, 'and its row is never remounted').toBe(1)
      dispose()
    })
  })
})

/**
 * The sidebar shows background agents also. The shared status handler must write their startup
 * fields through the same path as foreground agents.
 */
describe('handleAgentStatusChange for background agents', () => {
  function backgroundStores() {
    const harness = installTestBridge({ workspaceId: WS })
    const stores = createTestTabStores(WS)
    const chatStore = createChatStore()
    emitAddTab({ type: TabType.AGENT, id: 'bg-1', tileId: harness.rootTileId, position: 'a', workerId: 'w1' })
    stores.metadata.patch('bg-1', { agentStatus: AgentStatus.STARTING })
    return { ...stores, chatStore, repoGitStore: createRepoGitStore() }
  }

  it('writes the startup fields the foreground path writes', () => {
    createRoot((dispose) => {
      const { view, metadata, chatStore, selection, repoGitStore } = backgroundStores()
      const agentSessionStore = createAgentSessionStore()

      handleAgentStatusChange(
        'bg-1',
        { agentId: 'bg-1', status: AgentStatus.STARTUP_FAILED, startupError: 'boom', optionGroups: [] } as unknown as AgentStatusChange,
        'live',
        { chatStore, view, metadata, selection, getActiveWorkspaceId: () => WS, controlStore: createControlStore(), agentSessionStore, repoGitStore },
        createLoadingSignal(),
        () => {},
        undefined,
      )

      const tab = view.getAgentTab('bg-1')
      expect(tab?.agentStatus).toBe(AgentStatus.STARTUP_FAILED)
      expect(tab?.startupError, 'a hand-rolled subset dropped this').toBe('boom')
      dispose()
    })
  })
})

describe('handleAgentInactive', () => {
  it('retains a confirmed response that still needs recording', () => {
    createRoot((dispose) => {
      const stores = makeStores()
      stores.controlStore.addRequest('agent-1', { requestId: 'record', agentId: 'agent-1', payload: {}, claimToken: 'record-claim', responseState: ControlResponseState.DELIVERED })
      handleAgentInactive('agent-1', {} as AgentStatusChange, 'live', stores)
      expect(stores.controlStore.getRequests('agent-1').map(request => request.requestId)).toEqual(['record'])
      dispose()
    })
  })

  it('retains delivered cancellation state until recording completes', () => {
    const store = createControlStore()
    const request = { requestId: 'record', agentId: 'agent-1', payload: {}, claimToken: 'record-claim', responseState: ControlResponseState.READY }
    store.addRequest('agent-1', request)
    handleControlCancellation(create(AgentControlCancelRequestSchema, { ...request, responseState: ControlResponseState.DELIVERED }), store)
    expect(store.getRequests('agent-1')[0]?.responseState).toBe(ControlResponseState.DELIVERED)
    handleControlCancellation(create(AgentControlCancelRequestSchema, { ...request, responseState: ControlResponseState.COMPLETED }), store)
    expect(store.getRequests('agent-1')).toEqual([])
  })

  it('accepts confirmed delivery after cancellation but ignores replay after completion', () => {
    const store = createControlStore()
    const request = { requestId: 'record', agentId: 'agent-1', payload: {}, claimToken: 'record-claim' }
    store.addRequest('agent-1', request)
    handleControlCancellation(create(AgentControlCancelRequestSchema, { ...request, responseState: ControlResponseState.CANCELED }), store)
    store.addRequest('agent-1', { ...request, responseState: ControlResponseState.DELIVERED })
    expect(store.getRequests('agent-1')).toHaveLength(1)
    handleControlCancellation(create(AgentControlCancelRequestSchema, { ...request, responseState: ControlResponseState.COMPLETED }), store)
    store.addRequest('agent-1', { ...request, responseState: ControlResponseState.DELIVERED })
    expect(store.getRequests('agent-1')).toEqual([])
  })
  function makeStores() {
    const tabs = makeTabStores()
    tabs.addAgent('agent-1', { agentStatus: AgentStatus.INACTIVE })
    const controlStore = createControlStore()
    controlStore.addRequest('agent-1', { requestId: 'r1', agentId: 'agent-1', payload: {}, claimToken: 'tok-r1' })
    return {
      controlStore,
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore: createAgentActivityStore(),
      chatStore: createChatStore(),
      view: tabs.view,
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
      tabs,
    }
  }

  it('clears control requests when an agent goes INACTIVE', () => {
    createRoot((dispose) => {
      const stores = makeStores()
      handleAgentInactive('agent-1', { agentSessionId: 'sess-1' } as unknown as AgentStatusChange, 'live', stores)
      expect(stores.controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('clears control requests during catch-up too', () => {
    createRoot((dispose) => {
      const stores = makeStores()
      handleAgentInactive('agent-1', { agentSessionId: 'sess-1' } as unknown as AgentStatusChange, 'catchingUp', stores)
      expect(stores.controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  // The worker's activity transition owns the alert after a process exit.
  // An additional alert from INACTIVE status would notify twice.
  // The handleAgentSettled tests check the activity alert.
})

describe('workerOnline handling in agent statusChange', () => {
  it('ignores workerOnline=false from status-less git-only updates', () => {
    createRoot((dispose) => {
      try {
        let workerOnline = true
        const tabs = makeTabStores()
        tabs.addAgent('connectivity-agent', { agentStatus: AgentStatus.ACTIVE })
        const stores = handlerStores(tabs)
        const patch = vi.spyOn(tabs.metadata, 'patchLive')
        const applyStatusChange = (sc: MessageInitShape<typeof AgentStatusChangeSchema>) => {
          const before = patch.mock.calls.length
          handleAgentStatusChange('connectivity-agent', create(AgentStatusChangeSchema, { agentId: 'connectivity-agent', ...sc }), 'live', stores, createLoadingSignal(), (value) => {
            workerOnline = value
          })
          return patch.mock.calls.length > before
        }

        expect(applyStatusChange({
          status: AgentStatus.UNSPECIFIED,
          workerOnline: false,
          gitStatus: {},
        })).toBe(true)
        expect(workerOnline).toBe(true)

        expect(applyStatusChange({
          status: AgentStatus.INACTIVE,
          workerOnline: true,
        })).toBe(true)
        expect(workerOnline).toBe(true)
        expect(applyStatusChange({ status: AgentStatus.UNSPECIFIED, workerOnline: false })).toBe(false)
        expect(workerOnline).toBe(true)
      }
      finally {
        dispose()
      }
    })
  })
})

// A subscriber can join after the initial STARTING event. Catch-up must supply the current
// shell phase so the frontend displays the correct startup label.

describe('startupMessage handling in terminal statusChange', () => {
  // Call the real terminal status handler. A STARTING event writes the phase label, including a
  // changed label during the same startup.

  function applyStarting(
    tabs: TabStores,
    terminalId: string,
    msg: string | undefined,
  ) {
    applyTerminalStatusChange(
      tabs.metadata,
      createRepoGitStore(),
      tabs.view.getTerminalTab(terminalId),
      terminalId,
      create(TerminalStatusChangeSchema, { terminalId, status: TerminalStatus.STARTING, startupMessage: msg ?? '' }),
    )
  }

  it('stores startupMessage on the initial STARTING event so the overlay renders the backend phase label', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addTerminal('term-1')

      applyStarting(tabs, 'term-1', 'Starting zsh…')

      const tab = tabs.view.getTerminalTab('term-1')
      expect(tab?.status).toBe(TerminalStatus.STARTING)
      expect(tab?.startupMessage).toBe('Starting zsh…')
      dispose()
    })
  })

  it('updates startupMessage on a same-status STARTING event so later phase broadcasts refresh the overlay label', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

      applyStarting(tabs, 'term-1', 'Starting fish…')

      const tab = tabs.view.getTerminalTab('term-1')
      expect(tab?.startupMessage).toBe('Starting fish…')
      dispose()
    })
  })

  // The worker sends the worktree phase as another STARTING status. A rollback supplies its own
  // label before STARTUP_FAILED. Apply both labels.

  it('applies the "Creating worktree" phase-0 label to the tab', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

      applyStarting(tabs, 'term-1', 'Creating worktree "feature/x"…')

      const tab = tabs.view.getTerminalTab('term-1')
      expect(tab?.startupMessage).toBe('Creating worktree "feature/x"…')
      dispose()
    })
  })

  it('applies a following "Rolling back worktree" label on same-status STARTING', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING, startupMessage: 'Creating worktree "feature/x"…' })

      applyStarting(tabs, 'term-1', 'Rolling back worktree "feature/x"…')

      const tab = tabs.view.getTerminalTab('term-1')
      expect(tab?.startupMessage).toBe('Rolling back worktree "feature/x"…')
      dispose()
    })
  })
})

describe('applyTerminalStatusChange', () => {
  function statusChange(fields: Partial<TerminalStatusChange>): TerminalStatusChange {
    return {
      status: TerminalStatus.READY,
      gitStatus: undefined,
      startupError: '',
      startupMessage: '',
      ...fields,
    } as TerminalStatusChange
  }

  it('clears a starting terminal to READY', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const repoGitStore = createRepoGitStore()
      tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

      applyTerminalStatusChange(
        tabs.metadata,
        repoGitStore,
        tabs.view.getTerminalTab('term-1'),
        'term-1',
        statusChange({ status: TerminalStatus.READY }),
      )

      const tab = tabs.view.getTerminalTab('term-1')
      expect(tab?.status).toBe(TerminalStatus.READY)
      expect(tab?.startupMessage, 'the spinner label must go with it').toBe('')
      dispose()
    })
  })

  it('records STARTUP_FAILED with its error', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const repoGitStore = createRepoGitStore()
      tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING })

      applyTerminalStatusChange(
        tabs.metadata,
        repoGitStore,
        tabs.view.getTerminalTab('term-1'),
        'term-1',
        statusChange({ status: TerminalStatus.STARTUP_FAILED, startupError: 'no such shell' }),
      )

      const tab = tabs.view.getTerminalTab('term-1')
      expect(tab?.status).toBe(TerminalStatus.STARTUP_FAILED)
      expect(tab?.startupError).toBe('no such shell')
      dispose()
    })
  })

  it('leaves a DISCONNECTED terminal alone on a READY event', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const repoGitStore = createRepoGitStore()
      tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.DISCONNECTED })

      applyTerminalStatusChange(
        tabs.metadata,
        repoGitStore,
        tabs.view.getTerminalTab('term-1'),
        'term-1',
        statusChange({ status: TerminalStatus.READY }),
      )

      expect(tabs.view.getTerminalTab('term-1')?.status).toBe(TerminalStatus.DISCONNECTED)
      dispose()
    })
  })

  // A pending ListTerminals reply compares the live status epoch. Count an applied lifecycle
  // write. A refused event must not suppress the snapshot that can restore the tab.

  describe('liveStatusEpoch', () => {
    function applyEvent(tabs: ReturnType<typeof makeTabStores>, fields: Partial<TerminalStatusChange>) {
      applyTerminalStatusChange(
        tabs.metadata,
        createRepoGitStore(),
        tabs.view.getTerminalTab('term-1'),
        'term-1',
        statusChange(fields),
      )
    }

    it('counts the STARTING event that moves a tab to STARTING', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1')

        applyEvent(tabs, { status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

        expect(tabs.view.getTerminalTab('term-1')?.status).toBe(TerminalStatus.STARTING)
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(1)
        dispose()
      })
    })

    it('counts the READY event that ends a startup', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING })

        applyEvent(tabs, { status: TerminalStatus.READY })

        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(1)
        dispose()
      })
    })

    it('counts the READY event that reaches a tab with no status yet', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1')

        applyEvent(tabs, { status: TerminalStatus.READY })

        expect(tabs.view.getTerminalTab('term-1')?.status).toBe(TerminalStatus.READY)
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(1)
        dispose()
      })
    })

    it('counts a STARTUP_FAILED event', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING })

        applyEvent(tabs, { status: TerminalStatus.STARTUP_FAILED, startupError: 'no such shell' })

        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(1)
        dispose()
      })
    })

    it('counts a new phase label of a startup', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING, startupMessage: 'Creating worktree…' })

        applyEvent(tabs, { status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

        expect(tabs.view.getTerminalTab('term-1')?.startupMessage).toBe('Starting zsh…')
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(1)
        dispose()
      })
    })

    it('does not count a READY event that a DISCONNECTED tab refuses', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.DISCONNECTED })

        applyEvent(tabs, { status: TerminalStatus.READY })

        expect(tabs.view.getTerminalTab('term-1')?.status).toBe(TerminalStatus.DISCONNECTED)
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(0)
        dispose()
      })
    })

    it('does not count a STARTING event that a READY tab refuses', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.READY })

        applyEvent(tabs, { status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

        expect(tabs.view.getTerminalTab('term-1')?.status).toBe(TerminalStatus.READY)
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(0)
        dispose()
      })
    })

    it('does not count a STARTING event that repeats the label of the tab', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

        applyEvent(tabs, { status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(0)
        dispose()
      })
    })

    it('does not count an event that carries a git status and no lifecycle', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.STARTING })

        applyEvent(tabs, {
          status: TerminalStatus.UNSPECIFIED,
          gitStatus: { branch: 'main', toplevel: '/repo', originUrl: '', isWorktree: false } as never,
        })

        expect(tabs.metadata.get('term-1')?.gitToplevel).toBe('/repo')
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(0)
        dispose()
      })
    })

    it('counts the exit of a shell', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('term-1', { terminalStatus: TerminalStatus.READY })

        markTerminalExited(tabs.metadata, 'term-1')

        expect(tabs.view.getTerminalTab('term-1')?.status).toBe(TerminalStatus.EXITED)
        expect(tabs.metadata.liveStatusEpoch('term-1')).toBe(1)
        dispose()
      })
    })

    // ListTerminals never restores contentReady to false, so an older reply cannot clear it.
    // Check contentReady separately from TERMINAL_LIVE_STATUS_FIELDS.
    // Every other field that a live status event writes must belong to that protected list.
    // Otherwise, an older ListTerminals reply could replace the live field.
    it('writes exactly the fields in TERMINAL_LIVE_STATUS_FIELDS, and contentReady, through patchLiveStatus', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        const written = new Set<string>()
        const original = tabs.metadata.patchLiveStatus
        tabs.metadata.patchLiveStatus = (tabId, fields) => {
          for (const key of Object.keys(fields))
            written.add(key)
          original(tabId, fields)
        }

        tabs.addTerminal('term-1')
        applyEvent(tabs, { status: TerminalStatus.STARTING, startupMessage: 'Creating worktree…' })
        applyEvent(tabs, { status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })
        applyEvent(tabs, { status: TerminalStatus.READY })
        markTerminalExited(tabs.metadata, 'term-1')
        tabs.addTerminal('term-2', { terminalStatus: TerminalStatus.STARTING })
        applyTerminalStatusChange(
          tabs.metadata,
          createRepoGitStore(),
          tabs.view.getTerminalTab('term-2'),
          'term-2',
          statusChange({ status: TerminalStatus.STARTUP_FAILED, startupError: 'no such shell' }),
        )

        expect([...written].sort()).toEqual([...TERMINAL_LIVE_STATUS_FIELDS, 'contentReady'].sort())
        dispose()
      })
    })
  })

  it('writes repo identity for a terminal that has no joined tab yet', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const repoGitStore = createRepoGitStore()
      expect(tabs.view.getTerminalTab('term-unjoined'), 'precondition: not joined').toBeUndefined()

      applyTerminalStatusChange(
        tabs.metadata,
        repoGitStore,
        undefined,
        'term-unjoined',
        statusChange({
          status: TerminalStatus.STARTING,
          gitStatus: {
            branch: 'feature',
            toplevel: '/repo',
            originUrl: 'git@example.com:org/repo.git',
            isWorktree: true,
          } as never,
        }),
        'wkr-1',
      )

      const row = tabs.metadata.get('term-unjoined')
      expect(row?.gitToplevel).toBe('/repo')
      expect(repoGitStore.get(repoKey('wkr-1', '/repo'))?.branch).toBe('feature')
      expect(repoGitStore.get(repoKey('wkr-1', '/repo'))?.isWorktree).toBe(true)
      dispose()
    })
  })
})

/**
 * Call the real session-info handler. It translates recognized snake_case fields and skips an
 * empty scalar update.
 */
describe('agent_session_info snake_case wire normalization', () => {
  function applyAgentSessionInfo(
    sessionStore: ReturnType<typeof createAgentSessionStore>,
    agentId: string,
    info: Record<string, unknown> | undefined,
  ) {
    const msg = create(AgentChatMessageSchema, {
      source: MessageSource.LEAPMUX,
      seq: -1n,
      contentCompression: ContentCompression.NONE,
      content: new TextEncoder().encode(JSON.stringify({ type: 'agent_session_info', info })),
    })
    handleAgentSessionInfo(agentId, parseMessageContent(msg), { agentSessionStore: sessionStore, chatStore: createChatStore() })
  }

  it('writes totalCostUsd from a snake_case payload', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      applyAgentSessionInfo(store, 'cc-1', { total_cost_usd: 0.42 })
      expect(store.getInfo('cc-1').totalCostUsd).toBe(0.42)
      dispose()
    })
  })

  it('ignores a camelCase-only payload (legacy wire format removed)', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      applyAgentSessionInfo(store, 'cc-2', { totalCostUsd: 0.42 })
      expect(store.getInfo('cc-2').totalCostUsd).toBeUndefined()
      dispose()
    })
  })

  it('skips updateInfo for an empty info payload', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      applyAgentSessionInfo(store, 'cc-3', {})
      expect(Object.keys(store.getInfo('cc-3'))).toHaveLength(0)
      dispose()
    })
  })
})

describe('agentMessage sub-handlers', () => {
  function agentMessage(content: unknown, agentProvider = AgentProvider.CLAUDE_CODE) {
    return {
      id: 'm1',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify(content)),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      agentProvider,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  /** The two stores handleAgentSessionInfo writes to. */
  function sessionInfoStores() {
    return { agentSessionStore: createAgentSessionStore(), agentActivityStore: createAgentActivityStore(), chatStore: createChatStore() }
  }

  it('handleAgentSessionInfo consumes an agent_session_info message and applies its updates', () => {
    createRoot((dispose) => {
      const stores = sessionInfoStores()
      const msg = agentMessage({ type: 'agent_session_info', info: { total_cost_usd: 1.5 } })
      const handled = handleAgentSessionInfo('a1', parseMessageContent(msg), stores)
      // A true return tells the caller to skip transcript storage for this ephemeral message.
      expect(handled).toBe(true)
      expect(stores.agentSessionStore.getInfo('a1').totalCostUsd).toBe(1.5)
      dispose()
    })
  })

  it('handleAgentSessionInfo replaces a full rate-limit snapshot', () => {
    createRoot((dispose) => {
      const stores = sessionInfoStores()
      const first = agentMessage({
        type: 'agent_session_info',
        info: {
          rate_limits: {
            mode: 'replace',
            values: {
              five_hour: { status: 'allowed_warning' },
              seven_day: { status: 'allowed' },
            },
          },
        },
      })
      const second = agentMessage({
        type: 'agent_session_info',
        info: {
          rate_limits: {
            mode: 'replace',
            values: { seven_day: { status: 'allowed' } },
          },
        },
      })

      handleAgentSessionInfo('a1', parseMessageContent(first), stores)
      handleAgentSessionInfo('a1', parseMessageContent(second), stores)

      expect(stores.agentSessionStore.getInfo('a1').rateLimits).toEqual({
        seven_day: { status: 'allowed' },
      })
      dispose()
    })
  })

  it('handleAgentSessionInfo returns false for a persisted message (caller keeps processing it)', () => {
    createRoot((dispose) => {
      const msg = agentMessage({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), sessionInfoStores())).toBe(false)
      dispose()
    })
  })

  it('handleAgentSessionInfo clears a stale thinking-token estimate on a 0 (per-phase reset)', () => {
    createRoot((dispose) => {
      const stores = sessionInfoStores()
      stores.agentSessionStore.applyProgress('a1', { revision: 1, thinkingTokens: 500 })
      const msg = agentMessage({ type: 'agent_session_info', info: { generation_progress_revision: 2, thinking_tokens: 0, output_bytes: 0 } })
      handleAgentSessionInfo('a1', parseMessageContent(msg), stores)
      expect(stores.agentSessionStore.getProgress('a1').thinkingTokens).toBeUndefined()
      dispose()
    })
  })

  it('handleAgentSessionInfo routes running_tool to the chat store, not the session store', () => {
    createRoot((dispose) => {
      const stores = sessionInfoStores()
      const msg = agentMessage({
        type: 'agent_session_info',
        info: { running_tool: { span_id: 'toolu_A', agent_session_id: 'sess-1', tool_name: 'Bash', elapsed_seconds: 30 } },
      })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), stores)).toBe(true)
      expect(stores.chatStore.getToolProgress('a1', TOOL_A)).toEqual({ elapsedSeconds: 30 })
      // Tool progress is separate from AgentSessionInfo. Its updates create no stored session-info
      // field.

      expect(stores.agentSessionStore.getInfo('a1')).toEqual({})
      dispose()
    })
  })

  it('handleResultDivider rehydrates cost without alerting — the settle edge owns the alert', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const stores = {
        agentSessionStore: createAgentSessionStore(),
        agentActivityStore: createAgentActivityStore(),
        chatStore: createChatStore(),
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        getActiveWorkspaceId: () => WS,
      }
      const msg = agentMessage({ type: 'result', subtype: 'success', total_cost_usd: 0.25 })
      const parsed = parseMessageContent(msg)

      handleResultDivider('a1', msg, parsed, stores, 'live')

      // The divider restores scalar metadata through its delivery receipt. It supplies no activity alert.
      // handleActivityChanged supplies the sound and badge after a transition out of WORKING.
      expect(stores.agentSessionStore.getInfo('a1').totalCostUsd).toBe(0.25)
      dispose()
    })
  })
})

describe('reconcileLaggingTails', () => {
  function run(overrides: {
    agentTabs: Array<{ id: string, workerId: string }>
    hasNewerMessages?: (id: string) => boolean
    caughtUpToLiveTail?: (id: string) => boolean
    isTailFillDeferred?: (id: string) => boolean
    getLastSeq?: (id: string) => bigint
    getLiveTailSeq?: (id: string) => bigint
    isFetchingNewer?: (id: string) => boolean
  }) {
    const catchUp: Array<{ workerId: string, agentId: string, afterSeq: bigint }> = []
    const resume: Array<{ workerId: string, agentId: string }> = []
    const jumps: Array<{ workerId: string, agentId: string }> = []
    reconcileLaggingTails({
      agentTabs: () => overrides.agentTabs,
      hasNewerMessages: overrides.hasNewerMessages ?? (() => false),
      caughtUpToLiveTail: overrides.caughtUpToLiveTail ?? (() => true),
      isTailFillDeferred: overrides.isTailFillDeferred ?? (() => false),
      // Default to a loaded window with sequence 1n. The separate empty-window case supplies 0n.
      getLastSeq: overrides.getLastSeq ?? (() => 1n),
      getLiveTailSeq: overrides.getLiveTailSeq ?? (() => 1n),
      isFetchingNewer: overrides.isFetchingNewer ?? (() => false),
      catchUpToTail: (workerId, agentId, afterSeq) => catchUp.push({ workerId, agentId, afterSeq }),
      resumeDeferredTailFill: (workerId, agentId) => resume.push({ workerId, agentId }),
      jumpToLatest: (workerId, agentId) => jumps.push({ workerId, agentId }),
    })
    return { catchUp, resume, jumps }
  }

  it('forward-fills ONLY an agent that lags its live tail while AT the tail', () => {
    const { catchUp, resume } = run({
      agentTabs: [
        // Fill only the agent that remains behind the live tail.
        { id: 'lagging', workerId: 'w1' },
        { id: 'caught-up', workerId: 'w1' },
        // Preserve this history window because tail fill is not deferred.
        { id: 'scrolled-away', workerId: 'w1' },
      ],
      hasNewerMessages: id => id === 'scrolled-away',
      caughtUpToLiveTail: id => id === 'caught-up',
      // lagging is at the tail. scrolled-away has a nonempty loaded window.
      getLastSeq: id => (id === 'lagging' ? 42n : id === 'scrolled-away' ? 30n : 0n),
    })
    expect(catchUp).toEqual([{ workerId: 'w1', agentId: 'lagging', afterSeq: 42n }])
    // Preserve the history window when the user scrolls away without deferred tail fill.
    expect(resume).toEqual([])
  })

  it('skips a tab with no workerId (a non-active-workspace agent)', () => {
    const { catchUp } = run({
      agentTabs: [{ id: 'lagging', workerId: '' }],
      caughtUpToLiveTail: () => false, // The agent needs tail fill but has no worker that can supply it.
    })
    expect(catchUp).toEqual([])
  })

  it('forward-fills every lagging agent from its own loaded tail', () => {
    const { catchUp } = run({
      agentTabs: [
        { id: 'a', workerId: 'w1' },
        { id: 'b', workerId: 'w2' },
      ],
      caughtUpToLiveTail: () => false,
      getLastSeq: id => (id === 'a' ? 10n : 20n),
    })
    expect(catchUp).toEqual([
      { workerId: 'w1', agentId: 'a', afterSeq: 10n },
      { workerId: 'w2', agentId: 'b', afterSeq: 20n },
    ])
  })

  it('resumes an exhaustion-forced deferred fill, but not a plain scrolled-away wall', () => {
    const { catchUp, resume } = run({
      agentTabs: [
        // Resume deferred tail fill when the loaded history stays behind the live tail.
        { id: 'deferred', workerId: 'w1' },
        // Preserve this history window because tail fill is not deferred.
        { id: 'scrolled-away', workerId: 'w1' },
      ],
      // Both agents are away from the loaded tail, and both remain behind the live tail.
      hasNewerMessages: () => true,
      caughtUpToLiveTail: () => false,
      isTailFillDeferred: id => id === 'deferred',
    })
    expect(resume).toEqual([{ workerId: 'w1', agentId: 'deferred' }])
    // Resumption merges the deferred page through resumeDeferredTailFill instead of catchUpToTail.
    expect(catchUp).toEqual([])
  })

  it('prefers catchUpToTail at the tail over a deferred resume, and skips a caught-up agent', () => {
    const { catchUp, resume } = run({
      agentTabs: [
        // Use catchUpToTail when the agent remains behind and has no newer history page.
        { id: 'at-tail', workerId: 'w1' },
        // An agent at the live tail requires no fill, even when deferred.
        { id: 'caught-up-deferred', workerId: 'w1' },
      ],
      hasNewerMessages: () => false,
      caughtUpToLiveTail: id => id === 'caught-up-deferred',
      isTailFillDeferred: () => true,
      getLastSeq: () => 7n,
    })
    expect(catchUp).toEqual([{ workerId: 'w1', agentId: 'at-tail', afterSeq: 7n }])
    expect(resume).toEqual([])
  })

  it('re-anchors an EMPTY window (a full phantom reap) on the latest page instead of forward-filling', () => {
    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'emptied', workerId: 'w1' }],
      // The server retains content, but reconciliation removed every loaded row, so getLastSeq returns 0n.
      // No loaded row supplies a forward-paging cursor. Load the latest page instead.
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 0n,
    })
    expect(jumps).toEqual([{ workerId: 'w1', agentId: 'emptied' }])
    // An empty window cannot use forward paging.
    expect(catchUp).toEqual([])
    expect(resume).toEqual([])
  })

  it('does NOT re-issue the empty-window re-anchor while a newer fetch is already in flight', () => {
    const { jumps } = run({
      agentTabs: [{ id: 'emptied', workerId: 'w1' }],
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 0n,
      // The existing jumpToLatest fetch still owns this empty window.
      isFetchingNewer: () => true,
    })
    // Retain the current fetch. The reconcile effect must not cancel and restart it.
    expect(jumps).toEqual([])
  })

  it('re-anchors on the latest page when the live-tail gap exceeds the limit', () => {
    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'lagging', workerId: 'w1' }],
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 10n,
      getLiveTailSeq: () => 10n + CATCH_UP_GAP_LIMIT + 1n,
    })
    expect(jumps).toEqual([{ workerId: 'w1', agentId: 'lagging' }])
    expect(catchUp).toEqual([])
    expect(resume).toEqual([])
  })

  it('drains a live-tail gap at the inclusive limit', () => {
    const { catchUp, jumps } = run({
      agentTabs: [{ id: 'lagging', workerId: 'w1' }],
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 10n,
      getLiveTailSeq: () => 10n + CATCH_UP_GAP_LIMIT,
    })
    expect(catchUp).toEqual([{ workerId: 'w1', agentId: 'lagging', afterSeq: 10n }])
    expect(jumps).toEqual([])
  })

  it('does nothing for an over-limit gap while a newer fetch runs', () => {
    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'lagging', workerId: 'w1' }],
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 10n,
      getLiveTailSeq: () => 10n + CATCH_UP_GAP_LIMIT + 1n,
      isFetchingNewer: () => true,
    })
    expect(jumps).toEqual([])
    expect(catchUp).toEqual([])
    expect(resume).toEqual([])
  })

  it('re-anchors an over-limit deferred gap instead of resuming the fill', () => {
    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'deferred', workerId: 'w1' }],
      hasNewerMessages: () => true,
      caughtUpToLiveTail: () => false,
      isTailFillDeferred: () => true,
      getLastSeq: () => 10n,
      getLiveTailSeq: () => 10n + CATCH_UP_GAP_LIMIT + 1n,
    })
    expect(jumps).toEqual([{ workerId: 'w1', agentId: 'deferred' }])
    expect(catchUp).toEqual([])
    expect(resume).toEqual([])
  })

  it('does nothing for an over-limit gap on a plain scrolled-away wall', () => {
    // The user chose this history window. An excessive live-tail gap must preserve it. Normal
    // forward paging lets the user return to the tail.

    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'scrolled-away', workerId: 'w1' }],
      hasNewerMessages: () => true,
      caughtUpToLiveTail: () => false,
      isTailFillDeferred: () => false,
      getLastSeq: () => 10n,
      getLiveTailSeq: () => 10n + CATCH_UP_GAP_LIMIT + 1n,
    })
    expect(jumps).toEqual([])
    expect(catchUp).toEqual([])
    expect(resume).toEqual([])
  })

  it('does nothing for a caught-up tab', () => {
    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'caught-up', workerId: 'w1' }],
      caughtUpToLiveTail: () => true,
      getLastSeq: () => 10n,
      getLiveTailSeq: () => 10n,
    })
    expect(jumps).toEqual([])
    expect(catchUp).toEqual([])
    expect(resume).toEqual([])
  })
})

// Call the production handlers that handleAgentEvent selects, with actual stores.
// The separate replay-ownership cases exercise the dispatcher through registered transport listeners.
describe('extracted handleAgentEvent branch handlers', () => {
  const enc = (s: string) => new TextEncoder().encode(s)
  const argStores = () => {
    const tabs = makeTabStores()
    return {
      agentSessionStore: createAgentSessionStore(),
      agentActivityStore: createAgentActivityStore(),
      chatStore: createChatStore(),
      view: tabs.view,
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
      controlStore: createControlStore(),
      quakeStore: createTestQuakeStore(),
      getActiveQuakeKeyId: () => null,
      repoGitStore: createRepoGitStore(),
      tabs,
    }
  }

  describe('handleActivityChanged', () => {
    const activityStores = (
      tabs: ReturnType<typeof makeTabStores>,
      agentActivityStore: ReturnType<typeof createAgentActivityStore>,
      onAgentSettled?: (id: string, uses?: number) => void,
    ) => ({
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
      view: tabs.view,
      agentActivityStore,
      ...(onAgentSettled !== undefined ? { onAgentSettled } : {}),
    })

    it('stores what the worker says, and rings only on the busy -> idle edge', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        tabs.addAgent('a2')
        tabs.selection.setActiveById(TabType.AGENT, 'a2')
        const activity = createAgentActivityStore()
        // uses is number or undefined.
        // The settle handler calls the callback with undefined when the settle supplies no tool count.
        const ended: Array<{ id: string, uses: number | undefined }> = []
        const stores = activityStores(tabs, activity, (id, uses) => ended.push({ id, uses }))

        handleActivityChanged('a1', { state: AgentActivityState.WORKING }, stores)
        expect(activity.isBusy('a1')).toBe(true)
        expect(ended, 'going busy is not a settle').toEqual([])

        handleActivityChanged('a1', { state: AgentActivityState.IDLE, numToolUses: 3 }, stores)
        expect(activity.isBusy('a1')).toBe(false)
        expect(ended).toEqual([{ id: 'a1', uses: 3 }])
        expect(tabs.view.getAgentTab('a1')?.hasNotification, 'a1 is off screen').toBe(true)
        dispose()
      })
    })

    it('keeps the parent WORKING when its subagent settles', () => {
      createRoot((dispose) => {
        // ThinkingIndicator reads activity for its own agent ID. A child idle event must preserve the
        // parent's WORKING state while the parent turn continues.

        const tabs = makeTabStores()
        tabs.addAgent('root-1')
        tabs.addAgent('child-1', { parentAgentId: 'root-1' })
        const activity = createAgentActivityStore()
        const stores = activityStores(tabs, activity)

        handleActivityChanged('root-1', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('child-1', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('child-1', { state: AgentActivityState.IDLE }, stores)

        expect(activity.isBusy('child-1')).toBe(false)
        expect(activity.isBusy('root-1'), 'the parent owes the user a reply').toBe(true)
        dispose()
      })
    })

    it('rings once when the same idle report arrives twice', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        const activity = createAgentActivityStore()
        const ended: string[] = []
        const stores = activityStores(tabs, activity, id => ended.push(id))

        handleActivityChanged('a1', { state: AgentActivityState.WORKING }, stores)
        handleActivityChanged('a1', { state: AgentActivityState.IDLE }, stores)
        // Repeated live idle reports must not repeat the alert. CatchUpStart supplies replay activity
        // as a baseline without an alert.

        handleActivityChanged('a1', { state: AgentActivityState.IDLE }, stores)

        expect(ended).toEqual(['a1'])
        dispose()
      })
    })

    it('does not ring for an idle report about an agent it never saw working', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        const activity = createAgentActivityStore()
        const ended: string[] = []

        // A NOTIFY tab can subscribe after the turn starts and receive IDLE without an earlier WORKING level.
        // That report creates no settle.
        handleActivityChanged('a1', { state: AgentActivityState.IDLE }, activityStores(tabs, activity, id => ended.push(id)))

        expect(ended).toEqual([])
        expect(activity.isBusy('a1'), 'the write still lands').toBe(false)
        dispose()
      })
    })

    it('rings for a settle that races the replay it arrives during', () => {
      createRoot((dispose) => {
        // A live IDLE transition can arrive during replay. It must still produce its alert.
        // Suppressing live transitions throughout replay would discard that alert.
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        const activity = createAgentActivityStore()
        const ended: string[] = []
        const stores = activityStores(tabs, activity, id => ended.push(id))

        activity.seedPublished('a1', AgentActivityState.WORKING)
        handleActivityChanged('a1', { state: AgentActivityState.IDLE }, stores)

        expect(ended).toEqual(['a1'])
        dispose()
      })
    })
  })

  describe('handleAgentSettled', () => {
    const settledStores = (tabs: ReturnType<typeof makeTabStores>, onAgentSettled?: (id: string, uses?: number) => void) => ({
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
      view: tabs.view,
      ...(onAgentSettled !== undefined ? { onAgentSettled } : {}),
    })

    it('alerts with and without a tool count, and badges only an off-screen tab', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        tabs.addAgent('a2')
        tabs.selection.setActiveById(TabType.AGENT, 'a2')
        // Record uses as undefined when the settle supplies no tool count.
        // This distinguishes that settle from an absent callback.
        const ended: Array<{ id: string, uses: number | undefined }> = []
        const push = (id: string, uses: number | undefined) => ended.push({ id, uses })
        handleAgentSettled('a1', undefined, settledStores(tabs, push))
        handleAgentSettled('a2', 3, settledStores(tabs, push))
        // Keep an absent tool count distinct from zero.
        // The alert callback still runs and receives undefined when no count exists.
        expect(ended).toEqual([{ id: 'a1', uses: undefined }, { id: 'a2', uses: 3 }])
        expect(tabs.view.getAgentTab('a1')?.hasNotification).toBe(true)
        expect(tabs.view.getAgentTab('a2')?.hasNotification).not.toBe(true)
        dispose()
      })
    })

    it('does not badge a tile-active agent when another tab is workspace-active', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        const secondTile = tabs.layoutStore.splitTile(tabs.rootTileId, 'horizontal')!
        tabs.addAgent('a1')
        tabs.addAgent('a2', {}, { tileId: secondTile, activate: false })
        tabs.selection.setActiveById(TabType.AGENT, 'a2')
        tabs.selection.setActiveById(TabType.AGENT, 'a1')
        handleAgentSettled('a2', undefined, settledStores(tabs))
        expect(tabs.view.getAgentTab('a2')?.hasNotification).not.toBe(true)
        dispose()
      })
    })

    it('ignores an agent whose tab is gone', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        const ended: string[] = []
        handleAgentSettled('never-existed', undefined, settledStores(tabs, id => ended.push(id)))
        expect(ended).toEqual([])
        dispose()
      })
    })
  })

  describe('enqueuePendingTerminalData', () => {
    it('buffers deltas and lets a snapshot clear prior frames', () => {
      const pending = new Map<string, Array<{ data: Uint8Array, isSnapshot: boolean, endOffset: bigint }>>()
      enqueuePendingTerminalData(pending, 't1', { data: new Uint8Array([1]), isSnapshot: false, endOffset: 1n })
      enqueuePendingTerminalData(pending, 't1', { data: new Uint8Array([2]), isSnapshot: false, endOffset: 2n })
      enqueuePendingTerminalData(pending, 't1', { data: new Uint8Array([9]), isSnapshot: true, endOffset: 9n })
      expect(pending.get('t1')).toHaveLength(1)
      expect(pending.get('t1')![0]?.endOffset).toBe(9n)
    })

    it('caps the queue so a never-mounting terminal cannot grow it without bound', () => {
      const pending = new Map<string, Array<{ data: Uint8Array, isSnapshot: boolean, endOffset: bigint }>>()
      // Exceed the frame limit. Retain only the newest MAX_PENDING_TERMINAL_FRAMES frames.
      let evicted = false
      for (let i = 0; i < MAX_PENDING_TERMINAL_FRAMES + 50; i++)
        evicted = enqueuePendingTerminalData(pending, 't1', { data: new Uint8Array([i]), isSnapshot: false, endOffset: BigInt(i) }) || evicted
      expect(pending.get('t1')).toHaveLength(MAX_PENDING_TERMINAL_FRAMES)
      // The queue removes its oldest frames and retains its newest frame.
      expect(pending.get('t1')!.at(-1)!.endOffset).toBe(BigInt(MAX_PENDING_TERMINAL_FRAMES + 49))
      // The queue reports each eviction. The caller must request a full snapshot because incremental
      // output cannot restore the removed bytes.

      expect(evicted).toBe(true)
    })

    it('reports no eviction while the queue stays under the cap', () => {
      const pending = new Map<string, Array<{ data: Uint8Array, isSnapshot: boolean, endOffset: bigint }>>()
      for (let i = 0; i < 3; i++)
        expect(enqueuePendingTerminalData(pending, 't1', { data: new Uint8Array([i]), isSnapshot: false, endOffset: BigInt(i) })).toBe(false)
    })
  })

  describe('terminal notify events', () => {
    it('bell badges a background terminal tab', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('t1')
        tabs.addTerminal('t2')
        tabs.selection.setActiveById(TabType.TERMINAL, 't2')
        handleTerminalBell('t1', { metadata: tabs.metadata, selection: tabs.selection, getActiveWorkspaceId: () => WS, view: tabs.view })
        expect(tabs.view.getTerminalTab('t1')?.hasNotification).toBe(true)
        dispose()
      })
    })

    it('does not badge a tile-active terminal when another tab is workspace-active', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        const secondTile = tabs.layoutStore.splitTile(tabs.rootTileId, 'horizontal')!
        tabs.addTerminal('t1')
        nextPosition += 1
        emitAddTab({ type: TabType.TERMINAL, id: 't2', tileId: secondTile, position: `p${nextPosition}`, workerId: '' })
        tabs.selection.setActiveById(TabType.TERMINAL, 't2')
        tabs.selection.setActiveById(TabType.TERMINAL, 't1')
        handleTerminalBell('t2', { metadata: tabs.metadata, selection: tabs.selection, getActiveWorkspaceId: () => WS, view: tabs.view })
        expect(tabs.view.getTerminalTab('t2')?.hasNotification).not.toBe(true)
        dispose()
      })
    })

    it('titleChanged patches ptyTitle only', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('t1')
        handleTerminalTitleChanged('t1', { title: 'shell' } as never, tabs.metadata)
        expect(tabs.metadata.get('t1')?.ptyTitle).toBe('shell')
        expect(tabs.metadata.get('t1')?.title ?? '').toBe('')
        dispose()
      })
    })

    it('titleChanged ignores an empty OSC title so a rename sticks', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('t1')
        tabs.metadata.patch('t1', { title: 'My Shell', ptyTitle: '' })
        handleTerminalTitleChanged('t1', { title: '' } as never, tabs.metadata)
        expect(tabs.metadata.get('t1')?.title).toBe('My Shell')
        expect(tabs.metadata.get('t1')?.ptyTitle ?? '').toBe('')
        dispose()
      })
    })

    it('titleChanged does not overwrite a user rename in title', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('t1')
        tabs.metadata.patch('t1', { title: 'My Shell', ptyTitle: '' })
        handleTerminalTitleChanged('t1', { title: 'live-pty' } as never, tabs.metadata)
        expect(tabs.metadata.get('t1')?.title).toBe('My Shell')
        expect(tabs.metadata.get('t1')?.ptyTitle).toBe('live-pty')
        dispose()
      })
    })

    it('notification badges a background terminal and leaves the active one alone', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('t1')
        tabs.addTerminal('t2')
        tabs.selection.setActiveById(TabType.TERMINAL, 't2')
        handleTerminalNotification('t1', { title: '', body: 'hi' } as never, {
          metadata: tabs.metadata,
          selection: tabs.selection,
          getActiveWorkspaceId: () => WS,
        })
        expect(tabs.view.getTerminalTab('t1')?.hasNotification).toBe(true)
        dispose()
      })
    })

    // terminalEvents.test.ts checks detached-terminal visibility and the actual desktop
    // notification.

    it('progress patches metadata fields', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addTerminal('t1')
        handleTerminalProgress('t1', { state: 1, percent: 42 } as never, tabs.metadata)
        expect(tabs.metadata.get('t1')?.progressPercent).toBe(42)
        dispose()
      })
    })
  })

  describe('handleControlRequest', () => {
    it('retains a delivered response after the agent stops', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.INACTIVE })
        const request = { ...req('a1'), responseState: ControlResponseState.DELIVERED }
        handleControlRequest('a1', request, 'catchingUp', s)
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(s.controlStore.getRequests('a1')[0]?.responseState).toBe(ControlResponseState.DELIVERED)
        expect(s.tabs.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.INACTIVE)
        dispose()
      })
    })
    it('retains the exact native bytes after parsing the request', () => {
      createRoot((dispose) => {
        const s = argStores()
        const original = '{"method":"item/commandExecution/requestApproval","large":9007199254740993,"value":1,"value":2}'
        const request = { requestId: 'raw', agentId: 'a1', payload: enc(original) } as unknown as AgentControlRequest
        handleControlRequest('a1', request, 'live', s)
        expect(new TextDecoder().decode(s.controlStore.getRequests('a1')[0]?.originalPayload)).toBe(original)
        dispose()
      })
    })

    function req(agentId: string): AgentControlRequest {
      return { requestId: 'r1', agentId, payload: enc(JSON.stringify({ method: 'item/commandExecution/requestApproval' })) } as unknown as AgentControlRequest
    }

    // The provider waits for the control answer. Retain an unreadable request and its bytes so the
    // user can inspect it and stop the agent.

    it.each([
      ['malformed', '{"method":'],
      ['not-an-object', '[1,2]'],
      ['not-an-object', '"a string"'],
      ['not-an-object', 'null'],
    ])('keeps a request whose payload is %s (%s)', (fault, bytes) => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        const request = { requestId: 'r1', agentId: 'a1', payload: enc(bytes) } as unknown as AgentControlRequest
        handleControlRequest('a1', request, 'live', s)
        const [kept] = s.controlStore.getRequests('a1')
        expect(kept).toBeDefined()
        expect(kept?.payloadFault).toBe(fault)
        // Keep the decoded payload empty instead of inventing a provider shape.
        expect(kept?.payload).toEqual({})
        // The bytes that arrived are still there, so Copy JSON shows what the agent sent.
        expect(new TextDecoder().decode(kept?.originalPayload)).toBe(bytes)
        dispose()
      })
    })

    it('states no fault for a payload it could read', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        handleControlRequest('a1', req('a1'), 'live', s)
        expect(s.controlStore.getRequests('a1')[0]?.payloadFault).toBeUndefined()
        dispose()
      })
    })

    it('skips a replayed (catch-up) request for an already-INACTIVE agent', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.INACTIVE })
        handleControlRequest('a1', req('a1'), simulatePhase('catchingUp'), s)
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
        dispose()
      })
    })

    // The worker's idle transition owns the alert. The control handler retains the request and
    // sets its badge. A second alert here would notify twice.

    it('adds a live request and badges a backgrounded tab', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        s.tabs.addAgent('a2')
        s.tabs.selection.setActiveById(TabType.AGENT, 'a2')
        handleControlRequest('a1', req('a1'), 'live', s)
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(s.tabs.view.getAgentTab('a1')?.hasNotification).toBe(true)
        dispose()
      })
    })

    it('does not badge a tile-active agent on control request when another tab is workspace-active', () => {
      createRoot((dispose) => {
        const s = argStores()
        const secondTile = s.tabs.layoutStore.splitTile(s.tabs.rootTileId, 'horizontal')!
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        s.tabs.addAgent('a2', {}, { tileId: secondTile, activate: false })
        s.tabs.selection.setActiveById(TabType.AGENT, 'a2')
        s.tabs.selection.setActiveById(TabType.AGENT, 'a1')
        handleControlRequest('a2', req('a2'), 'live', s)
        expect(s.controlStore.getRequests('a2')).toHaveLength(1)
        expect(s.tabs.view.getAgentTab('a2')?.hasNotification).not.toBe(true)
        dispose()
      })
    })

    it('adds a catch-up request for an ACTIVE agent but does NOT badge it', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        handleControlRequest('a1', req('a1'), simulatePhase('catchingUp'), s)
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(s.tabs.view.getAgentTab('a1')?.hasNotification).not.toBe(true)
        dispose()
      })
    })

    it('keeps a malformed JSON payload instead of throwing out of the stream handler', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        const malformed = { requestId: 'r1', agentId: 'a1', payload: enc('{not json') } as unknown as AgentControlRequest
        expect(() => handleControlRequest('a1', malformed, 'live', s)).not.toThrow()
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(s.controlStore.getRequests('a1')[0]?.payloadFault).toBe('malformed')
        dispose()
      })
    })

    it('threads the wire claim_token into the stored request so the answer can echo it back', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        const withToken = {
          requestId: 'r1',
          agentId: 'a1',
          payload: enc(JSON.stringify({ method: 'item/commandExecution/requestApproval' })),
          claimToken: 'instance-token-1',
        } as unknown as AgentControlRequest
        handleControlRequest('a1', withToken, 'live', s)
        // Preserve the claim token in the stored request. The control answer returns that token so the
        // worker can identify the exact request instance.

        expect(s.controlStore.getRequests('a1').find(r => r.requestId === 'r1')?.claimToken).toBe('instance-token-1')
        dispose()
      })
    })
  })

  describe('handleAgentStatusChange', () => {
    it('applies a status update and reports worker-online on a full snapshot', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.STARTING })
        let online: boolean | undefined
        const sc = { agentId: 'a1', status: AgentStatus.ACTIVE, workerOnline: true, optionGroups: [], startupError: '', startupMessage: '' } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), v => void (online = v), undefined)
        expect(s.tabs.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
        expect(online).toBe(true)
        dispose()
      })
    })

    // A pending ListAgents request compares TabMetadataStore.liveStatusEpoch before and after the call.
    // An event with status must advance that count. An event without status must retain it.
    // Otherwise, the older reply could discard the status that no live event replaced.
    it('counts an event that carries a status, and only that', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.STARTING })
        const statusEvent = { agentId: 'a1', status: AgentStatus.ACTIVE, workerOnline: true, optionGroups: [], startupError: '', startupMessage: '' } as unknown as AgentStatusChange
        const gitOnly = { agentId: 'a1', status: AgentStatus.UNSPECIFIED, workerOnline: false, optionGroups: [], gitStatus: { toplevel: '/repo', branch: 'main', originUrl: '', isWorktree: false } } as unknown as AgentStatusChange

        expect(s.metadata.liveStatusEpoch('a1')).toBe(0)
        handleAgentStatusChange('a1', gitOnly, 'live', s, createLoadingSignal(), () => {}, undefined)
        expect(s.metadata.liveStatusEpoch('a1'), 'a git-only push').toBe(0)
        handleAgentStatusChange('a1', statusEvent, 'live', s, createLoadingSignal(), () => {}, undefined)
        expect(s.metadata.liveStatusEpoch('a1'), 'a status event').toBe(1)
        handleAgentStatusChange('a1', statusEvent, 'live', s, createLoadingSignal(), () => {}, undefined)
        expect(s.metadata.liveStatusEpoch('a1'), 'the same status again').toBe(2)
        dispose()
      })
    })

    it('counts a catch-up status marker too', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.STARTING })
        const marker = { agentId: 'a1', status: AgentStatus.ACTIVE, workerOnline: true, optionGroups: [], startupError: '', startupMessage: '' } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', marker, 'catchingUp', s, createLoadingSignal(), () => {}, undefined)
        expect(s.metadata.liveStatusEpoch('a1')).toBe(1)
        dispose()
      })
    })

    it('skips a payload-less sentinel without touching the tab or reporting worker-online', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        let online: boolean | undefined
        const sc = { agentId: 'a1', status: AgentStatus.UNSPECIFIED, workerOnline: false, optionGroups: [] } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), v => void (online = v), undefined)
        // An event without an explicit status retains the current status and sends no connectivity report.
        expect(s.tabs.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
        expect(online).toBeUndefined()
        dispose()
      })
    })

    it('clears pending control requests when the agent goes INACTIVE', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        s.controlStore.addRequest('a1', { requestId: 'r1', agentId: 'a1', payload: { method: 'x' }, claimToken: 'tok-r1' })
        const sc = { agentId: 'a1', status: AgentStatus.INACTIVE, workerOnline: true, optionGroups: [], startupError: '', startupMessage: '' } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), () => {}, undefined)
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
        dispose()
      })
    })

    it('applies the same status fields with and without a loaded chat window', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('with-chat', { agentStatus: AgentStatus.STARTING })
        s.tabs.addAgent('no-chat', { agentStatus: AgentStatus.STARTING })
        s.chatStore.setMessages('with-chat', [{
          id: 'm1',
          seq: 1n,
          source: MessageSource.USER,
          content: new Uint8Array(),
          createdAt: 0n,
          agentProvider: AgentProvider.CLAUDE_CODE,
        } as never])

        for (const agentId of ['with-chat', 'no-chat'] as const) {
          const sc = {
            agentId,
            status: AgentStatus.ACTIVE,
            workerOnline: true,
            optionGroups: [],
            startupError: '',
            startupMessage: '',
          } as unknown as AgentStatusChange
          handleAgentStatusChange(agentId, sc, 'live', s, createLoadingSignal(), () => {}, undefined)
        }

        expect(s.tabs.view.getAgentTab('with-chat')?.agentStatus).toBe(AgentStatus.ACTIVE)
        expect(s.tabs.view.getAgentTab('no-chat')?.agentStatus).toBe(AgentStatus.ACTIVE)
        dispose()
      })
    })
  })
})

describe('wireSessionInfoToUpdates', () => {
  it('returns an empty object for undefined or empty payloads', () => {
    expect(wireSessionInfoToUpdates(undefined)).toEqual({})
    expect(wireSessionInfoToUpdates({})).toEqual({})
  })

  it('maps snake_case wire keys to the camelCase store shape', () => {
    const updates = wireSessionInfoToUpdates({
      total_cost_usd: 1.5,
      context_usage: { input_tokens: 100 },
    })
    expect(updates.totalCostUsd).toBe(1.5)
    expect(updates.contextUsage).toMatchObject({ inputTokens: 100 })
  })

  it('deep-maps rate_limits tiers', () => {
    const update = wireRateLimitUpdateFromSessionInfo({
      rate_limits: { mode: 'merge', values: { five_hour: { status: 'allowed', utilization: 0.5 } } },
    })
    expect(update).toEqual({
      mode: 'merge',
      values: { five_hour: { status: 'allowed', utilization: 0.5 } },
    })
  })

  it('rejects a rate-limit update without a valid operation and values map', () => {
    expect(wireRateLimitUpdateFromSessionInfo(undefined)).toBeUndefined()
    expect(wireRateLimitUpdateFromSessionInfo({ rate_limits: [] })).toBeUndefined()
    expect(wireRateLimitUpdateFromSessionInfo({
      rate_limits: { mode: 'append', values: { five_hour: { status: 'allowed' } } },
    })).toBeUndefined()
    expect(wireRateLimitUpdateFromSessionInfo({
      rate_limits: { mode: 'merge', values: [] },
    })).toBeUndefined()
  })

  // Check all eight tier fields so an omitted translation fails before a rate-limit popover displays an empty cell.
  it('translates every rate_limits tier field to its camelCase name', () => {
    const update = wireRateLimitUpdateFromSessionInfo({
      rate_limits: {
        mode: 'replace',
        values: {
          five_hour: {
            rate_limit_type: 'five_hour',
            status: 'allowed_warning',
            utilization: 0.87,
            resets_at: 1_700_000_000,
            surpassed_threshold: 0.8,
            overage_status: 'allowed',
            overage_resets_at: 1_700_003_600,
            is_using_overage: true,
          },
        },
      },
    })
    expect(update).toEqual({
      mode: 'replace',
      values: {
        five_hour: {
          rateLimitType: 'five_hour',
          status: 'allowed_warning',
          utilization: 0.87,
          resetsAt: 1_700_000_000,
          surpassedThreshold: 0.8,
          overageStatus: 'allowed',
          overageResetsAt: 1_700_003_600,
          isUsingOverage: true,
        },
      },
    })
  })

  /**
   * Omit absent rate-limit fields. shallowEqual compares key counts, while toEqual ignores
   * undefined-valued keys. An explicit undefined field would therefore cause another store write
   * after serialization. Assert Object.keys also.
   */
  it('omits a tier field the payload does not carry, rather than setting it undefined', () => {
    const update = wireRateLimitUpdateFromSessionInfo({
      rate_limits: { mode: 'merge', values: { five_hour: { status: 'allowed', is_using_overage: false } } },
    })
    const tier = update?.values.five_hour as Record<string, unknown> | undefined
    if (tier === undefined)
      throw new Error('expected the five_hour tier to be present')
    expect(Object.keys(tier).sort()).toEqual(['isUsingOverage', 'status'])
    // Preserve false. Omit only an absent or invalid field.
    expect(tier.isUsingOverage).toBe(false)

    const sparse = wireRateLimitUpdateFromSessionInfo({
      rate_limits: { mode: 'merge', values: { five_hour: { status: 'allowed' } } },
    })
    const sparseTier = sparse?.values.five_hour
    if (sparseTier === undefined)
      throw new Error('expected the five_hour tier to be present')
    expect(Object.keys(sparseTier)).toEqual(['status'])
  })

  it('drops a tier field whose wire value is the wrong type', () => {
    const update = wireRateLimitUpdateFromSessionInfo({
      rate_limits: {
        mode: 'merge',
        values: {
          five_hour: {
            status: 'allowed',
            utilization: '0.5',
            resets_at: '1700000000',
            is_using_overage: 'true',
            surpassed_threshold: null,
          },
        },
      },
    })
    expect(Object.keys(update?.values.five_hour ?? {})).toEqual(['status'])
  })

  it('skips a tier that is not an object at all', () => {
    const update = wireRateLimitUpdateFromSessionInfo({
      rate_limits: {
        mode: 'merge',
        values: { five_hour: { status: 'allowed' }, weekly: 'nonsense', monthly: null },
      },
    })
    expect(update?.values).toEqual({ five_hour: { status: 'allowed' } })
  })

  it('skips keys that are absent or fail their type guard', () => {
    // Kiro reports the context percentage through session info without a token count.
    expect(wireSessionInfoToUpdates({ total_cost_usd: 'free', context_usage: {} })).toEqual({})
  })

  // Kiro reports context percentage without a token count through this session-info path.
  // That update arrives independently of a transcript message.
  it('maps a context_usage that states the fill alone', () => {
    expect(wireSessionInfoToUpdates({ context_usage: { usage_percent: 42.5 } })).toEqual({
      contextUsage: { inputTokens: 0, cacheCreationInputTokens: 0, cacheReadInputTokens: 0, usagePercent: 42.5 },
    })
  })

  it('maps a stated zero fill, and skips a fill that is no reading', () => {
    expect(wireSessionInfoToUpdates({ context_usage: { usage_percent: 0 } }).contextUsage).toMatchObject({ usagePercent: 0 })
    expect(wireSessionInfoToUpdates({ context_usage: { usage_percent: -3 } })).toEqual({})
    expect(wireSessionInfoToUpdates({ context_usage: { usage_percent: '42' } })).toEqual({})
  })
})

/**
 * The offline sweep reads tabs from every account workspace. Filter both tab types by worker
 * ID so a healthy worker retains its agents and terminals.
 */
describe('collectWorkerOfflineTargets', () => {
  // An explicit undefined override clears a default. An absent override retains it.
  // The partial type for each tab variant retains its fields, including terminal status.
  const tab = (over: Partial<AgentTab> | Partial<TerminalTab> | Partial<FileTab>): Tab => ({
    type: TabType.AGENT,
    id: 'a1',
    workspaceId: 'ws-1',
    workerId: 'w1',
    ...over,
  } as Tab)

  it('leaves agents on OTHER workers alone', () => {
    const { agents } = collectWorkerOfflineTargets([
      tab({ id: 'mine', workerId: 'w1' }),
      tab({ id: 'other-worker', workerId: 'w2' }),
      tab({ id: 'other-ws', workspaceId: 'ws-2', workerId: 'w2' }),
    ], 'w1')

    expect(agents.map(a => a.id), 'only the offline worker loses its stream').toEqual(['mine'])
  })

  it('leaves terminals on OTHER workers alone', () => {
    const { terminals } = collectWorkerOfflineTargets([
      tab({ type: TabType.TERMINAL, id: 'mine', workerId: 'w1', status: TerminalStatus.READY }),
      tab({ type: TabType.TERMINAL, id: 'theirs', workerId: 'w2', status: TerminalStatus.READY }),
    ], 'w1')

    expect([...terminals]).toEqual(['mine'])
  })

  it('only marks READY terminals', () => {
    const { terminals } = collectWorkerOfflineTargets([
      tab({ type: TabType.TERMINAL, id: 'ready', status: TerminalStatus.READY }),
      tab({ type: TabType.TERMINAL, id: 'already-gone', status: TerminalStatus.DISCONNECTED }),
      tab({ type: TabType.TERMINAL, id: 'exited', status: TerminalStatus.EXITED }),
    ], 'w1')

    expect([...terminals], 'a terminal already down has nothing to lose').toEqual(['ready'])
  })

  it('ignores tabs with no worker at all', () => {
    const { terminals, agents } = collectWorkerOfflineTargets([
      tab({ id: 'unhosted', workerId: undefined }),
      tab({ type: TabType.FILE, id: 'file', workerId: 'w1' }),
    ], 'w1')

    expect(agents).toEqual([])
    expect(terminals.size, 'a FILE tab is neither branch').toBe(0)
  })

  /**
   * A quake terminal has no placed tab. Add detached terminals to the offline sweep so they do
   * not retain READY during an outage.
   */
  it('marks a terminal that has no tile, which is the quake panel\'s shape', () => {
    const { terminals } = collectWorkerOfflineTargets([
      tab({ type: TabType.TERMINAL, id: 'companion', tileId: undefined, status: TerminalStatus.READY }),
    ], 'w1')

    expect([...terminals]).toEqual(['companion'])
  })

  it('returns nothing for a worker that hosts none of these tabs', () => {
    const { terminals, agents } = collectWorkerOfflineTargets([tab({ workerId: 'w1' })], 'w-unknown')
    expect(agents).toEqual([])
    expect(terminals.size).toBe(0)
  })
})

/**
 * A lost connection can omit the events that clear live indicators. Clear those indicators
 * locally so they do not remain throughout the outage.
 */
describe('clearOfflineAgentState', () => {
  function seededStores() {
    const chatStore = createChatStore()
    const agentSessionStore = createAgentSessionStore()
    const agentActivityStore = createAgentActivityStore()
    chatStore.applyToolProgress('a1', { ...TOOL_A, elapsedSeconds: 30 })
    chatStore.applyToolProgress('a1', { ...TOOL_B, elapsedSeconds: 90 })
    agentSessionStore.applyProgress('a1', { revision: 1, thinkingTokens: 500, output: { bytes: 4096, minimum: true } })
    agentActivityStore.apply('a1', AgentActivityState.WORKING)
    return { chatStore, agentSessionStore, agentActivityStore }
  }

  it('drops every live indicator the outage would otherwise freeze', () => {
    createRoot((dispose) => {
      const s = seededStores()
      clearOfflineAgentState('a1', s)

      // These two tool badges must disappear during the outage.
      // Otherwise, their old duration labels remain for the entire outage.
      expect(s.chatStore.getToolProgress('a1', TOOL_A)).toBeUndefined()
      expect(s.chatStore.getToolProgress('a1', TOOL_B)).toBeUndefined()
      expect(s.agentSessionStore.getProgress('a1').thinkingTokens).toBeUndefined()
      expect(s.agentSessionStore.getProgress('a1').output).toBeUndefined()
      // The disconnected worker cannot report a settle. Remove the stored activity level.
      // The view then removes its spinner and unavailable Interrupt control.

      expect(s.agentActivityStore.isBusy('a1')).toBe(false)
      dispose()
    })
  })

  it('leaves another agent on a healthy worker untouched', () => {
    createRoot((dispose) => {
      const s = seededStores()
      s.chatStore.applyToolProgress('a2', { ...TOOL_A, elapsedSeconds: 60 })
      s.agentSessionStore.applyProgress('a2', { revision: 1, thinkingTokens: 700 })
      s.agentActivityStore.apply('a2', AgentActivityState.WORKING)

      clearOfflineAgentState('a1', s)

      expect(s.chatStore.getToolProgress('a2', TOOL_A)?.elapsedSeconds).toBe(60)
      expect(s.agentSessionStore.getProgress('a2').thinkingTokens).toBe(700)
      expect(s.agentActivityStore.isBusy('a2')).toBe(true)
      dispose()
    })
  })

  it('is safe for an agent that has nothing live', () => {
    createRoot((dispose) => {
      const s = {
        chatStore: createChatStore(),
        agentSessionStore: createAgentSessionStore(),
        agentActivityStore: createAgentActivityStore(),
      }
      expect(() => clearOfflineAgentState('never-ran', s)).not.toThrow()
      dispose()
    })
  })
})

/**
 * Suppress history-load toasts for a lost connection. Report a worker RPC refusal. Exercise
 * the real hook and actual Toast rule.
 */
describe('useWorkspaceConnection chat history load', () => {
  /**
   * Mount one active agent whose initial history load is incomplete. The hook's lazy-load effect
   * then requests its history.
   */
  function mountWithActiveAgent(listAgentMessagesRejectsWith: unknown) {
    vi.mocked(workerRpc.listAgentMessages).mockRejectedValue(listAgentMessagesRejectsWith)
    const tabs = makeTabStores()
    tabs.addAgent('a1', { workerId: 'w1' })
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      useWorkspaceConnection({
        chatStore: createChatStore(),
        agentInputQueueStore: createAgentInputQueueStore(),
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        controlStore: createControlStore(),
        quakeStore: createTestQuakeStore(),
        getActiveQuakeKeyId: () => null,
        agentSessionStore: createAgentSessionStore(),
        agentActivityStore: createAgentActivityStore(),
        repoGitStore: createRepoGitStore(),
        settingsLoading: createLoadingSignal(),
        getActiveWorkspaceId: () => WS,
      })
    })
    return dispose
  }

  /** Let the load's promise chain settle. */
  async function settle() {
    for (let i = 0; i < 20; i++)
      await Promise.resolve()
  }

  it('stays silent when a dropped link is what failed the load', async () => {
    mockShowWarnToast.mockClear()
    const dispose = mountWithActiveAgent(channelNotOpenError())
    try {
      await settle()
      expect(workerRpc.listAgentMessages, 'the load has to have been attempted').toHaveBeenCalled()
      expect(mockShowWarnToast).not.toHaveBeenCalled()
    }
    finally {
      dispose()
    }
  })

  it('stays silent when the drained channel is what failed the load', async () => {
    mockShowWarnToast.mockClear()
    const dispose = mountWithActiveAgent(new ChannelError('transport', 'channel disconnected'))
    try {
      await settle()
      expect(mockShowWarnToast).not.toHaveBeenCalled()
    }
    finally {
      dispose()
    }
  })

  it('still announces a failure the worker itself reported', async () => {
    mockShowWarnToast.mockClear()
    const refusal = new ChannelError('rpc', 'agent not found', { code: 5 })
    const dispose = mountWithActiveAgent(refusal)
    try {
      await settle()
      expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to load chat history', refusal)
      expect(document.querySelector('.toast-message')).toHaveTextContent('agent not found')
    }
    finally {
      dispose()
    }
  })

  it('announces a failed reconcile-driven re-anchor instead of leaving it unhandled', async () => {
    // The loaded window remains behind the live tail by more than the catch-up limit.
    // The reconcile effect calls jumpToLatestMessages, whose LATEST request rejects.
    // The caller must catch the rejection and show the same toast.
    // Otherwise, each later recovery attempt produces an unhandled rejection.
    mockShowWarnToast.mockClear()
    const refusal = new ChannelError('rpc', 'agent not found', { code: 5 })
    vi.mocked(workerRpc.listAgentMessages).mockRejectedValue(refusal)
    const tabs = makeTabStores()
    tabs.addAgent('a1', { workerId: 'w1' })
    let dispose!: () => void
    createRoot((d) => {
      dispose = d
      const chatStore = createChatStore()
      chatStore.setMessages('a1', [{ seq: 10n, id: 'm10', source: MessageSource.AGENT } as AgentChatMessage])
      chatStore.liveTail.bump('a1', 10n + CATCH_UP_GAP_LIMIT + 1n)
      useWorkspaceConnection({
        chatStore,
        agentInputQueueStore: createAgentInputQueueStore(),
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        controlStore: createControlStore(),
        quakeStore: createTestQuakeStore(),
        getActiveQuakeKeyId: () => null,
        agentSessionStore: createAgentSessionStore(),
        agentActivityStore: createAgentActivityStore(),
        repoGitStore: createRepoGitStore(),
        settingsLoading: createLoadingSignal(),
        getActiveWorkspaceId: () => WS,
      })
    })
    try {
      await settle()
      expect(workerRpc.listAgentMessages, 'the re-anchor has to have been attempted').toHaveBeenCalled()
      expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to load chat history', refusal)
    }
    finally {
      dispose()
    }
  })
})

describe('useWorkspaceConnection replay ownership', () => {
  let dispose: (() => void) | undefined
  let restoreMarks: (() => void) | undefined

  beforeEach(() => {
    vi.useFakeTimers()
    vi.spyOn(Math, 'random').mockReturnValue(0.5)
    vi.mocked(workerRpc.watchEventsViaChannel).mockReset()
    vi.mocked(workerRpc.listAgentMessages).mockResolvedValue(create(ListAgentMessagesResponseSchema))
    restoreMarks = vi.spyOn(workerRpc, 'listMessageMarks').mockResolvedValue(create(ListMessageMarksResponseSchema, { minSeq: 0n, maxSeq: 0n })).mockRestore
  })

  afterEach(() => {
    dispose?.()
    dispose = undefined
    restoreMarks?.()
    vi.mocked(workerRpc.watchEventsViaChannel).mockReset()
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  async function flushStream() {
    for (let i = 0; i < 10; i++)
      await Promise.resolve()
  }

  function mountReplay(initial: {
    cost?: number
    seq?: bigint
    status?: AgentStatus
    agentId?: string
    parentAgentId?: string
    rootAgentId?: string
    hydrated?: boolean
    workerId?: string
    relatedAgents?: Array<{ id: string, parentAgentId?: string, rootAgentId?: string, workerId?: string, visible?: boolean, hydrated?: boolean }>
  } = {}) {
    const agentId = initial.agentId ?? 'a1'
    const handles: Array<{ workerId: string, handle: workerRpc.WatchEventsHandle, emit: (event: AgentEvent) => void, end: () => void, requestId: () => bigint }> = []
    const transmitted: WatchEventsRequest[] = []
    const requests: Array<{ workerId: string, request: WatchEventsRequest }> = []
    const ownershipAtTransport: boolean[] = []
    const settled = vi.fn<(agentId: string, numToolUses?: number) => void>()
    let messageId = 0
    const stores = createRoot((close) => {
      dispose = close
      const tabs = makeTabStores()
      tabs.addAgent(agentId, { workerId: initial.workerId ?? 'w1', agentStatus: initial.status ?? AgentStatus.ACTIVE, hydrated: initial.hydrated ?? initial.rootAgentId !== undefined, ...(initial.parentAgentId === undefined ? {} : { parentAgentId: initial.parentAgentId }), ...(initial.rootAgentId === undefined ? {} : { rootAgentId: initial.rootAgentId }) })
      const primaryTile = () => {
        const tileId = tabs.view.getAgentTab(agentId)?.tileId
        if (!tileId)
          throw new Error('The primary agent requires its current placed leaf.')
        return tileId
      }
      for (const related of initial.relatedAgents ?? []) {
        const tileId = related.visible ? tabs.layoutStore.splitTile(primaryTile(), 'horizontal') : primaryTile()
        if (!tileId)
          throw new Error('A visible related agent requires its own tile.')
        tabs.addAgent(related.id, { workerId: related.workerId ?? 'w1', agentStatus: AgentStatus.ACTIVE, hydrated: related.hydrated ?? related.rootAgentId !== undefined, ...(related.parentAgentId === undefined ? {} : { parentAgentId: related.parentAgentId }), ...(related.rootAgentId === undefined ? {} : { rootAgentId: related.rootAgentId }) }, { tileId, activate: related.visible ?? false })
      }
      tabs.selection.setActiveById(TabType.AGENT, agentId)
      const agentSessionStore = createAgentSessionStore()
      const chatStore = createChatStore()
      chatStore.setMessages(agentId, initial.seq === undefined ? [] : [create(AgentChatMessageSchema, { id: 'initial', seq: initial.seq, source: MessageSource.USER })])
      if (initial.cost !== undefined)
        agentSessionStore.updateInfo(agentId, { totalCostUsd: initial.cost })
      const agentActivityStore = createAgentActivityStore()
      const controlStore = createControlStore()
      const agentInputQueueStore = createAgentInputQueueStore()
      vi.mocked(workerRpc.watchEventsViaChannel).mockImplementation(async (workerId, request) => {
        const opening = fromBinary(WatchEventsRequestSchema, toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, request)))
        transmitted.push(opening)
        requests.push({ workerId, request: opening })
        let requestId = request.updateId ?? 0n
        ownershipAtTransport.push(agentSessionStore.acceptsReplay(agentId, requestId))
        let listener: ((response: WatchEventsResponse) => void) | undefined
        let ended: (() => void) | undefined
        const handle: workerRpc.WatchEventsHandle = {
          update: vi.fn((next) => {
            requestId = next.updateId ?? 0n
            const update = fromBinary(WatchEventsRequestSchema, toBinary(WatchEventsRequestSchema, create(WatchEventsRequestSchema, next)))
            transmitted.push(update)
            requests.push({ workerId, request: update })
          }),
          close: vi.fn(),
          onEvent: vi.fn((callback) => { listener = callback }),
          onEnd: (callback) => { ended = callback },
          onError: vi.fn(),
        }
        handles.push({
          workerId,
          handle,
          emit: event => listener?.(create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: event } })),
          end: () => ended?.(),
          requestId: () => requestId,
        })
        return handle
      })
      useWorkspaceConnection({
        chatStore,
        agentInputQueueStore,
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        controlStore,
        quakeStore: createTestQuakeStore(),
        getActiveQuakeKeyId: () => null,
        agentSessionStore,
        agentActivityStore,
        repoGitStore: createRepoGitStore(),
        settingsLoading: createLoadingSignal(),
        getActiveWorkspaceId: () => WS,
        onAgentSettled: settled,
      })
      return {
        ...tabs,
        addAgent(id: string, meta: Record<string, unknown> = {}, opts: { tileId?: string, activate?: boolean } = {}) {
          tabs.addAgent(id, meta, { ...opts, tileId: opts.tileId ?? primaryTile() })
        },
        agentId,
        agentSessionStore,
        agentActivityStore,
        agentInputQueueStore,
        chatStore,
        controlStore,
      }
    })
    const emitMessage = (content: unknown, options: { seq?: bigint, replay?: boolean, replayId?: bigint, transcriptOnly?: boolean, origin?: string } = {}) => {
      const current = handles.at(-1)
      if (!current)
        throw new Error('The test requires an open watch stream.')
      const replayId = options.replay
        ? options.replayId ?? transmitted.at(-1)?.agents.find(entry => entry.agentId === agentId)?.replayId
        : 0n
      if (replayId === undefined)
        throw new Error('A replay frame requires its actual transmitted identity.')
      messageId++
      const frame = receivedEvent({
        agentId,
        replay: options.replay ?? false,
        replayId,
        event: { case: 'agentMessage', value: create(AgentChatMessageSchema, {
          id: `wire-message-${messageId}`,
          seq: options.seq ?? BigInt(messageId),
          source: MessageSource.AGENT,
          agentProvider: AgentProvider.CLAUDE_CODE,
          content: new TextEncoder().encode(JSON.stringify(content)),
          contentCompression: ContentCompression.NONE,
          transcriptOnly: options.transcriptOnly ?? false,
        }) },
      }, options.origin ?? (options.replay ? agentId : ''))
      current.emit(frame)
      if (frame.event.case !== 'agentMessage')
        throw new Error('The fixture requires a message frame.')
      return frame.event.value
    }
    return { ...stores, handles, transmitted, requests, ownershipAtTransport, emitMessage, settled }
  }

  function receivedEvent(frame: MessageInitShape<typeof AgentEventSchema>, origin = frame.replay ? frame.agentId ?? '' : '') {
    return create(AgentEventSchema, { ...frame, replayAgentId: origin })
  }

  function emitActivity(state: ReturnType<typeof mountReplay>, level: AgentActivityState, replay = false, numToolUses?: number) {
    const current = state.handles.at(-1)
    const entry = state.transmitted.at(-1)?.agents.find(candidate => candidate.agentId === state.agentId)
    if (!current || !entry)
      throw new Error('The activity frame requires its actual originating watch entry.')
    current.emit(receivedEvent({
      agentId: state.agentId,
      replay,
      replayId: replay ? entry.replayId : 0n,
      event: replay
        ? { case: 'catchUpStart', value: { latestSeq: 0n, activityState: level } }
        : { case: 'activityChanged', value: { state: level, ...(numToolUses === undefined ? {} : { numToolUses }) } },
    }))
  }

  it('keeps live working activity when an older idle baseline replays', async () => {
    const state = mountReplay()
    await flushStream()
    emitActivity(state, AgentActivityState.WORKING)
    emitActivity(state, AgentActivityState.IDLE, true)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(true)
    expect(state.settled).not.toHaveBeenCalled()
    emitActivity(state, AgentActivityState.IDLE, false, 3)
    emitActivity(state, AgentActivityState.IDLE, false, 3)
    expect(state.settled).toHaveBeenCalledExactlyOnceWith(state.agentId, 3)
  })

  it('keeps live idle activity and one alert when an older working baseline replays', async () => {
    const state = mountReplay()
    await flushStream()
    emitActivity(state, AgentActivityState.WORKING)
    emitActivity(state, AgentActivityState.IDLE, false, 3)
    emitActivity(state, AgentActivityState.WORKING, true)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(false)
    emitActivity(state, AgentActivityState.IDLE, false, 3)
    expect(state.settled).toHaveBeenCalledExactlyOnceWith(state.agentId, 3)
  })

  it.each([
    { level: 'working', current: AgentActivityState.WORKING, replayed: AgentActivityState.IDLE, busy: true },
    { level: 'idle', current: AgentActivityState.IDLE, replayed: AgentActivityState.WORKING, busy: false },
  ])('keeps a repeated live $level level ahead of an older replay baseline', async ({ current, replayed, busy }) => {
    const state = mountReplay()
    await flushStream()
    state.agentActivityStore.seedPublished(state.agentId, current)
    emitActivity(state, current)
    emitActivity(state, replayed, true)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(busy)
    expect(state.settled).not.toHaveBeenCalled()
  })

  it.each([
    { level: 'working', before: AgentActivityState.IDLE, replayed: AgentActivityState.WORKING, busy: true },
    { level: 'idle', before: AgentActivityState.WORKING, replayed: AgentActivityState.IDLE, busy: false },
  ])('restores an unclaimed cold $level baseline without an alert', async ({ before, replayed, busy }) => {
    const state = mountReplay()
    await flushStream()
    state.agentActivityStore.seedPublished(state.agentId, before)
    emitActivity(state, replayed, true)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(busy)
    expect(state.settled).not.toHaveBeenCalled()
  })

  it.each([
    { topology: 'child-only', topic: 'goal' },
    { topology: 'child-only', topic: 'tasks' },
    { topology: 'root-and-child', topic: 'goal' },
    { topology: 'root-and-child', topic: 'tasks' },
    { topology: 'nested-child', topic: 'goal' },
    { topology: 'nested-child', topic: 'tasks' },
  ] as const)('restores the root $topic from the actual $topology replay lifetime', async ({ topology, topic }) => {
    const nested = topology === 'nested-child'
    const state = mountReplay({
      agentId: 'child-1',
      parentAgentId: nested ? 'middle-1' : 'root-1',
      ...(nested
        ? { relatedAgents: [{ id: 'root-1' }, { id: 'middle-1', parentAgentId: 'root-1' }] }
        : { rootAgentId: 'root-1' }),
    })
    await flushStream()
    const childEntry = state.transmitted[0]?.agents.find(entry => entry.agentId === 'child-1')
    const rootEntry = state.transmitted[0]?.agents.find(entry => entry.agentId === 'root-1')
    if (!childEntry || !rootEntry)
      throw new Error('The actual child watch must include its root notification entry.')
    expect(childEntry.mode).toBe(WatchMode.FULL)
    expect(rootEntry.mode).toBe(WatchMode.NOTIFY)
    expect(rootEntry.replayId).toBe(0n)
    expect(state.agentSessionStore.acceptsReplay('child-1', childEntry.replayId)).toBe(true)
    if (nested) {
      expect(state.view.getAgentTab('child-1')?.rootAgentId).toBeUndefined()
      expect(state.view.getAgentTab('child-1')?.parentAgentId).toBe('middle-1')
      expect(state.view.getAgentTab('middle-1')?.parentAgentId).toBe('root-1')
      expect(state.view.getAgentTab('root-1')?.parentAgentId).toBeUndefined()
    }
    if (topology === 'root-and-child') {
      const secondTile = state.layoutStore.splitTile(state.rootTileId, 'horizontal')
      if (!secondTile)
        throw new Error('The root and child require separate visible tiles.')
      state.addAgent('root-1', { workerId: 'w1', agentStatus: AgentStatus.ACTIVE }, { tileId: secondTile })
      await flushStream()
      const currentRoot = state.transmitted.at(-1)?.agents.find(entry => entry.agentId === 'root-1')
      expect(currentRoot?.mode).toBe(WatchMode.FULL)
      expect(currentRoot?.replayId).not.toBe(childEntry.replayId)
    }
    const projection = topic === 'goal'
      ? receivedEvent({
          agentId: 'root-1',
          replay: true,
          replayId: childEntry.replayId,
          event: { case: 'goalChanged', value: {
            agentId: 'root-1',
            goal: create(AgentGoalSchema, { nativeId: 'replayed-root-goal', objective: 'Restore the root goal', createdAt: '2026-10-09T00:00:00.000Z', status: AgentGoalStatus.ACTIVE }),
            supportedActions: [AgentGoalAction.CLEAR],
            goalUpdatedAt: '2026-10-09T00:00:01.000Z',
          } },
        }, 'child-1')
      : receivedEvent({
          agentId: 'root-1',
          replay: true,
          replayId: childEntry.replayId,
          event: { case: 'backgroundTasksChanged', value: {
            agentId: 'root-1',
            tasks: [create(BackgroundTaskItemSchema, { id: 'replayed-root-task', kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, childAgentId: 'child-1', parentAgentId: 'root-1', title: 'Restore the root task' })],
          } },
        }, 'child-1')
    state.handles[0]!.emit(projection)
    if (topic === 'goal') {
      expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('replayed-root-goal')
      expect(state.chatStore.goal.supportedActions('root-1')).toEqual(['clear'])
    }
    else {
      expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(['replayed-root-task'])
    }
  })

  function requestedEntry(state: ReturnType<typeof mountReplay>, origin = state.agentId, workerId = 'w1') {
    const entry = state.requests.findLast(record => record.workerId === workerId)?.request.agents.find(candidate => candidate.agentId === origin)
    if (!entry || entry.mode !== WatchMode.FULL || entry.replayId === 0n)
      throw new Error('The frame requires its actual current FULL watch entry.')
    return entry
  }

  function rootPublication(topic: 'goal' | 'tasks', options: {
    origin?: string
    destination?: string
    payloadAgentId?: string
    replayId?: bigint
    replay?: boolean
    value?: string
    empty?: boolean
    stamp?: string
  } = {}) {
    const destination = options.destination ?? 'root-1'
    const payloadAgentId = options.payloadAgentId ?? destination
    const value = options.value ?? 'replayed'
    const replay = options.replay ?? true
    return receivedEvent({
      agentId: destination,
      replay,
      replayId: replay ? options.replayId ?? 0n : 0n,
      event: topic === 'goal'
        ? { case: 'goalChanged', value: {
            agentId: payloadAgentId,
            ...(options.empty ? {} : { goal: create(AgentGoalSchema, { nativeId: `${value}-goal`, objective: `${value} objective`, createdAt: '2026-10-09T00:00:00.000Z', status: AgentGoalStatus.ACTIVE }) }),
            supportedActions: options.empty ? [AgentGoalAction.SET] : [value === 'live' ? AgentGoalAction.PAUSE : AgentGoalAction.CLEAR],
            goalUpdatedAt: options.stamp ?? '2026-10-09T00:00:03.000Z',
          } }
        : { case: 'backgroundTasksChanged', value: {
            agentId: payloadAgentId,
            tasks: options.empty ? [] : [create(BackgroundTaskItemSchema, { id: `${value}-task`, title: `${value} task`, kind: BackgroundTaskKind.SUBAGENT, status: BackgroundTaskStatus.RUNNING, childAgentId: 'child-1', parentAgentId: destination })],
          } },
    }, replay ? options.origin ?? 'child-1' : '')
  }

  function rootState(state: ReturnType<typeof mountReplay>, topic: 'goal' | 'tasks', destination = 'root-1') {
    return topic === 'goal'
      ? { goal: state.chatStore.goal.get(destination), actions: state.chatStore.goal.supportedActions(destination) }
      : state.chatStore.backgroundTasks.get(destination)
  }

  it('retains the explicit origin through the generated response wrapper', () => {
    const frame = receivedEvent({ agentId: 'root-1', replay: true, replayId: 1n }, 'child-1')
    const response = create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: frame } })
    expect(response.event.value).toBe(frame)
    expect(Reflect.get(response.event.value!, 'replayAgentId')).toBe('child-1')
  })

  it.each(['goal', 'tasks'] as const)('uses exact sibling origins when their FULL %s lifetimes share one numeric ID', async (topic) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1', relatedAgents: [{ id: 'child-2', parentAgentId: 'root-1', rootAgentId: 'root-1', visible: true }] })
    await flushStream()
    const first = requestedEntry(state, 'child-1')
    const sibling = requestedEntry(state, 'child-2')
    expect(first.replayId).toBe(sibling.replayId)
    state.handles[0]!.emit(rootPublication(topic, { origin: 'child-1', replayId: first.replayId }))
    expect(rootState(state, topic)).toMatchObject(topic === 'goal' ? { goal: { nativeId: 'replayed-goal' }, actions: ['clear'] } : [{ rowKey: 'replayed-task' }])
    state.handles[0]!.emit(rootPublication(topic, { origin: 'child-2', replayId: sibling.replayId, value: 'sibling' }))
    expect(rootState(state, topic)).toMatchObject(topic === 'goal' ? { goal: { nativeId: 'sibling-goal' }, actions: ['clear'] } : [{ rowKey: 'sibling-task' }])
  })

  it.each(['goal', 'tasks'] as const)('refuses every invalid origin and destination for root %s snapshots', async (topic) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1', relatedAgents: [{ id: 'root-1' }, { id: 'unrelated-root' }, { id: 'child-2', parentAgentId: 'root-1', rootAgentId: 'root-1' }, { id: 'foreign-root', workerId: 'w2', visible: true }] })
    await flushStream()
    const entry = requestedEntry(state)
    const frames = [
      { origin: '' },
      { origin: 'unsent-child' },
      { origin: 'child-2' },
      { replayId: 0n },
      { replayId: entry.replayId + 1n },
      { replayId: 9007199254740993n },
      { replayId: (1n << 64n) - 1n },
      { destination: 'unrelated-root' },
      { destination: 'child-2' },
      { destination: 'foreign-root' },
      { payloadAgentId: 'unrelated-root' },
      { destination: 'child-1', payloadAgentId: 'root-1' },
    ]
    for (const invalid of frames) {
      state.handles.find(handle => handle.workerId === 'w1')!.emit(rootPublication(topic, { replayId: entry.replayId, ...invalid }))
      expect(state.chatStore.goal.get('root-1')).toBeUndefined()
      expect(state.chatStore.backgroundTasks.get('root-1')).toEqual([])
      expect(state.chatStore.goal.get('unrelated-root')).toBeUndefined()
      expect(state.chatStore.backgroundTasks.get('unrelated-root')).toEqual([])
      expect(state.chatStore.goal.get('child-2')).toBeUndefined()
      expect(state.chatStore.backgroundTasks.get('child-2')).toEqual([])
    }
    state.handles.find(handle => handle.workerId === 'w2')!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(rootState(state, topic)).toEqual(topic === 'goal' ? { goal: undefined, actions: [] } : [])
    state.handles.find(handle => handle.workerId === 'w1')!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(rootState(state, topic)).toMatchObject(topic === 'goal' ? { goal: { nativeId: 'replayed-goal' } } : [{ rowKey: 'replayed-task' }])
  })

  it.each([
    { relationship: 'missing parent', relatedAgents: [] },
    { relationship: 'incomplete parent chain', relatedAgents: [{ id: 'middle-1', parentAgentId: 'missing-root' }] },
    { relationship: 'cyclic parent chain', relatedAgents: [{ id: 'middle-1', parentAgentId: 'child-1' }] },
  ])('refuses projection with a $relationship', async ({ relatedAgents }) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'middle-1', relatedAgents })
    await flushStream()
    const entry = requestedEntry(state)
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { destination: 'middle-1', replayId: entry.replayId }))
    expect(state.chatStore.goal.get('middle-1')).toBeUndefined()
    expect(state.chatStore.backgroundTasks.get('middle-1')).toEqual([])
  })

  it('restores the authoritative root before the hydrator sets its completion marker', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'missing-parent', rootAgentId: 'root-1', hydrated: false })
    await flushStream()
    const entry = requestedEntry(state)
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(state.metadata.get('child-1')?.hydrated).toBe(false)
    expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('replayed-goal')
    expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(['replayed-task'])
  })

  it.each(['statusChange', 'controlRequest', 'controlResponseChanged', 'controlCancel', 'todosChanged', 'inputQueueChanged', 'goalChanged', 'backgroundTasksChanged'] as const)('refuses an absent or foreign payload agent for replay %s', async (kind) => {
    const state = mountReplay({ relatedAgents: [{ id: 'foreign' }] })
    await flushStream()
    const entry = requestedEntry(state)
    handleControlRequest('foreign', create(AgentControlRequestSchema, { agentId: 'foreign', requestId: 'foreign-request', claimToken: 'foreign-token', responseState: ControlResponseState.READY, payload: new TextEncoder().encode('{}') }), 'live', { ...state, getActiveWorkspaceId: () => WS })
    for (const payloadAgentId of ['', 'foreign']) {
      const events: Record<typeof kind, AgentEvent['event']> = {
        statusChange: create(AgentEventSchema, { event: { case: 'statusChange', value: { agentId: payloadAgentId, status: AgentStatus.INACTIVE } } }).event,
        controlRequest: create(AgentEventSchema, { event: { case: 'controlRequest', value: { agentId: payloadAgentId, requestId: 'foreign-request', claimToken: 'foreign-token', responseState: ControlResponseState.DELIVERED, payload: new TextEncoder().encode('{}') } } }).event,
        controlResponseChanged: create(AgentEventSchema, { event: { case: 'controlResponseChanged', value: { agentId: payloadAgentId, requestId: 'foreign-request', claimToken: 'foreign-token', responseState: ControlResponseState.DELIVERED, payload: new TextEncoder().encode('{}') } } }).event,
        controlCancel: create(AgentEventSchema, { event: { case: 'controlCancel', value: { agentId: payloadAgentId, requestId: 'foreign-request', claimToken: 'foreign-token', responseState: ControlResponseState.CANCELED } } }).event,
        todosChanged: create(AgentEventSchema, { event: { case: 'todosChanged', value: { agentId: payloadAgentId, todos: [create(TodoItemSchema, { id: 'foreign-todo', content: 'Foreign todo', status: TodoStatus.IN_PROGRESS })] } } }).event,
        inputQueueChanged: create(AgentEventSchema, { event: { case: 'inputQueueChanged', value: { snapshot: { agentId: payloadAgentId, revision: 99n } } } }).event,
        goalChanged: create(AgentEventSchema, { event: { case: 'goalChanged', value: { agentId: payloadAgentId, goal: create(AgentGoalSchema, { objective: 'Foreign goal' }), goalUpdatedAt: '2026-10-09T00:00:04.000Z' } } }).event,
        backgroundTasksChanged: create(AgentEventSchema, { event: { case: 'backgroundTasksChanged', value: { agentId: payloadAgentId, tasks: [create(BackgroundTaskItemSchema, { id: 'foreign-task', title: 'Foreign task' })] } } }).event,
      }
      state.handles[0]!.emit(receivedEvent({ agentId: 'a1', replay: true, replayId: entry.replayId, event: events[kind] }))
      expect(state.controlStore.getRequests('foreign')).toHaveLength(1)
      expect(state.controlStore.getRequests('foreign')[0]?.responseState).toBe(ControlResponseState.READY)
      expect(state.controlStore.getRequests('')).toEqual([])
      expect(state.view.getAgentTab('foreign')?.agentStatus).toBe(AgentStatus.ACTIVE)
      expect(state.chatStore.todos.get(payloadAgentId)).toEqual([])
      expect(state.chatStore.backgroundTasks.get(payloadAgentId)).toEqual([])
      expect(state.chatStore.goal.get(payloadAgentId)).toBeUndefined()
      expect(state.agentInputQueueStore.get(payloadAgentId)).toBeUndefined()
      expect(state.agentSessionStore.acceptsReplay('a1', entry.replayId)).toBe(true)
    }
  })

  it.each(['origin worker', 'destination worker', 'root relationship'] as const)('refuses a replay after its %s changes', async (change) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1', relatedAgents: [{ id: 'root-1' }] })
    await flushStream()
    const entry = requestedEntry(state)
    if (change === 'origin worker') {
      state.addAgent('child-1', { workerId: 'w2' }, { activate: false })
      expect(state.view.getAgentTab('child-1')?.workerId).toBe('w2')
    }
    else if (change === 'destination worker') {
      state.addAgent('root-1', { workerId: 'w2' }, { activate: false })
      expect(state.view.getAgentTab('root-1')?.workerId).toBe('w2')
    }
    else {
      state.metadata.patch('child-1', { rootAgentId: 'different-root' })
    }
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(state.chatStore.goal.get('root-1')).toBeUndefined()
    expect(state.chatStore.backgroundTasks.get('root-1')).toEqual([])
  })

  it('refuses a nested root projection through a parent on another worker', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'middle-1', relatedAgents: [{ id: 'root-1' }, { id: 'middle-1', parentAgentId: 'root-1', workerId: 'w2' }] })
    await flushStream()
    const entry = requestedEntry(state)
    for (const topic of ['goal', 'tasks'] as const)
      state.handles.find(handle => handle.workerId === 'w1')!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(state.chatStore.goal.get('root-1')).toBeUndefined()
    expect(state.chatStore.backgroundTasks.get('root-1')).toEqual([])
  })

  it.each(['demoted', 'removed', 'replaced', 'completed'] as const)('refuses a %s child receipt beside an active sibling with the same original ID', async (change) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1', relatedAgents: [{ id: 'child-2', parentAgentId: 'root-1', rootAgentId: 'root-1', visible: true }] })
    await flushStream()
    const entry = requestedEntry(state)
    const sibling = requestedEntry(state, 'child-2')
    expect(entry.replayId).toBe(sibling.replayId)
    if (change === 'completed') {
      state.handles[0]!.emit(receivedEvent({ agentId: 'child-1', replay: true, replayId: entry.replayId, event: { case: 'catchUpComplete', value: { latestSeq: 0n } } }))
    }
    else if (change === 'removed') {
      emitRemoveTab(TabType.AGENT, 'child-1')
    }
    else {
      state.addAgent('replacement-1', { workerId: 'w1' })
    }
    await flushStream()
    if (change === 'replaced') {
      state.selection.setActiveById(TabType.AGENT, 'child-1')
      await flushStream()
      expect(requestedEntry(state).replayId).not.toBe(entry.replayId)
    }
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(state.chatStore.goal.get('root-1')).toBeUndefined()
    expect(state.chatStore.backgroundTasks.get('root-1')).toEqual([])
    expect(state.agentSessionStore.acceptsReplay('child-2', sibling.replayId)).toBe(true)
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { origin: 'child-2', replayId: sibling.replayId }))
    expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('replayed-goal')
    expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(['replayed-task'])
  })

  it('refuses a closed stream listener after a replacement stream opens', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const old = state.handles[0]!
    const oldEntry = requestedEntry(state)
    old.end()
    await vi.advanceTimersByTimeAsync(1000)
    await flushStream()
    const current = requestedEntry(state)
    expect(current.replayId).not.toBe(oldEntry.replayId)
    for (const topic of ['goal', 'tasks'] as const)
      old.emit(rootPublication(topic, { replayId: current.replayId }))
    expect(state.chatStore.goal.get('root-1')).toBeUndefined()
    expect(state.chatStore.backgroundTasks.get('root-1')).toEqual([])
    state.handles[1]!.emit(rootPublication('tasks', { replayId: current.replayId }))
    expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(['replayed-task'])
  })

  it('accepts the transmitted FULL lifetime before its desired demotion sends', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const entry = requestedEntry(state)
    state.addAgent('replacement-1', { workerId: 'w1' })
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(state.agentSessionStore.acceptsReplay('child-1', entry.replayId)).toBe(true)
    expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('replayed-goal')
    expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(['replayed-task'])
  })

  it.each(['catchUpStart', 'catchUpComplete', 'statusChange', 'controlRequest', 'todosChanged', 'inputQueueChanged'] as const)('refuses a projected %s event', async (kind) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1', relatedAgents: [{ id: 'root-1' }] })
    await flushStream()
    const entry = requestedEntry(state)
    const events: Record<typeof kind, AgentEvent['event']> = {
      catchUpStart: create(AgentEventSchema, { event: { case: 'catchUpStart', value: { latestSeq: 99n, activityState: AgentActivityState.WORKING } } }).event,
      catchUpComplete: create(AgentEventSchema, { event: { case: 'catchUpComplete', value: { latestSeq: 99n } } }).event,
      statusChange: create(AgentEventSchema, { event: { case: 'statusChange', value: { agentId: 'root-1', status: AgentStatus.INACTIVE } } }).event,
      controlRequest: create(AgentEventSchema, { event: { case: 'controlRequest', value: { agentId: 'root-1', requestId: 'foreign-request', responseState: ControlResponseState.READY, payload: new TextEncoder().encode('{}') } } }).event,
      todosChanged: create(AgentEventSchema, { event: { case: 'todosChanged', value: { agentId: 'root-1', todos: [create(TodoItemSchema, { id: 'foreign-todo', content: 'Foreign todo', status: TodoStatus.IN_PROGRESS })] } } }).event,
      inputQueueChanged: create(AgentEventSchema, { event: { case: 'inputQueueChanged', value: { snapshot: { agentId: 'root-1', revision: 99n } } } }).event,
    }
    const reconcile = vi.spyOn(state.chatStore, 'reconcileAuthoritativeTail')
    state.handles[0]!.emit(receivedEvent({ agentId: 'root-1', replay: true, replayId: entry.replayId, event: events[kind] }, 'child-1'))
    expect(reconcile).not.toHaveBeenCalled()
    expect(state.agentActivityStore.isBusy('root-1')).toBe(false)
    expect(state.view.getAgentTab('root-1')?.agentStatus).toBe(AgentStatus.ACTIVE)
    expect(state.controlStore.getRequests('root-1')).toEqual([])
    expect(state.chatStore.todos.get('root-1')).toEqual([])
    expect(state.agentInputQueueStore.get('root-1')).toBeUndefined()
    expect(state.agentSessionStore.acceptsReplay('child-1', entry.replayId)).toBe(true)
  })

  it.each(['root', 'child'] as const)('keeps live root goal capabilities against an equal-time %s replay', async (origin) => {
    const state = mountReplay(origin === 'root' ? { agentId: 'root-1' } : { agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const entry = requestedEntry(state)
    state.handles[0]!.emit(rootPublication('goal', { replay: false, value: 'live' }))
    state.handles[0]!.emit(rootPublication('goal', { origin: state.agentId, replayId: entry.replayId }))
    expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('live-goal')
    expect(state.chatStore.goal.supportedActions('root-1')).toEqual(['pause'])
  })

  it.each([
    { origin: 'root', empty: false },
    { origin: 'root', empty: true },
    { origin: 'child', empty: false },
    { origin: 'child', empty: true },
  ] as const)('keeps a live root task list with empty=$empty against a $origin replay', async ({ origin, empty }) => {
    const state = mountReplay(origin === 'root' ? { agentId: 'root-1' } : { agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const entry = requestedEntry(state)
    state.handles[0]!.emit(rootPublication('tasks', { replay: false, value: 'live', empty }))
    const current = state.chatStore.backgroundTasks.get('root-1')
    state.handles[0]!.emit(rootPublication('tasks', { origin: state.agentId, replayId: entry.replayId }))
    expect(state.chatStore.backgroundTasks.get('root-1')).toEqual(current)
    expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(empty ? [] : ['live-task'])
  })

  it.each(['goal', 'tasks'] as const)('restores an unclaimed root %s topic beside another live topic', async (topic) => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const entry = requestedEntry(state)
    const claimed = topic === 'goal' ? 'tasks' : 'goal'
    state.handles[0]!.emit(rootPublication(claimed, { replay: false, value: 'live' }))
    state.handles[0]!.emit(rootPublication(topic, { replayId: entry.replayId }))
    expect(rootState(state, topic)).toMatchObject(topic === 'goal' ? { goal: { nativeId: 'replayed-goal' } } : [{ rowKey: 'replayed-task' }])
    expect(rootState(state, claimed)).toMatchObject(claimed === 'goal' ? { goal: { nativeId: 'live-goal' }, actions: ['pause'] } : [{ rowKey: 'live-task' }])
  })

  it('makes no root goal claim for a refused stale live publication', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const entry = requestedEntry(state)
    const known = rootPublication('goal', { value: 'known' })
    if (known.event.case !== 'goalChanged')
      throw new Error('The fixture requires a goal publication.')
    const goal = known.event.value
    state.chatStore.goal.replace('root-1', goal.goal, goal.supportedActions, goal.goalUpdatedAt)
    state.handles[0]!.emit(rootPublication('goal', { replay: false, empty: true, stamp: '2026-10-09T00:00:01.000Z' }))
    state.handles[0]!.emit(rootPublication('goal', { replayId: entry.replayId, stamp: '2026-10-09T00:00:04.000Z' }))
    expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('replayed-goal')
    expect(state.chatStore.goal.supportedActions('root-1')).toEqual(['clear'])
  })

  it('starts a replacement lifetime with unclaimed root topics', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const old = requestedEntry(state)
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { replay: false, value: 'live' }))
    state.addAgent('replacement-1', { workerId: 'w1' })
    await flushStream()
    state.selection.setActiveById(TabType.AGENT, 'child-1')
    await flushStream()
    const current = requestedEntry(state)
    expect(current.replayId).not.toBe(old.replayId)
    for (const topic of ['goal', 'tasks'] as const)
      state.handles[0]!.emit(rootPublication(topic, { replayId: current.replayId, stamp: '2026-10-09T00:00:04.000Z' }))
    expect(state.chatStore.goal.get('root-1')?.nativeId).toBe('replayed-goal')
    expect(state.chatStore.backgroundTasks.get('root-1').map(task => task.rowKey)).toEqual(['replayed-task'])
  })

  it('keeps tail reconciliation when a live activity claim refuses the old baseline', async () => {
    const state = mountReplay({ seq: 7n })
    await flushStream()
    const entry = requestedEntry(state)
    const reconcile = vi.spyOn(state.chatStore, 'reconcileAuthoritativeTail')
    emitActivity(state, AgentActivityState.WORKING)
    state.handles[0]!.emit(receivedEvent({ agentId: state.agentId, replay: true, replayId: entry.replayId, event: { case: 'catchUpStart', value: { latestSeq: 9n, activityState: AgentActivityState.IDLE } } }))
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(true)
    expect(reconcile).toHaveBeenCalledExactlyOnceWith(state.agentId, 9n, 7n)
    expect(state.settled).not.toHaveBeenCalled()
  })

  it('lets a cold baseline restore after an unspecified live activity report', async () => {
    const state = mountReplay()
    await flushStream()
    emitActivity(state, AgentActivityState.UNSPECIFIED)
    emitActivity(state, AgentActivityState.WORKING, true)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(true)
    expect(state.settled).not.toHaveBeenCalled()
  })

  it.each([AgentActivityState.UNSPECIFIED, -1, 99])('refuses invalid live activity %s without claiming the replay baseline', async (level) => {
    const state = mountReplay()
    await flushStream()
    state.agentActivityStore.seedPublished(state.agentId, AgentActivityState.WORKING)
    emitActivity(state, level)
    expect(state.agentActivityStore.publishedState(state.agentId)).toBe(AgentActivityState.WORKING)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(true)
    expect(state.settled).not.toHaveBeenCalled()
    emitActivity(state, AgentActivityState.IDLE, true)
    expect(state.agentActivityStore.publishedState(state.agentId)).toBe(AgentActivityState.IDLE)
    expect(state.agentActivityStore.isBusy(state.agentId)).toBe(false)
    expect(state.settled).not.toHaveBeenCalled()
  })

  it.each([AgentActivityState.UNSPECIFIED, -1, 99])('refuses invalid replay activity %s while retaining tail reconciliation', async (level) => {
    const state = mountReplay({ seq: 7n })
    await flushStream()
    state.agentActivityStore.seedPublished(state.agentId, AgentActivityState.WORKING)
    const entry = requestedEntry(state)
    const reconcile = vi.spyOn(state.chatStore, 'reconcileAuthoritativeTail')
    state.handles[0]!.emit(receivedEvent({ agentId: state.agentId, replay: true, replayId: entry.replayId, event: { case: 'catchUpStart', value: { latestSeq: 9n, activityState: level } } }))
    expect(state.agentActivityStore.publishedState(state.agentId)).toBe(AgentActivityState.WORKING)
    expect(reconcile).toHaveBeenCalledExactlyOnceWith(state.agentId, 9n, 7n)
    expect(state.settled).not.toHaveBeenCalled()
  })

  it.each([
    { label: 'empty origin', origin: '', wrongId: false },
    { label: 'another origin', origin: 'unsent-agent', wrongId: false },
    { label: 'unsent lifetime', origin: 'a1', wrongId: true },
  ])('retains replay transcript bytes but refuses effects for $label', async ({ origin, wrongId }) => {
    const state = mountReplay({ cost: 1 })
    await flushStream()
    const entry = requestedEntry(state)
    state.agentSessionStore.applyProgress('a1', { revision: 10, thinkingTokens: 17 })
    state.chatStore.applyToolProgress('a1', { ...TOOL_A, outputTail: 'current output' })
    const row = state.emitMessage({ type: 'result', total_cost_usd: 9 }, { replay: true, replayId: wrongId ? entry.replayId + 1n : entry.replayId, origin })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(1)
    expect(state.agentSessionStore.getProgress('a1').thinkingTokens).toBe(17)
    expect(state.chatStore.getToolProgress('a1', TOOL_A)?.outputTail).toBe('current output')
    expect(state.chatStore.getMessages('a1')[0]?.id).toBe(row.id)
    expect(new TextDecoder().decode(row.content)).toBe('{"type":"result","total_cost_usd":9}')
    expect(row.transcriptOnly).toBe(false)
  })

  it.each([9007199254740993n, (1n << 64n) - 1n])('refuses unsent receipt %s even when a direct store caller opens it', async (unsentId) => {
    const state = mountReplay({ cost: 1 })
    await flushStream()
    state.agentSessionStore.beginReplay('a1', unsentId)
    const row = state.emitMessage({ type: 'result', total_cost_usd: 9 }, { replay: true, replayId: unsentId })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(1)
    expect(state.chatStore.getMessages('a1')[0]?.id).toBe(row.id)
    expect(row.transcriptOnly).toBe(false)
  })

  async function replaceReplay(state: ReturnType<typeof mountReplay>) {
    state.addAgent('lifetime-replacement', { workerId: 'w1' })
    await flushStream()
    state.selection.setActiveById(TabType.AGENT, state.agentId)
    await flushStream()
    return requestedEntry(state).replayId
  }

  describe('controlRequest guard for inactive agents', () => {
    function emitRequest(tabs: ReturnType<typeof mountReplay>, replay = false) {
      const current = tabs.handles.at(-1)
      const entry = tabs.transmitted.at(-1)?.agents.find(candidate => candidate.agentId === tabs.agentId)
      if (!current || !entry)
        throw new Error('The request requires its actual transmitted watch entry.')
      current.emit(receivedEvent({
        agentId: tabs.agentId,
        replay,
        replayId: replay ? entry.replayId : 0n,
        event: { case: 'controlRequest', value: {
          agentId: tabs.agentId,
          requestId: 'r1',
          claimToken: 'tok-r1',
          responseState: ControlResponseState.READY,
          payload: new TextEncoder().encode(JSON.stringify({ method: 'item/commandExecution/requestApproval' })),
        } },
      }))
    }

    function emitStatus(tabs: ReturnType<typeof mountReplay>, status: AgentStatus, replay = false) {
      const current = tabs.handles.at(-1)
      const entry = tabs.transmitted.at(-1)?.agents.find(candidate => candidate.agentId === tabs.agentId)
      if (!current || !entry)
        throw new Error('The status requires its actual transmitted watch entry.')
      current.emit(receivedEvent({
        agentId: tabs.agentId,
        replay,
        replayId: replay ? entry.replayId : 0n,
        event: { case: 'statusChange', value: { agentId: tabs.agentId, status, workerOnline: true } },
      }))
    }

    it('should not add catch-up control request when agent is INACTIVE', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.INACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs, true)
      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
    })

    it('should revive stale INACTIVE state and add live control request', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.INACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs)
      expect(tabs.view.getAgentTab('agent-1')?.agentStatus).toBe(AgentStatus.ACTIVE)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
    })

    it('should add control request when agent is ACTIVE', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.ACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs, true)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
    })

    it('should clear control requests when agent becomes INACTIVE', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.ACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      emitStatus(tabs, AgentStatus.INACTIVE)
      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
    })

    it('should preserve pending control requests across short connection blips', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.ACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      tabs.handles[0]!.end()
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
    })

    it('should clear control requests on worker restart because agent processes stop', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.ACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      tabs.handles[0]!.end()
      await vi.advanceTimersByTimeAsync(1000)
      await flushStream()
      expect(tabs.handles).toHaveLength(2)
      emitStatus(tabs, AgentStatus.INACTIVE, true)
      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      emitRequest(tabs, true)
      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
    })

    it('should preserve pending control requests across WatchEvents stream restarts', async () => {
      const tabs = mountReplay({ agentId: 'agent-1', status: AgentStatus.ACTIVE })
      const controlStore = tabs.controlStore
      await flushStream()
      emitRequest(tabs)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      tabs.handles[0]!.end()
      await vi.advanceTimersByTimeAsync(1000)
      await flushStream()
      expect(tabs.handles).toHaveLength(2)
      emitStatus(tabs, AgentStatus.ACTIVE, true)
      emitRequest(tabs, true)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
    })
  })

  it('restores a cold unknown root goal through the actual child replay listener', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1' })
    await flushStream()
    const entry = requestedEntry(state)
    state.handles[0]!.emit(receivedEvent({ agentId: 'root-1', replay: true, replayId: entry.replayId, event: { case: 'goalChanged', value: {
      agentId: 'root-1',
      goal: create(AgentGoalSchema, { nativeId: 'unknown-root-goal', objective: 'Keep the root objective', status: AgentGoalStatus.UNKNOWN, statusDetail: 'native-future-state', createdAt: '2026-10-09T00:00:00.000Z' }),
      supportedActions: [AgentGoalAction.SET, AgentGoalAction.CLEAR],
      goalUpdatedAt: '2026-10-09T00:00:01.000Z',
    } } }, 'child-1'))
    expect(state.chatStore.goal.get('root-1')).toMatchObject({ nativeId: 'unknown-root-goal', objective: 'Keep the root objective', status: 'unknown', statusDetail: 'native-future-state' })
    expect(state.chatStore.goal.supportedActions('root-1')).toEqual(['set', 'clear'])
    expect(state.chatStore.goal.progress('root-1')).toEqual({})
  })

  it('keeps an accepted live unknown root goal and its progress ahead of the child replay baseline', async () => {
    const state = mountReplay({ agentId: 'child-1', parentAgentId: 'root-1', rootAgentId: 'root-1', relatedAgents: [{ id: 'root-1', visible: true }] })
    await flushStream()
    const entry = requestedEntry(state, 'child-1')
    expect(requestedEntry(state, 'root-1').mode).toBe(WatchMode.FULL)
    const current = create(AgentGoalSchema, { nativeId: 'same-root-goal', objective: 'Keep the root objective', createdAt: '2026-10-09T00:00:00.000Z', status: AgentGoalStatus.ACTIVE })
    state.chatStore.goal.replace('root-1', current, [AgentGoalAction.PAUSE], '2026-10-09T00:00:01.000Z')
    state.chatStore.goal.setProgress('root-1', { tokensUsed: 150, iterations: 3 })
    state.handles[0]!.emit(receivedEvent({ agentId: 'root-1', event: { case: 'goalChanged', value: {
      agentId: 'root-1',
      goal: create(AgentGoalSchema, { ...current, status: AgentGoalStatus.UNKNOWN, statusDetail: 'native-future-state' }),
      supportedActions: [AgentGoalAction.SET, AgentGoalAction.CLEAR],
      goalUpdatedAt: '2026-10-09T00:00:01.000Z',
    } } }))
    state.handles[0]!.emit(receivedEvent({ agentId: 'root-1', replay: true, replayId: entry.replayId, event: { case: 'goalChanged', value: {
      agentId: 'root-1',
      goal: current,
      supportedActions: [AgentGoalAction.PAUSE],
      goalUpdatedAt: '2026-10-09T00:00:01.000Z',
    } } }, 'child-1'))
    expect(state.chatStore.goal.get('root-1')).toMatchObject({ nativeId: 'same-root-goal', status: 'unknown', statusDetail: 'native-future-state' })
    expect(state.chatStore.goal.progress('root-1')).toEqual({ tokensUsed: 150, iterations: 3 })
    expect(state.chatStore.goal.supportedActions('root-1')).toEqual(['set', 'clear'])
  })

  it('opens metadata ownership before the actual transport call', async () => {
    const state = mountReplay({ cost: 1 })
    await flushStream()
    expect(state.ownershipAtTransport).toEqual([true])
    state.emitMessage({ type: 'result', total_cost_usd: 2 }, { replay: true })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(2)
  })

  it.each(['replacement', 'clear'] as const)('keeps an accepted live goal %s clear when an older progress snapshot arrives', async (change) => {
    const state = mountReplay()
    await flushStream()
    const oldGoal = create(AgentGoalSchema, { nativeId: 'old-goal', objective: 'Old goal', createdAt: '2026-10-08T00:00:00.000Z', status: AgentGoalStatus.ACTIVE })
    const nextGoal = change === 'clear' ? undefined : create(AgentGoalSchema, { nativeId: 'new-goal', objective: 'New goal', createdAt: '2026-10-08T00:00:02.000Z', status: AgentGoalStatus.ACTIVE })
    state.chatStore.goal.replace('a1', oldGoal, [AgentGoalAction.CLEAR], '2026-10-08T00:00:01.000Z')
    state.chatStore.goal.setProgress('a1', { tokensUsed: 150 })
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', event: { case: 'goalChanged', value: {
      agentId: 'a1',
      goal: nextGoal,
      supportedActions: [AgentGoalAction.SET],
      goalUpdatedAt: '2026-10-08T00:00:03.000Z',
    } } }))
    expect(state.chatStore.goal.progress('a1')).toEqual({})
    const replayId = state.transmitted[0]?.agents.find(entry => entry.agentId === 'a1')?.replayId
    if (replayId === undefined)
      throw new Error('The goal snapshot requires the actual opening replay identity.')
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', replay: true, replayId, event: { case: 'goalChanged', value: {
      agentId: 'a1',
      goal: oldGoal,
      supportedActions: [AgentGoalAction.CLEAR],
      goalUpdatedAt: '2026-10-08T00:00:01.000Z',
    } } }))
    state.emitMessage({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 100, iterations: 3 } } }, { seq: -1n, replay: true, replayId })
    expect(state.chatStore.goal.progress('a1')).toEqual({})
    expect(state.chatStore.goal.get('a1')?.nativeId).toBe(change === 'clear' ? undefined : 'new-goal')
    expect(state.chatStore.goal.supportedActions('a1')).toEqual(['set'])
  })

  it('makes no live progress clear claim for a refused stale goal event', async () => {
    const state = mountReplay()
    await flushStream()
    const current = create(AgentGoalSchema, { nativeId: 'current-goal', objective: 'Current goal', createdAt: '2026-10-08T00:00:02.000Z', status: AgentGoalStatus.ACTIVE })
    state.chatStore.goal.replace('a1', current, [AgentGoalAction.CLEAR], '2026-10-08T00:00:03.000Z')
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', event: { case: 'goalChanged', value: {
      agentId: 'a1',
      supportedActions: [],
      goalUpdatedAt: '2026-10-08T00:00:01.000Z',
    } } }))
    state.emitMessage({ type: 'agent_session_info', info: { goal_progress: { tokens_used: 100 } } }, { seq: -1n, replay: true })
    expect(state.chatStore.goal.get('a1')?.nativeId).toBe('current-goal')
    expect(state.chatStore.goal.progress('a1').tokensUsed).toBe(100)
    expect(state.chatStore.goal.supportedActions('a1')).toEqual(['clear'])
  })

  it('keeps equal-timestamp capabilities and permits unclaimed cold goal progress', async () => {
    const state = mountReplay()
    await flushStream()
    const current = create(AgentGoalSchema, { nativeId: 'current-goal', objective: 'Current goal', createdAt: '2026-10-08T00:00:02.000Z', status: AgentGoalStatus.ACTIVE })
    state.chatStore.goal.replace('a1', current, [AgentGoalAction.SET], '2026-10-08T00:00:03.000Z')
    state.chatStore.goal.setProgress('a1', { tokensUsed: 150 })
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', event: { case: 'goalChanged', value: {
      agentId: 'a1',
      goal: current,
      supportedActions: [AgentGoalAction.CLEAR],
      goalUpdatedAt: '2026-10-08T00:00:03.000Z',
    } } }))
    state.emitMessage({ type: 'agent_session_info', info: { goal_progress: { iterations: 3 } } }, { seq: -1n, replay: true })
    expect(state.chatStore.goal.progress('a1')).toEqual({ tokensUsed: 150, iterations: 3 })
    expect(state.chatStore.goal.supportedActions('a1')).toEqual(['clear'])
  })

  it('passes exact replay IDs while retaining superseded transcript rows', async () => {
    const state = mountReplay()
    await flushStream()
    const earlierId = requestedEntry(state).replayId
    const currentId = await replaceReplay(state)
    expect(currentId).not.toBe(earlierId)
    state.emitMessage({ type: 'result', total_cost_usd: 9 }, { replay: true, replayId: earlierId })
    state.emitMessage({ type: 'result', total_cost_usd: 2 }, { replay: true, replayId: currentId })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(2)
    expect(state.chatStore.getMessages('a1').map(message => message.id)).toEqual(['wire-message-1', 'wire-message-2'])
  })

  it('compares stored history with live sequences on the same request', async () => {
    const state = mountReplay()
    await flushStream()
    state.agentSessionStore.beginReplay('a1', state.handles[0]!.requestId())
    state.emitMessage({ type: 'assistant', total_cost_usd: 3 }, { seq: 30n })
    state.emitMessage({ type: 'result', total_cost_usd: 2 }, { seq: 20n, replay: true })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(3)
    state.emitMessage({ type: 'result', total_cost_usd: 4 }, { seq: 40n, replay: true })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(4)
  })

  it('keeps ephemeral usage when replay starts after that live update', async () => {
    const state = mountReplay()
    await flushStream()
    const requestId = state.handles[0]!.requestId()
    state.agentSessionStore.beginReplay('a1', requestId)
    state.emitMessage({ type: 'agent_session_info', info: { total_cost_usd: 0, context_usage: { input_tokens: 50 } } }, { seq: -1n })
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', replay: true, replayId: requestId, event: { case: 'catchUpStart', value: { latestSeq: 0n, activityState: AgentActivityState.WORKING } } }))
    state.emitMessage({ type: 'result', total_cost_usd: 8, context_usage: { input_tokens: 10 } }, { seq: 40n, replay: true })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(0)
    expect(state.agentSessionStore.getInfo('a1').contextUsage?.inputTokens).toBe(50)
  })

  it('closes only the replay that the completion frame identifies', async () => {
    const state = mountReplay()
    await flushStream()
    const currentId = await replaceReplay(state)
    state.agentSessionStore.beginReplay('a1', currentId)
    state.chatStore.setCatchingUp('a1', true)
    const complete = (replayId: bigint) => state.handles[0]!.emit(receivedEvent({ agentId: 'a1', replay: true, replayId, event: { case: 'catchUpComplete', value: { latestSeq: 0n } } }))
    complete(currentId - 1n)
    expect(state.chatStore.state.catchingUp.a1).toBe(true)
    expect(state.agentSessionStore.acceptsReplay('a1', currentId)).toBe(true)
    complete(currentId)
    expect(state.chatStore.state.catchingUp.a1).toBe(false)
    expect(state.agentSessionStore.acceptsReplay('a1', currentId)).toBe(false)
    state.emitMessage({ type: 'result', total_cost_usd: 9 }, { replay: true, replayId: currentId })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBeUndefined()
    expect(state.chatStore.getMessages('a1').some(message => message.id === 'wire-message-1')).toBe(true)
  })

  it('ignores an old catch-up activity baseline and session snapshot', async () => {
    const state = mountReplay()
    await flushStream()
    const currentId = await replaceReplay(state)
    state.agentSessionStore.beginReplay('a1', currentId)
    state.agentActivityStore.seedPublished('a1', AgentActivityState.WORKING)
    state.agentSessionStore.applyProgress('a1', { revision: 2, thinkingTokens: 30 })
    state.chatStore.applyToolProgress('a1', { ...TOOL_A, outputTail: 'current output' })
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', replay: true, replayId: currentId - 1n, event: { case: 'catchUpStart', value: { latestSeq: 0n, activityState: AgentActivityState.IDLE } } }))
    state.emitMessage({ type: 'agent_session_info', info: { generation_progress_revision: 3, thinking_tokens: 0, running_tool: { span_id: TOOL_A.spanId, agent_session_id: TOOL_A.agentSessionId, output_tail: 'old output' } } }, { seq: -1n, replay: true, replayId: currentId - 1n })
    expect(state.agentActivityStore.isBusy('a1')).toBe(true)
    expect(state.agentSessionStore.getProgress('a1').thinkingTokens).toBe(30)
    expect(state.chatStore.getToolProgress('a1', TOOL_A)?.outputTail).toBe('current output')
  })

  it('keeps a transcript-only live row from reviving an inactive tab', async () => {
    const state = mountReplay({ status: AgentStatus.INACTIVE })
    await flushStream()
    state.emitMessage({ type: 'assistant', total_cost_usd: 9 }, { transcriptOnly: true })
    expect(state.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.INACTIVE)
    expect(state.metadata.liveStatusEpoch('a1')).toBe(0)
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBeUndefined()
    expect(state.chatStore.getMessages('a1')).toHaveLength(1)
  })

  it('retains current live activation and divider cleanup', async () => {
    const state = mountReplay({ status: AgentStatus.INACTIVE })
    await flushStream()
    state.agentSessionStore.applyProgress('a1', { revision: 2, thinkingTokens: 30, output: { bytes: 8, minimum: false } })
    state.chatStore.applyToolProgress('a1', { ...TOOL_A, outputTail: 'current output' })
    state.emitMessage({ type: 'result', subtype: 'success', total_cost_usd: 2 })
    expect(state.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
    expect(state.metadata.liveStatusEpoch('a1')).toBe(1)
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(2)
    expect(state.agentSessionStore.getProgress('a1').thinkingTokens).toBeUndefined()
    expect(state.agentSessionStore.getProgress('a1').output).toBeUndefined()
    expect(state.chatStore.getToolProgress('a1', TOOL_A)).toBeUndefined()
  })

  it('retains the transmitted cursor when live rows precede the ACK', async () => {
    const state = mountReplay({ seq: 7n })
    await flushStream()
    const reconcile = vi.spyOn(state.chatStore, 'reconcileAuthoritativeTail')
    state.emitMessage({ type: 'assistant' }, { seq: 8n })
    // The acknowledgement can arrive after the live row. The transmitted cursor still remains seven.
    const handle = state.handles[0]!
    const emitResponse = vi.mocked(handle.handle.onEvent).mock.calls[0]?.[0]
    // Deliver through the actual registered transport handler.
    expect(emitResponse).toBeDefined()
    const transmitted = state.transmitted.at(-1)
    if (!transmitted)
      throw new Error('The acknowledgment requires the actual transmitted request.')
    emitResponse?.(create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: { updateId: handle.requestId(), agentStates: transmitted.agents.map(entry => ({ agentId: entry.agentId, mode: entry.mode, replayId: entry.replayId })) } } }))
    handle.emit(receivedEvent({ agentId: 'a1', replay: true, replayId: handle.requestId(), event: { case: 'catchUpStart', value: { latestSeq: 7n } } }))
    expect(reconcile).toHaveBeenLastCalledWith('a1', 7n, 7n)
    expect(state.chatStore.getMessages('a1').some(message => message.seq === 8n)).toBe(true)
  })

  it('keeps a completed replay closed when a coalesced FULL request receives its ACK', async () => {
    const state = mountReplay()
    await flushStream()
    const handle = state.handles[0]!
    const replayId = handle.requestId()
    state.agentSessionStore.beginReplay('a1', replayId)
    state.chatStore.setCatchingUp('a1', true)
    state.addAgent('a2', { workerId: 'w1' }, { activate: false })
    await flushStream()
    const acknowledgedId = handle.requestId()
    expect(acknowledgedId).not.toBe(replayId)
    handle.emit(receivedEvent({ agentId: 'a1', replay: true, replayId, event: { case: 'catchUpComplete', value: { latestSeq: 0n } } }))
    expect(state.chatStore.state.catchingUp.a1).toBe(false)
    const listener = vi.mocked(handle.handle.onEvent).mock.calls[0]?.[0]
    const transmitted = state.transmitted.at(-1)
    if (!transmitted)
      throw new Error('The acknowledgment requires the actual transmitted request.')
    listener?.(create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: { updateId: acknowledgedId, agentStates: transmitted.agents.map(entry => ({ agentId: entry.agentId, mode: entry.mode, replayId: entry.replayId })) } } }))
    expect(state.chatStore.state.catchingUp.a1).toBe(false)
  })

  it('restores metadata from the coalesced transmitted lifetime without clearing live progress', async () => {
    const state = mountReplay({ cost: 1, seq: 7n })
    await flushStream()
    const first = state.transmitted[0]
    if (!first)
      throw new Error('The test requires the transmitted opening request.')
    state.emitMessage({ type: 'assistant' }, { seq: 8n })
    state.agentSessionStore.applyProgress('a1', { revision: 2, thinkingTokens: 30 })
    state.chatStore.applyToolProgress('a1', { ...TOOL_A, outputTail: 'current output' })
    state.addAgent('a2', { workerId: 'w1' }, { activate: false })
    await flushStream()
    const accepted = state.transmitted.at(-1)
    const entry = accepted?.agents.find(candidate => candidate.agentId === 'a1')
    if (!accepted || !entry)
      throw new Error('The coalesced request must contain the root agent.')
    expect(accepted.updateId).not.toBe(first.updateId)
    expect(entry.replayId).toBe(first.updateId)
    expect(entry.cursorSeq).toBe(7n)
    expect(entry.windowTailSeq).toBe(7n)
    const handle = state.handles[0]!
    handle.emit(receivedEvent({ agentId: 'a1', replay: true, replayId: entry.replayId, event: { case: 'catchUpStart', value: { latestSeq: 9n, activityState: AgentActivityState.WORKING } } }))
    state.emitMessage({ type: 'result', total_cost_usd: 2 }, { seq: 9n, replay: true, replayId: entry.replayId })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(2)
    expect(state.agentSessionStore.getProgress('a1').thinkingTokens).toBe(30)
    expect(state.chatStore.getToolProgress('a1', TOOL_A)?.outputTail).toBe('current output')
    expect(state.chatStore.getMessages('a1').some(message => message.seq === 8n)).toBe(true)
  })

  it('uses the new transmitted lifetime after a skipped demotion and ignores the earlier replay metadata', async () => {
    const state = mountReplay({ cost: 1, seq: 7n })
    await flushStream()
    const first = state.transmitted[0]
    if (!first)
      throw new Error('The test requires the transmitted opening request.')
    state.handles[0]!.emit(receivedEvent({ agentId: 'a1', replay: true, replayId: first.updateId, event: { case: 'catchUpComplete', value: { latestSeq: 7n } } }))
    state.addAgent('a2', { workerId: 'w1' })
    await flushStream()
    const demoted = state.transmitted.at(-1)?.agents.find(entry => entry.agentId === 'a1')
    expect(demoted?.mode).toBe(WatchMode.NOTIFY)
    expect(demoted?.replayId).toBe(0n)
    state.selection.setActiveById(TabType.AGENT, 'a1')
    await flushStream()
    const resumed = state.transmitted.at(-1)
    const entry = resumed?.agents.find(candidate => candidate.agentId === 'a1')
    if (!resumed || !entry)
      throw new Error('The resumed request must contain the root agent.')
    expect(entry.replayId).toBe(resumed.updateId)
    expect(entry.replayId).not.toBe(first.updateId)
    state.agentSessionStore.applyProgress('a1', { revision: 2, thinkingTokens: 30 })
    state.emitMessage({ type: 'result', total_cost_usd: 2 }, { seq: 8n, replay: true, replayId: entry.replayId })
    state.emitMessage({ type: 'result', total_cost_usd: 99 }, { seq: 9n, replay: true, replayId: first.updateId })
    state.emitMessage({ type: 'result', total_cost_usd: 99 }, { seq: 99n, replay: true, replayId: first.updateId })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(2)
    expect(state.agentSessionStore.getProgress('a1').thinkingTokens).toBe(30)
    expect(state.chatStore.getMessages('a1').some(message => message.seq === 9n)).toBe(true)
    // The window guard keeps a non-contiguous frame outside the loaded window.
    expect(state.chatStore.getMessages('a1').some(message => message.seq === 99n)).toBe(false)
    expect(state.chatStore.liveTail.get('a1')).toBe(99n)
  })

  it('clears catch-up ownership when a coalesced opening never reaches registration', async () => {
    const state = mountReplay()
    await flushStream()
    const openingId = state.handles[0]!.requestId()
    state.addAgent('a2', { workerId: 'w1' }, { activate: false })
    await flushStream()
    const handle = state.handles[0]!
    expect(handle.requestId()).not.toBe(openingId)
    const listener = vi.mocked(handle.handle.onEvent).mock.calls[0]?.[0]
    if (!listener)
      throw new Error('The test requires the actual registered response handler.')
    listener(create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: {
      updateId: handle.requestId(),
      rejectedAgents: ['a1', 'a2'].map(entityId => ({ entityId, reason: WatchRejectionReason.LOOKUP_FAILED })),
      agentStates: [],
    } } }))
    expect(state.agentSessionStore.acceptsReplay('a1', openingId)).toBe(false)
    expect(state.chatStore.state.catchingUp.a1).toBe(false)
    await vi.advanceTimersByTimeAsync(500)
    await flushStream()
    const retried = state.transmitted.at(-1)
    const entry = retried?.agents.find(candidate => candidate.agentId === 'a1')
    if (!retried || !entry)
      throw new Error('The retry must contain the root agent.')
    expect(entry.replayId).not.toBe(openingId)
    expect(state.agentSessionStore.acceptsReplay('a1', entry.replayId)).toBe(true)
  })

  it('opens a fresh receipt after reconnect restores missed history', async () => {
    const state = mountReplay({ cost: 1 })
    await flushStream()
    const old = state.handles[0]!
    old.end()
    await vi.advanceTimersByTimeAsync(1000)
    await flushStream()
    const current = state.handles[1]!
    expect(current.requestId()).not.toBe(old.requestId())
    expect(state.agentSessionStore.acceptsReplay('a1', old.requestId())).toBe(false)
    expect(state.agentSessionStore.acceptsReplay('a1', current.requestId())).toBe(true)
    state.emitMessage({ type: 'result', total_cost_usd: 2 }, { replay: true })
    expect(state.agentSessionStore.getInfo('a1').totalCostUsd).toBe(2)
  })
})
