import { describe, expect, it } from 'vitest'
import { museNativeResult } from './sourceData'

function sources() {
  const payload = { method: 'item/completed', params: { sessionId: 'session', sourceRange: { stream: { kind: 'session', id: 'session' }, first: { id: 'origin', sequence: 1 } }, item: { itemId: 'item', kind: 'toolCall', turnId: 'turn', callId: 'call', tool: 'bash', args: '{}' } } }
  const origin = { id: 'origin', sequence: 1, stream: { kind: 'session', id: 'session' }, payload: { kind: 'run', run_id: 'turn', event: { kind: 'assistant_tool_calls_committed', message_id: 'batch', tool_calls: [{ call_id: 'call', name: 'bash', args: '{}' }] } } }
  const result = { id: 'result', sequence: 2, stream: { kind: 'session', id: 'session' }, payload: { kind: 'run', run_id: 'turn', event: { kind: 'tool_result_batch_committed', batch_id: 'batch', results: [{ tool_call_id: 'call', tool_call_index: 0, text: '{"exit_code":7}' }] } } }
  return { payload, origin, result, supplement: { nativeRecords: [origin, result] } }
}

describe('museNativeResult', () => {
  it('reads the exact native batch without changing the original or the records', () => {
    const source = sources()
    const before = structuredClone(source)
    expect(museNativeResult(source.payload, source.supplement)).toEqual({ text: '{"exit_code":7}', structured: { exit_code: 7 } })
    expect(source).toEqual(before)
  })

  it.each(['session', 'turn', 'origin', 'origin sequence', 'source stream', 'batch', 'call', 'arguments', 'malformed earlier call', 'duplicate call', 'duplicate batch', 'conflicting record', 'unavailable'])('refuses a mismatched %s', (change) => {
    const source = sources()
    switch (change) {
      case 'session': source.result.stream.id = 'foreign'
        break
      case 'turn': source.result.payload.run_id = 'foreign'
        break
      case 'origin': source.payload.params.sourceRange.first.id = 'missing'
        break
      case 'origin sequence': source.payload.params.sourceRange.first.sequence = 99
        break
      case 'source stream': source.payload.params.sourceRange.stream.id = 'foreign'
        break
      case 'batch': source.result.payload.event.batch_id = 'foreign'
        break
      case 'call': source.result.payload.event.results[0]!.tool_call_id = 'foreign'
        break
      case 'arguments': source.origin.payload.event.tool_calls[0]!.args = '{"foreign":true}'
        break
      case 'malformed earlier call': source.origin.payload.event.tool_calls.unshift(null as never)
        break
      case 'duplicate call': source.origin.payload.event.tool_calls.push(source.origin.payload.event.tool_calls[0]!)
        break
      case 'duplicate batch': source.supplement.nativeRecords.push({ ...source.result, id: 'other', sequence: 3 })
        break
      case 'conflicting record': source.supplement.nativeRecords.push({ ...source.result, sequence: 3 })
        break
      case 'unavailable': Object.assign(source.supplement, { nativeResultUnavailable: { reason: 'Native data is absent' } })
        break
    }
    expect(museNativeResult(source.payload, source.supplement)).toBeUndefined()
  })

  it('accepts an exact replay without selecting a second result', () => {
    const source = sources()
    source.supplement.nativeRecords.push(structuredClone(source.result))
    expect(museNativeResult(source.payload, source.supplement)?.structured).toEqual({ exit_code: 7 })
  })

  it.each([
    { label: 'null entry', other: null },
    { label: 'absent index', other: { tool_call_id: 'other', text: '' } },
    { label: 'negative index', other: { tool_call_index: -1, tool_call_id: 'other', text: '' } },
    { label: 'fractional index', other: { tool_call_index: 0.5, tool_call_id: 'other', text: '' } },
    { label: 'absent call ID', other: { tool_call_index: 1, text: '' } },
    { label: 'empty call ID', other: { tool_call_index: 1, tool_call_id: '', text: '' } },
    { label: 'numeric text', other: { tool_call_index: 1, tool_call_id: 'other', text: 0 } },
    { label: 'null text', other: { tool_call_index: 1, tool_call_id: 'other', text: null } },
  ])('rejects the complete batch for $label', ({ other }) => {
    const source = sources()
    const supplement = { nativeRecords: [source.origin, {
      ...source.result,
      payload: { ...source.result.payload, event: { ...source.result.payload.event, results: [source.result.payload.event.results[0], other] } },
    }] }
    const original = structuredClone(supplement)
    expect(museNativeResult(source.payload, supplement)).toBeUndefined()
    expect(supplement).toEqual(original)
  })

  it.each([undefined, null, {}, { nativeRecords: [null] }])('refuses malformed supplemental data %j', (supplement) => {
    expect(museNativeResult(sources().payload, supplement)).toBeUndefined()
  })

  it.each(['turn', 'call', 'tool', 'arguments', 'batch'])('requires a nonempty exact native %s identity', (field) => {
    const source = sources()
    switch (field) {
      case 'turn':
        Reflect.deleteProperty(source.payload.params.item, 'turnId')
        Reflect.deleteProperty(source.origin.payload, 'run_id')
        Reflect.deleteProperty(source.result.payload, 'run_id')
        break
      case 'call':
        Reflect.deleteProperty(source.payload.params.item, 'callId')
        Reflect.deleteProperty(source.origin.payload.event.tool_calls[0]!, 'call_id')
        Reflect.deleteProperty(source.result.payload.event.results[0]!, 'tool_call_id')
        break
      case 'tool':
        Reflect.deleteProperty(source.payload.params.item, 'tool')
        Reflect.deleteProperty(source.origin.payload.event.tool_calls[0]!, 'name')
        break
      case 'arguments':
        Reflect.deleteProperty(source.payload.params.item, 'args')
        Reflect.deleteProperty(source.origin.payload.event.tool_calls[0]!, 'args')
        break
      case 'batch':
        Reflect.deleteProperty(source.origin.payload.event, 'message_id')
        Reflect.deleteProperty(source.result.payload.event, 'batch_id')
        break
    }
    expect(museNativeResult(source.payload, source.supplement)).toBeUndefined()
  })
})
