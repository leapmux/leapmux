import type { AgentControlRequest, AgentStatusChange, AgentStreamChunk, AgentStreamEnd, AvailableOptionGroup } from '~/generated/proto/leapmux/v1/agent_pb'
import type { TerminalStatusChange } from '~/generated/proto/leapmux/v1/terminal_pb'
import type { AgentTab, Tab, TerminalTab } from '~/stores/tab.types'
import { createRoot, mapArray } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import * as workerRpc from '~/api/workerRpc'
import { providerFor } from '~/components/chat/providers/registry'
import { AgentProvider, AgentStatus, ContentCompression, MessageSource, WatchReplayMode } from '~/generated/proto/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/proto/leapmux/v1/terminal_pb'
import { TabType, WatchMode } from '~/generated/proto/leapmux/v1/workspace_pb'
import { applyAgentLifecycle, applyNotificationMetadata, applyPendingAxisSuppression, buildAgentStatusTabUpdate, clearCompletedSpanStream, drainPendingOutboundOnStart, handleAgentInactive, handleAgentMessage, handleAgentSessionInfo, handleAgentStatusChange, handleControlRequest, handleResultDivider, handleStreamChunk, handleStreamEnd, handleTurnEnd, resolveSettingsTabFields, shouldClearThinkingTokensForMessage, wireSessionInfoToUpdates } from '~/hooks/agentEvents'
import { createLoadingSignal } from '~/hooks/createLoadingSignal'
import { applyTerminalStatusChange, handleTerminalBell, handleTerminalNotification, handleTerminalProgress, handleTerminalTitleChanged } from '~/hooks/terminalEvents'
import { clearOfflineAgentState, collectWorkerOfflineTargets, enqueuePendingTerminalData, MAX_PENDING_TERMINAL_FRAMES, reconcileLaggingTails, useWorkspaceConnection } from '~/hooks/useWorkspaceConnection'
import { agentWatchEntry, watchPlanKey } from '~/hooks/watchPlan'
import { ChannelError, channelNotOpenError } from '~/lib/channelError'
import { extractCompactionContextTokens, extractResultMetadata, parseMessageContent } from '~/lib/messageParser'
import { compactionContextUsage, createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { repoKey } from '~/stores/repoGit'
import { createRepoGitStore } from '~/stores/repoGit.store'
import { createTabMetadataStore } from '~/stores/tabMetadata.store'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'

vi.mock('~/api/workerRpc', async (importOriginal) => {
  const actual = await importOriginal<typeof import('~/api/workerRpc')>()
  return {
    ...actual,
    watchEventsViaChannel: vi.fn(),
    // The chat-history load the hook fires for the active agent tab. Stubbed so
    // a test can choose HOW it fails; the real one would reach for a channel
    // this file never stands up.
    listAgentMessages: vi.fn(),
    // Only getOrOpenChannel is reached from this module, and it must not
    // attempt a real handshake.
    channelManager: {
      getOrOpenChannel: vi.fn().mockResolvedValue('ch-1'),
      hasOpenChannelForWorker: vi.fn().mockReturnValue(true),
      // Asked by useWatchEventsStreams before it arms a redial, so a caller with
      // no error in hand still parks on a latched relay. Null is "dialable".
      fatalCloseInfo: vi.fn(() => null),
    },
  }
})

/** Everything the app told the USER about, whichever helper carried it. */
const mockShowWarnToast = vi.fn()

vi.mock('~/components/common/Toast', async () => {
  // showWarnToastUnlessDisconnected keeps its REAL rule and reports through the
  // same spy: these tests ask whether the USER was told, and a stub that always
  // forwarded would pass whether the rule held or not.
  const { isDisconnectError } = await import('~/api/workerErrors')
  return {
    showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
    showInfoToast: vi.fn(),
    showWarnToastUnlessDisconnected: (message: string, err: unknown) => {
      if (!isDisconnectError(err))
        mockShowWarnToast(message, err)
    },
  }
})

/**
 * Types a simulated catch-up phase as the runtime union so the guard
 * comparisons (`!== 'live'`, `=== 'live'`) mirror the source instead of
 * being collapsed to a known literal by TS const-narrowing.
 */
const WS = 'ws-test'

let nextPosition = 0

/**
 * The tab stores these tests drive, over a real CRDT bridge.
 *
 * The hook reads tabs from the joined `view` and writes worker-sourced fields
 * to `tabMetadata`, so a tab needs BOTH halves: a placement op (which is what
 * puts it in the projection at all) and its metadata. `addAgent` writes both,
 * which is the only reason this helper exists — everything these tests care
 * about (agentStatus, title, git fields) lives on the metadata side.
 */
function makeTabStores(workspaceId = WS) {
  const harness = installTestBridge({ workspaceId })
  const stores = createTestTabStores(workspaceId)
  return {
    ...stores,
    rootTileId: harness.rootTileId,
    /** Place an agent and patch its metadata. Activates, as `addTab` used to. */
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
    /** Same, for a TERMINAL tab. */
    addTerminal(id: string, meta: Record<string, unknown> = {}) {
      nextPosition += 1
      emitAddTab({ type: TabType.TERMINAL, id, tileId: harness.rootTileId, position: `p${nextPosition}`, workerId: '' })
      if (Object.keys(meta).length > 0)
        stores.metadata.patch(id, meta)
    },
  }
}

type TabStores = ReturnType<typeof makeTabStores>

function simulatePhase(phase: 'catchingUp' | 'live'): 'catchingUp' | 'live' {
  return phase
}

describe('watch plan helpers', () => {
  it('maps resume cursors to explicit replay modes', () => {
    expect(agentWatchEntry('a1', 0n, WatchMode.FULL)).toMatchObject({
      agentId: 'a1',
      replay: WatchReplayMode.LATEST,
      cursorSeq: 0n,
      mode: WatchMode.FULL,
    })
    expect(agentWatchEntry('a1', 42n, WatchMode.NOTIFY)).toMatchObject({
      agentId: 'a1',
      replay: WatchReplayMode.AFTER_CURSOR,
      cursorSeq: 42n,
      mode: WatchMode.NOTIFY,
    })
  })

  it('watchPlanKey moves when a mode changes', () => {
    const notify = { agents: [{ agentId: 'a1', mode: WatchMode.NOTIFY } as never], terminals: [] as never[], terminalResync: new Set<string>() }
    const full = { agents: [{ agentId: 'a1', mode: WatchMode.FULL } as never], terminals: [] as never[], terminalResync: new Set<string>() }
    expect(watchPlanKey(notify)).not.toBe(watchPlanKey(full))
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
  function makeRequest(requestId: string, agentId: string) {
    return { requestId, agentId, payload: { method: 'item/commandExecution/requestApproval' }, claimToken: `tok-${requestId}` }
  }

  it('should not add catch-up control request when agent is INACTIVE', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.INACTIVE })

      // Simulate the guard in useWorkspaceConnection's controlRequest handler:
      // if (catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE) break
      const catchUpPhase = simulatePhase('catchingUp')
      // const agentEntry = tabs.view.getAgentTab(cr.agentId)
      const agentEntry = tabs.view.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should revive stale INACTIVE state and add live control request', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.INACTIVE })

      const catchUpPhase = 'live'
      if (catchUpPhase === 'live') {
        const current = tabs.view.getAgentTab('agent-1')
        if (current?.agentStatus === AgentStatus.INACTIVE)
          tabs.metadata.patch('agent-1', { agentStatus: AgentStatus.ACTIVE })
      }
      const agentEntry = tabs.view.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(tabs.view.getAgentTab('agent-1')?.agentStatus).toBe(AgentStatus.ACTIVE)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should add control request when agent is ACTIVE', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.ACTIVE })

      const catchUpPhase = simulatePhase('catchingUp')
      const agentEntry = tabs.view.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests when agent becomes INACTIVE', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.ACTIVE })
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate statusChange INACTIVE → controlStore.clearAgent()
      tabs.metadata.patch('agent-1', { agentStatus: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should preserve pending control requests across short connection blips', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.ACTIVE })
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate worker-offline transition: agent goes INACTIVE but pending
      // control requests must survive transient transport blips.
      tabs.metadata.patch('agent-1', { agentStatus: AgentStatus.INACTIVE })

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests on worker restart because agent processes stop', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.ACTIVE })
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate the statusChange handler during catch-up replay after a
      // worker restart: the replayed INACTIVE statusChange triggers clearAgent
      // because the agent process no longer exists.
      tabs.metadata.patch('agent-1', { agentStatus: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)

      // A replayed controlRequest for the now-INACTIVE agent must be skipped
      // by the controlRequest-case guard in useWorkspaceConnection: catch-up
      // replay + INACTIVE → break.
      const catchUpPhase = simulatePhase('catchingUp')
      const agentEntry = tabs.view.getAgentTab('agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.agentStatus === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should preserve pending control requests across WatchEvents stream restarts', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const controlStore = createControlStore()

      tabs.addAgent('agent-1', { agentStatus: AgentStatus.ACTIVE })
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
  /**
   * Driven through the real `handleAgentMessage`, deliberately.
   *
   * These used to re-implement the visibility predicate in the test and assert
   * against that reimplementation, so the production guard was never executed:
   * when it regressed from an identity check to a bare truthiness check on
   * `activeKeyForTile` -- which heals on read and is therefore truthy for any
   * tile holding any tab -- `trimOldestEnd` became unreachable and every suite
   * stayed green while background chat history grew without bound.
   */
  function makeTrimStores() {
    const tabs = makeTabStores()
    return {
      stores: {
        controlStore: createControlStore(),
        agentSessionStore: createAgentSessionStore(),
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
   * Fill `agentId` to exactly the cap, then deliver ONE more through the real
   * handler -- which both persists it and decides whether to trim.
   */
  function overflowThroughHandler(
    stores: ReturnType<typeof makeTrimStores>['stores'],
    agentId: string,
    cap: number,
  ) {
    stores.chatStore.setMessages(agentId, Array.from({ length: cap }, (_, i) =>
      makeUserMessage(`m${i + 1}`, BigInt(i + 1))))
    handleAgentMessage(agentId, makeUserMessage(`m${cap + 1}`, BigInt(cap + 1)) as never, stores as never, undefined, 'live')
  }

  it('trims an agent the user is not looking at', () => {
    createRoot((dispose) => {
      const { stores, tabs } = makeTrimStores()
      tabs.addAgent('active-agent')
      tabs.addAgent('background-agent', {}, { activate: false })
      // The user is on `active-agent`; both share the root tile.
      tabs.selection.setActiveById(TabType.AGENT, 'active-agent')

      overflowThroughHandler(stores, 'background-agent', MAX_BACKGROUND_CHAT_MESSAGES)

      const messages = stores.chatStore.getMessages('background-agent')
      expect(messages, 'the cap must actually bound the backlog').toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES)
      expect(messages[0].seq).toBe(2n)
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
      expect(messages[0].seq).toBe(1n)
      dispose()
    })
  })

  it('does not trim a tab that is tile-active while another tab is workspace-active', () => {
    createRoot((dispose) => {
      const { stores, tabs } = makeTrimStores()
      // A real second leaf: the point is a tab that is active on ITS tile while
      // not being the workspace's active tab, so the two must differ.
      const secondTile = tabs.layoutStore.splitTile(tabs.rootTileId, 'horizontal')!
      tabs.addAgent('active-agent')
      tabs.addAgent('visible-agent', {}, { tileId: secondTile, activate: false })
      tabs.selection.setActiveById(TabType.AGENT, 'visible-agent')
      tabs.selection.setActiveById(TabType.AGENT, 'active-agent')

      overflowThroughHandler(stores, 'visible-agent', MAX_BACKGROUND_CHAT_MESSAGES)

      const messages = stores.chatStore.getMessages('visible-agent')
      expect(messages).toHaveLength(MAX_BACKGROUND_CHAT_MESSAGES + 1)
      expect(messages[0].seq).toBe(1n)
      dispose()
    })
  })
})

describe('agent tab notification keys', () => {
  it('does not notify the active agent tab when key formats match store keys', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      tabs.addAgent('agent-1')

      if (tabs.selection.activeKeyForWorkspace(WS) !== `${TabType.AGENT}:agent-1`) {
        tabs.metadata.patch('agent-1', { hasNotification: true })
      }

      expect(tabs.view.getAgentTab('agent-A')?.hasNotification).not.toBe(true)
      dispose()
    })
  })

  // Mirrors the live controlRequest branch in useWorkspaceConnection: a
  // background tab that receives a control request must light its badge
  // so the user knows to switch over. The active tab must NOT be badged
  // (the prompt is already on screen).
  function applyControlRequestNotification(
    tabs: TabStores,
    agentId: string,
    catchUpPhase: 'catchingUp' | 'live',
  ) {
    if (catchUpPhase !== 'live')
      return
    if (tabs.selection.activeKeyForWorkspace(WS) !== `${TabType.AGENT}:${agentId}`) {
      tabs.metadata.patch(agentId, { hasNotification: true })
    }
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

  // A page reload replays still-pending control_requests via the catch-up
  // path; surfacing them as new badges would alarm the user about prompts
  // they were already aware of. Only 'live' arrivals should badge.
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

      const meta = extractResultMetadata(
        parseMessageContent(msg),
        undefined,
        p => providerFor(AgentProvider.CODEX)?.resultSubtype?.(p),
      )
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

describe('applyNotificationMetadata usage folding', () => {
  // The unified context-usage path: usage/cost fold into session info here (the single call site),
  // via extractContextUsage delegating the raw per-provider shape to Provider.contextUsageFromMessage
  // while the shared wrapper owns the neutral guards (subagent skip, prefer-normalized, cost).
  function stores() {
    const { view, metadata, selection } = makeTabStores()
    return { agentSessionStore: createAgentSessionStore(), chatStore: createChatStore(), view, metadata, selection, getActiveWorkspaceId: () => WS }
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

  // Distinct agent ids per case: the session store persists through localStorage, so a shared id
  // would leak one case's usage into the next.

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
      // The user renamed the tab after the plan was written. Reloading replays
      // the historical plan_updated, and re-applying it here silently undid
      // that rename -- the tab came back named after the plan every time.
      // Every other side effect in applyNotificationMetadata is derived state
      // that catch-up SHOULD restore; the title is the one the user owns.
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
      // Carries BOTH a normalized context_usage and a raw message.usage; the normalized value wins
      // and the provider hook is never consulted (guard owned by the shared wrapper).
      const msg = msgOf({ type: 'message_end', context_usage: { input_tokens: 100, cache_read_input_tokens: 20 }, message: { usage: { input: 999 } } }, AgentProvider.PI)
      applyNotificationMetadata('u-normalized', msg, parseMessageContent(msg), s, 'live')
      expect(s.agentSessionStore.getInfo('u-normalized').contextUsage).toEqual({ inputTokens: 100, cacheCreationInputTokens: 0, cacheReadInputTokens: 20 })
      dispose()
    })
  })

  it('does NOT fold usage/cost from a non-AGENT (LEAPMUX) row — the source gate survives the moved call site', () => {
    // Every provider persists its usage frames AGENT-source; a LEAPMUX/USER row carrying
    // total_cost_usd / context_usage / message.usage must be ignored, exactly as the old
    // applyAgentLifecycleAndUsage (msg.source === AGENT) gate ensured before this extraction moved.
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
    tabs: TabStores,
    sc: { agentId: string, status: AgentStatus, startupMessage?: string },
  ) {
    const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
    tabs.metadata.patch(sc.agentId, {
      ...(hasStatus ? { agentStatus: sc.status } : {}),
      ...(sc.status === AgentStatus.STARTING
        ? { startupMessage: sc.startupMessage ?? '' }
        : hasStatus ? { startupMessage: '' } : {}),
    })
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

  /**
   * A re-broadcast that changed no current value must not wake readers of
   * `optionValues` -- but this producer is deliberately NOT where that is
   * enforced. It emits the freshly-derived record and `tabMetadata.patch` drops
   * the write when the content matches (`sameStoredValue`), which covers this
   * producer, the others, and any written later. Asserted through the store for
   * that reason: a producer-side assertion would keep passing while the rule
   * moved out from under it.
   */
  it('lets the write point drop a re-broadcast that changes no current value', () => {
    const metadata = createTabMetadataStore()
    metadata.patch('a1', { optionValues: { model: 'opus' } })
    const stored = metadata.get('a1')!.optionValues

    const prev: AgentTab = { type: TabType.AGENT, id: 'a1', workspaceId: 'ws-1', optionValues: { model: 'opus' } }
    const fields = resolveSettingsTabFields(prev, [group('model', 'opus')], new Set())
    metadata.patch('a1', fields)

    // Same content -> the stored reference survives, so `<For>` rows keyed on
    // the assembled tab are not torn down and rebuilt.
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

  // The consequence, end to end: the identity guard above is only worth anything if it
  // actually keeps the tab object -- and with it the `<For>` row -- alive across a push.
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

      // `<For>` IS `mapArray`: a row body runs once per item IDENTITY, so a body that
      // re-runs is a real remount of TileRenderer's agent pane.
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

/**
 * The statusChange arm for an agent the user is NOT looking at.
 *
 * The sidebar renders every workspace, so a background row must be as correct
 * as a foreground one. This arm used to hand-roll a subset of the foreground
 * patch, which is how the pending-message drain and four metadata fields fell
 * out of it. The drain is the one with a permanent consequence: it fires on the
 * single STARTING -> ACTIVE/STARTUP_FAILED edge, and that edge is never
 * replayed, so a message composed against a starting agent in a background
 * workspace was stranded at "Queued" for the life of the page.
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

  it('drains a message queued against a starting agent', () => {
    createRoot((dispose) => {
      const { view, metadata, chatStore, selection, repoGitStore } = backgroundStores()
      const agentSessionStore = createAgentSessionStore()
      chatStore.pendingOutbound.enqueue('bg-1', { localId: 'l1', content: 'hi', attachments: [] })

      handleAgentStatusChange(
        'bg-1',
        { agentId: 'bg-1', status: AgentStatus.STARTUP_FAILED, startupError: 'boom', optionGroups: [] } as unknown as AgentStatusChange,
        'live',
        { chatStore, view, metadata, selection, getActiveWorkspaceId: () => WS, controlStore: createControlStore(), agentSessionStore, repoGitStore },
        createLoadingSignal(),
        () => {},
        undefined,
      )

      expect(chatStore.pendingOutbound.take('bg-1'), 'the queue must not outlive the transition').toHaveLength(0)
      expect(chatStore.messageErrors().l1, 'and the user has to be told why').toBe('Agent failed to start')
      dispose()
    })
  })

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
  function makeStores() {
    const tabs = makeTabStores()
    tabs.addAgent('agent-1', { agentStatus: AgentStatus.INACTIVE })
    const controlStore = createControlStore()
    controlStore.addRequest('agent-1', { requestId: 'r1', agentId: 'agent-1', payload: {}, claimToken: 'tok-r1' })
    return {
      controlStore,
      agentSessionStore: createAgentSessionStore(),
      chatStore: createChatStore(),
      view: tabs.view,
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
      tabs,
    }
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
    tabs: TabStores,
    terminalId: string,
    msg: string | undefined,
  ) {
    const existing = tabs.view.all().find(
      (t): t is TerminalTab => t.type === TabType.TERMINAL && t.id === terminalId,
    )
    if (existing && existing.status !== TerminalStatus.READY && existing.status !== TerminalStatus.STARTING) {
      tabs.metadata.patch(terminalId, {
        terminalStatus: TerminalStatus.STARTING,
        startupMessage: msg || undefined,
      })
    }
    else if (existing?.status === TerminalStatus.STARTING && msg && msg !== existing.startupMessage) {
      tabs.metadata.patch(terminalId, { startupMessage: msg })
    }
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

  // Phase-0 ("Preparing working tree") labels are dispatched by the
  // worker as same-status STARTING events with the per-mode label.
  // Rolling back on failure uses the same pipe with the rollback label
  // and then transitions to STARTUP_FAILED. Both should be applied.
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
      chatStore.appendCommandStream('agent-1', 'span1', 'output', 'output') // marks span1 renderable

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

  /** The two stores handleAgentSessionInfo writes to. */
  function sessionInfoStores() {
    return { agentSessionStore: createAgentSessionStore(), chatStore: createChatStore() }
  }

  it('handleAgentSessionInfo consumes an agent_session_info message and applies its updates', () => {
    createRoot((dispose) => {
      const stores = sessionInfoStores()
      const msg = agentMessage({ type: 'agent_session_info', info: { total_cost_usd: 1.5 } })
      const handled = handleAgentSessionInfo('a1', parseMessageContent(msg), stores)
      // Returning true is the early-break signal: the caller must NOT persist it.
      expect(handled).toBe(true)
      expect(stores.agentSessionStore.getInfo('a1').totalCostUsd).toBe(1.5)
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
      stores.agentSessionStore.updateInfo('a1', { thinkingTokens: 500 })
      const msg = agentMessage({ type: 'agent_session_info', info: { thinking_tokens: 0 } })
      handleAgentSessionInfo('a1', parseMessageContent(msg), stores)
      expect(stores.agentSessionStore.getInfo('a1').thinkingTokens).not.toBe(500)
      dispose()
    })
  })

  it('handleAgentSessionInfo routes running_tool to the chat store, not the session store', () => {
    createRoot((dispose) => {
      const stores = sessionInfoStores()
      const msg = agentMessage({
        type: 'agent_session_info',
        info: { running_tool: { span_id: 'toolu_A', tool_name: 'Bash', elapsed_seconds: 30 } },
      })
      expect(handleAgentSessionInfo('a1', parseMessageContent(msg), stores)).toBe(true)
      expect(stores.chatStore.getToolProgress('a1', 'toolu_A')).toEqual({ elapsedSeconds: 30 })
      // It is span-keyed state, so it must not leak into AgentSessionInfo (which is
      // persisted to localStorage minus its ephemeral keys).
      expect(stores.agentSessionStore.getInfo('a1')).toEqual({})
      dispose()
    })
  })

  it('handleResultDivider does not fire onTurnEnd — AgentTurnEnd owns that', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const stores = {
        agentSessionStore: createAgentSessionStore(),
        chatStore: createChatStore(),
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        getActiveWorkspaceId: () => WS,
      }
      const turnEnds: string[] = []
      const msg = agentMessage({ type: 'result', subtype: 'success', total_cost_usd: 0.25 })
      const parsed = parseMessageContent(msg)

      handleResultDivider('a1', msg, parsed, stores, 'catchingUp')
      handleResultDivider('a1', msg, parsed, stores, 'live')
      expect(turnEnds).toEqual([])
      expect(stores.agentSessionStore.getInfo('a1').totalCostUsd).toBe(0.25)
      dispose()
    })
  })

  /** A span (commandExecution/fileChange/reasoning) row carrying its `item` payload. */
  // Command streams (and the item-shape completion check) are a Codex feature, so span fixtures
  // carry the Codex provider -- clearCompletedSpanStream dispatches commandSpanSuperseded per plugin.
  function spanMessage(item: unknown, spanType: string, spanId = 'span1') {
    return {
      id: 'm1',
      source: MessageSource.AGENT,
      content: new TextEncoder().encode(JSON.stringify({ item })),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      spanId,
      spanType,
      agentProvider: AgentProvider.CODEX,
    } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]
  }

  it('clearCompletedSpanStream reclaims a COMPLETED span command stream', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      chatStore.appendCommandStream('a1', 'span1', 'output', 'output')
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
      chatStore.appendCommandStream('a1', 'span1', 'output', 'output')

      // A still-running span (status != completed) must keep its live stream.
      const msg = spanMessage({ type: 'commandExecution', status: 'in_progress' }, 'commandExecution')
      clearCompletedSpanStream('a1', msg, parseMessageContent(msg), chatStore)
      expect(chatStore.getCommandStream('a1', 'span1')).toHaveLength(1)
      dispose()
    })
  })

  it('handleAgentMessage does not clear a completed span stream when the row is dropped beyond the window', () => {
    createRoot((dispose) => {
      const tabs = makeTabStores()
      const stores = {
        agentSessionStore: createAgentSessionStore(),
        chatStore: createChatStore(),
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        getActiveWorkspaceId: () => WS,
      }
      stores.chatStore.setMessages('a1', Array.from({ length: 50 }, (_, i) => ({
        ...agentMessage({ type: 'assistant' }),
        id: `m${i + 1}`,
        seq: BigInt(i + 1),
      })))
      stores.chatStore.trimNewestEnd('a1', 30) // hasMoreNewer=true; seq 60 is recorded but not inserted.
      stores.chatStore.appendCommandStream('a1', 'span1', 'output', 'output')
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

  it('applyAgentLifecycle clears a stale Codex turn id on thread/started', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('a1', { codexTurnId: 'turn-123' })
      const msg = agentMessage({ method: 'thread/started' }, AgentProvider.CODEX)
      applyAgentLifecycle('a1', msg, parseMessageContent(msg), agentSessionStore)
      // A new thread starts idle: the stale turn id is cleared so the chat shows its
      // empty state instead of a phantom thinking indicator.
      expect(agentSessionStore.getInfo('a1').codexTurnId).toBe('')
      dispose()
    })
  })

  it('applyAgentLifecycle skips a non-AGENT message (the source gate)', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('a1', { codexTurnId: 'turn-123' })
      // A USER-source message carrying a thread/started method must be ignored, so the
      // turn id survives -- the gate that keeps a hidden-classified lifecycle item from
      // being processed off a non-AGENT row.
      const msg = { ...agentMessage({ method: 'thread/started' }), source: MessageSource.USER }
      applyAgentLifecycle('a1', msg, parseMessageContent(msg), agentSessionStore)
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
  const argStores = () => {
    const tabs = makeTabStores()
    return {
      agentSessionStore: createAgentSessionStore(),
      chatStore: createChatStore(),
      view: tabs.view,
      metadata: tabs.metadata,
      selection: tabs.selection,
      getActiveWorkspaceId: () => WS,
      controlStore: createControlStore(),
      repoGitStore: createRepoGitStore(),
      tabs,
    }
  }

  describe('handleStreamChunk', () => {
    it('accumulates free-form streaming text when there is no spanId', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        handleStreamChunk('a1', { delta: enc('hello '), spanId: '', method: '', agentProvider: AgentProvider.CODEX } as unknown as AgentStreamChunk, chatStore)
        handleStreamChunk('a1', { delta: enc('world'), spanId: '', method: '', agentProvider: AgentProvider.CODEX } as unknown as AgentStreamChunk, chatStore)
        expect(chatStore.streamingText.get('a1')).toBe('hello world')
        dispose()
      })
    })

    it('routes a spanId chunk to the command-stream buffer, not the free-form text', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        handleStreamChunk('a1', { delta: enc('out'), spanId: 's1', method: 'bash', agentProvider: AgentProvider.CODEX } as unknown as AgentStreamChunk, chatStore)
        expect(chatStore.streamingText.get('a1')).toBe('') // NOT the free-form text
        expect(chatStore.getCommandStream('a1', 's1').map(seg => seg.text).join('')).toContain('out')
        dispose()
      })
    })

    it('resolves the segment kind from the CHUNK\'s own agentProvider, not a tab lookup', () => {
      // Regression guard: the chunk carries its authoritative provider (backend stamps it on every
      // AgentStreamChunk). A Codex reasoning-summary delta must map to `reasoning_summary` purely
      // from the chunk -- no tab is registered here, so a tab-provider lookup would resolve
      // undefined and mis-bucket every Codex delta as plain `output`.
      createRoot((dispose) => {
        const chatStore = createChatStore()
        handleStreamChunk('a1', { delta: enc('pondering'), spanId: 's1', method: 'item/reasoning/summaryTextDelta', agentProvider: AgentProvider.CODEX } as unknown as AgentStreamChunk, chatStore)
        const segs = chatStore.getCommandStream('a1', 's1')
        expect(segs).toHaveLength(1)
        expect(segs[0].kind).toBe('reasoning_summary')
        dispose()
      })
    })

    it('preserves a content-less reasoning_summary_break delta when the chunk provider maps it', () => {
      // `item/reasoning/summaryPartAdded` carries empty text and maps to `reasoning_summary_break`;
      // the store keeps it (the `!text && kind !== 'reasoning_summary_break'` guard). If the kind
      // degraded to `output` (the tab-lookup failure mode this dispatch avoids, when the tab is
      // still bare), the empty delta would be dropped entirely.
      createRoot((dispose) => {
        const chatStore = createChatStore()
        handleStreamChunk('a1', { delta: enc(''), spanId: 's1', method: 'item/reasoning/summaryPartAdded', agentProvider: AgentProvider.CODEX } as unknown as AgentStreamChunk, chatStore)
        const segs = chatStore.getCommandStream('a1', 's1')
        expect(segs).toHaveLength(1)
        expect(segs[0].kind).toBe('reasoning_summary_break')
        dispose()
      })
    })
  })

  describe('handleStreamEnd', () => {
    it('clears the free-form streaming text without badging', () => {
      createRoot((dispose) => {
        const chatStore = createChatStore()
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        tabs.addAgent('a2')
        tabs.selection.setActiveById(TabType.AGENT, 'a2')
        chatStore.streamingText.set('a1', 'partial')
        handleStreamEnd('a1', { spanId: '' } as unknown as AgentStreamEnd, { chatStore })
        expect(chatStore.streamingText.get('a1')).toBe('')
        expect(tabs.view.getAgentTab('a1')?.hasNotification).toBeFalsy()
        dispose()
      })
    })
  })

  describe('handleTurnEnd', () => {
    it('fires onTurnEnd with and without numToolUses', () => {
      createRoot((dispose) => {
        const tabs = makeTabStores()
        tabs.addAgent('a1')
        tabs.addAgent('a2')
        tabs.selection.setActiveById(TabType.AGENT, 'a2')
        const ended: Array<{ id: string, uses?: number }> = []
        const stores = { metadata: tabs.metadata, selection: tabs.selection, getActiveWorkspaceId: () => WS, view: tabs.view }
        handleTurnEnd('a1', {}, stores, (id, uses) => ended.push({ id, uses }))
        handleTurnEnd('a2', { numToolUses: 3 }, stores, (id, uses) => ended.push({ id, uses }))
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
        const stores = { metadata: tabs.metadata, selection: tabs.selection, getActiveWorkspaceId: () => WS, view: tabs.view }
        handleTurnEnd('a2', {}, stores, undefined)
        expect(tabs.view.getAgentTab('a2')?.hasNotification).not.toBe(true)
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
      expect(pending.get('t1')![0].endOffset).toBe(9n)
    })

    it('caps the queue so a never-mounting terminal cannot grow it without bound', () => {
      const pending = new Map<string, Array<{ data: Uint8Array, isSnapshot: boolean, endOffset: bigint }>>()
      // Enqueue well over the cap; only the newest MAX_PENDING_TERMINAL_FRAMES survive.
      let evicted = false
      for (let i = 0; i < MAX_PENDING_TERMINAL_FRAMES + 50; i++)
        evicted = enqueuePendingTerminalData(pending, 't1', { data: new Uint8Array([i]), isSnapshot: false, endOffset: BigInt(i) }) || evicted
      expect(pending.get('t1')).toHaveLength(MAX_PENDING_TERMINAL_FRAMES)
      // The oldest frames were dropped; the last surviving frame is the most recent.
      expect(pending.get('t1')!.at(-1)!.endOffset).toBe(BigInt(MAX_PENDING_TERMINAL_FRAMES + 49))
      // The eviction is reported: the caller must flag the terminal for a
      // full-snapshot resubscribe, because the dropped bytes leave a hole no
      // incremental delta can fill.
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
    const req = (agentId: string) =>
      ({ requestId: 'r1', agentId, payload: enc(JSON.stringify({ method: 'item/commandExecution/requestApproval' })) }) as unknown as AgentControlRequest

    it('skips a replayed (catch-up) request for an already-INACTIVE agent', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.INACTIVE })
        handleControlRequest('a1', req('a1'), simulatePhase('catchingUp'), s, undefined)
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
        dispose()
      })
    })

    it('adds a live request, badges a backgrounded tab, and ends the turn', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        s.tabs.addAgent('a2')
        s.tabs.selection.setActiveById(TabType.AGENT, 'a2')
        let ended = ''
        handleControlRequest('a1', req('a1'), 'live', s, id => void (ended = id))
        expect(s.controlStore.getRequests('a1')).toHaveLength(1)
        expect(s.tabs.view.getAgentTab('a1')?.hasNotification).toBe(true)
        expect(ended).toBe('a1')
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
        handleControlRequest('a2', req('a2'), 'live', s, undefined)
        expect(s.controlStore.getRequests('a2')).toHaveLength(1)
        expect(s.tabs.view.getAgentTab('a2')?.hasNotification).not.toBe(true)
        dispose()
      })
    })

    it('adds a catch-up request for an ACTIVE agent but does NOT run the live-only turn-end', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
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
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        const malformed = { requestId: 'r1', agentId: 'a1', payload: enc('{not json') } as unknown as AgentControlRequest
        expect(() => handleControlRequest('a1', malformed, 'live', s, undefined)).not.toThrow()
        expect(s.controlStore.getRequests('a1')).toHaveLength(0)
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
        handleControlRequest('a1', withToken, 'live', s, undefined)
        // The per-instance token from AgentControlRequest.claim_token must reach the store, since the
        // answer (handleControlResponse) echoes it back so the worker dedups the answer per instance.
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

    it('skips a payload-less sentinel without touching the tab or reporting worker-online', () => {
      createRoot((dispose) => {
        const s = argStores()
        s.tabs.addAgent('a1', { agentStatus: AgentStatus.ACTIVE })
        let online: boolean | undefined
        const sc = { agentId: 'a1', status: AgentStatus.UNSPECIFIED, workerOnline: false, optionGroups: [] } as unknown as AgentStatusChange
        handleAgentStatusChange('a1', sc, 'live', s, createLoadingSignal(), v => void (online = v), undefined)
        expect(s.tabs.view.getAgentTab('a1')?.agentStatus).toBe(AgentStatus.ACTIVE) // unchanged
        expect(online).toBeUndefined() // setWorkerOnline only on a full status snapshot
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
      codex_turn_id: 'turn-7',
      streaming_type: 'plan',
    })
    expect(updates.totalCostUsd).toBe(1.5)
    expect(updates.contextUsage).toMatchObject({ inputTokens: 100 })
    expect(updates.codexTurnId).toBe('turn-7')
    expect(updates.streamingType).toBe('plan')
  })

  it('deep-maps rate_limits tiers', () => {
    const updates = wireSessionInfoToUpdates({
      rate_limits: { five_hour: { status: 'allowed', utilization: 0.5 } },
    })
    expect(updates.rateLimits).toEqual({ five_hour: { status: 'allowed', utilization: 0.5 } })
  })

  // All eight tier fields, so a translation that drops one is caught here rather
  // than by a blank cell in the rate-limit popover. Only two of the eight had any
  // coverage before.
  it('translates every rate_limits tier field to its camelCase name', () => {
    const updates = wireSessionInfoToUpdates({
      rate_limits: {
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
    })
    expect(updates.rateLimits).toEqual({
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
    })
  })

  /**
   * A field the tier does not carry is ABSENT from the result, not present and
   * undefined. That is load-bearing and `toEqual` cannot see it: `toEqual`
   * ignores an undefined-valued key, but `agentSession.store.ts` decides whether
   * a tier changed with `shallowEqual`, which compares key COUNTS first. A tier
   * rehydrated from localStorage has lost its undefined keys (JSON.stringify
   * drops them), so a translation that emitted all eight keys would compare
   * unequal on every broadcast and write the store each time.
   *
   * Asserted on Object.keys for exactly that reason.
   */
  it('omits a tier field the payload does not carry, rather than setting it undefined', () => {
    const updates = wireSessionInfoToUpdates({
      rate_limits: { five_hour: { status: 'allowed', is_using_overage: false } },
    })
    const tier = (updates.rateLimits as Record<string, Record<string, unknown>>).five_hour
    expect(Object.keys(tier).sort()).toEqual(['isUsingOverage', 'status'])
    // A real `false` still lands -- only an ABSENT key is dropped.
    expect(tier.isUsingOverage).toBe(false)

    const sparse = wireSessionInfoToUpdates({ rate_limits: { five_hour: { status: 'allowed' } } })
    expect(Object.keys((sparse.rateLimits as Record<string, object>).five_hour)).toEqual(['status'])
  })

  it('drops a tier field whose wire value is the wrong type', () => {
    const updates = wireSessionInfoToUpdates({
      rate_limits: {
        five_hour: {
          status: 'allowed',
          utilization: '0.5',
          resets_at: '1700000000',
          is_using_overage: 'true',
          surpassed_threshold: null,
        },
      },
    })
    expect(Object.keys((updates.rateLimits as Record<string, object>).five_hour)).toEqual(['status'])
  })

  it('skips a tier that is not an object at all', () => {
    const updates = wireSessionInfoToUpdates({
      rate_limits: { five_hour: { status: 'allowed' }, weekly: 'nonsense', monthly: null },
    })
    expect(updates.rateLimits).toEqual({ five_hour: { status: 'allowed' } })
  })

  it('only forwards a positive numeric thinking_tokens estimate', () => {
    expect(wireSessionInfoToUpdates({ thinking_tokens: 230 }).thinkingTokens).toBe(230)
    // 0 (the zero-estimate first delta), NaN, and non-numbers are all dropped,
    // so the indicator never has to defend against "0 tokens" or a NaN.
    expect('thinkingTokens' in wireSessionInfoToUpdates({ thinking_tokens: 0 })).toBe(false)
    expect('thinkingTokens' in wireSessionInfoToUpdates({ thinking_tokens: Number.NaN })).toBe(false)
    expect('thinkingTokens' in wireSessionInfoToUpdates({ thinking_tokens: '5' })).toBe(false)
  })

  it('skips keys that are absent or fail their type guard', () => {
    // A non-number cost and a context_usage with no token data contribute nothing.
    expect(wireSessionInfoToUpdates({ total_cost_usd: 'free', context_usage: {} })).toEqual({})
  })

  it('keeps an empty-string streaming_type (the "not streaming plan" signal)', () => {
    // streaming_type uses `!== undefined`, so "" is a meaningful value, not a skip.
    expect(wireSessionInfoToUpdates({ streaming_type: '' }).streamingType).toBe('')
  })
})

describe('shouldClearThinkingTokensForMessage', () => {
  const agentMsg = (parentSpanId = '') => ({ source: MessageSource.AGENT, parentSpanId })
  // A plugin that always clears, mirroring Claude's telemetry-driven counter.
  const alwaysClears = { clearsThinkingTokensForMessage: () => true }

  it('clears on a main-agent AGENT message by default (empty parentSpanId)', () => {
    expect(shouldClearThinkingTokensForMessage(agentMsg(''), undefined)).toBe(true)
  })

  it('does NOT clear on a subagent message by default (nested under a span)', () => {
    expect(shouldClearThinkingTokensForMessage(agentMsg('collab-span'), undefined)).toBe(false)
  })

  it('does NOT clear on non-AGENT messages, even with an always-clear plugin', () => {
    expect(shouldClearThinkingTokensForMessage({ source: MessageSource.USER, parentSpanId: '' }, undefined)).toBe(false)
    expect(shouldClearThinkingTokensForMessage(
      { source: MessageSource.LEAPMUX, parentSpanId: '' },
      alwaysClears,
    )).toBe(false)
  })

  it('delegates the AGENT-message policy to the provider plugin', () => {
    // A plugin (e.g. Claude) that always clears overrides the default main-scope
    // gate, so even a message with a non-empty parentSpanId clears.
    expect(shouldClearThinkingTokensForMessage(agentMsg('sys-tu-999'), alwaysClears)).toBe(true)
  })
})

/**
 * The worker-offline sweep walks `view.all()` -- every tab in the ACCOUNT, not
 * one workspace. Both arms must therefore filter on `workerId`: a tab hosted by
 * any other worker still has its transport, and clearing an agent's
 * `streamingText` throws away deltas that are never resent while flipping it
 * INACTIVE hides a thinking indicator for a turn that is still running.
 *
 * The agent arm was missing that filter. Before every workspace became live it
 * was a one-workspace bug; the widening made it account-wide.
 */
describe('collectWorkerOfflineTargets', () => {
  const tab = (over: Partial<Tab>): Tab => ({
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
    expect(terminals.size, 'a FILE tab is neither arm').toBe(0)
  })

  it('returns nothing for a worker that hosts none of these tabs', () => {
    const { terminals, agents } = collectWorkerOfflineTargets([tab({ workerId: 'w1' })], 'w-unknown')
    expect(agents).toEqual([])
    expect(terminals.size).toBe(0)
  })
})

/**
 * What the offline sweep actually reclaims, per agent.
 *
 * A dropped link ends the turn silently: the worker sends no result row, no
 * turn-end divider and no INACTIVE status change, and every other reclamation
 * site in the app runs off one of those three. So each live indicator this sweep
 * forgets stays frozen on screen for the whole outage -- and the sweep is the
 * only place that can catch it.
 */
describe('clearOfflineAgentState', () => {
  function seededStores() {
    const chatStore = createChatStore()
    const agentSessionStore = createAgentSessionStore()
    chatStore.streamingText.set('a1', 'half a sentence')
    chatStore.appendCommandStream('a1', 'toolu_A', 'output', 'mid-line')
    chatStore.applyToolProgress('a1', { spanId: 'toolu_A', elapsedSeconds: 30 })
    chatStore.applyToolProgress('a1', { spanId: 'toolu_B', elapsedSeconds: 90 })
    agentSessionStore.updateInfo('a1', { thinkingTokens: 500 })
    return { chatStore, agentSessionStore }
  }

  it('drops every live indicator the outage would otherwise freeze', () => {
    createRoot((dispose) => {
      const s = seededStores()
      clearOfflineAgentState('a1', s)

      expect(s.chatStore.streamingText.get('a1')).toBe('')
      expect(s.chatStore.getCommandStream('a1', 'toolu_A')).toHaveLength(0)
      // The two the sweep used to miss. Each badge would otherwise read "30s" /
      // "1m 30s" for as long as the worker stayed away.
      expect(s.chatStore.getToolProgress('a1', 'toolu_A')).toBeUndefined()
      expect(s.chatStore.getToolProgress('a1', 'toolu_B')).toBeUndefined()
      expect(s.agentSessionStore.getInfo('a1').thinkingTokens).toBeUndefined()
      dispose()
    })
  })

  it('leaves another agent on a healthy worker untouched', () => {
    createRoot((dispose) => {
      const s = seededStores()
      s.chatStore.applyToolProgress('a2', { spanId: 'toolu_A', elapsedSeconds: 60 })
      s.agentSessionStore.updateInfo('a2', { thinkingTokens: 700 })

      clearOfflineAgentState('a1', s)

      expect(s.chatStore.getToolProgress('a2', 'toolu_A')?.elapsedSeconds).toBe(60)
      expect(s.agentSessionStore.getInfo('a2').thinkingTokens).toBe(700)
      dispose()
    })
  })

  it('is safe for an agent that has nothing live', () => {
    createRoot((dispose) => {
      const s = { chatStore: createChatStore(), agentSessionStore: createAgentSessionStore() }
      expect(() => clearOfflineAgentState('never-ran', s)).not.toThrow()
      dispose()
    })
  })
})

/**
 * What the user is told when a chat-history load fails.
 *
 * A dropped connection fails every background load at once, so each one that
 * announced its own failure turned a single outage into a row of toasts -- and
 * the copy the user read was `err.message`, which names our own plumbing
 * ("channel not open"). The reconnect loop in useWatchEventsStreams announces
 * the outage once, in plain language, and re-promotes the agent when the link
 * returns, which re-runs this load. A failure the WORKER reported is the
 * opposite case: nothing is retrying it, so it must still reach the user.
 *
 * Drives the real hook, not a helper, because the defect was at the call site:
 * every part of the decision can be right and the site can still call the wrong
 * toast.
 */
describe('useWorkspaceConnection chat history load', () => {
  /**
   * Mount the hook over one active agent tab whose history has never loaded,
   * which is the condition its lazy-load effect fires on.
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
        view: tabs.view,
        metadata: tabs.metadata,
        selection: tabs.selection,
        controlStore: createControlStore(),
        agentSessionStore: createAgentSessionStore(),
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
    }
    finally {
      dispose()
    }
  })
})
