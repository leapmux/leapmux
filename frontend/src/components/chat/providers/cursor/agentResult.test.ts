import { describe, expect, it } from 'vitest'
import { acpToolPresentation } from '../acp/toolPresentation'
import { cursorAgentPresentation } from './agentResult'

function source(success: Record<string, unknown>, fields: Record<string, unknown> = {}, savedOutput?: string) {
  const tool = { sessionUpdate: 'tool_call_update', status: 'completed', toolCallId: 'task', rawInput: { _toolName: 'task', description: 'Inspect sample' }, ...fields }
  const body = cursorAgentPresentation(tool, acpToolPresentation(tool), { output: { success } }, savedOutput).body
  if (body.type !== 'agent')
    throw new Error('Expected an agent result')
  return body.source
}

describe('cursor agent sources', () => {
  it('preserves separate assistant messages and the native suffix', () => {
    const body = source({ conversationSteps: [{ assistantMessage: { text: '**First**' } }, { toolCall: {} }, { assistantMessage: { text: '- Second' } }], resultSuffix: 'Native notice' }).body
    expect(body).toBe('**First**\n\n- Second\n\nNative notice')
  })

  it('keeps the original result when the native steps have no readable report', () => {
    expect(source({ conversationSteps: [null, 4, { assistantMessage: { text: 7 } }] }, {}, 'Unrecognized native report').body).toBe('Unrecognized native report')
    expect(source({ resultSuffix: 'Native notice' }).body).toBe('Native notice')
  })

  it('keeps cancellation and failure distinct from a background launch', () => {
    expect(source({ isBackground: true }).outcome).toBe('running')
    expect(source({ isBackground: true }, { status: 'cancelled' }).outcome).toBe('stopped')
    expect(source({ isBackground: true }, { status: 'failed' }).outcome).toBe('failed')
  })

  it('formats zero and large protobuf durations without losing digits', () => {
    expect(source({ durationMs: '0' }).metadata).toContainEqual({ label: 'Duration', value: '0ms' })
    expect(source({ durationMs: '18446744073709551615' }).metadata).toContainEqual({ label: 'Duration', value: '18446744073709551615ms' })
    for (const durationMs of [-1, 0.5, '', null, 'invalid', Number.NaN, Number.POSITIVE_INFINITY])
      expect(source({ durationMs }).metadata).toEqual([])
  })

  it('uses the original error instead of a success-shaped stored body', () => {
    const result = source({ conversationSteps: [{ assistantMessage: { text: 'Old report' } }] }, { status: 'failed', rawOutput: { error: 'Current error' } })
    expect(result.body).toBe('Current error')
    expect(result.outcome).toBe('failed')
  })

  it('resolves a custom agent type and rejects prototype labels', () => {
    const cases: Array<[Record<string, unknown>, string]> = [[{ custom: { name: 'Reviewer' } }, 'Reviewer'], [{ explore: {} }, 'Explore'], [{ toString: {} }, 'toString']]
    for (const [subagentType, expected] of cases) {
      const tool = { sessionUpdate: 'tool_call', toolCallId: 'task', status: 'pending', rawInput: { _toolName: 'task', subagentType } }
      expect(cursorAgentPresentation(tool, acpToolPresentation(tool)).agentRequest?.agentType).toBe(expected)
    }
  })
})
