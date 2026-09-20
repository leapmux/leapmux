import type { RowExtractionContext } from './rowModelCache'
import type { ParsedMessageContent } from '~/lib/messageParser'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { createMessageRenderCacheStore } from './messageRenderCache'
import { resolveMessageForRendering } from './providers/registry'
import { extractedRow } from './rowExtraction'
import { cachedChatRow } from './rowModelCache'
import './providers'

function parsed(parentObject: Record<string, unknown>): ParsedMessageContent {
  return { rawText: JSON.stringify(parentObject), topLevel: parentObject, parentObject, wrapper: null }
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
      content: [{ type: 'content', content: { type: 'text', text: 'passed' } }],
    }), AgentProvider.OPENCODE)
    let requestVisible = false
    const context = {
      renderCache: createMessageRenderCacheStore().forRow('result-revision'),
      sources: {
        request: () => request,
        result: () => result,
        role: () => 'result' as const,
        visibleRows: () => ({ request: requestVisible, result: true }),
      },
      spanType: 'execute',
    } as unknown as RowExtractionContext

    const first = extractedRow(cachedChatRow(context, AgentProvider.OPENCODE, result, { kind: 'tool_use' }, undefined))
    expect(first?.kind === 'tool' ? first.hasRequestRow : undefined).toBe(false)

    requestVisible = true
    const second = extractedRow(cachedChatRow(context, AgentProvider.OPENCODE, result, { kind: 'tool_use' }, undefined))
    expect(second?.kind === 'tool' ? second.hasRequestRow : undefined).toBe(true)
    expect(second).not.toBe(first)
  })
})
