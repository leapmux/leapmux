import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { createSpanIndex } from '~/stores/chatSpanIndex'
// Register the provider plugins: createSpanIndex resolves span roles through pluginFor (Claude
// reads Anthropic tool_use/tool_result blocks, Pi routes by envelope type).
import '~/components/chat/providers'

function parsed(message: AgentChatMessage | undefined) {
  return message ? parseMessageContent(message) : undefined
}

function encode(raw: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(raw))
}

/** A Claude tool_use request (an assistant message that carries a tool_use block). */
function toolUse(id: string, spanId: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: encode({ type: 'assistant', message: { content: [{ type: 'tool_use', name: 'Read', input: {} }] } }),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    spanId,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

/** A Claude tool_result (a user message carrying a tool_result block). */
function toolResult(id: string, spanId: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: encode({ type: 'user', span_type: 'Read', message: { role: 'user', content: [{ type: 'tool_result', content: 'output', tool_use_id: 't1' }] } }),
    contentCompression: ContentCompression.NONE,
    seq: 2n,
    spanId,
    agentProvider: AgentProvider.CLAUDE_CODE,
  })
}

/** A plain message (neither tool_use nor tool_result) carrying a spanId. */
function plain(id: string, spanId: string, content: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: encode({ content }),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    spanId,
  })
}

/**
 * A Pi tool span. Pi discriminates by the flat envelope `type`
 * (tool_execution_start / _end), NOT Anthropic content blocks -- the `_end`
 * carries no `message.content[]`, so a content-block-only role check would
 * mis-bucket it as `other`, and first-message-is-request would misfile it.
 */
function piStart(id: string, spanId: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: encode({ type: 'tool_execution_start', toolName: 'bash', toolCallId: 'tc1' }),
    contentCompression: ContentCompression.NONE,
    seq: 1n,
    spanId,
    agentProvider: AgentProvider.PI,
  })
}
function piEnd(id: string, spanId: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.USER,
    content: encode({ type: 'tool_execution_end', toolCallId: 'tc1', toolName: 'bash', result: 'done' }),
    contentCompression: ContentCompression.NONE,
    seq: 2n,
    spanId,
    agentProvider: AgentProvider.PI,
  })
}

/** A Codex command row with its native item status. */
function codexSpan(id: string, spanId: string, seq: bigint, status: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: encode({ item: { id: spanId, type: 'commandExecution', status } }),
    contentCompression: ContentCompression.NONE,
    seq,
    spanId,
    spanType: 'commandExecution',
    agentProvider: AgentProvider.CODEX,
  })
}

describe('createSpanIndex', () => {
  it('routes by content-block role, not arrival order (result before request)', () => {
    const idx = createSpanIndex()
    // Out-of-order: the result is indexed before its request.
    idx.index('a1', toolResult('res', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))).toBeUndefined() // no request yet
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('user')

    idx.index('a1', toolUse('op', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('assistant')
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('user')
  })

  it.each([
    AgentProvider.OPENCODE,
    AgentProvider.KILO,
    AgentProvider.CURSOR,
    AgentProvider.GOOSE,
    AgentProvider.REASONIX,
  ])('pairs ACP provider %s by role when its result arrives first', (agentProvider) => {
    const idx = createSpanIndex()
    const request = create(AgentChatMessageSchema, {
      id: 'request',
      seq: 1n,
      spanId: 'call',
      agentProvider,
      contentCompression: ContentCompression.NONE,
      content: encode({ sessionUpdate: 'tool_call', toolCallId: 'call', status: 'pending' }),
    })
    const result = create(AgentChatMessageSchema, {
      id: 'result',
      seq: 2n,
      spanId: 'call',
      agentProvider,
      contentCompression: ContentCompression.NONE,
      content: encode({ sessionUpdate: 'tool_call_update', toolCallId: 'call', status: 'completed' }),
    })
    idx.index('agent', result)
    expect(idx.getRequestMessage('agent', { spanId: 'call', agentSessionId: '' })).toBeUndefined()
    idx.index('agent', request)
    expect(idx.getRequestMessage('agent', { spanId: 'call', agentSessionId: '' })).toBe(request)
    expect(idx.getResultMessage('agent', { spanId: 'call', agentSessionId: '' })).toBe(result)
  })

  // Copilot speaks its own native protocol, so its pair is a start event and a
  // completion event rather than the Agent Client Protocol's two session updates.
  it('pairs Copilot by role when its completion arrives first', () => {
    const idx = createSpanIndex()
    const agentProvider = AgentProvider.GITHUB_COPILOT
    const frame = (type: string, data: Record<string, unknown>) => ({
      jsonrpc: '2.0',
      method: 'session.event',
      params: { sessionId: 'session-1', event: { id: `${type}-1`, type, data } },
    })
    const request = create(AgentChatMessageSchema, {
      id: 'request',
      seq: 1n,
      spanId: 'call',
      agentProvider,
      contentCompression: ContentCompression.NONE,
      content: encode(frame('tool.execution_start', { toolCallId: 'call', toolName: 'view' })),
    })
    const result = create(AgentChatMessageSchema, {
      id: 'result',
      seq: 2n,
      spanId: 'call',
      agentProvider,
      contentCompression: ContentCompression.NONE,
      content: encode(frame('tool.execution_complete', { toolCallId: 'call', success: true })),
    })
    idx.index('agent', result)
    expect(idx.getRequestMessage('agent', { spanId: 'call', agentSessionId: '' })).toBeUndefined()
    idx.index('agent', request)
    expect(idx.getRequestMessage('agent', { spanId: 'call', agentSessionId: '' })).toBe(request)
    expect(idx.getResultMessage('agent', { spanId: 'call', agentSessionId: '' })).toBe(result)
  })

  it('routes by role when the request arrives first too', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'), toolResult('res', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('assistant')
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('user')
  })

  it('routes Pi tool_execution_start/_end by envelope type, not content blocks', () => {
    const idx = createSpanIndex()
    idx.index('a1', piStart('op', 's1'), piEnd('res', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('tool_execution_start')
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('tool_execution_end')
  })

  it('files a Pi tool_execution_end that arrives BEFORE its start into the result map', () => {
    const idx = createSpanIndex()
    // The motivating regression: Pi's end carries no Anthropic blocks, so the old
    // content-block heuristic returned `other` and first-message-is-request filed the
    // end as the request. This left getResultParsed undefined. Provider-aware role
    // routing fixes it regardless of arrival order.
    idx.index('a1', piEnd('res', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))).toBeUndefined() // the end is not a request
    idx.index('a1', piStart('op', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('tool_execution_start')
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('tool_execution_end')
  })

  it('routes non-tool kinds by first-seen, but flags a conflict on the second member for a safety reindex', () => {
    const idx = createSpanIndex()
    // Two non-tool messages share a span and BOTH classify as 'other'. In order
    // (request first) the first-seen fallback routes correctly. The second
    // member flags a conflict, because role can't order two 'other' members, so the
    // caller reindexes from the authoritative window as a backstop.
    expect(idx.index('a1', plain('first', 's1', 'REQUEST'), plain('second', 's1', 'RESULT'))).toBe(true)
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.content).toBe('REQUEST')
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.content).toBe('RESULT')
  })

  it('flags a conflict when two "other" members arrive OUT of order, so a reindex fixes the routing', () => {
    const idx = createSpanIndex()
    const request = plain('op', 's1', 'REQUEST')
    const result = plain('res', 's1', 'RESULT')
    // Incremental, OUT of order: the result arrives first and the first-seen
    // fallback misfiles it as the request. The later request flags a conflict.
    expect(idx.index('a1', result)).toBe(false) // first member, fresh slot
    expect(idx.index('a1', request)).toBe(true) // second 'other' member -> conflict
    // The store rebuilds from its seq-ordered window (request before result on the
    // wire), which routes both correctly.
    idx.reindex('a1', [request, result])
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.content).toBe('REQUEST')
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.content).toBe('RESULT')
  })

  it('indexes a Codex result before its request without guessing from arrival order', () => {
    const idx = createSpanIndex()
    expect(idx.index('a1', codexSpan('completed', 's1', 2n, 'completed'))).toBe(false)
    expect(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' })).toBeUndefined()
    expect(idx.index('a1', codexSpan('started', 's1', 1n, 'inProgress'))).toBe(false)
    expect(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' })?.id).toBe('started')
    expect(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' })?.id).toBe('completed')
  })

  it('keeps repeated messages with an unknown role on their original side', () => {
    const idx = createSpanIndex()
    const request = plain('op', 's1', 'REQUEST')
    const result = plain('res', 's1', 'RESULT')
    idx.reindex('a1', [request, result])
    expect(idx.index('a1', request)).toBe(false)
    expect(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' })?.id).toBe('op')
    expect(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' })?.id).toBe('res')
    expect(idx.index('a1', result)).toBe(false)
    expect(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' })?.id).toBe('op')
    expect(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' })?.id).toBe('res')
  })

  it('keeps agents and absent spans isolated', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'))
    // A message without a spanId is not indexed.
    idx.index('a1', toolUse('nospan', ''))
    // A different agent shares the spanId namespace but its own map.
    expect(parsed(idx.getRequestMessage('a2', { spanId: 's1', agentSessionId: '' }))).toBeUndefined()
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's-missing', agentSessionId: '' }))).toBeUndefined()
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))?.parentObject?.type).toBe('assistant')
  })

  // The resolver files the same side the store does: both read the role through
  // the one shared helper, over the RESOLVED parse -- so a supplement that lands
  // after the row can move a span's role, and the two indexes cannot disagree.
  it('files the same side the store does when a supplement changes the role', () => {
    const index = createSpanIndex()
    // A Copilot start event under a retained completion reads as the RESULT side
    // through the shared role helper; without the completion it is the request.
    const start = create(AgentChatMessageSchema, {
      id: 'start',
      source: MessageSource.AGENT,
      content: encode({ jsonrpc: '2.0', method: 'session.event', params: { sessionId: 's1', event: { id: 'e1', type: 'tool.execution_start', data: { toolCallId: 'c1', toolName: 'bash', arguments: {} } } } }),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      spanId: 'c1',
      agentProvider: AgentProvider.GITHUB_COPILOT,
    })
    index.reindex('a', [start])
    expect(index.getRequestMessage('a', { spanId: 'c1', agentSessionId: '' })?.id).toBe('start')
    expect(index.getResultMessage('a', { spanId: 'c1', agentSessionId: '' })).toBeUndefined()
  })

  it('removes the stale side when a role change re-files a message', () => {
    const index = createSpanIndex()
    const start = create(AgentChatMessageSchema, {
      id: 'start',
      source: MessageSource.AGENT,
      content: encode({ jsonrpc: '2.0', method: 'session.event', params: { sessionId: 's1', event: { id: 'e1', type: 'tool.execution_start', data: { toolCallId: 'c1', toolName: 'bash', arguments: {} } } } }),
      contentCompression: ContentCompression.NONE,
      seq: 1n,
      spanId: 'c1',
      agentProvider: AgentProvider.GITHUB_COPILOT,
      completion: MessageCompletion.COMPLETE,
    })
    // The completion column retains the row: the same event now files as the
    // RESULT side, and the request slot it held is gone.
    index.reindex('a', [start])
    expect(index.getResultMessage('a', { spanId: 'c1', agentSessionId: '' })?.id).toBe('start')
    expect(index.getRequestMessage('a', { spanId: 'c1', agentSessionId: '' })).toBeUndefined()
  })

  it('reindex replaces the agent window (clears stale entries)', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'), toolResult('res', 's1'))
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))).toBeDefined()

    // Rebuild from a window that no longer contains s1.
    idx.reindex('a1', [toolUse('op2', 's2')])
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))).toBeUndefined()
    expect(parsed(idx.getResultMessage('a1', { spanId: 's1', agentSessionId: '' }))).toBeUndefined()
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's2', agentSessionId: '' }))?.parentObject?.type).toBe('assistant')

    // Reindex with an empty window clears everything.
    idx.reindex('a1', [])
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's2', agentSessionId: '' }))).toBeUndefined()
  })

  it('reports a conflict when an incremental index reassigns a spanId to a new message id', () => {
    const idx = createSpanIndex()
    // First request for s1 -> no conflict (fresh slot).
    expect(idx.index('a1', toolUse('op', 's1'))).toBe(false)
    // Re-broadcast of the SAME message id -> in-place update, not a conflict.
    expect(idx.index('a1', toolUse('op', 's1'))).toBe(false)
    // A DIFFERENT message id under the same spanId -> conflict: the caller must
    // rebuild from the authoritative window (the old 'op' may still be loaded).
    expect(idx.index('a1', toolUse('op-rebroadcast', 's1'))).toBe(true)
    // A result for an as-yet-unindexed span is not a conflict.
    expect(idx.index('a1', toolResult('res', 's2'))).toBe(false)
    // A different result id under s2 is a conflict on the result side too.
    expect(idx.index('a1', toolResult('res-rebroadcast', 's2'))).toBe(true)
  })

  it('reports a conflict when a span message flips sides under the SAME id (request -> result)', () => {
    const idx = createSpanIndex()
    // 'm1' is first a request for s1.
    expect(idx.index('a1', toolUse('m1', 's1'))).toBe(false)
    expect(parsed(idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' }))).toBeDefined()
    // The SAME id is re-indexed as a RESULT (its role flipped). It now sits on
    // BOTH sides, so the request entry is stale. A same-side-only check would miss
    // this (the result side is empty), but the cross-side check catches it so the
    // caller reindexes from the authoritative window.
    expect(idx.index('a1', toolResult('m1', 's1'))).toBe(true)
  })

  it('preserves the indexed message reference', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'))
    const first = idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' })
    const second = idx.getRequestMessage('a1', { spanId: 's1', agentSessionId: '' })
    expect(first).toBeDefined()
    expect(second).toBe(first)
  })
})
