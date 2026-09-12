import { create } from '@bufbuild/protobuf'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { applyNotificationMetadata } from '~/hooks/agentEvents'
import { parseMessageContent } from '~/lib/messageParser'
import { createAgentSessionStore } from '~/stores/agentSession.store'
import { createChatStore } from '~/stores/chat.store'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import '~/components/chat/providers'

describe('stored supplemental usage', () => {
  it.each([AgentProvider.PI, AgentProvider.ZCODE])('restores provider %s usage through the shared message resolver', (provider) => {
    createRoot((dispose) => {
      installTestBridge({ workspaceId: 'supplemental-usage' })
      const tabs = createTestTabStores('supplemental-usage')
      const stores = { ...tabs, agentSessionStore: createAgentSessionStore(), chatStore: createChatStore(), getActiveWorkspaceId: () => 'supplemental-usage' }
      const id = `supplemental-usage-${provider}`
      const type = provider === AgentProvider.PI ? 'agent_end' : 'turn.completed'
      const original = ` {"type":"${type}","payload":{},"messages":[],"total_cost_usd":"provider value"} `
      const message = create(AgentChatMessageSchema, {
        id,
        agentProvider: provider,
        source: MessageSource.AGENT,
        content: new TextEncoder().encode(original),
        contentCompression: ContentCompression.NONE,
        supplementalContent: new TextEncoder().encode(JSON.stringify({ metadata: { total_cost_usd: 0, context_usage: { input_tokens: 12, context_window: 1000 } } })),
        supplementalContentCompression: ContentCompression.NONE,
      })
      const parsed = parseMessageContent(message)
      applyNotificationMetadata(id, message, parsed, stores, 'catchingUp')
      expect(stores.agentSessionStore.getInfo(id).totalCostUsd).toBe(0)
      expect(stores.agentSessionStore.getInfo(id).contextUsage).toMatchObject({ inputTokens: 12, contextWindow: 1000 })
      expect(new TextDecoder().decode(message.content)).toBe(original)
      expect(parsed.parentObject?.total_cost_usd).toBe('provider value')
      dispose()
    })
  })
})
