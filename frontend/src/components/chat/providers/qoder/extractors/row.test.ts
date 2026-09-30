import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { classifyQoderMessage } from '../classification'
import { qoderExtractRow } from './row'

function resolvedQoderFrame(frame: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.QODER)
}

describe('qoderExtractRow', () => {
  it('keeps native image bytes and text from a Read tool result', () => {
    const request = resolvedQoderFrame({
      type: 'assistant',
      message: { content: [{ type: 'tool_use', id: 'read-1', name: 'Read', input: { file_path: '/repo/shot.png' } }] },
    })
    const result = resolvedQoderFrame({
      type: 'user',
      message: {
        content: [{
          type: 'tool_result',
          tool_use_id: 'read-1',
          content: [
            { type: 'text', text: 'Image file: shot.png' },
            { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'iVBORw0KGgo=' } },
          ],
        }],
      },
    })
    const span: ToolSpanContext = { request, result, role: 'result', visibleRows: { request: true, result: true } }
    const row = qoderExtractRow({ resolved: result, category: classifyQoderMessage({ ...result, agentProvider: AgentProvider.QODER }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      return
    expect(row.call.extraContent).toEqual([
      { type: 'text', text: 'Image file: shot.png' },
      { type: 'image', source: { mimeType: 'image/png', data: 'iVBORw0KGgo=' } },
    ])
    expect(JSON.stringify(row.call.result)).not.toContain('Image file: shot.png')
  })

  it('marks a failed native tool result as failed', () => {
    const result = resolvedQoderFrame({
      type: 'user',
      message: { content: [{ type: 'tool_result', tool_use_id: 'read-failed', is_error: true, content: 'Read failed' }] },
    })
    const span: ToolSpanContext = { request: undefined, result, role: 'result', visibleRows: { request: false, result: true } }
    const row = qoderExtractRow({ resolved: result, category: classifyQoderMessage({ ...result, agentProvider: AgentProvider.QODER }), span })

    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      return
    expect(row.call.status).toBe('failed')
    expect(row.call.degradation).toBeUndefined()
  })
})
