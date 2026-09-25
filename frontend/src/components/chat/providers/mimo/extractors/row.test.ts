import type { ToolSpanContext } from '../../../rowExtractionTypes'
import type { MessageCategory } from '~/components/chat/messageClassifier'
import { describe, expect, it } from 'vitest'
import { MIMO_TOOL, MIMO_TOOL_STATUS } from '~/generated/contracts/mimo-protocol'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { openingFrame, parsedFrame, statusFrame, toolFrame } from '~/test-support/mimoFixtures'
import { resolveMessageForRendering } from '../../registry'
import { mimoExtractRow } from './row'
import '~/components/chat/providers'

const provider = AgentProvider.MIMO_CODE
const noSpan: ToolSpanContext = { request: undefined, result: undefined, role: 'result', visibleRows: { request: false, result: true } }

function extract(parent: Record<string, unknown>, kind: MessageCategory['kind'], span: ToolSpanContext = noSpan, completion?: MessageCompletion) {
  const resolved = resolveMessageForRendering({ rawText: '', topLevel: parent, parentObject: parent, wrapper: null }, provider)
  return mimoExtractRow({ category: { kind } as MessageCategory, resolved, span, ...(completion !== undefined ? { completion } : {}) })
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
    const row = extract(openingFrame(MIMO_TOOL.Bash, input), 'tool_use', { ...noSpan, role: 'request', visibleRows: { request: true, result: false } })
    expect(row).toMatchObject({ kind: 'tool', role: 'request', hasResultRow: false, call: { kind: 'execute', status: 'in_progress' } })
  })

  it('reads the final frame as the result row, with the request row beside it', () => {
    const span: ToolSpanContext = { request: parsedFrame(openingFrame(MIMO_TOOL.Bash, input)), result: undefined, role: 'result', visibleRows: { request: true, result: true } }
    const row = extract(toolFrame(MIMO_TOOL.Bash, { input, output: 'hi\n', metadata: { output: 'hi\n', exit: 0 } }), 'tool_result', span)
    expect(row).toMatchObject({ kind: 'tool', role: 'result', hasRequestRow: true, call: { kind: 'execute', status: 'completed' } })
  })

  // The worker ends a cut call with its last running frame. LeapMux's own completion
  // decides that row, and the extraction input's completion counts as well as the
  // one the frame carries.
  it('reads a running frame as the result row when the input completion ends it', () => {
    const row = extract(openingFrame(MIMO_TOOL.Bash, input), 'tool_result', noSpan, MessageCompletion.INTERRUPTED)
    expect(row).toMatchObject({ kind: 'tool', role: 'result', call: { status: 'cancelled' } })
  })

  // An opening row whose call already answered draws the answer's status, because
  // every MiMo frame states the whole call.
  it('reads the final frame of the same call on the opening row', () => {
    const span: ToolSpanContext = { request: undefined, result: parsedFrame(toolFrame(MIMO_TOOL.Bash, { input, output: 'hi\n' })), role: 'request', visibleRows: { request: true, result: true } }
    const row = extract(openingFrame(MIMO_TOOL.Bash, input), 'tool_use', span)
    expect(row).toMatchObject({ kind: 'tool', role: 'request', hasResultRow: true, call: { status: 'completed' } })
  })

  // One turn can run several calls at once. A frame of another call is no side of
  // this one, and its answer must not draw under this call's header.
  it('ignores a result side of another call', () => {
    const sibling = parsedFrame(toolFrame(MIMO_TOOL.Bash, { status: MIMO_TOOL_STATUS.Error, input: { command: 'false' }, error: 'Command exited with code 1' }, 'call-2'))
    const row = extract(openingFrame(MIMO_TOOL.Bash, input), 'tool_use', { ...noSpan, role: 'request', result: sibling, visibleRows: { request: true, result: true } })
    expect(row).toMatchObject({ kind: 'tool', role: 'request', call: { id: 'call-1', status: 'in_progress' } })
    expect(row?.kind === 'tool' && row.call.kind === 'execute' ? row.call.request.command : null).toBe('echo hi')
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
