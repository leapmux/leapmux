import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { classifyCodebuddyMessage } from '../classification'
import { codebuddyExtractRow } from './row'

const NO_SIDES: ToolSpanContext = { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } }

describe('codebuddyExtractRow', () => {
  it('extracts a stored Workflow child tool request and its native result', () => {
    const request = { type: 'function_call', id: 'request-record', callId: 'child-read-1', name: 'Read', arguments: '{"file_path":"/work/marker.txt"}' }
    const result = { type: 'function_call_result', id: 'result-record', callId: 'child-read-1', status: 'completed', output: { type: 'text', text: 'CHILD_FILE_MARKER' } }
    const resolvedRequest = resolveMessageForRendering({ rawText: JSON.stringify(request), topLevel: request, parentObject: request, wrapper: null }, AgentProvider.CODEBUDDY)
    const resolvedResult = resolveMessageForRendering({ rawText: JSON.stringify(result), topLevel: result, parentObject: result, wrapper: null }, AgentProvider.CODEBUDDY)
    const requestCategory = classifyCodebuddyMessage({ ...resolvedRequest, agentProvider: AgentProvider.CODEBUDDY })
    const resultCategory = classifyCodebuddyMessage({ ...resolvedResult, agentProvider: AgentProvider.CODEBUDDY })
    const requestRow = codebuddyExtractRow({ resolved: resolvedRequest, category: requestCategory, span: { ...NO_SIDES, role: 'request', visibleRows: { request: true, result: true } } })
    const resultRow = codebuddyExtractRow({ resolved: resolvedResult, category: resultCategory, span: { ...NO_SIDES, request: resolvedRequest, role: 'result', visibleRows: { request: true, result: true } } })

    expect(requestRow?.kind).toBe('tool')
    expect(resultRow?.kind).toBe('tool')
    if (requestRow?.kind !== 'tool' || resultRow?.kind !== 'tool')
      return
    expect(requestRow.role).toBe('request')
    expect(requestRow.call.id).toBe('child-read-1')
    expect(requestRow.call.name).toBe('Read')
    expect(requestRow.call.kind).toBe('read')
    if (requestRow.call.kind === 'read')
      expect(requestRow.call.request.path).toBe('/work/marker.txt')
    expect(resultRow.role).toBe('result')
    expect(resultRow.call.id).toBe('child-read-1')
    expect(resultRow.call.name).toBe('Read')
    expect(resultRow.call.result).toEqual({ text: 'CHILD_FILE_MARKER', unparsed: true })
  })

  it('marks a failed stored tool result as failed', () => {
    const request = { type: 'function_call', callId: 'child-read-2', name: 'Read', arguments: '{"file_path":"/work/marker.txt"}' }
    const result = { type: 'function_call_result', callId: 'child-read-2', status: 'failed', output: { type: 'text', text: 'Read failed.' } }
    const resolvedRequest = resolveMessageForRendering({ rawText: JSON.stringify(request), topLevel: request, parentObject: request, wrapper: null }, AgentProvider.CODEBUDDY)
    const resolvedResult = resolveMessageForRendering({ rawText: JSON.stringify(result), topLevel: result, parentObject: result, wrapper: null }, AgentProvider.CODEBUDDY)
    const category = classifyCodebuddyMessage({ ...resolvedResult, agentProvider: AgentProvider.CODEBUDDY })
    const row = codebuddyExtractRow({ resolved: resolvedResult, category, span: { ...NO_SIDES, request: resolvedRequest, role: 'result', visibleRows: { request: true, result: true } } })
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      return
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toEqual({ failure: true, text: 'Read failed.' })
  })

  it('extracts stored Workflow child answer text', () => {
    const stored = { type: 'message', role: 'assistant', content: [{ type: 'output_text', text: 'ARCHIVED_CHILD_TEXT' }] }
    const resolved = resolveMessageForRendering({ rawText: JSON.stringify(stored), topLevel: stored, parentObject: stored, wrapper: null }, AgentProvider.CODEBUDDY)
    const category = classifyCodebuddyMessage({ ...resolved, agentProvider: AgentProvider.CODEBUDDY })

    expect(codebuddyExtractRow({ resolved, category, span: NO_SIDES }))
      .toEqual({ kind: 'assistant-text', text: 'ARCHIVED_CHILD_TEXT' })
  })

  it('joins stored answer blocks in native order', () => {
    const stored = { type: 'message', role: 'assistant', content: [
      { type: 'output_text', text: 'FIRST' },
      { type: 'other', text: 'HIDDEN' },
      { type: 'output_text', text: 'SECOND' },
    ] }
    const resolved = resolveMessageForRendering({ rawText: JSON.stringify(stored), topLevel: stored, parentObject: stored, wrapper: null }, AgentProvider.CODEBUDDY)
    const category = classifyCodebuddyMessage({ ...resolved, agentProvider: AgentProvider.CODEBUDDY })

    expect(codebuddyExtractRow({ resolved, category, span: NO_SIDES }))
      .toEqual({ kind: 'assistant-text', text: 'FIRSTSECOND' })
  })
})
