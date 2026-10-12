import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { classifyCodebuddyMessage } from '../classification'
import { codebuddyExtractRow } from './row'

const NO_SIDES: ToolSpanContext = { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } }

function nativeReplRows(stored: boolean, text: string, completion?: MessageCompletion) {
  const code = 'throw new Error("computed-" + (70 + 7))'
  const request = stored
    ? { type: 'function_call', callId: 'native-repl-row', name: 'REPL', arguments: JSON.stringify({ code }) }
    : { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'native-repl-row', name: 'REPL', input: { code } }] } }
  const result = stored
    ? { type: 'function_call_result', callId: 'native-repl-row', status: 'completed', output: { type: 'text', text } }
    : { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'native-repl-row', is_error: false, content: text }] } }
  const resolvedRequest = resolveMessageForRendering({ rawText: JSON.stringify(request), topLevel: request, parentObject: request, wrapper: null }, AgentProvider.CODEBUDDY)
  const resolvedResult = resolveMessageForRendering({ rawText: JSON.stringify(result), topLevel: result, parentObject: result, wrapper: null }, AgentProvider.CODEBUDDY)
  const category = classifyCodebuddyMessage({ ...resolvedResult, agentProvider: AgentProvider.CODEBUDDY })
  return codebuddyExtractRow({ resolved: resolvedResult, category, span: { ...NO_SIDES, request: resolvedRequest, role: 'result', visibleRows: { request: true, result: true } }, ...(completion === undefined ? {} : { completion }) })
}

describe('codebuddyExtractRow', () => {
  it.each([false, true])('keeps an unknown final request without a result: stored=%s', (stored) => {
    const frame = stored
      ? { type: 'function_call', callId: 'retained-call', name: 'Read', arguments: '{"file_path":"/work/native.txt"}' }
      : { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'retained-call', name: 'Read', input: { file_path: '/work/native.txt' } }] } }
    const resolved = resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.CODEBUDDY)
    const category = classifyCodebuddyMessage({ ...resolved, agentProvider: AgentProvider.CODEBUDDY })
    const row = codebuddyExtractRow({ resolved, category, completion: MessageCompletion.FINISHED, span: { ...NO_SIDES, role: 'result', visibleRows: { request: false, result: true } } })
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The retained CodeBuddy request requires a tool row.')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
  })

  it.each([
    { stored: false, side: 'request' },
    { stored: false, side: 'result' },
    { stored: true, side: 'request' },
    { stored: true, side: 'result' },
  ] as const)('refuses an explicit no-side context for a native $side with stored=$stored', ({ stored, side }) => {
    const frame = stored
      ? side === 'request'
        ? { type: 'function_call', callId: 'native-role-call', name: 'Read', arguments: '{"file_path":"/work/native.txt"}' }
        : { type: 'function_call_result', callId: 'native-role-call', status: 'completed', output: { type: 'text', text: 'Native bytes.' } }
      : side === 'request'
        ? { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'native-role-call', name: 'Read', input: { file_path: '/work/native.txt' } }] } }
        : { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'native-role-call', content: 'Native bytes.' }] } }
    const resolved = resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.CODEBUDDY)
    const category = classifyCodebuddyMessage({ ...resolved, agentProvider: AgentProvider.CODEBUDDY })
    expect(codebuddyExtractRow({ resolved, category, span: { ...NO_SIDES, role: 'none' } })).toBeNull()
    expect(codebuddyExtractRow({ resolved, category, span: { ...NO_SIDES, role: side } })).toMatchObject({ kind: 'tool', role: side, call: { id: 'native-role-call' } })
    expect(codebuddyExtractRow({ resolved, category, span: NO_SIDES })).toMatchObject({ kind: 'tool', role: side })
  })

  describe('a live Bash result', () => {
    // Verbatim shape of a CodeBuddy Code 2.160.0 Bash result: the text is the native
    // record, and `_meta.rawResponse` states how the command ended.
    const record = 'Command: printf x >&2; exit 7\nStdout: (empty)\nStderr: SHELLERR77\n\nExit Code: 7\nSignal: (none)'
    function bashRow(rawResponse: Record<string, unknown> | undefined, text = record, rendered = 'SHELLERR77') {
      const request = { type: 'assistant', message: { content: [{ type: 'tool_use', id: 'native-bash', name: 'Bash', input: { command: 'printf x >&2; exit 7' } }] } }
      const result = { type: 'user', message: { content: [{ type: 'tool_result', tool_use_id: 'native-bash', content: [{ type: 'text', text }], is_error: false, ...(rawResponse === undefined ? {} : { _meta: { rawResponse, renderer: { type: 'text', value: rendered } } }) }] } }
      const resolvedRequest = resolveMessageForRendering({ rawText: JSON.stringify(request), topLevel: request, parentObject: request, wrapper: null }, AgentProvider.CODEBUDDY)
      const resolvedResult = resolveMessageForRendering({ rawText: JSON.stringify(result), topLevel: result, parentObject: result, wrapper: null }, AgentProvider.CODEBUDDY)
      const category = classifyCodebuddyMessage({ ...resolvedResult, agentProvider: AgentProvider.CODEBUDDY })
      const row = codebuddyExtractRow({ resolved: resolvedResult, category, span: { ...NO_SIDES, request: resolvedRequest, role: 'result', visibleRows: { request: true, result: true } } })
      if (row?.kind !== 'tool' || row.call.kind !== 'execute')
        throw new Error('The native Bash result must remain an execute row.')
      return row.call
    }

    it('draws a command result with the code that the native response states', () => {
      const call = bashRow({ exitCode: 7, signal: null, interrupted: false, is_error: true })
      expect(call.result).toStrictEqual({ commands: [{ output: 'SHELLERR77', exitCode: 7 }], unresolvedTerminals: [] })
    })

    it('states the signal that ended the command', () => {
      expect(bashRow({ exitCode: null, signal: 'SIGTERM' }).result).toStrictEqual({ commands: [{ output: 'SHELLERR77', signal: 'SIGTERM' }], unresolvedTerminals: [] })
    })

    it('states no code when the native response is absent or states no number', () => {
      expect(bashRow(undefined).result).toStrictEqual({ commands: [{ output: record }], unresolvedTerminals: [] })
      expect(bashRow({ exitCode: '7' }).result).toStrictEqual({ commands: [{ output: 'SHELLERR77' }], unresolvedTerminals: [] })
    })

    it('keeps an exact empty native failure record instead of the renderer notice', () => {
      const empty = record.replace('SHELLERR77\n', '(empty)')
      expect(bashRow({ exitCode: 7, signal: null }, empty, '(No output)').result)
        .toStrictEqual({ commands: [{ output: empty, exitCode: 7 }], unresolvedTerminals: [] })
    })

    it('keeps the original persisted preview when the renderer supplies only a file notice', () => {
      const preview = '<persisted-output>\nOutput too large (20KB). Full output saved to: /native/output.txt\n\nPreview\nSHELL42\n</persisted-output>'
      expect(bashRow({ exitCode: 0, signal: null }, preview, 'Output too large. Full output saved to: /native/output.txt').result)
        .toStrictEqual({ commands: [{ output: preview, exitCode: 0 }], unresolvedTerminals: [] })
    })
  })

  it('keeps the captured native MCP refusal completed when the provider reports no tool error', () => {
    const request = { type: 'assistant', session_id: 'a9667e8b-85fb-4558-a68b-6cb817df14e6', message: { role: 'assistant', content: [{ type: 'tool_use', id: 'codebuddy-mcp-form', name: 'mcp__form_probe__ask', input: {} }] } }
    const result = { type: 'user', session_id: 'a9667e8b-85fb-4558-a68b-6cb817df14e6', message: { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'codebuddy-mcp-form', content: [{ type: 'text', text: 'FORM_ROUND_TRIP_REFUSED: -32601 Method not found' }], is_error: false }] } }
    const resolvedRequest = resolveMessageForRendering({ rawText: JSON.stringify(request), topLevel: request, parentObject: request, wrapper: null }, AgentProvider.CODEBUDDY)
    const resolvedResult = resolveMessageForRendering({ rawText: JSON.stringify(result), topLevel: result, parentObject: result, wrapper: null }, AgentProvider.CODEBUDDY)
    const category = classifyCodebuddyMessage({ ...resolvedResult, agentProvider: AgentProvider.CODEBUDDY })
    const row = codebuddyExtractRow({ resolved: resolvedResult, category, span: { ...NO_SIDES, request: resolvedRequest, role: 'result', visibleRows: { request: true, result: true } } })
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The captured native MCP result must remain a tool row.')
    expect(row.call.id).toBe('codebuddy-mcp-form')
    expect(row.call.name).toBe('mcp__form_probe__ask')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toEqual({ text: 'FORM_ROUND_TRIP_REFUSED: -32601 Method not found', unparsed: true })
  })

  it.each([false, true])('reads native REPL failures from live and stored frames: stored=%s', (stored) => {
    const text = JSON.stringify({ stdout: '', stderr: '', error: 'Error: computed-77' })
    const row = nativeReplRows(stored, text)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The exact native REPL result must remain a tool row.')
    expect(row.call.id).toBe('native-repl-row')
    expect(row.call.kind).toBe('execute')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toEqual({ failure: true, text })
    if (row.call.kind === 'execute')
      expect(row.call.request).toMatchObject({ command: 'throw new Error("computed-" + (70 + 7))', language: 'javascript' })
  })

  it.each([false, true])('keeps retained cancellation for live and stored native REPL results: stored=%s', (stored) => {
    const row = nativeReplRows(stored, JSON.stringify({ stdout: '', stderr: '', error: 'Error after cancellation.' }), MessageCompletion.INTERRUPTED)
    expect(row?.kind).toBe('tool')
    if (row?.kind !== 'tool')
      throw new Error('The cancelled native REPL result must remain a tool row.')
    expect(row.call.status).toBe('cancelled')
  })

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
      throw new Error('The stored native request and result must remain tool rows.')
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
      throw new Error('The stored native failure must remain a tool row.')
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
