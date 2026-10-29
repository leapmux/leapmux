import type { AgentMessageStores } from './agentEvents'
import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, AgentStatusChangeSchema, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { createControlStore } from '~/stores/control.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { applyNotificationMetadata, handleAgentInactive, handleAgentSessionInfo, handleResultDivider, wireRateLimitUpdateFromSessionInfo } from './agentEvents'
import { clearOfflineAgentState } from './useWorkspaceConnection'
import '~/components/chat/providers'

function compactionStores(): AgentMessageStores & { controlStore: ReturnType<typeof createControlStore> } {
  installTestBridge({ workspaceId: 'compaction-workspace' })
  const tabs = createTestTabStores('compaction-workspace')
  return {
    agentSessionStore: createAgentSessionStore(),
    chatStore: createChatStore(),
    controlStore: createControlStore(),
    view: tabs.view,
    metadata: tabs.metadata,
    selection: tabs.selection,
    getActiveWorkspaceId: () => 'compaction-workspace',
  }
}

function compactionMessage(content: unknown) {
  return create(AgentChatMessageSchema, {
    id: 'message',
    source: MessageSource.AGENT,
    content: new TextEncoder().encode(JSON.stringify(content)),
    contentCompression: ContentCompression.NONE,
    agentProvider: AgentProvider.CLAUDE_CODE,
    seq: 1n,
  })
}

describe('wireRateLimitUpdateFromSessionInfo calendar resets', () => {
  it('decodes optional labels and valid dates while retaining zero usage', () => {
    expect(wireRateLimitUpdateFromSessionInfo({
      rate_limits: {
        mode: 'replace',
        values: {
          credit: { label: 'Plan — Credits', resets_on: '2024-02-29', utilization: 0 },
          malformed: { label: 42, resets_on: '2026-02-29' },
        },
      },
    })).toEqual({
      mode: 'replace',
      values: {
        credit: { label: 'Plan — Credits', resetsOn: '2024-02-29', utilization: 0 },
        malformed: {},
      },
    })
  })
})

describe('handleAgentSessionInfo compaction', () => {
  it('sets and clears the live compaction', () => {
    createRoot((dispose) => {
      const stores = compactionStores()
      const receive = (compaction: unknown) => handleAgentSessionInfo('a', parseMessageContent(compactionMessage({
        type: 'agent_session_info',
        info: { compaction },
      })), stores)
      expect(receive({ trigger: 'auto', extra: 'ignored' })).toBe(true)
      expect(stores.chatStore.getLiveCompaction('a')).toEqual({ trigger: 'auto' })
      receive({})
      expect(stores.chatStore.getLiveCompaction('a')).toEqual({})
      for (const value of [false, 'invalid', 0, []]) {
        receive(value)
        expect(stores.chatStore.getLiveCompaction('a')).toEqual({})
      }
      receive(null)
      expect(stores.chatStore.getLiveCompaction('a')).toBeUndefined()
      dispose()
    })
  })

  it('keeps the live compaction at a live turn-end divider', () => {
    createRoot((dispose) => {
      const stores = compactionStores()
      stores.chatStore.setLiveCompaction('a', { trigger: 'auto' })
      const message = compactionMessage({ type: 'result', subtype: 'success' })
      handleResultDivider('a', message, parseMessageContent(message), stores, 'live')
      expect(stores.chatStore.getLiveCompaction('a')).toEqual({ trigger: 'auto' })
      dispose()
    })
  })

  it.each([
    'inactive',
    'offline',
    'removed',
    'context-cleared',
  ] as const)('clears the compaction when the agent is %s', (reason) => {
    createRoot((dispose) => {
      const stores = compactionStores()
      stores.chatStore.setLiveCompaction('a', {})
      if (reason === 'inactive') {
        handleAgentInactive('a', create(AgentStatusChangeSchema), 'live', stores)
      }
      else if (reason === 'offline') {
        clearOfflineAgentState('a', stores)
      }
      else if (reason === 'removed') {
        stores.chatStore.forgetAgent('a')
      }
      else {
        const message = compactionMessage({ type: 'context_cleared' })
        applyNotificationMetadata('a', message, parseMessageContent(message), stores, 'live')
      }
      expect(stores.chatStore.getLiveCompaction('a')).toBeUndefined()
      dispose()
    })
  })

  it('ignores transcript-only live state', () => {
    createRoot((dispose) => {
      const stores = compactionStores()
      const parsed = parseMessageContent(compactionMessage({
        type: 'agent_session_info',
        info: { compaction: {} },
      }))
      handleAgentSessionInfo('a', parsed, stores, { phase: 'live' }, true)
      expect(stores.chatStore.getLiveCompaction('a')).toBeUndefined()
      dispose()
    })
  })
})
