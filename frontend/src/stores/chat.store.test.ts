import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { createChatStore } from './chat.store'

function jsonContent(value: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(value))
}

function claudeToolUse(id: string, seq: bigint, spanId: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: jsonContent({
      type: 'assistant',
      message: {
        role: 'assistant',
        content: [{ type: 'tool_use', id: 'toolu_1', name: 'TaskGet', input: { task_id: 'task-1' } }],
      },
    }),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId,
  })
}

function claudeToolResult(id: string, seq: bigint, spanId: string, content: string): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: jsonContent({
      type: 'user',
      message: {
        role: 'user',
        content: [{ type: 'tool_result', tool_use_id: 'toolu_1', content }],
      },
    }),
    contentCompression: ContentCompression.NONE,
    seq,
    agentProvider: AgentProvider.CLAUDE_CODE,
    spanId,
  })
}

describe('chatstore span content versions', () => {
  it('exposes same-seq tool_result content-version bumps by span id', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const spanId = 'span-1'

    store.setMessages(agentId, [
      claudeToolUse('opener-1', 1n, spanId),
      claudeToolResult('result-1', 2n, spanId, 'first result'),
    ])

    expect(JSON.stringify(store.getToolResultParsedBySpanId(agentId, spanId)?.parentObject)).toContain('first result')
    expect(store.getToolUseRevisionBySpanId(agentId, spanId)).toEqual({ id: 'opener-1', seq: 1n, contentVersion: 0 })
    expect(store.getToolResultRevisionBySpanId(agentId, spanId)).toEqual({ id: 'result-1', seq: 2n, contentVersion: 0 })
    expect(store.getToolResultContentVersionBySpanId(agentId, spanId)).toBe(0)
    expect(store.getToolUseContentVersionBySpanId(agentId, spanId)).toBe(0)

    store.addMessage(agentId, claudeToolResult('result-1', 2n, spanId, 'updated result'))

    expect(JSON.stringify(store.getToolResultParsedBySpanId(agentId, spanId)?.parentObject)).toContain('updated result')
    expect(store.getToolResultRevisionBySpanId(agentId, spanId)).toEqual({ id: 'result-1', seq: 2n, contentVersion: 1 })
    expect(store.getToolResultContentVersionBySpanId(agentId, spanId)).toBe(1)
    expect(store.getToolUseContentVersionBySpanId(agentId, spanId)).toBe(0)
  })

  it('updates span revisions when a full window replace swaps a sibling at content version zero', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const spanId = 'span-1'

    store.setMessages(agentId, [
      claudeToolUse('opener-1', 1n, spanId),
      claudeToolResult('result-1', 2n, spanId, 'first result'),
    ])

    store.setMessages(agentId, [
      claudeToolUse('opener-2', 3n, spanId),
      claudeToolResult('result-1', 2n, spanId, 'first result'),
    ])

    expect(store.getToolUseContentVersionBySpanId(agentId, spanId)).toBe(0)
    expect(store.getToolUseRevisionBySpanId(agentId, spanId)).toEqual({ id: 'opener-2', seq: 3n, contentVersion: 0 })
  })

  it('returns zero when no result is indexed for a span id', () => {
    const store = createChatStore()
    const agentId = 'agent-1'
    const spanId = 'span-1'

    store.setMessages(agentId, [claudeToolUse('opener-1', 1n, spanId)])

    expect(store.getToolResultParsedBySpanId(agentId, spanId)).toBeUndefined()
    expect(store.getToolResultContentVersionBySpanId(agentId, spanId)).toBe(0)
  })
})
