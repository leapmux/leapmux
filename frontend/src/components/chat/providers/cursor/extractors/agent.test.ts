import type { ACPToolFacts } from '../../acp/extractors/toolCall'
import { describe, expect, it } from 'vitest'
import { typedResult } from '../../../model/toolCall'
import { acpToolFinished } from '../../acp/extractors/toolCall'
import { cursorAgentCall } from './agent'

function facts(fields: Record<string, unknown>): ACPToolFacts {
  const tool = { sessionUpdate: 'tool_call_update', status: 'completed', toolCallId: 'task', rawInput: { _toolName: 'task', description: 'Inspect sample' }, ...fields }
  // `finished` is a FACT the shared build derives, and the adapter reads it rather
  // than asking the frame again: the frame alone cannot see the turn's own outcome.
  return { tool, finished: acpToolFinished(tool) } as unknown as ACPToolFacts
}

function agent(fields: Record<string, unknown>, native?: Record<string, unknown> | null, savedOutput?: string) {
  const model = facts(fields)
  return cursorAgentCall(model, model.tool.rawInput as Record<string, unknown>, native, savedOutput)
}

function source(fields: Record<string, unknown>, extra: Record<string, unknown> = {}, savedOutput?: string) {
  const payload = agent({ rawOutput: undefined, ...extra }, { output: { success: fields } }, savedOutput)
  const run = typedResult(payload)?.agents[0]
  if (run === undefined)
    throw new Error('Expected an agent result with one run')
  return run
}

describe('cursor agent sources', () => {
  it('preserves separate assistant messages and the native suffix', () => {
    expect(source({ conversationSteps: [{ assistantMessage: { text: '**First**' } }, { toolCall: {} }, { assistantMessage: { text: '- Second' } }], resultSuffix: 'Native notice' }).body).toBe('**First**\n\n- Second\n\nNative notice')
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
    const result = agent({ status: 'failed', rawOutput: { error: 'Current error' } }, { output: { success: { conversationSteps: [{ assistantMessage: { text: 'Old report' } }] } } })
    expect(typedResult(result)?.agents[0]?.body).toBe('Current error')
    expect(typedResult(result)?.agents[0]?.outcome).toBe('failed')
  })

  it('resolves a custom agent type and rejects prototype labels', () => {
    const cases: Array<[Record<string, unknown>, string]> = [[{ custom: { name: 'Reviewer' } }, 'Reviewer'], [{ explore: {} }, 'Explore'], [{ toString: {} }, 'toString']]
    for (const [subagentType, expected] of cases) {
      const payload = agent({ sessionUpdate: 'tool_call', status: 'pending', rawInput: { _toolName: 'task', subagentType } })
      expect(payload.request.agentType).toBe(expected)
    }
  })
})

describe('cursor subagent card', () => {
  it('titles the call from the description and keeps the resolved report', () => {
    const payload = agent({}, { output: { success: { resultSuffix: 'Native notice' } } })
    expect(payload.kind).toBe('agent')
    expect(payload.title).toBe('Inspect sample')
    expect(typedResult(payload)?.agents[0]?.body).toBe('Native notice')
  })

  it('falls back to the shared word when the launch describes nothing', () => {
    const payload = agent({ sessionUpdate: 'tool_call', status: 'pending', rawInput: { _toolName: 'task' } })
    expect(payload.title).toBe('Task')
    expect(payload.result).toBeUndefined()
  })
})
