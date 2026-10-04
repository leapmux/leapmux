import { Buffer } from 'node:buffer'
import { describe, expect, it } from 'vitest'
import { cursorProtobufNumber, cursorProtobufString, readCursorProtobufFields } from './cursorProtobuf'
import { cursorSubagentReplyFixture, cursorSubagentSuccessFixture } from './cursorSubagentFixtures'
import { cursorSubagentExecutionRequest, cursorSubagentExecutionResponseOf, cursorTaskCompletedFromNativeReply } from './cursorSubagentWire'
import { descend, encodeLengthDelimited, encodeStringField } from './cursorWire'

const call = { callID: 'native-task', description: 'Actual native task', prompt: 'Execute the actual child.', modelID: 'auto', parentConversationID: 'actual-parent' }

describe('cursorSubagentExecutionRequest', () => {
  it('uses the actual native execution, task, parent, model, and prompt fields', () => {
    const frame = cursorSubagentExecutionRequest(7, call)
    const envelope = descend(frame, [2])
    if (!envelope)
      throw new Error('The native subagent execution envelope is absent.')
    const fields = readCursorProtobufFields(envelope)
    expect(cursorProtobufNumber(fields, 1)).toBe(7)
    expect(cursorProtobufString(fields, 15)).toBe(call.callID)
    const args = descend(frame, [2, 28])
    if (!args)
      throw new Error('The native subagent arguments are absent.')
    const values = readCursorProtobufFields(args)
    expect(cursorProtobufString(values, 1)).toBe(call.callID)
    expect(cursorProtobufString(values, 2)).toBe('explore')
    expect(cursorProtobufString(values, 3)).toBe(call.modelID)
    expect(cursorProtobufString(values, 4)).toBe(call.prompt)
    expect(cursorProtobufNumber(values, 5)).toBe(1)
    expect(cursorProtobufString(values, 9)).toBe(call.parentConversationID)
    expect(cursorProtobufString(values, 16)).toBe(call.parentConversationID)
  })

  it('preserves an explicit mutable child and the maximum uint32 ID', () => {
    const args = descend(cursorSubagentExecutionRequest(0xFFFF_FFFF, { ...call, readonly: false }), [2, 28])
    if (!args)
      throw new Error('The native subagent arguments are absent.')
    expect(cursorProtobufNumber(readCursorProtobufFields(args), 5)).toBe(0)
    expect(cursorSubagentExecutionRequest(0, call)).toBeInstanceOf(Uint8Array)
  })

  it.each([-1, 0.5, Number.NaN, Number.POSITIVE_INFINITY, 0x1_0000_0000])('rejects an invalid native execution ID: %s', (id) => {
    expect(() => cursorSubagentExecutionRequest(id, call)).toThrow('must fit uint32')
  })

  it.each(['callID', 'modelID', 'parentConversationID', 'prompt'] as const)('rejects an empty required native field: %s', (field) => {
    expect(() => cursorSubagentExecutionRequest(1, { ...call, [field]: '' })).toThrow('requires its tool, model, parent, and prompt')
  })
})

describe('cursorSubagentExecutionResponseOf', () => {
  it('reads the actual child ID, report, and zero tool count', () => {
    const outcome = cursorSubagentSuccessFixture({ agentID: 'actual-child', finalMessage: 'ACTUAL_CHILD_REPORT', toolCallCount: 0 })
    expect(cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(3, outcome))).toMatchObject({ id: 3, execID: 'native-task', success: true, agentID: 'actual-child', finalMessage: 'ACTUAL_CHILD_REPORT', toolCallCount: 0, rawResult: outcome })
  })

  it('keeps absent and explicit empty reports distinct', () => {
    const absent = cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(0, cursorSubagentSuccessFixture({ agentID: 'actual-child' })))
    const empty = cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(0, cursorSubagentSuccessFixture({ agentID: 'actual-child', finalMessage: '' })))
    expect(absent).not.toHaveProperty('finalMessage')
    expect(empty).toHaveProperty('finalMessage', '')
  })

  it('retains the native error text and optional child ID', () => {
    const outcome = encodeLengthDelimited(2, new Uint8Array(Buffer.concat([encodeStringField(1, 'actual-child'), encodeStringField(2, 'Actual native child failure.')])))
    expect(cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(5, outcome))).toMatchObject({ success: false, agentID: 'actual-child', error: 'Actual native child failure.' })
    const empty = cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(5, encodeLengthDelimited(2, encodeStringField(2, ''))))
    expect(empty).toMatchObject({ success: false, error: '' })
    expect(empty).not.toHaveProperty('agentID')
  })

  it('rejects a native success without an actual child ID', () => {
    expect(() => cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(1, cursorSubagentSuccessFixture()))).toThrow('has no child ID')
    expect(() => cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(1, cursorSubagentSuccessFixture({ agentID: '' })))).toThrow('has no child ID')
  })

  it('rejects a missing outcome and two native outcome choices', () => {
    expect(() => cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(1, new Uint8Array()))).toThrow('has no result')
    const duplicate = new Uint8Array(Buffer.concat([cursorSubagentSuccessFixture({ agentID: 'actual-child' }), encodeLengthDelimited(2, encodeStringField(2, 'Failure.'))]))
    expect(() => cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(1, duplicate))).toThrow('repeats its result choice')
  })

  it('rejects truncated native bytes before they supply an outcome', () => {
    const complete = cursorSubagentReplyFixture(1, cursorSubagentSuccessFixture({ agentID: 'actual-child' }))
    for (const cut of [1, 3, complete.length - 1]) expect(() => cursorSubagentExecutionResponseOf(complete.subarray(0, cut))).toThrow(/truncated/)
  })

  it('ignores an actual non-execution frame', () => {
    expect(cursorSubagentExecutionResponseOf(encodeLengthDelimited(1, encodeStringField(1, 'heartbeat')))).toBeUndefined()
  })
})

describe('cursorTaskCompletedFromNativeReply', () => {
  it('uses the actual child identity and report instead of a derived fake identity', () => {
    const reply = cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(1, cursorSubagentSuccessFixture({ agentID: 'actual-native-child', finalMessage: 'ACTUAL_NATIVE_REPORT' })))
    if (!reply)
      throw new Error('The native child fixture returned no reply.')
    const frame = cursorTaskCompletedFromNativeReply(call, reply)
    const success = descend(frame, [1, 3, 2, 19, 2, 1])
    if (!success)
      throw new Error('The native Task success is absent.')
    expect(cursorProtobufString(readCursorProtobufFields(success), 2)).toBe('actual-native-child')
    expect(new TextDecoder().decode(descend(success, [1, 1, 1]))).toBe('ACTUAL_NATIVE_REPORT')
    expect(Buffer.from(frame).includes(Buffer.from('native-task-child'))).toBe(false)
  })

  it('keeps an actual native error as a failed Task result', () => {
    const reply = cursorSubagentExecutionResponseOf(cursorSubagentReplyFixture(1, encodeLengthDelimited(2, encodeStringField(2, 'Actual child failure.'))))
    if (!reply)
      throw new Error('The native child fixture returned no reply.')
    const frame = cursorTaskCompletedFromNativeReply(call, reply)
    expect(descend(frame, [1, 3, 2, 19, 2, 1])).toBeUndefined()
    expect(new TextDecoder().decode(descend(frame, [1, 3, 2, 19, 2, 2, 1]))).toBe('Actual child failure.')
  })
})
