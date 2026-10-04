import type { RowExtractionInput, ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { classifyCommandCodeMessage } from '../classification'
import { commandCodeNativeFuzzyEditRequest, commandCodeNativeFuzzyEditResult } from '../toolResults.fixtures'
import { commandCodeExtractRow } from './row'
import '../plugin'

const provider = AgentProvider.COMMAND_CODE
const emptySpan: ToolSpanContext = { request: undefined, result: undefined, role: 'other', visibleRows: { request: false, result: false } }
const event = (type: string, fields: Record<string, unknown> = {}) => ({ type: 'event', seq: 1, event: { type, ...fields } })

function resolved(payload: Record<string, unknown>) {
  return resolveMessageForRendering({ rawText: JSON.stringify(payload), topLevel: payload, parentObject: payload, wrapper: null }, provider)
}

function extract(payload: Record<string, unknown>, options: Partial<Omit<RowExtractionInput, 'resolved' | 'category'>> = {}) {
  const content = resolved(payload)
  const category = classifyCommandCodeMessage({ ...content, agentProvider: provider, ...(options.completion === undefined ? {} : { completion: options.completion }) })
  return commandCodeExtractRow({ resolved: content, category, span: emptySpan, ...options })
}

describe('commandCodeExtractRow', () => {
  it.each(['request', 'result'] as const)('refuses an explicit no-side context for a native %s', (side) => {
    const frame = side === 'request'
      ? event('tool_queued', { toolCallId: 'native-role-call', toolName: 'read_file', input: { file_path: '/work/native.txt' } })
      : event('tool_completed', { toolCallId: 'native-role-call', toolName: 'read_file', result: [{ type: 'text', text: 'Native bytes.' }] })
    expect(extract(frame, { span: { ...emptySpan, role: 'none' } })).toBeNull()
    expect(extract(frame, { span: { ...emptySpan, role: side } })).toMatchObject({ kind: 'tool', role: side, call: { id: 'native-role-call' } })
    expect(extract(frame)).toMatchObject({ kind: 'tool', role: side })
  })

  it.each(['界😀\nsecond line\n', ''])('keeps exact native queued write content %j', (content) => {
    const queued = event('tool_queued', { toolCallId: 'native-write', toolName: 'write_file', input: { file_path: '/work/native-created.txt', content } })
    const row = extract(queued)
    if (row?.kind !== 'tool' || row.call.kind !== 'write')
      throw new Error('The native queued write must remain a write row.')
    expect(row.call.request.changes).toEqual([{ filePath: '/work/native-created.txt', operation: 'add', oldStr: '', newStr: content, structuredPatch: null }])
    expect(row.call.result).toBeUndefined()
  })

  it('keeps the real normalized native edit result separate from its requested changes', () => {
    const request = resolved(commandCodeNativeFuzzyEditRequest)
    const row = extract(commandCodeNativeFuzzyEditResult, { span: { ...emptySpan, request, role: 'result', visibleRows: { request: true, result: true } } })
    if (row?.kind !== 'tool' || row.call.kind !== 'edit')
      throw new Error('The real native edit must remain an edit row.')
    expect(row.call.request.changes[0]?.newStr).toBe('const value = "new";\n')
    expect(row.call.result).toEqual({ text: commandCodeNativeFuzzyEditResult.event.result[0]!.text, unparsed: true })
    expect(row.call.result).not.toHaveProperty('changes')
  })
  it('joins native text blocks in exact order and keeps thinking separate', () => {
    expect(extract(event('message_end', { content: [{ type: 'thinking', thinking: 'Hidden duplicate.' }, { type: 'text', text: ' First\n' }, { type: 'text', text: 'Second ' }] }))).toEqual({ kind: 'assistant-text', text: ' First\nSecond ' })
    expect(extract(event('thinking_end', { text: 'Exact native reasoning.' }))).toEqual({ kind: 'assistant-thinking', text: 'Exact native reasoning.' })
  })

  it('reads the shared user and plan execution rows', () => {
    expect(extract({ content: 'Exact user text.' })).toEqual({ kind: 'user', text: 'Exact user text.', attachments: [] })
    expect(extract({ content: 'Execute the plan.', planExecution: true })).toEqual({ kind: 'plan-execution', text: 'Execute the plan.' })
  })

  it('pairs only exact native request and result identities', () => {
    const result = event('tool_completed', { toolCallId: 'my-call', toolName: 'read_file', result: [{ type: 'text', text: 'Exact file bytes.' }] })
    const request = resolved(event('tool_queued', { toolCallId: 'my-call', toolName: 'read_file', input: { file_path: '/my/file.txt', offset: 0 } }))
    const row = extract(result, { span: { ...emptySpan, request, role: 'result', visibleRows: { request: true, result: true } } })
    expect(row?.kind === 'tool' ? row.call.request : null).toMatchObject({ path: '/my/file.txt', offset: 0 })
    expect(row?.kind === 'tool' ? row.call.result : null).toEqual({ lines: null, fallbackContent: 'Exact file bytes.' })
    const sibling = resolved(event('tool_queued', { toolCallId: 'another-call', toolName: 'read_file', input: { file_path: '/another/file.txt' } }))
    const unpaired = extract(result, { span: { ...emptySpan, request: sibling, role: 'result' } })
    expect(unpaired?.kind === 'tool' ? unpaired.call.request : null).toMatchObject({ path: '' })
  })

  it('does not treat a retained opener as a completed native result', () => {
    const request = event('tool_queued', { toolCallId: 'call', toolName: 'shell_command', input: { command: 'native command' } })
    const row = extract(request, { completion: MessageCompletion.INTERRUPTED, span: { ...emptySpan, result: resolved(request), role: 'result' } })
    expect(row?.kind === 'tool' ? row.call.status : null).toBe('cancelled')
    expect(row?.kind === 'tool' ? row.call.result : null).toBeUndefined()
  })

  it('keeps denied and failed native tools distinct', () => {
    const denied = extract(event('tool_hook_blocked', { toolCallId: 'call', toolName: 'write_file', hookOutput: 'Native permission is required.' }))
    expect(denied?.kind === 'tool' ? denied.call.status : null).toBe('declined')
    expect(denied?.kind === 'tool' ? denied.call.result : null).toEqual({ failure: true, text: 'Native permission is required.' })
    const failed = extract(event('tool_errored', { toolCallId: 'call', toolName: 'read_file', error: { message: 'Native file is absent.' } }))
    expect(failed?.kind === 'tool' ? failed.call.status : null).toBe('failed')
  })

  it('preserves exact native image bytes in a read result', () => {
    const image = { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'aGVsbG8=' } }
    const row = extract(event('tool_completed', { toolCallId: 'call', toolName: 'read_file', result: [{ type: 'text', text: 'Native image.' }, image] }))
    expect(row?.kind === 'tool' && row.call.kind === 'read' ? row.call.images : null).toHaveLength(1)
  })

  it('keeps typed native MCP text, image and resource content', () => {
    const row = extract(event('tool_completed', { toolCallId: 'call', toolName: 'mcp__echo_probe__echo', result: [
      { type: 'text', text: 'Exact MCP text.' },
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'aGVsbG8=' } },
      { type: 'resource', resource: { uri: 'probe://native', text: 'Exact resource text.' } },
    ] }))
    expect(row?.kind === 'tool' && row.call.kind === 'mcp' ? row.call.result : null).toMatchObject({ content: [{ type: 'text', text: 'Exact MCP text.' }, { type: 'image' }, { type: 'resource', uri: 'probe://native', text: 'Exact resource text.' }] })
  })

  it('does not identify an unknown native tool as an MCP tool', () => {
    const row = extract(event('tool_queued', { toolCallId: 'call', toolName: 'new_native_tool', input: { native: true } }))
    expect(row?.kind === 'tool' ? row.call.kind : null).toBe('other')
  })

  it('reads a real child launch prompt and the returned native report', () => {
    const request = resolved(event('tool_queued', { toolCallId: 'spawn', toolName: 'agent', input: { description: 'Native child', prompt: 'Exact child prompt.', subagent_type: 'general' } }))
    const row = extract(event('tool_completed', { toolCallId: 'spawn', toolName: 'agent', result: [{ type: 'text', text: 'Exact child report.' }] }), { span: { ...emptySpan, request, role: 'result' } })
    expect(row?.kind === 'tool' ? row.call.request : null).toMatchObject({ prompt: 'Exact child prompt.', agentType: 'general' })
    expect(row?.kind === 'tool' ? row.call.result : null).toMatchObject({ agents: [{ agentId: 'spawn', body: 'Exact child report.' }] })
  })
})
