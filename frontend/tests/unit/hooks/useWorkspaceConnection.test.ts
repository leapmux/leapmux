import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentProvider, AgentStatus, ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { extractResultMetadata, parseMessageContent } from '~/lib/messageParser'
import { createAgentStore } from '~/stores/agent.store'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore, MAX_BACKGROUND_CHAT_MESSAGES, MAX_LOADED_CHAT_MESSAGES } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { createTabStore, TabType } from '~/stores/tab.store'

/**
 * These tests verify the control-request guard in useWorkspaceConnection's
 * handleAgentEvent. Because handleAgentEvent is a closure that depends on
 * gRPC streams, we simulate its logic with real stores to verify the
 * invariant: control requests must not be added for INACTIVE agents.
 */
describe('controlRequest guard for inactive agents', () => {
  function makeAgent(id: string, status: AgentStatus) {
    return { id, status } as Parameters<ReturnType<typeof createAgentStore>['addAgent']>[0]
  }

  it('should not add control request when agent is INACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.INACTIVE))

      // Simulate the guard in useWorkspaceConnection's controlRequest handler:
      // const agentEntry = agentStore.state.agents.find(a => a.id === cr.agentId)
      // if (agentEntry?.status === AgentStatus.INACTIVE) break
      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (agentEntry?.status !== AgentStatus.INACTIVE) {
        controlStore.addRequest('agent-1', {
          requestId: 'r1',
          agentId: 'agent-1',
          payload: { method: 'item/commandExecution/requestApproval' },
        })
      }

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })

  it('should add control request when agent is ACTIVE', () => {
    createRoot((dispose) => {
      const agentStore = createAgentStore()
      const controlStore = createControlStore()

      agentStore.addAgent(makeAgent('agent-1', AgentStatus.ACTIVE))

      const agentEntry = agentStore.state.agents.find(a => a.id === 'agent-1')
      if (agentEntry?.status !== AgentStatus.INACTIVE) {
        controlStore.addRequest('agent-1', {
          requestId: 'r1',
          agentId: 'agent-1',
          payload: { method: 'item/commandExecution/requestApproval' },
        })
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
      controlStore.addRequest('agent-1', {
        requestId: 'r1',
        agentId: 'agent-1',
        payload: { method: 'item/commandExecution/requestApproval' },
      })

      expect(controlStore.getRequests('agent-1')).toHaveLength(1)

      // Simulate statusChange INACTIVE → controlStore.clearAgent()
      agentStore.updateAgent('agent-1', { status: AgentStatus.INACTIVE })
      controlStore.clearAgent('agent-1')

      expect(controlStore.getRequests('agent-1')).toHaveLength(0)
      dispose()
    })
  })
})

describe('background agent history trimming', () => {
  function makeUserMessage(id: string, seq: bigint) {
    return {
      id,
      role: MessageRole.USER,
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
        tabStore.state.activeTabKey !== `${TabType.AGENT}:background-agent`
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
        tabStore.state.activeTabKey !== `${TabType.AGENT}:active-agent`
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
})

describe('codex result replay handling', () => {
  it('clears stale codexTurnId when a persisted turn/completed result is replayed', () => {
    createRoot((dispose) => {
      const agentSessionStore = createAgentSessionStore()
      agentSessionStore.updateInfo('agent-1', { codexTurnId: 'turn-stale' })

      const msg = {
        id: 'm1',
        role: MessageRole.RESULT,
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
