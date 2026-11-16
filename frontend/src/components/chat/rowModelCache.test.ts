import type { ResolvedMessage } from './messageContextResolver'
import type { RowExtractionContext } from './rowModelCache'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { createMessageRenderCacheStore } from './messageRenderCache'
import { resolveMessageForRendering } from './providers/registry'
import { extractedRow } from './rowExtraction'
import { cachedChatRow, cachedSpanHeaderCall } from './rowModelCache'
import { prepareMessage } from './rowPreparation'
import './providers'

function parsed(parentObject: Record<string, unknown>): ParsedMessageContent {
  return {
    rawText: JSON.stringify(parentObject),
    topLevel: parentObject,
    parentObject,
    wrapper: null,
  }
}

describe('rowModelCache', () => {
  it('rebuilds a row when its resolved sibling becomes visible', () => {
    const request = resolveMessageForRendering(parsed({
      sessionUpdate: 'tool_call',
      toolCallId: 'call',
      status: 'pending',
      kind: 'execute',
      title: 'Run checks',
      rawInput: { command: 'bun test' },
    }), AgentProvider.OPENCODE)
    const result = resolveMessageForRendering(parsed({
      sessionUpdate: 'tool_call_update',
      toolCallId: 'call',
      status: 'completed',
      content: [
        {
          type: 'content',
          content: { type: 'text', text: 'passed' },
        },
      ],
    }), AgentProvider.OPENCODE)
    let requestVisible = false
    const context = {
      renderCache: createMessageRenderCacheStore().forRow('result-revision'),
      sources: {
        request: () => request,
        role: () => 'result' as const,
        visibleRows: () => ({ request: requestVisible }),
      },
      spanType: 'execute',
    } as unknown as RowExtractionContext

    const first = extractedRow(cachedChatRow(context, AgentProvider.OPENCODE, result, { kind: 'tool_use' }, undefined))
    expect(first?.kind === 'tool'
      ? first.hasRequestRow
      : undefined).toBe(false)

    requestVisible = true
    const second = extractedRow(cachedChatRow(context, AgentProvider.OPENCODE, result, { kind: 'tool_use' }, undefined))
    expect(second?.kind === 'tool'
      ? second.hasRequestRow
      : undefined).toBe(true)
    expect(second).not.toBe(first)
  })
})

describe('cachedSpanHeaderCall', () => {
  function resolvedOf(message: AgentChatMessage): ResolvedMessage {
    const prepared = prepareMessage(message)
    return {
      message,
      original: prepared.original,
      resolved: prepared.resolved,
      revision: {
        id: message.id,
        seq: message.seq,
        contentVersion: 0,
        supplementalRevision: 0n,
      },
    }
  }

  /** A Claude Bash call: its request asks for `make`, and its result states the output. */
  function bashSpan(): { request: ResolvedMessage, result: ResolvedMessage } {
    return {
      request: resolvedOf(makeMessage({
        id: 'bash-request',
        seq: 1n,
        spanId: 'toolu_bash',
        content: rawContent({
          type: 'assistant',
          message: {
            content: [
              {
                type: 'tool_use',
                id: 'toolu_bash',
                name: 'Bash',
                input: { command: 'make', description: 'Build it' },
              },
            ],
          },
        }),
      })),
      result: resolvedOf(makeMessage({
        id: 'bash-result',
        seq: 2n,
        spanId: 'toolu_bash',
        content: rawContent({
          type: 'user',
          message: {
            content: [
              { type: 'tool_result', tool_use_id: 'toolu_bash', content: 'built' },
            ],
          },
        }),
      })),
    }
  }

  it('reads the call that the result row draws', () => {
    const { request, result } = bashSpan()
    const call = cachedSpanHeaderCall(result, request.resolved)
    expect(call?.kind).toBe('execute')
    expect(call?.status).toBe('completed')
  })

  // Every copy of the request row asks for the same header.
  it('extracts the result once for one revision of each side', () => {
    const { request, result } = bashSpan()
    expect(cachedSpanHeaderCall(result, request.resolved)).toBe(cachedSpanHeaderCall(result, request.resolved))
  })

  it('extracts again for a new revision of a side', () => {
    const { request, result } = bashSpan()
    const first = cachedSpanHeaderCall(result, request.resolved)
    const revised = bashSpan()
    const again = cachedSpanHeaderCall(result, revised.request.resolved)
    expect(again).not.toBe(first)
    expect(again).toEqual(first)
    expect(cachedSpanHeaderCall(revised.result, request.resolved)).not.toBe(first)
  })

  it('reads no header from a message that draws no result row', () => {
    const { request } = bashSpan()
    expect(cachedSpanHeaderCall(request, request.resolved)).toBeUndefined()
  })
})
