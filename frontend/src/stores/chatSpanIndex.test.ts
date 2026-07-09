import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { providerFor } from '~/components/chat/providers/registry'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { createSpanIndex } from '~/stores/chatSpanIndex'
// Register the provider plugins: createSpanIndex resolves span roles through pluginFor (Claude
// reads Anthropic tool_use/tool_result blocks, Pi routes by envelope type).
import '~/components/chat/providers'

function encode(raw: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(raw))
}

/** A Claude tool_use opener (an assistant message carrying a tool_use block). */
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
 * mis-bucket it as `other` and first-seen-is-opener would misfile it.
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

/** A Codex command-span row. Codex registers no spanRole hook, so it classifies as 'other'. */
function codexSpan(id: string, spanId: string, seq: bigint, status: string) {
  return create(AgentChatMessageSchema, {
    id,
    source: MessageSource.AGENT,
    content: encode({ item: { type: 'commandExecution', status } }),
    contentCompression: ContentCompression.NONE,
    seq,
    spanId,
    spanType: 'commandExecution',
    agentProvider: AgentProvider.CODEX,
  })
}

describe('createspanindex', () => {
  it('routes by content-block role, not arrival order (result before opener)', () => {
    const idx = createSpanIndex()
    // Out-of-order: the result is indexed before its opener.
    idx.index('a1', toolResult('res', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')).toBeUndefined() // no opener yet
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.type).toBe('user')

    idx.index('a1', toolUse('op', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.type).toBe('assistant')
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.type).toBe('user')
  })

  it('routes by role when the opener arrives first too', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'), toolResult('res', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.type).toBe('assistant')
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.type).toBe('user')
  })

  it('routes Pi tool_execution_start/_end by envelope type, not content blocks', () => {
    const idx = createSpanIndex()
    idx.index('a1', piStart('op', 's1'), piEnd('res', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.type).toBe('tool_execution_start')
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.type).toBe('tool_execution_end')
  })

  it('files a Pi tool_execution_end that arrives BEFORE its start into the result map', () => {
    const idx = createSpanIndex()
    // The motivating regression: Pi's end carries no Anthropic blocks, so the old
    // content-block heuristic returned `other` and first-seen-is-opener filed the
    // end as the opener -- leaving getResultParsed undefined. Provider-aware role
    // routing fixes it regardless of arrival order.
    idx.index('a1', piEnd('res', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')).toBeUndefined() // the end is not an opener
    idx.index('a1', piStart('op', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.type).toBe('tool_execution_start')
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.type).toBe('tool_execution_end')
  })

  it('routes non-tool kinds by first-seen, but flags a conflict on the second member for a safety reindex', () => {
    const idx = createSpanIndex()
    // Two non-tool messages share a span and BOTH classify as 'other'. In order
    // (opener first) the first-seen fallback routes correctly -- AND the second
    // member flags a conflict, because role can't order two 'other' members, so the
    // caller reindexes from the authoritative window as a backstop.
    expect(idx.index('a1', plain('first', 's1', 'OPENER'), plain('second', 's1', 'RESULT'))).toBe(true)
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.content).toBe('OPENER')
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.content).toBe('RESULT')
  })

  it('flags a conflict when two "other" members arrive OUT of order, so a reindex fixes the routing', () => {
    const idx = createSpanIndex()
    const opener = plain('op', 's1', 'OPENER')
    const result = plain('res', 's1', 'RESULT')
    // Incremental, OUT of order: the result arrives first and the first-seen
    // fallback misfiles it as the opener; the later opener flags a conflict.
    expect(idx.index('a1', result)).toBe(false) // first member, fresh slot
    expect(idx.index('a1', opener)).toBe(true) // second 'other' member -> conflict
    // The store rebuilds from its seq-ordered window (opener before result on the
    // wire), which routes both correctly.
    idx.reindex('a1', [opener, result])
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.content).toBe('OPENER')
    expect(idx.getResultParsed('a1', 's1')?.parentObject?.content).toBe('RESULT')
  })

  it('files Codex/ACP span rows (no spanRole hook) first-seen-is-opener, pairing same-span rows via the backstop', () => {
    // Codex and the ACP providers intentionally register NO spanRole hook: their spans are
    // single-logical-row (the item IS both opener and terminal state), so createSpanIndex defaults
    // them to 'other'. Pin that so nobody "fixes" it by adding a Codex/ACP spanRole returning
    // 'opener' -- that would file two same-span rows both on the opener side and break result lookup
    // (the very pairing this test asserts). A DISTINCT result-role row would instead need 'result'.
    expect(providerFor(AgentProvider.CODEX)?.spanRole).toBeUndefined()
    expect(providerFor(AgentProvider.OPENCODE)?.spanRole).toBeUndefined()

    const idx = createSpanIndex()
    // A started row then a re-broadcast completed row (distinct id) share the span. In seq order the
    // first files as the opener; the second 'other' member flags a conflict and the backstop files it
    // as the result -- so getResultMessage still resolves.
    expect(idx.index('a1', codexSpan('started', 's1', 1n, 'in_progress'), codexSpan('completed', 's1', 2n, 'completed'))).toBe(true)
    expect(idx.getOpenerMessage('a1', 's1')?.id).toBe('started')
    expect(idx.getResultMessage('a1', 's1')?.id).toBe('completed')
  })

  it('keeps agents and absent spans isolated', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'))
    // A message without a spanId is not indexed.
    idx.index('a1', toolUse('nospan', ''))
    // A different agent shares the spanId namespace but its own map.
    expect(idx.getOpenerParsed('a2', 's1')).toBeUndefined()
    expect(idx.getOpenerParsed('a1', 's-missing')).toBeUndefined()
    expect(idx.getOpenerParsed('a1', 's1')?.parentObject?.type).toBe('assistant')
  })

  it('reindex replaces the agent window (clears stale entries)', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'), toolResult('res', 's1'))
    expect(idx.getOpenerParsed('a1', 's1')).toBeDefined()

    // Rebuild from a window that no longer contains s1.
    idx.reindex('a1', [toolUse('op2', 's2')])
    expect(idx.getOpenerParsed('a1', 's1')).toBeUndefined()
    expect(idx.getResultParsed('a1', 's1')).toBeUndefined()
    expect(idx.getOpenerParsed('a1', 's2')?.parentObject?.type).toBe('assistant')

    // Reindex with an empty window clears everything.
    idx.reindex('a1', [])
    expect(idx.getOpenerParsed('a1', 's2')).toBeUndefined()
  })

  it('reports a conflict when an incremental index reassigns a spanId to a new message id', () => {
    const idx = createSpanIndex()
    // First opener for s1 -> no conflict (fresh slot).
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

  it('reports a conflict when a span message flips sides under the SAME id (opener -> result)', () => {
    const idx = createSpanIndex()
    // 'm1' is first an opener for s1.
    expect(idx.index('a1', toolUse('m1', 's1'))).toBe(false)
    expect(idx.getOpenerParsed('a1', 's1')).toBeDefined()
    // The SAME id is re-indexed as a RESULT (its role flipped). It now sits on
    // BOTH sides, so the opener entry is stale -- a same-side-only check would miss
    // this (the result side is empty), but the cross-side check catches it so the
    // caller reindexes from the authoritative window.
    expect(idx.index('a1', toolResult('m1', 's1'))).toBe(true)
  })

  it('caches the parse per message instance', () => {
    const idx = createSpanIndex()
    idx.index('a1', toolUse('op', 's1'))
    const first = idx.getOpenerParsed('a1', 's1')
    const second = idx.getOpenerParsed('a1', 's1')
    expect(first).toBeDefined()
    expect(second).toBe(first) // same reference -> parse memoized
  })
})
