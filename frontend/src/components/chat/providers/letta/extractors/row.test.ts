import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { classifyLettaMessage } from '../classification'
import { lettaExtractRow } from './row'

function resolvedLettaFrame(frame: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.LETTA)
}

describe('lettaExtractRow', () => {
  it('shows the native child Read call with its exact tool id and path', () => {
    const request = resolvedLettaFrame({
      type: 'message',
      message_type: 'tool_call_message',
      tool_calls: [{ tool_call_id: 'call-read-native', name: 'Read', arguments: '{"file_path":"note.txt"}' }],
    })
    const span: ToolSpanContext = { request, result: undefined, role: 'request', visibleRows: { request: true, result: false } }
    const row = lettaExtractRow({ resolved: request, category: classifyLettaMessage({ ...request, agentProvider: AgentProvider.LETTA }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      return
    expect(row.call.id).toBe('call-read-native')
    expect(row.call.name).toBe('Read')
    expect(JSON.stringify(row.call.request)).toContain('note.txt')
  })
})
