import type { ToolSpanContext } from '../../../rowExtractionTypes'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '~/generated/proto/leapmux/v1/agent_pb'
import { resolveMessageForRendering } from '../../registry'
import { input } from '../../testUtils'
import { ohMyPiExtractRow } from './row'
import '~/components/chat/providers'

const provider = AgentProvider.OH_MY_PI
const noSpan: ToolSpanContext = {
  request: undefined,
  role: 'result',
  visibleRows: { request: false },
}

function extract(parent: Record<string, unknown>, kind: string, span: ToolSpanContext = noSpan) {
  const resolved = resolveMessageForRendering({
    rawText: '',
    topLevel: parent,
    parentObject: parent,
    wrapper: null,
  }, provider)
  return ohMyPiExtractRow({
    category: { kind } as never,
    resolved,
    span,
  })
}

describe('ohMyPiExtractRow', () => {
  it('hides an assistant message with no text', () => {
    expect(extract({
      type: 'message_end',
      message: { role: 'assistant', content: [] },
    }, 'assistant_text')).toEqual({ kind: 'hidden' })
  })

  it('reads an assistant message as its text blocks alone', () => {
    const frame = {
      type: 'message_end',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'Plan.' },
          { type: 'text', text: 'One.' },
          { type: 'text', text: 'Two.' },
        ],
      },
    }
    expect(extract(frame, 'assistant_text')).toEqual({ kind: 'assistant-text', text: 'One.\n\nTwo.' })
  })

  it('draws no thinking row of its own, because the worker writes one', () => {
    expect(extract({
      type: 'message_end',
      message: {
        role: 'assistant',
        content: [
          { type: 'thinking', thinking: 'Plan.' },
        ],
      },
    }, 'assistant_thinking')).toBeNull()
  })

  it('pairs a result with the start frame of the same call only', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call_1',
      toolName: 'read',
      result: {
        content: [
          { type: 'text', text: '1:x' },
        ],
        details: {},
      },
      isError: false,
    }
    const mine = input({
      type: 'tool_execution_start',
      toolCallId: 'call_1',
      toolName: 'read',
      args: { path: 'a.ts' },
    }, undefined, provider)
    const sibling = input({
      type: 'tool_execution_start',
      toolCallId: 'call_2',
      toolName: 'read',
      args: { path: 'b.ts' },
    }, undefined, provider)
    const paired = extract(end, 'tool_result', { ...noSpan, request: mine })
    expect(paired?.kind === 'tool'
      ? paired.call.request
      : null).toMatchObject({ path: 'a.ts' })
    const unpaired = extract(end, 'tool_result', { ...noSpan, request: sibling })
    expect(unpaired?.kind === 'tool'
      ? unpaired.call.request
      : null).toMatchObject({ path: '' })
  })

  it('draws a result row as the result side of its span', () => {
    const end = {
      type: 'tool_execution_end',
      toolCallId: 'call_1',
      toolName: 'bash',
      result: {
        content: [
          { type: 'text', text: 'ok' },
        ],
        details: {},
      },
      isError: false,
    }
    const row = extract(end, 'tool_result')
    expect(row?.kind === 'tool'
      ? row.role
      : null).toBe('result')
  })

  // A start row reads its own frame alone. The end frame of a `read` states that the
  // path was a URL, so only the end row draws the fetch, and the start row draws the
  // read it asked for.
  it('builds a running call from its own start frame, and the full call on its end row', () => {
    const start = {
      type: 'tool_execution_start',
      toolCallId: 'call_1',
      toolName: 'read',
      args: { path: 'example.com' },
    }
    const startRow = input(start, undefined, provider)
    const request = extract(start, 'tool_use', {
      request: startRow,
      role: 'request',
      visibleRows: { request: true },
    })
    if (request?.kind !== 'tool')
      throw new Error('The omp start frame drew no tool row.')
    expect(request.role).toBe('request')
    expect(request.call.kind).toBe('read')
    expect(request.call.status).toBe('unstated')
    expect('result' in request.call
      ? request.call.result
      : undefined).toBeUndefined()

    const result = extract({
      type: 'tool_execution_end',
      toolCallId: 'call_1',
      toolName: 'read',
      result: {
        content: [
          { type: 'text', text: '# Example' },
        ],
        details: { kind: 'url', url: 'https://example.com' },
      },
      isError: false,
    }, 'tool_result', {
      request: startRow,
      role: 'result',
      visibleRows: { request: true },
    })
    if (result?.kind !== 'tool')
      throw new Error('The omp end frame drew no tool row.')
    expect(result).toMatchObject({ role: 'result', hasRequestRow: true })
    expect(result.call.kind).toBe('fetch')
    expect(result.call.status).toBe('completed')
    expect(result.call.request).toEqual({ url: 'example.com' })
  })

  // omp states the whole checklist in the RESULT, so the start row states no items.
  it('states the checklist on the end row alone', () => {
    const start = {
      type: 'tool_execution_start',
      toolCallId: 'call_1',
      toolName: 'todo',
      args: { op: 'start', task: 'Write code' },
    }
    const startRow = input(start, undefined, provider)
    const request = extract(start, 'tool_use', {
      request: startRow,
      role: 'request',
      visibleRows: { request: true },
    })
    const result = extract({
      type: 'tool_execution_end',
      toolCallId: 'call_1',
      toolName: 'todo',
      result: {
        content: [
          { type: 'text', text: 'Remaining items (1)' },
        ],
        details: {
          op: 'start',
          phases: [
            {
              name: 'Build',
              tasks: [
                { content: 'Write code', status: 'in_progress' },
              ],
            },
          ],
        },
      },
      isError: false,
    }, 'tool_result', {
      request: startRow,
      role: 'result',
      visibleRows: { request: true },
    })
    if (request?.kind !== 'tool' || request.call.kind !== 'todo' || result?.kind !== 'tool' || result.call.kind !== 'todo')
      throw new Error('The omp to-do frames drew no to-do rows.')
    expect(request.call.request.items).toEqual([])
    expect(request.call.status).toBe('unstated')
    expect(result.call.request.items.map(item => item.content)).toEqual(['Write code'])
    expect(result.call.status).toBe('completed')
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
