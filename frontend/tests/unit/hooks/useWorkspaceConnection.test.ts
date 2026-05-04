import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AgentStatus, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { TerminalStatus } from '~/generated/leapmux/v1/terminal_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { extractResultMetadata, parseMessageContent } from '~/lib/messageParser'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES, MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createTabStore } from '~/stores/tab.store'

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
    return { id, status } as Parameters<ReturnType<typeof createAgentStore>['addAgent']>[0]
  }

  function makeRequest(requestId: string, agentId: string) {
    return { requestId, agentId, payload: { method: 'item/commandExecution/requestApproval' } }
  }

  it('should not add catch-up control request when agent is INACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.INACTIVE))

      // Simulate the guard in useWorkspaceConnection's controlRequest handler:
      // if (catchUpPhase !== 'live' && agentEntry?.status === AgentStatus.INACTIVE) break
      const catchUpPhase = 'catchingUp'
      // const agentEntry = agentStore.state.agents.find(a => a.id === cr.agentId)
      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.status === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should revive stale INACTIVE state and add live control request', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.INACTIVE))

      const catchUpPhase = 'live'
      if (catchUpPhase === 'live') {
        const current = agentStore.getById('agent-1')
        if (current?.status === AgentStatus.INACTIVE)
          agentStore.updateAgent('agent-1', { status: AgentStatus.ACTIVE })
      }
      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.status === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(agentStore.getById('agent-1')?.status).toBe(AgentStatus.ACTIVE)
      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should add control request when agent is ACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))

      const catchUpPhase = 'catchingUp'
      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.status === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests when agent becomes INACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate statusChange INACTIVE → controlStore.clearAgent()
      agentStore.updateAgent('agent-1', { status: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should preserve pending control requests across short connection blips', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate worker-offline transition: agent goes INACTIVE but pending
      // control requests must survive transient transport blips.
      agentStore.updateAgent('agent-1', { status: AgentStatus.INACTIVE })

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)
      dispose()
    })
  })

  it('should clear control requests on worker restart because agent processes stop', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))
      controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate the statusChange handler during catch-up replay after a
      // worker restart: the replayed INACTIVE statusChange triggers clearAgent
      // because the agent process no longer exists.
      agentStore.updateAgent('agent-1', { status: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)

      // A replayed controlRequest for the now-INACTIVE agent must be skipped
      // by the controlRequest-case guard in useWorkspaceConnection: catch-up
      // replay + INACTIVE → break.
      const catchUpPhase = 'catchingUp'
      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (!(catchUpPhase !== 'live' && agentEntry?.status === AgentStatus.INACTIVE)) {
        controlStore.addRequest('agent-1', makeRequest('r1', 'agent-1'))
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should preserve pending control requests across WatchEvents stream restarts', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))
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
        chatStore.trimOldMessages('background-agent', MAX_BACKGROUND_CHAT_MESSAGES)
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
        chatStore.trimOldMessages('active-agent', MAX_LOADED_CHAT_MESSAGES)
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
        chatStore.trimOldMessages('visible-agent', MAX_BACKGROUND_CHAT_MESSAGES)
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

describe('streaming text preservation', () => {
  it('keeps accumulated assistant streaming text when a persisted user message arrives mid-stream', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()

      chatStore.setStreamingText('agent-1', 'Hello')

      const echoedUserMessage = {
        id: 'server-user-1',
        source: MessageSource.USER,
        content: new TextEncoder().encode(JSON.stringify({ content: 'follow-up' })),
        contentCompression: ContentCompression.NONE,
        seq: 1n,
      } as Parameters<ReturnType<typeof createChatStore>['addMessage']>[1]

      chatStore.addMessage('agent-1', echoedUserMessage)

      chatStore.setStreamingText('agent-1', `${chatStore.state.streamingText['agent-1'] ?? ''} world`)

      expect(chatStore.state.streamingText['agent-1']).toBe('Hello world')

      chatStore.clearStreamingText('agent-1')
      expect(chatStore.state.streamingText['agent-1']).toBe('')
      dispose()
    })
  })

  it('clears top-level streaming text when a persisted codex agentMessage completion arrives', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()

      chatStore.setStreamingText('agent-1', 'Hello')

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
        chatStore.clearStreamingText('agent-1')

      expect(chatStore.state.streamingText['agent-1']).toBe('')
      dispose()
    })
  })

  it('clears top-level plan streaming text and streamingType when a persisted codex plan completion arrives', () => {
    createRoot((dispose) => {
      const chatStore = createChatStore()
      const agentSessionStore = createAgentSessionStore()

      chatStore.setStreamingText('agent-1', '# Plan\n')
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
        chatStore.clearStreamingText('agent-1')
        agentSessionStore.updateInfo('agent-1', { streamingType: '' })
      }

      expect(chatStore.state.streamingText['agent-1']).toBe('')
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
    agentStore: ReturnType<typeof createAgentStore>,
    sc: { agentId: string, status: AgentStatus, startupMessage?: string },
  ) {
    const hasStatus = sc.status !== AgentStatus.UNSPECIFIED
    agentStore.updateAgent(sc.agentId, {
      ...(hasStatus ? { status: sc.status } : {}),
      ...(sc.status === AgentStatus.STARTING
        ? { startupMessage: sc.startupMessage ?? '' }
        : hasStatus ? { startupMessage: '' } : {}),
    })
  }

  it('stores startupMessage while STARTING so the startup panel can render the phase label', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      agentStore.addAgent({ id: 'agent-1', status: AgentStatus.STARTING } as Parameters<typeof agentStore.addAgent>[0])

      applyStatusChange(agentStore, {
        agentId: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      })
      expect(agentStore.state.agents.find(a => a.id === 'agent-1')?.startupMessage).toBe('Checking Git status…')

      applyStatusChange(agentStore, {
        agentId: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Starting Claude Code…',
      })
      expect(agentStore.state.agents.find(a => a.id === 'agent-1')?.startupMessage).toBe('Starting Claude Code…')
      dispose()
    })
  })

  it('clears startupMessage on ACTIVE so the label does not linger after startup succeeds', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      agentStore.addAgent({
        id: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Starting Claude Code…',
      } as Parameters<typeof agentStore.addAgent>[0])

      applyStatusChange(agentStore, { agentId: 'agent-1', status: AgentStatus.ACTIVE })

      expect(agentStore.state.agents.find(a => a.id === 'agent-1')?.startupMessage).toBe('')
      dispose()
    })
  })

  it('clears startupMessage on STARTUP_FAILED so the error banner replaces the phase label', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      agentStore.addAgent({
        id: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      } as Parameters<typeof agentStore.addAgent>[0])

      applyStatusChange(agentStore, { agentId: 'agent-1', status: AgentStatus.STARTUP_FAILED })

      expect(agentStore.state.agents.find(a => a.id === 'agent-1')?.startupMessage).toBe('')
      dispose()
    })
  })

  it('leaves startupMessage alone on status-less events (UNSPECIFIED) so catchUp sentinels do not wipe live phases', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      agentStore.addAgent({
        id: 'agent-1',
        status: AgentStatus.STARTING,
        startupMessage: 'Checking Git status…',
      } as Parameters<typeof agentStore.addAgent>[0])

      applyStatusChange(agentStore, { agentId: 'agent-1', status: AgentStatus.UNSPECIFIED })

      expect(agentStore.state.agents.find(a => a.id === 'agent-1')?.startupMessage).toBe('Checking Git status…')
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
      t => t.type === TabType.TERMINAL && t.id === terminalId,
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

      const tab = tabStore.state.tabs.find(t => t.type === TabType.TERMINAL && t.id === 'term-1')
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

      const tab = tabStore.state.tabs.find(t => t.type === TabType.TERMINAL && t.id === 'term-1')
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

      const tab = tabStore.state.tabs.find(t => t.type === TabType.TERMINAL && t.id === 'term-1')
      expect(tab?.startupMessage).toBe('Creating worktree "feature/x"…')
      dispose()
    })
  })

  it('applies a following "Rolling back worktree" label on same-status STARTING', () => {
    createRoot((dispose) => {
      const tabStore = createTabStore()
      tabStore.addTab({ type: TabType.TERMINAL, id: 'term-1', status: TerminalStatus.STARTING, startupMessage: 'Creating worktree "feature/x"…' })

      applyStarting(tabStore, 'term-1', 'Rolling back worktree "feature/x"…')

      const tab = tabStore.state.tabs.find(t => t.type === TabType.TERMINAL && t.id === 'term-1')
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
      t => t.type === TabType.TERMINAL && t.id === terminalId,
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

      const tab = tabStore.state.tabs.find(t => t.type === TabType.TERMINAL && t.id === 'term-1')
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
