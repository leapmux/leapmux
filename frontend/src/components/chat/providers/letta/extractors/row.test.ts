import type { ToolSpanContext } from '~/components/chat/rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { classifyLettaMessage } from '../classification'
import { lettaExtractRow } from './row'

function resolvedLettaFrame(frame: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(frame), topLevel: frame, parentObject: frame, wrapper: null }, AgentProvider.LETTA)
}

describe('lettaExtractRow', () => {
  function nativeToolRow(options: { name: string, args: unknown, status?: string, result?: unknown, resultId?: string, completion?: MessageCompletion, role?: 'request' | 'result' }) {
    const request = resolvedLettaFrame({ message_type: 'client_tool_start', tool_call_id: 'actual-tool', tool_name: options.name, tool_args: options.args })
    const result = options.status === undefined
      ? undefined
      : resolvedLettaFrame({
          message_type: 'tool_return_message',
          tool_call_id: options.resultId ?? 'actual-tool',
          status: options.status,
          tool_return: options.result ?? '',
        })
    const role = options.role ?? 'request'
    const selected = role === 'result' && result ? result : request
    const row = lettaExtractRow({
      resolved: selected,
      category: classifyLettaMessage({ ...selected, agentProvider: AgentProvider.LETTA }),
      span: { request, result, role, visibleRows: { request: true, result: result !== undefined } },
      ...(options.completion !== undefined ? { completion: options.completion } : {}),
    })
    expect(row?.kind).toBe('tool')
    if (!row || row.kind !== 'tool')
      throw new Error('The actual Letta tool frame produced no tool row.')
    return row
  }

  it('uses actual serialized client arguments and matching success to draw the applied edit', () => {
    const row = nativeToolRow({
      name: 'Edit',
      args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }),
      status: 'success',
      result: '{"message":"Successfully replaced 1 occurrence","replacements":1,"startLine":1}',
    })
    expect(row.call.kind).toBe('edit')
    expect(row.call.name).toBe('Edit')
    expect(row.call.id).toBe('actual-tool')
    expect(row.call.status).toBe('completed')
    expect(row.call.request).toMatchObject({ changes: [{ filePath: '/private/native.txt', oldStr: 'OLD42', newStr: 'NEW42' }] })
    expect(row.call.result).toMatchObject({ changes: [{ filePath: '/private/native.txt', oldStr: 'OLD42', newStr: 'NEW42' }] })
  })

  it('keeps native error status even when Bash stderr contains no failure words', () => {
    const row = nativeToolRow({ name: 'Bash', args: JSON.stringify({ command: 'printf computed >&2; exit 7' }), status: 'error', result: 'SHELLERR77', role: 'result' })
    expect(row.call.name).toBe('Bash')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toMatchObject({ failure: true, text: 'SHELLERR77' })
  })

  it('preserves an empty replacement in a completed native edit', () => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: '' }), status: 'success', result: 'The replacement completed.' })
    expect(row.call.kind).toBe('edit')
    expect(row.call.result).toMatchObject({ changes: [{ oldStr: 'OLD42', newStr: '' }] })
  })

  it('does not apply an unrelated completed tool result to this edit', () => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }), status: 'success', result: 'OTHER_RESULT', resultId: 'another-tool' })
    expect(row.call.result).toBeUndefined()
    expect(row.call.status).not.toBe('completed')
  })

  it.each(['error', 'success'])('keeps a retained interruption distinct from native %s', (status) => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }), status, result: 'native result', completion: MessageCompletion.INTERRUPTED })
    expect(row.call.status).toBe('cancelled')
    expect(row.call.result ?? {}).not.toHaveProperty('changes')
  })

  it('keeps a failed native edit from reporting applied changes', () => {
    const row = nativeToolRow({ name: 'Edit', args: JSON.stringify({ file_path: '/private/native.txt', old_string: 'OLD42', new_string: 'NEW42' }), status: 'error', result: 'No matching input.' })
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toMatchObject({ failure: true, text: 'No matching input.' })
    expect(row.call.result ?? {}).not.toHaveProperty('changes')
  })

  it('preserves successful empty native output as a completed result', () => {
    const row = nativeToolRow({ name: 'Bash', args: JSON.stringify({ command: 'exit 0' }), status: 'success', result: '', role: 'result' })
    expect(row.call.status).toBe('completed')
    expect(row.call.name).toBe('Bash')
    expect(row.call.result).toBeDefined()
  })

  it.each(['{', 'null', '[]', '', false])('does not invent an applied diff from malformed native arguments: %j', (args) => {
    const row = nativeToolRow({ name: 'Edit', args, status: 'success', result: 'A native result.' })
    expect(row.call.result ?? {}).not.toHaveProperty('changes')
    expect(row.call.request).not.toHaveProperty('changes')
  })

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

function pairedLettaNativeRow(requestFrame: Record<string, unknown>, resultFrame: Record<string, unknown>, role: 'request' | 'result') {
  const request = resolvedLettaFrame(requestFrame)
  const result = resolvedLettaFrame(resultFrame)
  const selected = role === 'result' ? result : request
  const row = lettaExtractRow({
    resolved: selected,
    category: classifyLettaMessage({ ...selected, agentProvider: AgentProvider.LETTA }),
    span: { request, result, role, visibleRows: { request: true, result: true } },
  })
  if (!row || row.kind !== 'tool')
    throw new Error('The native Letta frames must retain their tool row.')
  return row
}

describe('letta native result boundaries', () => {
  const request = { message_type: 'client_tool_start', tool_call_id: 'call', run_id: 'current-run', tool_name: 'Edit', tool_args: '{"file_path":"native.txt","old_string":"old","new_string":"new"}' }
  const result = { message_type: 'tool_return_message', tool_call_id: 'call', run_id: 'current-run', status: 'success', tool_return: 'native returned data' }

  it('keeps a current-window snapshot from completing the paired request', () => {
    const progress = { ...result, id: 'synthetic-tool-return-stream-call' }
    const row = pairedLettaNativeRow(request, progress, 'request')
    expect(row.call.result).toBeUndefined()
    expect(row.call.status).not.toBe('completed')
  })

  it.each(['old-run', undefined])('keeps a different or absent native run from applying a result: %s', (run_id) => {
    const row = pairedLettaNativeRow(request, { ...result, run_id }, 'request')
    expect(row.call.result).toBeUndefined()
    expect(row.call.status).not.toBe('completed')
  })

  it('retains an actual old-run final without pairing the new request arguments', () => {
    const row = pairedLettaNativeRow(request, { ...result, run_id: 'old-run', id: 'synthetic-tool-return-actual-final' }, 'result')
    expect(row.call.status).toBe('completed')
    expect(row.call.kind).toBe('other')
    expect(row.call.result).not.toHaveProperty('changes')
    expect(row.call.result).toMatchObject({ content: [{ type: 'text', text: 'native returned data' }] })
  })

  it.each(['', 0, false, null])('retains present client-end output without replacing %j', (tool_return) => {
    const row = pairedLettaNativeRow({ ...request, tool_name: 'Bash', tool_args: '{"command":"native command"}' }, { ...result, message_type: 'client_tool_end', tool_return }, 'result')
    expect(row.call.status).toBe('completed')
    expect(row.call.result).toMatchObject({ content: [{ type: 'text', text: typeof tool_return === 'string' ? tool_return : JSON.stringify(tool_return) }] })
  })

  it('retains a matching composite error and its zero output', () => {
    const native = { message_type: 'tool_return_message', run_id: 'current-run', tool_call_id: 'call', tool_returns: [{ tool_call_id: 'call', status: 'error', tool_return: 0 }] }
    const row = pairedLettaNativeRow(request, native, 'result')
    expect(row.call.status).toBe('failed')
    expect(row.call.result).toMatchObject({ failure: true, text: '0' })
  })

  it.each([
    { message_type: 'tool_return_message', run_id: 'current-run', tool_call_id: 'call', status: 'success' },
    { message_type: 'tool_return_message', run_id: 'current-run', tool_call_id: 'call', status: 'success', tool_returns: [{ tool_call_id: 'foreign', tool_return: 'foreign data' }] },
  ])('retains an incomplete native final without inventing returned data: %j', (native) => {
    const row = pairedLettaNativeRow({ ...request, tool_name: 'Bash', tool_args: '{"command":"native command"}' }, native, 'result')
    expect(row.call.status).toBe('incomplete')
    expect(row.call.result).toBeUndefined()
  })
})
