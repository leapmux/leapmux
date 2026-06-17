import type { AgentControlRequest, AgentStatusChange, AgentStreamChunk, AgentStreamEnd, AvailableOptionGroup } from '~/generated/leapmux/v1/agent_pb'
import type { AgentTab, TerminalTab } from '~/stores/tab.types'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AgentStatus, ContentCompression, MessageSource, WatchReplayMode } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { agentWatchEntry, applyAgentLifecycleAndUsage, applyPendingAxisSuppression, buildAgentStatusTabUpdate, buildWatchTargetsKey, clearCompletedSpanStream, drainPendingOutboundOnStart, handleAgentInactive, handleAgentMessage, handleAgentSessionInfo, handleAgentStatusChange, handleControlRequest, handleResultDivider, handleStreamChunk, handleStreamEnd, reconcileLaggingTails, resolveSettingsTabFields } from '~/hooks/useWorkspaceConnection'
import { extractCompactionContextTokens, extractResultMetadata, parseMessageContent } from '~/lib/messageParser'
import { compactionContextUsage, createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES, MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createTabStore } from '~/stores/tab.store'

/**
 * Types a simulated catch-up phase as the runtime union so the guard
 * comparisons (`!== 'live'`, `=== 'live'`) mirror the source instead of
 * being collapsed to a known literal by TS const-narrowing.
 */
function simulatePhase(phase: 'catchingUp' | 'live'): 'catchingUp' | 'live' {
  return phase
}

describe('watch target helpers', () => {
  it('maps resume cursors to explicit replay modes', () => {
    expect(agentWatchEntry('a1', 0n)).toMatchObject({
      agentId: 'a1',
      replay: WatchReplayMode.LATEST,
      cursorSeq: 0n,
    })
    expect(agentWatchEntry('a1', 42n)).toMatchObject({
      agentId: 'a1',
      replay: WatchReplayMode.AFTER_CURSOR,
      cursorSeq: 42n,
    })
  })

  it('distinguishes active and non-active target roles in the subscription key', () => {
    const active = buildWatchTargetsKey(
      'w1',
      [agentWatchEntry('a1', 0n), agentWatchEntry('a2', 0n)],
      ['t1', 't2'],
      new Set(['a2']),
      new Set(['t2']),
    )
    const movedToActive = buildWatchTargetsKey(
      'w1',
      [agentWatchEntry('a1', 0n), agentWatchEntry('a2', 0n)],
      ['t1', 't2'],
      new Set(),
      new Set(),
    )
    expect(active).not.toBe(movedToActive)
    expect(active).toContain('aa:a1')
    expect(active).toContain('pa:a2')
    expect(active).toContain('at:t1')
    expect(active).toContain('pt:t2')
  })
})

/**
 * These tests verify the control-request guard in useWorkspaceConnection's
 * handleAgentEvent. Because handleAgentEvent is a closure that depends on
 * gRPC streams, we simulate its logic with real stores to verify the
 * invariant: replayed catch-up control requests must not be added for
 * INACTIVE agents, but live control requests are proof that a stale
 * INACTIVE status should be corrected.
 */
describe('controlRequest guard for inactive agents', () => {
  function makeAgent(id: string, status: AgentStatus) {
    return { id, status }
  }
  function asAgentTab(a: { id: string, status: AgentStatus }): AgentTab {
    return { type: TabType.AGENT, id: a.id, agentStatus: a.status }
  }

  function makeRequest(requestId: string, agentId: string) {
    return { requestId, agentId, payload: { method: 'item/commandExecution/requestApproval' } }
  }

  it('should not add catch-up control request when agent is INACTIVE', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.INACTIVE)))

      // Simulate the guard in useWorkspaceConnection's controlRequest handler:
      // if (catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE) break
      const catchUpPhase = simulatePhase('catchingUp')
      // const agentEntry = tabStore.getAgentTab(cr.agentId)
      const agentEntry = tabStore.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should revive stale INACTIVE state and add live control request', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.INACTIVE)))

      const catchUpPhase = 'live'
      if (catchUpPhase === 'live') {
        const current = tabStore.getAgentTab('agent-1')
        if (current?.agentStatus === AgentStatus.INACTIVE)
          tabStore.updateTab(TabType.AGENT, 'agent-1', { agentStatus: AgentStatus.ACTIVE })
      }
      const agentEntry = tabStore.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(tabStore.getAgentTab('agent-1')?.agentStatus).toBe(AgentStatus.ACTIVE)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should add control request when agent is ACTIVE', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.ACTIVE)))

      const catchUpPhase = simulatePhase('catchingUp')
      const agentEntry = tabStore.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests when agent becomes INACTIVE', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.ACTIVE)))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate statusChange INACTIVE → controlStore.clearAgent()
      tabStore.updateTab(TabType.AGENT, 'agent-1', { agentStatus: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should preserve pending control requests across short connection blips', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.ACTIVE)))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate worker-offline transition: agent goes INACTIVE but pending
      // control requests must survive transient transport blips.
      tabStore.updateTab(TabType.AGENT, 'agent-1', { agentStatus: AgentStatus.INACTIVE })

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests on worker restart because agent processes stop', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.ACTIVE)))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate the statusChange handler during catch-up replay after a
      // worker restart: the replayed INACTIVE statusChange triggers clearAgent
      // because the agent process no longer exists.
      tabStore.updateTab(TabType.AGENT, 'agent-1', { agentStatus: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)

      // A replayed controlRequest for the now-INACTIVE agent must be skipped
      // by the controlRequest-case guard in useWorkspaceConnection: catch-up
      // replay + INACTIVE → break.
      const catchUpPhase = simulatePhase('catchingUp')
      const agentEntry = tabStore.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should preserve pending control requests across WatchEvents stream restarts', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      const controlStore = createControlStore()

      tabStore.addTab(asAgentTab(makeAgent('agent-1', AgentStatus.ACTIVE)))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate WatchEvents stream reconnect: the agent is still ACTIVE
      // (worker didn't restart), so the replayed statusChange does NOT
      // trigger clearAgent. The same controlRequest is replayed but
      // addRequest deduplicates by requestId.
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })
})

describe('background agent history trimming', () => {
  function isAgentTabVisible(tabStore: ReturnType<typeof createTabStore>, agentId: string): boolean {
    const key = `${TabType.AGENT}:${agentId}`
    if (tabStore.state.activeTabKey === key)
      return true
    return Object.values(tabStore.state.tileActiveTabKeys).includes(key)
  }

  function makeUserMessage(id: string, seq: bigint) {
    return {
      id,
      source: MessageSource.USER,
      content: new TextEncoder().encode('{"content":"test"}'),
      seq,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  it('trims non-active agent history when new messages arrive', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'active-agent', tileId: 'tile-1' })

      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('background-agent', initial)

      chatStore.addMessage('background-agent', makeUserMessage(`m${MAX_BACKGROUND_CHAT_MESSAGES + 1}`, BigInt(MAX_BACKGROUND_CHAT_MESSAGES + 1)))
      if (
        !isAgentTabVisible(tabStore, 'background-agent')
        && chatStore.getMessages('background-agent').length > MAX_BACKGROUND_CHAT_MESSAGES
      ) {
        chatStore.trimOldestEnd('background-agent', MAX_BACKGROUND_CHAT_MESSAGES)
      }

      const messages = chatStore.getMessages('background-agent')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(messages[0].seq).toBe(2n)
      expect(messages.at(-1)?.seq).toBe(BigInt(MAX_BACKGROUND_CHAT_MESSAGES + 1))
      expect(chatStore.hasOlderMessages('background-agent')).toBe(true)
      dispose()
    })
  })

  it('does not trim the active agent in the event-handler path', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'active-agent', tileId: 'tile-1' })

      const initial = Array.from({ length: MAX_LOADED_CHAT_MESSAGES }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('active-agent', initial)

      chatStore.addMessage('active-agent', makeUserMessage('m151', 151n))
      if (
        !isAgentTabVisible(tabStore, 'active-agent')
        && chatStore.getMessages('active-agent').length > MAX_LOADED_CHAT_MESSAGES
      ) {
        chatStore.trimOldestEnd('active-agent', MAX_LOADED_CHAT_MESSAGES)
      }

      const messages = chatStore.getMessages('active-agent')
      expect(messages).toHaveLength(MAX_LOADED_CHAT_MESSAGES + 1)
      expect(messages[0].seq).toBe(1n)
      expect(messages.at(-1)?.seq).toBe(151n)
      dispose()
    })
  })

  it('does not trim an agent tab that is active in its tile even when not globally active', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'active-agent', tileId: 'tile-1' })
      tabStore.addTab({ type: TabType.AGENT, id: 'visible-agent', tileId: 'tile-2' }, { activate: false })
      tabStore.setActiveTabForTile('tile-2', TabType.AGENT, 'visible-agent')

      const initial = Array.from({ length: MAX_BACKGROUND_CHAT_MESSAGES }, (_, i) =>
        makeUserMessage(`m${i + 1}`, BigInt(i + 1)))
      chatStore.setMessages('visible-agent', initial)

      chatStore.addMessage('visible-agent', makeUserMessage(`m${MAX_BACKGROUND_CHAT_MESSAGES + 1}`, BigInt(MAX_BACKGROUND_CHAT_MESSAGES + 1)))
      if (
        !isAgentTabVisible(tabStore, 'visible-agent')
        && chatStore.getMessages('visible-agent').length > MAX_BACKGROUND_CHAT_MESSAGES
      ) {
        chatStore.trimOldestEnd('visible-agent', MAX_BACKGROUND_CHAT_MESSAGES)
      }

      const messages = chatStore.getMessages('visible-agent')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 1)
      expect(messages[0].seq).toBe(1n)
      expect(messages.at(-1)?.seq).toBe(BigInt(MAX_BACKGROUND_CHAT_MESSAGES + 1))
      dispose()
    })
  })
})

describe('agent tab notification keys', () => {
  it('does not notify the active agent tab when key formats match store keys', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-1', tileId: 'tile-1' })

      if (tabStore.state.activeTabKey !== `${TabType.AGENT}:agent-1`) {
        tabStore.setNotification(TabType.AGENT, 'agent-1', true)
      }

      expect(tabStore.state.tabs[0].hasNotification).not.toBe(true)
      dispose()
    })
  })

  // Mirrors the live controlRequest branch in useWorkspaceConnection: a
  // background tab that receives a control request must light its badge
  // so the user knows to switch over. The active tab must NOT be badged
  // (the prompt is already on screen).
  function applyControlRequestNotification(
    tabStore: ReturnType<typeof createTabStore>,
    agentId: string,
    catchUpPhase: 'catchingUp' | 'live',
  ) {
    if (catchUpPhase !== 'live')
      return
    if (tabStore.state.activeTabKey !== `${TabType.AGENT}:${agentId}`) {
      tabStore.setNotification(TabType.AGENT, agentId, true)
    }
  }

  it('badges a background tab when a live control request arrives', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-A', tileId: 'tile-1' })
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-B', tileId: 'tile-1' }, { activate: false })
      tabStore.setActiveTab(TabType.AGENT, 'agent-A')

      applyControlRequestNotification(tabStore, 'agent-B', 'live')

      const tabB = tabStore.state.tabs.find(t => t.id === 'agent-B')
      const tabA = tabStore.state.tabs.find(t => t.id === 'agent-A')
      expect(tabB?.hasNotification).toBe(true)
      expect(tabA?.hasNotification).not.toBe(true)
      dispose()
    })
  })

  it('does not badge the focused tab when its own control request arrives', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-A', tileId: 'tile-1' })
      tabStore.setActiveTab(TabType.AGENT, 'agent-A')

      applyControlRequestNotification(tabStore, 'agent-A', 'live')

      expect(tabStore.state.tabs[0].hasNotification).not.toBe(true)
      dispose()
    })
  })

  // A page reload replays still-pending control_requests via the catch-up
  // path; surfacing them as new badges would alarm the user about prompts
  // they were already aware of. Only 'live' arrivals should badge.
  it('does not badge during catch-up replay', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-A', tileId: 'tile-1' })
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-B', tileId: 'tile-1' }, { activate: false })
      tabStore.setActiveTab(TabType.AGENT, 'agent-A')

      applyControlRequestNotification(tabStore, 'agent-B', 'catchingUp')

      const tabB = tabStore.state.tabs.find(t => t.id === 'agent-B')
      expect(tabB?.hasNotification).not.toBe(true)
      dispose()
    })
  })
})

describe('codex result replay handling', () => {
  it('clears stale codexTurnId when a persisted turn/completed result is replayed', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('agent-1', { codexTurnId: 'turn-stale' })

      const msg = {
        id: 'm1',
        source: MessageSource.AGENT,
        content: new TextEncoder().encode(JSON.stringify({
          num_tool_uses: 2,
          threadId: 'thread-1',
          turn: {
            id: 'turn-1',
            items: [],
            status: 'completed',
            error: null,
          },
        })),
        contentCompression: ContentCompression.NONE,
        seq: 1n,
        agentProvider: AgentProvider.CODEX,
      } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]

      const meta = extractResultMetadata(parseMessageContent(msg))
      if (meta && msg.agentProvider === AgentProvider.CODEX && meta.subtype === 'turn_completed')
        agentSessionStore.updateInfo('agent-1', { codexTurnId: '' })

      expect(agentSessionStore.getInfo('agent-1').codexTurnId).toBe('')
      dispose()
    })
  })
})

describe('context usage refresh on compaction boundary', () => {
  // Mirrors the compaction branch in useWorkspaceConnection's message handler:
  // a completed boundary refreshes the grid from its post-compaction token
  // count (resetting the now-stale input/cache components, since the boundary
  // carries no breakdown) while preserving the known context window, instead of
  // leaving the pre-compaction usage on screen until the next turn.
  function applyCompaction(
    sessionStore: ReturnType<typeof createAgentSessionStore>,
    agentId: string,
    content: unknown,
  ) {
    const msg = {
      id: 'compact-1',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify(content)),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      agentProvider: AgentProvider.CLAUDE_CODE,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]

    const postTokens = extractCompactionContextTokens(parseMessageContent(msg))
    if (postTokens === undefined)
      return
    const existing = sessionStore.getInfo(agentId).contextUsage
    sessionStore.updateInfo(agentId, {
      contextUsage: compactionContextUsage(postTokens, existing),
    })
  }

  const compactBoundary = (meta: Record<string, unknown>) => ({ type: 'system', subtype: 'compact_boundary', compact_metadata: meta })

  // Distinct agent ids per case: the store persists through localStorage, so a
  // shared id would leak one case's contextWindow into the next.
  it('drops the grid to the post-compaction size and preserves the context window', () => {
    createRoot((dispose) => {
      const store = createAgentSessionStore()
      // Pre-compaction usage from the last assistant turn: ~150k of input/cache.
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

      // Only pre_tokens -- nothing to resolve a post-compaction size from.
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

describe('streaming text preservation', () => {
  it('keeps accumulated assistant streaming text when a persisted user message arrives mid-stream', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()

      chatStore.streamingText.set('agent-1', 'Hello')

      const echoedUserMessage = {
        id: 'server-user-1',
        source: MessageSource.USER,
        content: new TextEncoder().encode(JSON.stringify({ content: 'follow-up' })),
        contentCompression: ContentCompression.NONE,
        seq: 1n,
      } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]

      chatStore.addMessage('agent-1', echoedUserMessage)

      chatStore.streamingText.set('agent-1', `${chatStore.streamingText.get('agent-1') ?? ''} world`)

      expect(chatStore.streamingText.get('agent-1')).toBe('Hello world')

      chatStore.streamingText.clear('agent-1')
      expect(chatStore.streamingText.get('agent-1')).toBe('')
      dispose()
    })
  })

  it('clears top-level streaming text when a persisted codex agentMessage completion arrives', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()

      chatStore.streamingText.set('agent-1', 'Hello')

      const completedAssistantMessage = {
        id: 'assistant-1',
        source: MessageSource.AGENT,
        content: new TextEncoder().encode(JSON.stringify({
          item: {
            type: 'agentMessage',
            id: 'msg-1',
            text: 'Hello world',
          },
          threadId: 'thread-1',
          turnId: 'turn-1',
        })),
        contentCompression: ContentCompression.NONE,
        seq: 2n,
        agentProvider: AgentProvider.CODEX,
      } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]

      chatStore.addMessage('agent-1', completedAssistantMessage)
      const parsed = parseMessageContent(completedAssistantMessage)
      const item = parsed.parentObject?.item as Record<string, unknown> | undefined
      if (item?.type === 'agentMessage')
        chatStore.streamingText.clear('agent-1')

      expect(chatStore.streamingText.get('agent-1')).toBe('')
      dispose()
    })
  })

  it('clears top-level plan streaming text and streamingType when a persisted codex plan completion arrives', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      const agentSessionStore = createAgentSessionStore()

      chatStore.streamingText.set('agent-1', '# Plan\n')
      agentSessionStore.updateInfo('agent-1', { streamingType: 'plan' })

      const completedPlanMessage = {
        id: 'plan-1',
        source: MessageSource.AGENT,
        content: new TextEncoder().encode(JSON.stringify({
          item: {
            type: 'plan',
            id: 'plan-1',
            text: '# Plan\nStep 1',
          },
          threadId: 'thread-1',
          turnId: 'turn-1',
        })),
        contentCompression: ContentCompression.NONE,
        seq: 2n,
        agentProvider: AgentProvider.CODEX,
      } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]

      chatStore.addMessage('agent-1', completedPlanMessage)
      const parsed = parseMessageContent(completedPlanMessage)
      const item = parsed.parentObject?.item as Record<string, unknown> | undefined
      if (item?.type === 'plan') {
        chatStore.streamingText.clear('agent-1')
        agentSessionStore.updateInfo('agent-1', { streamingType: '' })
      }

      expect(chatStore.streamingText.get('agent-1')).toBe('')
      expect(agentSessionStore.getInfo('agent-1').streamingType).toBe('')
      dispose()
    })
  })
})

/**
 * These tests lock in the startup_message plumbing rules in
 * useWorkspaceConnection's agent statusChange handler:
 *  - STARTING status → store sc.startupMessage on the agent record.
 *  - Any other concrete status → clear startupMessage (so stale phase
 *    labels don't linger).
 *  - UNSPECIFIED / status-less events (catchUp sentinels, git-only
 *    updates) → leave startupMessage alone.
 */
describe('startupMessage handling in agent statusChange', () => {
  function applyStatusChange(
    tabStore: ReturnType<typeof createTabStore>,
    sc: { agentId: string, status: AgentStatus, startupMessage?: string },
  ) {
    const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
    tabStore.updateTab(TabType.AGENT, sc.agentId, {
      ...(hasStatus ? { agentStatus: sc.status } : {}),
      ...(sc.status === AgentStatus.STARTING
        ? { startupMessage: sc.startupMessage ?? '' }
        : hasStatus ? { startupMessage: '' } : {}),
    })
  }

  it('stores startupMessage while STARTING so the startup panel can render the phase label', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.AGENT, id: 'agent-1', agentStatus: AgentStatus.STARTING })

      applyStatusChange(tabStore, {
        agentId: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      })
      expect(tabStore.getAgentTab('agent-1')?.startupMessage).toBe('Checking Git status…')

      applyStatusChange(tabStore, {
        agentId: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Starting Claude Code…',
      })
      expect(tabStore.getAgentTab('agent-1')?.startupMessage).toBe('Starting Claude Code…')
      dispose()
    })
  })

  it('clears startupMessage on ACTIVE so the label does not linger after startup succeeds', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({
        type: TabType.AGENT,
        id: 'agent-1',
        agentStatus: AgentStatus.STARTING,
        startupMessage: 'Starting Claude Code…',
      })

      applyStatusChange(tabStore, { agentId: 'agent-1', status: AgentStatus.ACTIVE })

      expect(tabStore.getAgentTab('agent-1')?.startupMessage).toBe('')
      dispose()
    })
  })

  it('clears startupMessage on STARTUP_FAILED so the error banner replaces the phase label', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({
        type: TabType.AGENT,
        id: 'agent-1',
        agentStatus: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      })

      applyStatusChange(tabStore, { agentId: 'agent-1', status: AgentStatus.STARTUP_FAILED })

      expect(tabStore.getAgentTab('agent-1')?.startupMessage).toBe('')
      dispose()
    })
  })

  it('leaves startupMessage alone on status-less events (UNSPECIFIED) so catchUp sentinels do not wipe live phases', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({
        type: TabType.AGENT,
        id: 'agent-1',
        agentStatus: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      })

      applyStatusChange(tabStore, { agentId: 'agent-1', status: AgentStatus.UNSPECIFIED })

      expect(tabStore.getAgentTab('agent-1')?.startupMessage).toBe('Checking Git status…')
      dispose()
    })
  })
})

describe('per-axis optimistic suppression in agent statusChange', () => {
  // Exercises the REAL applyPendingAxisSuppression (the handler's per-axis merge):
  // keep the optimistic value for each axis the agent is actively changing
  // (settingsLoading.pendingAxes), and apply the server's confirmed value for every
  // OTHER axis. A pending axis ABSENT from prev is an in-flight CLEAR (useAgentOperations
  // deletes a cleared key before marking it pending), so it stays absent rather than
  // re-absorbing the server value. Driven by the REAL createLoadingSignal so the
  // integration of pendingAxes with the merge is exercised end to end.
  it('keeps the pending axis optimistic while applying a server change to an unrelated axis', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      // The user is optimistically switching the MODEL; the tab already holds that optimistic value.
      s.start('agent-1', ['model'])
      const prev = { model: 'opus', permissionMode: 'default' }
      // A push confirms the OLD model (the in-flight RPC hasn't resolved server-side) AND a new
      // server-initiated permissionMode.
      const serverValues = { model: 'sonnet', permissionMode: 'plan' }

      const merged = applyPendingAxisSuppression(serverValues, prev, s.pendingAxes('agent-1'))
      expect(merged.model).toBe('opus') // pending axis: optimistic value preserved
      expect(merged.permissionMode).toBe('plan') // unrelated axis: server value applied, not stranded
      dispose()
    })
  })

  it('keeps a pending CLEARED axis absent rather than re-absorbing the server value', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      // The user optimistically CLEARED permissionMode: useAgentOperations deleted the key from the
      // tab's optionValues before marking the axis pending, so prev carries no permissionMode entry.
      s.start('agent-1', ['permissionMode'])
      const prev = { model: 'opus' }
      // A push still carries the server's pre-clear permissionMode (the in-flight RPC hasn't resolved).
      const serverValues = { model: 'opus', permissionMode: 'plan' }

      const merged = applyPendingAxisSuppression(serverValues, prev, s.pendingAxes('agent-1'))
      expect('permissionMode' in merged).toBe(false) // in-flight clear preserved, not re-absorbed
      expect(merged.model).toBe('opus') // unrelated axis untouched
      dispose()
    })
  })

  it('applies all server values once the pending change settles', () => {
    createRoot((dispose) => {
      const s = createLoadingSignal()
      s.start('agent-1', ['model'])
      s.stop('agent-1', ['model']) // the RPC resolved
      const prev = { model: 'opus', permissionMode: 'default' }
      const serverValues = { model: 'sonnet', permissionMode: 'plan' }

      const merged = applyPendingAxisSuppression(serverValues, prev, s.pendingAxes('agent-1'))
      expect(merged.model).toBe('sonnet') // no longer pending -> server value applies
      expect(merged.permissionMode).toBe('plan')
      dispose()
    })
  })

  it('returns the server values unchanged (same reference) when nothing is pending', () => {
    const serverValues = { model: 'sonnet', permissionMode: 'plan' }
    // No pending axes -> a no-op, and the SAME reference so the handler's downstream
    // shallow-equal ref-reuse can short-circuit.
    expect(applyPendingAxisSuppression(serverValues, { model: 'opus' }, new Set())).toBe(serverValues)
  })
})

describe('resolveSettingsTabFields', () => {
  // Minimal option-group stubs: deriveOptionGroupTabFields reads only id + currentValue.
  const group = (id: string, currentValue: string): AvailableOptionGroup =>
    ({ id, label: id, currentValue, options: [] }) as unknown as AvailableOptionGroup

  it('returns {} for an empty option-group push, leaving the previously-derived fields untouched', () => {
    expect(resolveSettingsTabFields(undefined, [], new Set())).toEqual({})
  })

  it('reuses the prior optionValues reference when a re-broadcast changes no current value', () => {
    // prev has no optionGroups, so the stable-group-ref step is skipped; only the
    // optionValues shallow-equal ref-reuse runs.
    const prev: AgentTab = { type: TabType.AGENT, id: 'a1', optionValues: { model: 'opus' } }
    const fields = resolveSettingsTabFields(prev, [group('model', 'opus')], new Set())
    // Same content -> same reference, so reactive readers of optionValues don't wake.
    expect(fields.optionValues).toBe(prev.optionValues)
  })

  it('keeps the optimistic value for a pending axis while applying the server value elsewhere', () => {
    const prev: AgentTab = { type: TabType.AGENT, id: 'a1', optionValues: { model: 'opus', permissionMode: 'default' } }
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
    expect(update.optionValues).toEqual({ model: 'opus' }) // settings still apply
  })

  it('carries status, clears startupError/startupMessage on ACTIVE, and merges settings', () => {
    const sc = { status: AgentStatus.ACTIVE, agentSessionId: 's1', startupError: 'stale', startupMessage: 'stale' } as unknown as AgentStatusChange
    const update = buildAgentStatusTabUpdate(sc, true, settings)
    expect(update.agentStatus).toBe(AgentStatus.ACTIVE)
    expect(update.agentSessionId).toBe('s1')
    expect(update.startupError).toBe('')
    expect(update.startupMessage).toBe('')
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

  it('derives the git fields + full proto from a gitStatus payload (a git-only push)', () => {
    const sc = {
      status: AgentStatus.UNSPECIFIED,
      gitStatus: { branch: 'main', originUrl: 'git@x:y.git', toplevel: '/repo', isWorktree: true },
    } as unknown as AgentStatusChange
    const update = buildAgentStatusTabUpdate(sc, false, {})
    expect(update.gitBranch).toBe('main')
    expect(update.gitOriginUrl).toBe('git@x:y.git')
    expect(update.gitToplevel).toBe('/repo')
    expect(update.gitIsWorktree).toBe(true)
    expect(update.agentGitStatus).toBe(sc.gitStatus) // full proto carried for the diff view
  })
})

describe('drainPendingOutboundOnStart', () => {
  const queued = (localId: string) => ({ localId, content: 'hi', attachments: [] })

  it('is a no-op when the prior status was not STARTING (queue left intact)', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      chatStore.pendingOutbound.enqueue('agent-1', queued('local-1'))
      drainPendingOutboundOnStart(
        { agentId: 'agent-1', status: AgentStatus.ACTIVE } as unknown as AgentStatusChange,
        { agentStatus: AgentStatus.ACTIVE } as AgentTab, // prior status not STARTING
        chatStore,
      )
      expect(chatStore.pendingOutbound.take('agent-1')).toHaveLength(1) // not drained
      dispose()
    })
  })

  it('surfaces a failure error and clears the pending label on every queued message on STARTUP_FAILED', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      chatStore.pendingOutbound.enqueue('agent-1', queued('local-1'))
      chatStore.pendingOutbound.enqueue('agent-1', queued('local-2'))
      chatStore.setMessagePendingLabel('local-1', 'Queued…')
      drainPendingOutboundOnStart(
        { agentId: 'agent-1', status: AgentStatus.STARTUP_FAILED } as unknown as AgentStatusChange,
        { agentStatus: AgentStatus.STARTING } as AgentTab,
        chatStore,
      )
      expect(chatStore.messageErrors()['local-1']).toBe('Agent failed to start')
      expect(chatStore.messageErrors()['local-2']).toBe('Agent failed to start')
      expect(chatStore.messagePendingLabels()['local-1']).toBeUndefined() // label cleared
      expect(chatStore.pendingOutbound.take('agent-1')).toHaveLength(0) // queue drained
      dispose()
    })
  })
})

describe('handleAgentInactive', () => {
  function makeStores() {
    const tabStore = createTabStore()
    tabStore.addTab({ type: TabType.AGENT, id: 'agent-1', agentStatus: AgentStatus.INACTIVE } as unknown as Parameters<typeof tabStore.addTab>[0])
    const controlStore = createControlStore()
    controlStore.addRequest('agent-1', { requestId: 'r1', agentId: 'agent-1', payload: {} })
    return { controlStore, agentSessionStore: createAgentSessionStore(), chatStore: createChatStore(), tabStore }
  }

  it('clears control requests and signals turn-end while LIVE', () => {
    createRoot((dispose) => {
      const stores = makeStores()
      const turnEnds: string[] = []
      handleAgentInactive('agent-1', { agentSessionId: 'sess-1' } as unknown as AgentStatusChange, 'live', stores, id => turnEnds.push(id))
      expect(stores.controlStore.getRequests('agent-1')).toHaveLength(0)
      expect(turnEnds).toEqual(['agent-1']) // live + sessionId + tab present -> turn end
      dispose()
    })
  })

  it('clears control requests but does NOT signal turn-end during catch-up', () => {
    createRoot((dispose) => {
      const stores = makeStores()
      const turnEnds: string[] = []
      handleAgentInactive('agent-1', { agentSessionId: 'sess-1' } as unknown as AgentStatusChange, 'catchingUp', stores, id => turnEnds.push(id))
      expect(stores.controlStore.getRequests('agent-1')).toHaveLength(0) // still cleared
      expect(turnEnds).toEqual([]) // catchUpComplete sweep owns turn-end during catch-up
      dispose()
    })
  })
})

describe('workerOnline handling in agent statusChange', () => {
  it('ignores workerOnline=false from status-less git-only updates', () => {
    let workerOnline = true
    const applyStatusChange = (sc: { status: AgentStatus, workerOnline: boolean, gitStatus?: unknown }) => {
      const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
      if (hasStatus)
        workerOnline = sc.workerOnline
      return hasStatus || sc.gitStatus !== undefined
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
  })
})

// Regression tests for the "new terminal tab shows 'Starting terminal…'
// instead of 'Starting <shell>…'" bug: the client subscribes to
// WatchEvents only after the OpenTerminal response, so the sync-path
// STARTING broadcast lands with no watcher attached. The fix surfaces
// the phase label via catch-up replay — these tests lock in the
// frontend half of that contract.
describe('startupMessage handling in terminal statusChange', () => {
  // Mirrors the switch in useWorkspaceConnection.handleTerminalEvent's
  // STARTING branch: on a STARTING event for a tab that is not
  // already running/starting, store both status and message; on a
  // same-status STARTING update with a fresh message, patch just the
  // label so a later phase broadcast refreshes the overlay text.
  function applyStarting(
    tabStore: ReturnType<typeof createTabStore>,
    terminalId: string,
    msg: string | undefined,
  ) {
    const existing = tabStore.state.tabs.find(
      (t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === terminalId,
    )
    if (existing && existing.status !== TerminalStatus.READY && existing.status !== TerminalStatus.STARTING) {
      tabStore.updateTab(TabType.TERMINAL, terminalId, {
        status: TerminalStatus.STARTING,
        startupMessage: msg || undefined,
      })
    }
    else if (existing?.status === TerminalStatus.STARTING && msg && msg !== existing.startupMessage) {
      tabStore.updateTab(TabType.TERMINAL, terminalId, { startupMessage: msg })
    }
  }

  it('stores startupMessage on the initial STARTING event so the overlay renders the backend phase label', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1' })

      applyStarting(tabStore, 'term-1', 'Starting zsh…')

      const tab = tabStore.state.tabs.find((t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === 'term-1')
      expect(tab?.status).toBe(TerminalStatus.STARTING)
      expect(tab?.startupMessage).toBe('Starting zsh…')
      dispose()
    })
  })

  it('updates startupMessage on a same-status STARTING event so later phase broadcasts refresh the overlay label', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

      applyStarting(tabStore, 'term-1', 'Starting fish…')

      const tab = tabStore.state.tabs.find((t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === 'term-1')
      expect(tab?.startupMessage).toBe('Starting fish…')
      dispose()
    })
  })

  // Phase-0 ("Preparing working tree") labels are dispatched by the
  // worker as same-status STARTING events with the per-mode label.
  // Rolling back on failure uses the same pipe with the rollback label
  // and then transitions to STARTUP_FAILED. Both should be applied.
  it('applies the "Creating worktree" phase-0 label to the tab', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', status: TerminalStatus.STARTING, startupMessage: 'Starting zsh…' })

      applyStarting(tabStore, 'term-1', 'Creating worktree "feature/x"…')

      const tab = tabStore.state.tabs.find((t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === 'term-1')
      expect(tab?.startupMessage).toBe('Creating worktree "feature/x"…')
      dispose()
    })
  })

  it('applies a following "Rolling back worktree" label on same-status STARTING', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', status: TerminalStatus.STARTING, startupMessage: 'Creating worktree "feature/x"…' })

      applyStarting(tabStore, 'term-1', 'Rolling back worktree "feature/x"…')

      const tab = tabStore.state.tabs.find((t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === 'term-1')
      expect(tab?.startupMessage).toBe('Rolling back worktree "feature/x"…')
      dispose()
    })
  })

  // Mirrors the git-fields branch of useWorkspaceConnection.handleTerminalEvent:
  // on any STARTING event that carries non-empty git_branch / git_origin_url,
  // update the tab so the sidebar badge matches the new worktree immediately.
  function applyGitFromStatusChange(
    tabStore: ReturnType<typeof createTabStore>,
    terminalId: string,
    gitBranch: string,
    gitOriginUrl: string,
  ) {
    const existing = tabStore.state.tabs.find(
      (t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === terminalId,
    )
    if (!existing)
      return
    const nextBranch = gitBranch || undefined
    const nextOrigin = gitOriginUrl || undefined
    if (existing.gitBranch !== nextBranch || existing.gitOriginUrl !== nextOrigin) {
      tabStore.updateTab(TabType.TERMINAL, terminalId, { gitBranch: nextBranch, gitOriginUrl: nextOrigin })
    }
  }

  it('updates gitBranch/gitOriginUrl from a terminal statusChange event', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', status: TerminalStatus.STARTING })

      applyGitFromStatusChange(tabStore, 'term-1', 'feature/x', 'git@example.com:org/repo.git')

      const tab = tabStore.state.tabs.find((t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === 'term-1')
      expect(tab?.gitBranch).toBe('feature/x')
      expect(tab?.gitOriginUrl).toBe('git@example.com:org/repo.git')
      dispose()
    })
  })
})

/**
 * agent_session_info wire-shape handling. The worker broadcasts
 * snake_case keys exclusively (`total_cost_usd`, `context_usage`,
 * `rate_limits`, `codex_turn_id`, `streaming_type`, `pi_*`); these tests
 * reproduce the unwrap-and-merge logic in useWorkspaceConnection that
 * translates wire keys back to the frontend store's camelCase shape.
 */
describe('agent_session_info snake_case wire normalization', () => {
  function applyAgentSessionInfo(
    sessionStore: ReturnType<typeof createAgentSessionStore>,
    agentId: string,
    info: Record<string, unknown> | undefined,
  ) {
    if (!info)
      return
    const updates: Record<string, unknown> = {}
    if (typeof info.total_cost_usd === 'number')
      updates.totalCostUsd = info.total_cost_usd
    if (info.context_usage !== undefined)
      updates.contextUsage = info.context_usage
    if (info.rate_limits !== undefined)
      updates.rateLimits = info.rate_limits
    if (Object.keys(updates).length > 0)
      sessionStore.updateInfo(agentId, updates)
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

// handleAgentEvent gates the turn-end orphan sweep on catchUpPhase === 'live', so a
// turn-end divider replayed DURING catch-up skips it; the catchUpComplete handler
// then sweeps once on the transition. Simulated inline with the real chatStore (as
// the other handler tests here do) to verify the cross-cutting invariant: an orphan
// recorded mid-catch-up is reclaimed on catch-up completion rather than leaking.
describe('orphaned command-stream sweep across catch-up', () => {
  function makeSpanMessage(id: string, seq: bigint, spanId: string) {
    return {
      id,
      source: MessageSource.AGENT,
      content: new TextEncoder().encode('{"type":"assistant"}'),
      seq,
      spanId,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  it('reclaims a stream orphaned during catch-up on the catch-up -> live transition', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      chatStore.addMessage('agent-1', makeSpanMessage('m1', 1n, 'span1'))
      chatStore.appendCommandStream('agent-1', 'span1', 'item/commandExecution/output', 'output') // marks span1 renderable

      // A mid-stream delete spares the still-buffered stream and records it as an
      // orphan (clearing now would lose the in-flight segments).
      chatStore.removeMessage('agent-1', 'm1')
      expect(chatStore.getCommandStream('agent-1', 'span1')).toHaveLength(1)

      // The result_divider turn-end sweep is GATED on catchUpPhase === 'live'
      // (see handleAgentEvent), so a turn-end replayed during catch-up skips it.
      const catchUpPhase = simulatePhase('catchingUp')
      if (catchUpPhase === 'live')
        chatStore.sweepOrphanedBufferedSpans('agent-1')
      expect(chatStore.getCommandStream('agent-1', 'span1')).toHaveLength(1) // still spared

      // The catchUpComplete handler flips the phase to 'live' AND sweeps once, so
      // the orphan recorded during catch-up is reclaimed instead of leaking until
      // (or past) the next live turn-end.
      chatStore.sweepOrphanedBufferedSpans('agent-1')
      expect(chatStore.getCommandStream('agent-1', 'span1')).toHaveLength(0)
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

  it('handleAgentSessionInfo consumes an agent_session_info message and applies its updates', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      const msg = agentMessage({ type: 'agent_session_info', info: { total_cost_usd: 1.5 } })
      const handled = handleAgentSessionInfo('a1', parseMessageContent(msg), agentSessionStore)
      // Returning true is the early-break signal: the caller must NOT persist it.
      expect(handled).toBe(true)
      expect(agentSessionStore.getInfo('a1').totalCostUsd).toBe(1.5)
      dispose()
    })
  })

  it('handleAgentSessionInfo returns false for a persisted message (caller keeps processing it)', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      const msg = agentMessage({ type: 'assistant', message: { content: [{ type: 'text', text: 'hi' }] } })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), agentSessionStore)).toBe(false)
      dispose()
    })
  })

  it('handleAgentSessionInfo clears a stale thinking-token estimate on a 0 (per-phase reset)', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('a1', { thinkingTokens: 500 })
      const msg = agentMessage({ type: 'agent_session_info', info: { thinking_tokens: 0 } })
      handleAgentSessionInfo('a1', parseMessageContent(msg), agentSessionStore)
      expect(agentSessionStore.getInfo('a1').thinkingTokens).not.toBe(500)
      dispose()
    })
  })

  it('handleResultDivider fires onTurnEnd only in the live phase, not during catch-up replay', () => {
    createRoot((dispose) => {
      const stores = {
        agentSessionStore: createAgentSessionStore(),
        chatStore: createChatStore(),
        tabStore: createTabStore(),
      }
      const turnEnds: string[] = []
      const onTurnEnd = (id: string) => turnEnds.push(id)
      const msg = agentMessage({ type: 'result', subtype: 'success', total_cost_usd: 0.25 })
      const parsed = parseMessageContent(msg)

      // A catch-up replay must not re-play the turn-end side effects.
      handleResultDivider('a1', msg, parsed, stores, onTurnEnd, 'catchingUp')
      expect(turnEnds).toEqual([])

      // A live divider fires onTurnEnd and rehydrates total cost.
      handleResultDivider('a1', msg, parsed, stores, onTurnEnd, 'live')
      expect(turnEnds).toEqual(['a1'])
      expect(stores.agentSessionStore.getInfo('a1').totalCostUsd).toBe(0.25)
      dispose()
    })
  })

  /** A span (commandExecution/fileChange/reasoning) row carrying its `item` payload. */
  function spanMessage(item: unknown, spanType: string, spanId = 'span1') {
    return {
      id: 'm1',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify({ item })),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      spanId,
      spanType,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  it('clearCompletedSpanStream reclaims a COMPLETED span command stream', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      chatStore.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
      expect(chatStore.getCommandStream('a1', 'span1')).toHaveLength(1)

      // The persisted row reports the commandExecution span completed -> its buffered
      // in-flight segments are superseded and reclaimed.
      const msg = spanMessage({ type: 'commandExecution', status: 'completed' }, 'commandExecution')
      clearCompletedSpanStream('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getCommandStream('a1', 'span1')).toHaveLength(0)
      dispose()
    })
  })

  it('clearCompletedSpanStream leaves an IN-PROGRESS span stream buffered', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      chatStore.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')

      // A still-running span (status != completed) must keep its live stream.
      const msg = spanMessage({ type: 'commandExecution', status: 'in_progress' }, 'commandExecution')
      clearCompletedSpanStream('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getCommandStream('a1', 'span1')).toHaveLength(1)
      dispose()
    })
  })

  it('handleAgentMessage does not clear a completed span stream when the row is dropped beyond the window', () => {
    createRoot((dispose) => {
      const stores = {
        agentSessionStore: createAgentSessionStore(),
        chatStore: createChatStore(),
        tabStore: createTabStore(),
      }
      stores.chatStore.setMessages('a1', Array.from({ length: 50 }, (_, i) => ({
        ...agentMessage({ type: 'assistant' }),
        id: `m${i + 1}`,
        seq: BigInt(i + 1),
      })))
      stores.chatStore.trimNewestEnd('a1', 30) // hasMoreNewer=true; seq 60 is recorded but not inserted.
      stores.chatStore.appendCommandStream('a1', 'span1', 'item/commandExecution/output', 'output')
      const dropped = {
        ...spanMessage({ type: 'commandExecution', status: 'completed' }, 'commandExecution'),
        id: 'dropped-complete',
        seq: 60n,
      }

      handleAgentMessage('a1', dropped, stores, undefined, 'live')

      expect(stores.chatStore.getMessages('a1').some(m => m.id === 'dropped-complete')).toBe(false)
      expect(stores.chatStore.getCommandStream('a1', 'span1')).toHaveLength(1)
      dispose()
    })
  })

  it('applyAgentLifecycleAndUsage clears a stale Codex turn id on thread/started', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('a1', { codexTurnId: 'turn-123' })
      const msg = agentMessage({ method: 'thread/started' })
      applyAgentLifecycleAndUsage('a1', msg, parseMessageContent(msg), agentSessionStore)
      // A new thread starts idle: the stale turn id is cleared so the chat shows its
      // empty state instead of a phantom thinking indicator.
      expect(agentSessionStore.getInfo('a1').codexTurnId).toBe('')
      dispose()
    })
  })

  it('applyAgentLifecycleAndUsage skips a non-AGENT message (the source gate)', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('a1', { codexTurnId: 'turn-123' })
      // A USER-source message carrying a thread/started method must be ignored, so the
      // turn id survives -- the gate that keeps a hidden-classified lifecycle item from
      // being processed off a non-AGENT row.
      const msg = { ...agentMessage({ method: 'thread/started' }), source: MessageSource.USER }
      applyAgentLifecycleAndUsage('a1', msg, parseMessageContent(msg), agentSessionStore)
      expect(agentSessionStore.getInfo('a1').codexTurnId).toBe('turn-123')
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
      // Default to a NON-empty loaded window (1n); the empty-window (0n) recovery branch
      // is exercised explicitly by its own test.
      getLastSeq: overrides.getLastSeq ?? (() => 1n),
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
        { id: 'lagging', workerId: 'w1' }, // at tail, not caught up -> fill
        { id: 'caught-up', workerId: 'w1' }, // at tail, caught up -> no fill
        { id: 'scrolled-away', workerId: 'w1' }, // hasNewer, NOT deferred -> no fill
      ],
      hasNewerMessages: id => id === 'scrolled-away',
      caughtUpToLiveTail: id => id === 'caught-up',
      // 'lagging' is at the tail; 'scrolled-away' has a loaded (non-empty) window.
      getLastSeq: id => (id === 'lagging' ? 42n : id === 'scrolled-away' ? 30n : 0n),
    })
    expect(catchUp).toEqual([{ workerId: 'w1', agentId: 'lagging', afterSeq: 42n }])
    expect(resume).toEqual([]) // a plain scrolled-away wall is left to the affordance
  })

  it('skips a tab with no workerId (a non-active-workspace agent)', () => {
    const { catchUp } = run({
      agentTabs: [{ id: 'lagging', workerId: '' }],
      caughtUpToLiveTail: () => false, // lagging, but no worker to fetch from
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
        { id: 'deferred', workerId: 'w1' }, // hasNewer + deferred + lagging -> resume
        { id: 'scrolled-away', workerId: 'w1' }, // hasNewer, NOT deferred -> nothing
      ],
      hasNewerMessages: () => true, // both away from the loaded tail
      caughtUpToLiveTail: () => false, // both genuinely lagging
      isTailFillDeferred: id => id === 'deferred',
    })
    expect(resume).toEqual([{ workerId: 'w1', agentId: 'deferred' }])
    expect(catchUp).toEqual([]) // resume uses the merge path, not catchUpToTail
  })

  it('prefers catchUpToTail at the tail over a deferred resume, and skips a caught-up agent', () => {
    const { catchUp, resume } = run({
      agentTabs: [
        { id: 'at-tail', workerId: 'w1' }, // !hasNewer, lagging -> catchUpToTail
        { id: 'caught-up-deferred', workerId: 'w1' }, // caught up, even if deferred -> nothing
      ],
      hasNewerMessages: () => false,
      caughtUpToLiveTail: id => id === 'caught-up-deferred',
      isTailFillDeferred: () => true,
      getLastSeq: () => 7n,
    })
    expect(catchUp).toEqual([{ workerId: 'w1', agentId: 'at-tail', afterSeq: 7n }])
    expect(resume).toEqual([])
  })

  it('re-seats an EMPTY window (a full phantom reap) on the latest page instead of forward-filling', () => {
    const { catchUp, resume, jumps } = run({
      agentTabs: [{ id: 'emptied', workerId: 'w1' }],
      // Not caught up (server content survives), but the loaded window is empty (getLastSeq
      // 0n) -- a full phantom reap dropped every loaded row. There's no anchor to
      // forward-fill from, so re-seat on the latest page.
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 0n,
    })
    expect(jumps).toEqual([{ workerId: 'w1', agentId: 'emptied' }])
    expect(catchUp).toEqual([]) // no forward-fill from an empty window
    expect(resume).toEqual([])
  })

  it('does NOT re-issue the empty-window re-seat while a newer fetch is already in flight', () => {
    const { jumps } = run({
      agentTabs: [{ id: 'emptied', workerId: 'w1' }],
      caughtUpToLiveTail: () => false,
      getLastSeq: () => 0n,
      isFetchingNewer: () => true, // jumpToLatest's own fetch is resolving
    })
    expect(jumps).toEqual([]) // guarded so the reconcile tick doesn't abort + restart it
  })
})

// Direct coverage for the per-case handlers extracted from handleAgentEvent's
// dispatcher. The dispatcher closure itself is only driven by gRPC streams, so these
// exercise the real production handlers (not a re-implementation) against live stores.
describe('extracted handleAgentEvent arm handlers', () => {
  const enc = (s: string) => new TextEncoder().encode(s)
  const argStores = () => ({
    agentSessionStore: createAgentSessionStore(),
    chatStore: createChatStore(),
    tabStore: createTabStore(),
    controlStore: createControlStore(),
  })

  describe('handleStreamChunk', () => {
    it('accumulates free-form streaming text when there is no spanId', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        handleStreamChunk('a1', { delta: enc('hello '), spanId: '', method: '' } as unknown as AgentStreamChunk, chatStore)
        handleStreamChunk('a1', { delta: enc('world'), spanId: '', method: '' } as unknown as AgentStreamChunk, chatStore)
        expect(chatStore.streamingText.get('a1')).toBe('hello world')
        dispose()
      })
    })

    it('routes a spanId chunk to the command-stream buffer, not the free-form text', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        handleStreamChunk('a1', { delta: enc('out'), spanId: 's1', method: 'bash' } as unknown as AgentStreamChunk, chatStore)
        expect(chatStore.streamingText.get('a1')).toBe('') // NOT the free-form text
        expect(chatStore.getCommandStream('a1', 's1').map(seg => seg.text).join('')).toContain('out')
        dispose()
      })
    })
  })

  describe('handleStreamEnd', () => {
    it('clears the free-form streaming text and badges a backgrounded tab', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        const tabStore = createTabStore()
        tabStore.addTab({ type: TabType.AGENT, id: 'a1' } as AgentTab)
        tabStore.addTab({ type: TabType.AGENT, id: 'a2' } as AgentTab)
        tabStore.setActiveTab(TabType.AGENT, 'a2') // a1 is backgrounded
        chatStore.streamingText.set('a1', 'partial')
        handleStreamEnd('a1', { spanId: '' } as unknown as AgentStreamEnd, { chatStore, tabStore })
        expect(chatStore.streamingText.get('a1')).toBe('')
        expect(tabStore.getAgentTab('a1')?.hasNotification).toBe(true)
        dispose()
      })
    })

    it('does not badge the tab when the agent IS the active tab', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        const tabStore = createTabStore()
        tabStore.addTab({ type: TabType.AGENT, id: 'a1' } as AgentTab)
        tabStore.setActiveTab(TabType.AGENT, 'a1')
        handleStreamEnd('a1', { spanId: '' } as unknown as AgentStreamEnd, { chatStore, tabStore })
        expect(tabStore.getAgentTab('a1')?.hasNotification).toBeFalsy()
        dispose()
      })
    })
  })

  describe('handleControlRequest', () => {
    const req = (agentId: string) =>
      ({ requestId: 'r1', agentId, payload: enc(JSON.stringify({ method: 'item/commandExecution/requestApproval' })) }) as unknown as AgentControlRequest

    it('skips a replayed (catch-up) request for an already-INACTIVE agent', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.INACTIVE } as AgentTab)
        handleControlRequest('a1', req('a1'), simulatePhase('catchingUp'), s, undefined)
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
        dispose()
      })
    })

    it('adds a live request, badges a backgrounded tab, and ends the turn', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.ACTIVE } as AgentTab)
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a2' } as AgentTab)
        s.tabStore.setActiveTab(TabType.AGENT, 'a2')
        let ended = ''
        handleControlRequest('a1', req('a1'), 'live', s, id => void (ended = id))
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(s.tabStore.getAgentTab('a1')?.hasNotification).toBe(true)
        expect(ended).toBe('a1')
        dispose()
      })
    })

    it('adds a catch-up request for an ACTIVE agent but does NOT run the live-only turn-end', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.ACTIVE } as AgentTab)
        let ended = ''
        handleControlRequest('a1', req('a1'), simulatePhase('catchingUp'), s, id => void (ended = id))
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(ended).toBe('') // onTurnEnd gated to live
        dispose()
      })
    })

    it('ignores a malformed JSON payload instead of throwing out of the stream handler', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.ACTIVE } as AgentTab)
        const malformed = { requestId: 'r1', agentId: 'a1', payload: enc('{not json') } as unknown as AgentControlRequest
        expect(() => handleControlRequest('a1', malformed, 'live', s, undefined)).not.toThrow()
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
        dispose()
      })
    })
  })

  describe('handleAgentStatusChange', () => {
    it('applies a status update and reports worker-online on a full snapshot', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.STARTING } as AgentTab)
        let online: boolean | undefined
        const sc = { agentId: 'a1', status: AgentStatus.ACTIVE, workerOnline: true, optionGroups: [], startupError: '', startupMessage: '' } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), v => void (online = v), undefined)
        expect(s.tabStore.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE)
        expect(online).toBe(true)
        dispose()
      })
    })

    it('skips a payload-less sentinel without touching the tab or reporting worker-online', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.ACTIVE } as AgentTab)
        let online: boolean | undefined
        const sc = { agentId: 'a1', status: AgentStatus.UNSPECIFIED, workerOnline: false, optionGroups: [] } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), v => void (online = v), undefined)
        expect(s.tabStore.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE) // unchanged
        expect(online).toBeUndefined() // setWorkerOnline only on a full status snapshot
        dispose()
      })
    })

    it('clears pending control requests when the agent goes INACTIVE', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabStore.addTab({ type: TabType.AGENT, id: 'a1', agentStatus: AgentStatus.ACTIVE } as AgentTab)
        s.controlStore.addRequest('a1', { requestId: 'r1', agentId: 'a1', payload: { method: 'x' } })
        const sc = { agentId: 'a1', status: AgentStatus.INACTIVE, workerOnline: true, optionGroups: [], startupError: '', startupMessage: '' } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), () => {}, undefined)
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
        dispose()
      })
    })
  })
})
