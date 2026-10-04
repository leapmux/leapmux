import type { ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { ohMyPiExtractRow } from './row'
import '~/components/chat/providers'

const provider = AgentProvider.OH_MY_PI
const noSpan: ToolSpanContext = { request: undefined, result: undefined, role: 'result', visibleRows: { request: false, result: true } }

function extract(parent: Record<string, unknown>, kind: string, span: ToolSpanContext = noSpan) {
  const resolved = resolveMessageForRendering({ rawText: '', topLevel: parent, parentObject: parent, wrapper: null }, provider)
  return ohMyPiExtractRow({ category: { kind } as never, resolved, span })
}

describe('ohMyPiExtractRow', () => {
  it('hides an assistant message with no text', () => {
    expect(extract({ type: 'message_end', message: { role: 'assistant', content: [] } }, 'assistant_text')).toEqual({ kind: 'hidden' })
  })

  it('reads an assistant message as its text blocks alone', () => {
    const frame = { type: 'message_end', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'Plan.' }, { type: 'text', text: 'One.' }, { type: 'text', text: 'Two.' }] } }
    expect(extract(frame, 'assistant_text')).toEqual({ kind: 'assistant-text', text: 'One.\n\nTwo.' })
  })

  it('draws no thinking row of its own, because the worker writes one', () => {
    expect(extract({ type: 'message_end', message: { role: 'assistant', content: [{ type: 'thinking', thinking: 'Plan.' }] } }, 'assistant_thinking')).toBeNull()
  })

  it('pairs a result with the start frame of the same call only', () => {
    const end = { type: 'tool_execution_end', toolCallId: 'call_1', toolName: 'read', result: { content: [{ type: 'text', text: '1:x' }], details: {} }, isError: false }
    const mine = input({ type: 'tool_execution_start', toolCallId: 'call_1', toolName: 'read', args: { path: 'a.ts' } }, undefined, provider)
    const sibling = input({ type: 'tool_execution_start', toolCallId: 'call_2', toolName: 'read', args: { path: 'b.ts' } }, undefined, provider)
    const paired = extract(end, 'tool_result', { ...noSpan, request: mine })
    expect(paired?.kind === 'tool' ? paired.call.request : null).toMatchObject({ path: 'a.ts' })
    const unpaired = extract(end, 'tool_result', { ...noSpan, request: sibling })
    expect(unpaired?.kind === 'tool' ? unpaired.call.request : null).toMatchObject({ path: '' })
  })

  it('draws a result row as the result side of its span', () => {
    const end = { type: 'tool_execution_end', toolCallId: 'call_1', toolName: 'bash', result: { content: [{ type: 'text', text: 'ok' }], details: {} }, isError: false }
    const row = extract(end, 'tool_result')
    expect(row?.kind === 'tool' ? row.role : null).toBe('result')
  })

  it('pairs a running call with the end frame of the same call only', () => {
    const start = { type: 'tool_execution_start', toolCallId: 'call_1', toolName: 'bash', args: { command: 'ls' } }
    const mine = input({ type: 'tool_execution_end', toolCallId: 'call_1', toolName: 'bash', result: { content: [{ type: 'text', text: 'a.ts' }], details: {} }, isError: false }, undefined, provider)
    const sibling = input({ type: 'tool_execution_end', toolCallId: 'call_2', toolName: 'bash', result: { content: [{ type: 'text', text: 'b.ts' }], details: {} }, isError: true }, undefined, provider)
    const requestSpan: ToolSpanContext = { request: undefined, result: undefined, role: 'request', visibleRows: { request: true, result: false } }
    const paired = extract(start, 'tool_use', { ...requestSpan, result: mine })
    expect(paired?.kind === 'tool' ? paired.call.status : null).toBe('completed')
    // A sibling's end frame states neither this call's output nor its failure. omp
    // states no status for a call that runs, so the lifecycle leaves it unstated.
    const unpaired = extract(start, 'tool_use', { ...requestSpan, result: sibling })
    expect(unpaired?.kind === 'tool' ? unpaired.call.status : null).toBe('unstated')
    expect(unpaired?.kind === 'tool' && 'result' in unpaired.call ? unpaired.call.result : undefined).toBeUndefined()
  })

  it('reads LeapMux\'s user row and plan-execution row', () => {
    expect(extract({ content: 'hello' }, 'user_content')).toEqual({ kind: 'user', text: 'hello', attachments: [] })
    expect(extract({ content: 'Run the plan.', planExecution: true }, 'plan_execution')).toEqual({ kind: 'plan-execution', text: 'Run the plan.' })
  })

  it('answers null for a tool category on a frame that is not a tool frame', () => {
    expect(extract({ type: 'message_end' }, 'tool_use')).toBeNull()
    // A tool frame that states no call id is no call either.
    expect(extract({ type: 'tool_execution_start', toolName: 'bash', args: {} }, 'tool_use')).toBeNull()
  })

  it('answers null for a category it does not draw', () => {
    expect(extract({ type: 'agent_end' }, 'result_divider')).toBeNull()
  })
})
