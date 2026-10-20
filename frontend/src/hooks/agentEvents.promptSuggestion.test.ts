import type { AgentMessageStores } from './agentEvents'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { clearPerTurnLiveState, handleAgentSessionInfo } from './agentEvents'
import { clearOfflineAgentState } from './useWorkspaceConnection'

type Stores = Pick<AgentMessageStores, 'agentSessionStore' | 'chatStore'>

function withStores(run: (stores: Stores) => void) {
  createRoot((dispose) => {
    try {
      run({ agentSessionStore: createAgentSessionStore(), chatStore: createChatStore() })
    }
    finally {
      dispose()
    }
  })
}

function receive(stores: Stores, info: unknown, transcriptOnly = false) {
  const message = create(AgentChatMessageSchema, {
    id: 'suggestion',
    source: MessageSource.AGENT,
    agentProvider: AgentProvider.QWEN_CODE,
    contentCompression: ContentCompression.NONE,
    content: new TextEncoder().encode(JSON.stringify({ type: 'agent_session_info', info })),
  })
  return handleAgentSessionInfo('a', parseMessageContent(message), stores, { phase: 'live' }, transcriptOnly)
}

describe('handleAgentSessionInfo prompt_suggestion', () => {
  it('sets a suggestion and withdraws it on null', () => {
    withStores((stores) => {
      expect(receive(stores, { prompt_suggestion: { text: 'run the tests', extra: 'ignore' } })).toBe(true)
      expect(stores.chatStore.getPromptSuggestion('a')).toEqual({ text: 'run the tests' })
      receive(stores, { prompt_suggestion: null })
      expect(stores.chatStore.getPromptSuggestion('a')).toBeUndefined()
    })
  })

  it('ignores malformed suggestion values', () => {
    withStores((stores) => {
      stores.chatStore.setPromptSuggestion('a', { text: 'keep' })
      for (const value of [undefined, false, 0, 'text', [], {}, { text: 1 }, { text: '' }, { text: '  ' }]) {
        receive(stores, { prompt_suggestion: value })
        expect(stores.chatStore.getPromptSuggestion('a')).toEqual({ text: 'keep' })
      }
    })
  })

  it('ignores malformed session-info containers', () => {
    withStores((stores) => {
      stores.chatStore.setPromptSuggestion('a', { text: 'keep' })
      for (const info of [null, 'invalid', 1, true, []]) {
        expect(() => receive(stores, info)).not.toThrow()
        expect(stores.chatStore.getPromptSuggestion('a')).toEqual({ text: 'keep' })
      }
    })
  })

  it('ignores transcript-only live state', () => {
    withStores((stores) => {
      receive(stores, { prompt_suggestion: { text: 'old' } }, true)
      expect(stores.chatStore.getPromptSuggestion('a')).toBeUndefined()
    })
  })

  it.each(['turn', 'offline', 'removed'] as const)('clears a suggestion at the %s boundary', (boundary) => {
    withStores((stores) => {
      stores.chatStore.setPromptSuggestion('a', { text: 'old' })
      if (boundary === 'turn')
        clearPerTurnLiveState('a', stores)
      else if (boundary === 'offline')
        clearOfflineAgentState('a', stores)
      else
        stores.chatStore.forgetAgent('a')
      expect(stores.chatStore.getPromptSuggestion('a')).toBeUndefined()
    })
  })
})
