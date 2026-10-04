import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage, rawContent } from '~/test-support/messageFactory'
import { typedResult } from '../../../model/toolCall'
import { extractPreparedRow, prepareChatRow, prepareMessage } from '../../../rowPreparation'
import { resolveMessageForRendering } from '../../registry'
import { classifyQoderMessage } from '../classification'
import { qoderExtractRow } from './row'

function resolvedQoderFrame(frame: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.QODER)
}

describe('qoderExtractRow', () => {
  const requestFrame = {
    type: 'assistant',
    session_id: 'native-session',
    message: { content: [
      { type: 'tool_use', id: 'first-call', name: 'Bash', input: { command: 'node -e "process.stdout.write(String(40 + 2))"' } },
      { type: 'tool_use', id: 'second-call', name: 'Bash', input: { command: 'node -e "process.stdout.write(String(70 + 7))"' } },
    ] },
  }
  const resultFrame = {
    type: 'user',
    session_id: 'native-session',
    message: { content: [{ type: 'tool_result', tool_use_id: 'second-call', is_error: false, content: '77' }] },
    tool_use_result: { kind: 'completed', stdout: '77', stderr: '', exitCode: 0, signal: null },
  }

  it.each(['request', 'result'] as const)('refuses an explicit no-side context for a native %s', (side) => {
    const resolved = resolvedQoderFrame(side === 'request' ? requestFrame : resultFrame)
    const category = classifyQoderMessage({ ...resolved, agentProvider: AgentProvider.QODER })
    const span: ToolSpanContext = { request: resolvedQoderFrame(requestFrame), result: undefined, role: 'none', visibleRows: { request: false, result: false } }
    expect(qoderExtractRow({ resolved, category, span, spanId: 'second-call' })).toBeNull()
    expect(qoderExtractRow({ resolved, category, span: { ...span, role: side }, spanId: 'second-call' })).toMatchObject({ kind: 'tool', role: side, call: { id: 'second-call' } })
    expect(qoderExtractRow({ resolved, category, span: { ...span, role: 'other' }, spanId: 'second-call' })).toMatchObject({ kind: 'tool', role: side })
  })

  it('keeps the second native command when both tool names match', () => {
    const source = makeMessage({ agentProvider: AgentProvider.QODER, content: rawContent(requestFrame), spanId: 'second-call', spanType: 'Bash' })
    const { prepared, extraction } = prepareChatRow(source)
    expect(extraction.kind).toBe('row')
    if (extraction.kind !== 'row' || extraction.row.kind !== 'tool' || extraction.row.call.kind !== 'execute')
      throw new Error('The second native Qoder call produced no execute row.')
    expect(extraction.row.call.id).toBe('second-call')
    expect(extraction.row.call.request.command).toBe('node -e "process.stdout.write(String(70 + 7))"')
    expect(prepared.original.rawText).toBe(JSON.stringify(requestFrame))
    expect(prepared.original.parentObject).toEqual(requestFrame)
  })

  it('keeps the second native result and its exact paired command', () => {
    const request = prepareMessage(makeMessage({ agentProvider: AgentProvider.QODER, content: rawContent(requestFrame), spanId: 'second-call', spanType: 'Bash' }))
    const result = prepareMessage(makeMessage({ agentProvider: AgentProvider.QODER, content: rawContent(resultFrame), spanId: 'second-call', spanType: 'Bash' }))
    const extraction = extractPreparedRow(result, { span: { request: request.resolved, result: result.resolved, role: 'result', visibleRows: { request: true, result: true } } })
    expect(extraction.kind).toBe('row')
    if (extraction.kind !== 'row' || extraction.row.kind !== 'tool' || extraction.row.call.kind !== 'execute')
      throw new Error('The second native Qoder result produced no execute row.')
    expect(extraction.row.call.id).toBe('second-call')
    expect(extraction.row.call.request.command).toBe('node -e "process.stdout.write(String(70 + 7))"')
    expect(typedResult(extraction.row.call)?.commands[0]?.output).toBe('77')
    expect(request.original.rawText).toBe(JSON.stringify(requestFrame))
    expect(result.original.rawText).toBe(JSON.stringify(resultFrame))
  })

  it('does not substitute the first native call for a foreign stored span', () => {
    const source = makeMessage({ agentProvider: AgentProvider.QODER, content: rawContent(requestFrame), spanId: 'foreign-call', spanType: 'Bash' })
    const { prepared, extraction } = prepareChatRow(source)
    expect(extraction.kind).toBe('unsupported')
    expect(prepared.original.rawText).toBe(JSON.stringify(requestFrame))
  })

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
