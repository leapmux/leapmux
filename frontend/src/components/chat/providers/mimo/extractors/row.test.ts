import type { ToolSpanContext } from '../../../rowExtractionTypes'
import type { MessageCategory } from '~/components/chat/messageClassifier'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL } from '~/generated/contracts/mimo-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openingFrame, parsedFrame, statusFrame, toolFrame } from '~/test-support/mimoFixtures'
import { resolveMessageForRendering } from '../../registry'
import { mimoExtractRow } from './row'
import '~/components/chat/providers'

const provider = AgentProvider.MIMO_CODE
const noSpan: ToolSpanContext = {
  request: undefined,
  role: 'result',
  visibleRows: { request: false },
}

function extract(parent: Record<string, unknown>, kind: MessageCategory['kind'], span: ToolSpanContext = noSpan, completion?: MessageCompletion) {
  const resolved = resolveMessageForRendering({
    rawText: '',
    topLevel: parent,
    parentObject: parent,
    wrapper: null,
  }, provider)
  return mimoExtractRow({
    category: { kind } as MessageCategory,
    resolved,
    span,
    ...(completion !== undefined
      ? { completion }
      : {}),
  })
}

const input = { command: 'echo hi' }

describe('mimoExtractRow', () => {
  it('reads a LeapMux user row with its words', () => {
    expect(extract({ content: 'hello' }, 'user_content')).toEqual({ kind: 'user', text: 'hello', attachments: [] })
  })

  it('reads the plan the reader sent into execution', () => {
    expect(extract({ content: '# Plan', planExecution: true }, 'plan_execution')).toEqual({ kind: 'plan-execution', text: '# Plan' })
  })

  it('reads the opening frame as the request row of a running call', () => {
    const row = extract(openingFrame(MIMO_TOOL.Bash, input), 'tool_use', {
      ...noSpan,
      role: 'request',
      visibleRows: { request: true },
    })
    expect(row).toMatchObject({
      kind: 'tool',
      role: 'request',
      call: { kind: 'execute', status: 'in_progress' },
    })
    expect(row?.kind === 'tool' && row.call.result).toBeUndefined()
  })

  it('reads the final frame as the result row, with the request row beside it', () => {
    const span: ToolSpanContext = {
      request: parsedFrame(openingFrame(MIMO_TOOL.Bash, input)),
      role: 'result',
      visibleRows: { request: true },
    }
    const row = extract(toolFrame(MIMO_TOOL.Bash, {
      input,
      output: 'hi\n',
      metadata: { output: 'hi\n', exit: 0 },
    }), 'tool_result', span)
    expect(row).toMatchObject({
      kind: 'tool',
      role: 'result',
      hasRequestRow: true,
      call: { kind: 'execute', status: 'completed' },
    })
  })

  // The worker ends a cut call with its last running frame. LeapMux's own completion
  // decides that row, and the extraction input's completion counts as well as the
  // one the frame carries.
  it('reads a running frame as the result row when the input completion ends it', () => {
    const row = extract(openingFrame(MIMO_TOOL.Bash, input), 'tool_result', noSpan, MessageCompletion.INTERRUPTED)
    expect(row).toMatchObject({
      kind: 'tool',
      role: 'result',
      call: { status: 'cancelled' },
    })
  })

  // Only the answer says whether a read hit a directory. The opening row reads its own
  // frame alone, so it draws the read it asked for, and the final row draws the list.
  it('states the kind that only the answer decides on the final row alone', () => {
    const readInput = { file_path: '/p' }
    const opening = parsedFrame(openingFrame(MIMO_TOOL.Read, readInput))
    const request = extract(openingFrame(MIMO_TOOL.Read, readInput), 'tool_use', {
      request: opening,
      role: 'request',
      visibleRows: { request: true },
    })
    expect(request).toMatchObject({
      kind: 'tool',
      role: 'request',
      call: { kind: 'read', status: 'in_progress' },
    })
    expect(request?.kind === 'tool' && request.call.result).toBeUndefined()
    const result = extract(toolFrame(MIMO_TOOL.Read, {
      input: readInput,
      output: '<path>/p</path>\n<type>directory</type>\n<entries>\na.ts\n\n(1 entries)\n</entries>',
    }), 'tool_result', {
      request: opening,
      role: 'result',
      visibleRows: { request: true },
    })
    expect(result).toMatchObject({
      kind: 'tool',
      role: 'result',
      hasRequestRow: true,
      call: { kind: 'list', status: 'completed' },
    })
  })

  it('answers null for a tool category on a frame that is not a tool part', () => {
    expect(extract(statusFrame('idle'), 'tool_use')).toBeNull()
    expect(extract({ content: 'hi' }, 'tool_result')).toBeNull()
  })

  it.each([
    'result_divider',
    'notification',
    'hidden',
    'unknown',
  ] as const)('answers null for the %s category', (kind) => {
    expect(extract(statusFrame('idle'), kind)).toBeNull()
  })
})
