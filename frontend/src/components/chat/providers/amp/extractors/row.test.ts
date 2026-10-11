import type { ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { providerToolCall } from '~/test-support/toolCallFixture'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { ampToolResultRow, ampToolUseRow } from '../toolResults.fixtures'
import { ampExtractRow } from './row'
import '~/components/chat/providers'

const provider = AgentProvider.AMP
const noSpan: ToolSpanContext = { request: undefined, result: undefined, role: 'result', visibleRows: { request: false, result: true } }

function extract(parent: Record<string, unknown>, kind: string, span: ToolSpanContext = noSpan) {
  const resolved = resolveMessageForRendering({ rawText: '', topLevel: parent, parentObject: parent, wrapper: null }, provider)
  return ampExtractRow({ category: { kind } as never, resolved, span })
}

const assistant = (content: unknown[]) => ({ type: 'assistant', message: { role: 'assistant', content }, session_id: 'T-1' })

describe('ampExtractRow', () => {
  it('reads the text and the thinking of an assistant row', () => {
    expect(extract(assistant([{ type: 'text', text: 'done' }]), 'assistant_text')).toEqual({ kind: 'assistant-text', text: 'done' })
    expect(extract(assistant([{ type: 'thinking', thinking: '**Planning**' }]), 'assistant_thinking')).toEqual({ kind: 'assistant-thinking', text: '**Planning**' })
    expect(extract(assistant([]), 'assistant_text')).toEqual({ kind: 'hidden' })
  })

  it('reads LeapMux\'s own user row', () => {
    expect(extract({ content: 'hello' }, 'user_content')).toMatchObject({ kind: 'user', text: 'hello' })
  })

  it('reads LeapMux\'s own plan execution row', () => {
    expect(extract({ content: 'Run the plan.', planExecution: true }, 'plan_execution')).toEqual({ kind: 'plan-execution', text: 'Run the plan.' })
  })

  it('pairs a result with the call of the same id only', () => {
    const result = ampToolResultRow('{"output":"a\\n","exitCode":0}', false, 'TU-a')
    const mine = input(ampToolUseRow('shell_command', { command: 'printf a' }, 'TU-a'), undefined, provider)
    const sibling = input(ampToolUseRow('shell_command', { command: 'printf b' }, 'TU-b'), undefined, provider)
    const paired = extract(result, 'tool_result', { ...noSpan, request: mine })
    expect(paired?.kind === 'tool' ? paired.call.request : null).toMatchObject({ command: 'printf a' })
    const unpaired = extract(result, 'tool_result', { ...noSpan, request: sibling })
    // A result whose call the store did not resolve draws as a tool with no name.
    expect(unpaired?.kind === 'tool' ? unpaired.call.kind : null).toBe('mcp')
  })

  it('draws a running call as the request side, and its landed result on it', () => {
    const call = ampToolUseRow('shell_command', { command: 'ls' }, 'TU-a')
    const running = extract(call, 'tool_use', { ...noSpan, role: 'request' })
    expect(running).toMatchObject({ kind: 'tool', role: 'request' })
    expect(running?.kind === 'tool' ? running.call.status : null).toBe('unstated')
    const landed = input(ampToolResultRow('{"output":"x","exitCode":0}', false, 'TU-a'), undefined, provider)
    const answered = extract(call, 'tool_use', { ...noSpan, role: 'request', result: landed })
    expect(answered?.kind === 'tool' ? answered.call.status : null).toBe('completed')
  })

  it('answers null for a tool category on a row that is not a tool row', () => {
    expect(extract({ type: 'result' }, 'tool_use')).toBeNull()
  })

  it('answers null for a category it does not draw', () => {
    expect(extract({ type: 'result' }, 'result_divider')).toBeNull()
  })
})

describe('amp tool rows', () => {
  it('reads a call that a stopped turn left behind as cancelled', () => {
    const call = providerToolCall(provider, ampToolUseRow('shell_command', { command: 'sleep 40' }), { role: 'result' })
    expect(call?.status).toBe('unstated')
    const retained = resolveMessageForRendering({ ...input(ampToolUseRow('shell_command', { command: 'sleep 40' }), undefined, provider), completion: MessageCompletion.INTERRUPTED }, provider)
    const row = ampExtractRow({ category: { kind: 'tool_result' }, resolved: retained, span: noSpan })
    expect(row?.kind === 'tool' ? [row.role, row.call.status] : null).toEqual(['result', 'cancelled'])
  })

  it('reads a refusal as declined and a cancellation as cancelled', () => {
    const request = input(ampToolUseRow('shell_command', { command: 'rm -rf /' }), undefined, provider)
    const status = (content: string, isError: boolean) => providerToolCall(provider, ampToolResultRow(content, isError), { request })?.status
    expect(status('Plugin error: Not that one.\n', true)).toBe('declined')
    expect(status('Tool rejected by plugin: Matches built-in permissions rule 75: ask shell_command', false)).toBe('declined')
    expect(status('Tool execution rejected by user: no', true)).toBe('declined')
    expect(status('Tool execution cancelled: User cancelled', true)).toBe('cancelled')
    expect(status('Error: command not found', true)).toBe('failed')
    expect(status('{"output":"","exitCode":1}', false)).toBe('completed')
  })

  it('draws a tool of a Model Context Protocol server with its server and its tool', () => {
    const request = input(ampToolUseRow('mcp__github__search_code', { q: 'x' }), undefined, provider)
    const call = providerToolCall(provider, ampToolResultRow('found 3'), { request })
    expect(call?.kind).toBe('mcp')
    expect(call?.kind === 'mcp' ? call.request : null).toMatchObject({ server: 'github', tool: 'search_code' })
  })

  it('draws a tool no table lists as the generic card with its words', () => {
    const request = input(ampToolUseRow('upload_thread_file', { path: '/a' }), undefined, provider)
    const call = providerToolCall(provider, ampToolResultRow('Uploaded.'), { request })
    expect(call?.kind).toBe('mcp')
    expect(call?.kind === 'mcp' ? call.request : null).toMatchObject({ server: '', tool: 'upload_thread_file' })
  })
})
