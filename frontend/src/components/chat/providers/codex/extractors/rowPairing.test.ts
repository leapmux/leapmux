import type { MessageInitShape } from '@bufbuild/protobuf'
import type { ChatRowIR } from '../../../ir/row'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { createRoot, createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageCompletion, MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { parseMessageContent } from '~/lib/messageParser'
import { createSpanIndex } from '~/stores/chatSpanIndex'
import { createMessageContextResolver, createMessageRenderSources } from '../../../messageContextResolver'
import { prepareMessage } from '../../../rowPreparation'
import { cachedChatRow } from '../../../rowRenderers'
import '../plugin'

function collabMessage(id: string, seq: bigint, item: Record<string, unknown>, frame: Record<string, unknown>): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id,
    seq,
    spanId: 'shared-agent-call',
    spanType: 'collabAgentToolCall',
    agentProvider: AgentProvider.CODEX,
    source: MessageSource.AGENT,
    agentSessionId: 'sess-1',
    content: new TextEncoder().encode(JSON.stringify({ item, ...frame })),
    contentCompression: ContentCompression.NONE,
  } satisfies MessageInitShape<typeof AgentChatMessageSchema>)
}

/**
 * The span a `collabAgentToolCall` pair becomes, through the REAL resolver, span
 * index, preparation, and row cache -- the same route a mounted row takes.
 *
 * A request row whose result side has landed carries it, so its own `inProgress`
 * word must not keep the envelope in progress: the validating builder refuses an
 * in-progress envelope over a result, and the degrade traded the typed agent card
 * for the generic one -- the request drew its prompt as raw JSON and the result
 * stood alone, which the e2e suite saw as a missing "Show prompt" button.
 */
describe('codex tool span pairing', () => {
  it('keeps both sides of a paired spawnAgent call the agent kind, whatever the opener frame still says', () => {
    createRoot((dispose) => {
      const prompt = '**Instruction marker**\n\nRead the fixture and report the findings.'
      const report = '**Report marker**\n\n- First finding'
      const base = { id: 'shared-agent-call', type: 'collabAgentToolCall', tool: 'spawnAgent', prompt }
      const request = collabMessage('request', 1n, { ...base, status: 'inProgress', receiverThreadIds: [] }, { startedAtMs: 1 })
      const result = collabMessage('result', 2n, { ...base, status: 'completed', receiverThreadIds: ['child'], agentsStates: { child: { status: 'completed', message: report } } }, { completedAtMs: 2 })
      const [messages] = createSignal([request, result])
      const index = createSpanIndex()
      index.reindex('sess-1', [request, result])
      const resolver = createMessageContextResolver({
        scopeKey: 'worker/agent',
        messages,
        messageVersion: () => 0,
        contentVersion: () => 0,
        spanMessage: (spanId, side) => side === 'request'
          ? index.getOpenerMessage('sess-1', spanId)
          : index.getResultMessage('sess-1', spanId),
        messageBySeq: seq => [request, result].find(message => message.seq === seq),
        fetchSpan: vi.fn(async () => []),
        fetchMessage: vi.fn(async () => undefined),
        fetchFileImage: async () => { throw new Error('The image source is unavailable') },
        subscribe: () => () => {},
        todo: () => undefined,
        backgroundTask: () => undefined,
        progress: () => undefined,
      })
      const kindOf = (message: AgentChatMessage): { kind: string, role: string, hasResult: boolean, hasRequestRow: boolean } | null => {
        const sources = createMessageRenderSources(() => resolver, () => message, () => resolver.current(message).parsed)
        const prepared = prepareMessage(message, { resolved: resolver.current(message).parsed })
        const extraction = cachedChatRow({ sources, spanType: 'collabAgentToolCall' }, AgentProvider.CODEX, prepared.resolved, prepared.category, message.completion)
        if (extraction.kind !== 'row')
          return null
        const row = extraction.row as ChatRowIR
        if (row.kind !== 'tool')
          return { kind: row.kind, role: '', hasResult: false, hasRequestRow: false }
        return { kind: row.call.kind, role: row.role, hasResult: 'hasResultRow' in row ? row.hasResultRow : false, hasRequestRow: 'hasRequestRow' in row ? row.hasRequestRow : false }
      }
      const requestRow = kindOf(request)
      const resultRow = kindOf(result)
      // The request row keeps the agent kind and the landed result side.
      expect(requestRow).toEqual({ kind: 'agent', role: 'request', hasResult: true, hasRequestRow: false })
      expect(resultRow).toEqual({ kind: 'agent', role: 'result', hasResult: false, hasRequestRow: true })
      dispose()
    })
  })

  // The span's own outcome word outranks BOTH the item word and the landed
  // result: a frame the reader interrupted still says `inProgress` in its own
  // bytes, and the completion column is what says the call never finished. The
  // row must stay cancelled -- `answered`-promotion to completed would resurrect
  // a call the reader stopped.
  it('keeps an interrupted completion over the frame\'s own in-progress word', () => {
    createRoot((dispose) => {
      const stopped = collabMessage('request', 1n, { id: 'shared-agent-call', type: 'collabAgentToolCall', tool: 'spawnAgent', prompt: 'Look', status: 'inProgress', receiverThreadIds: [] }, { startedAtMs: 1 })
      const message = create(AgentChatMessageSchema, { ...stopped, completion: MessageCompletion.INTERRUPTED } satisfies MessageInitShape<typeof AgentChatMessageSchema>) ?? stopped
      const sources = createMessageRenderSources(() => undefined, () => message, () => parseMessageContent(message))
      const prepared = prepareMessage(message)
      const extraction = cachedChatRow({ sources, spanType: 'collabAgentToolCall' }, AgentProvider.CODEX, prepared.resolved, prepared.category, message.completion)
      expect(extraction.kind).toBe('row')
      if (extraction.kind === 'row') {
        const row = extraction.row as ChatRowIR
        expect(row.kind).toBe('tool')
        if (row.kind === 'tool') {
          expect(row.call.kind).toBe('agent')
          expect(row.call.status).toBe('cancelled')
        }
      }
      dispose()
    })
  })
})
