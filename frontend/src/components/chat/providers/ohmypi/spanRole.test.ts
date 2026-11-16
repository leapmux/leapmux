import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { ohMyPiNeedsRequest, ohMyPiSpanRole } from './spanRole'

function frame(type: string, completion?: MessageCompletion) {
  return {
    ...input({ type, toolCallId: 'c', toolName: 'bash' }, undefined, AgentProvider.OH_MY_PI),
    ...(completion !== undefined
      ? { completion }
      : {}),
  }
}

describe('ohMyPiSpanRole', () => {
  it('reads the side of a span from the frame type', () => {
    expect(ohMyPiSpanRole(frame('tool_execution_start'))).toBe('request')
    expect(ohMyPiSpanRole(frame('tool_execution_end'))).toBe('result')
    expect(ohMyPiSpanRole(frame('message_end'))).toBe('other')
  })

  it('reads a retained start frame as the result', () => {
    expect(ohMyPiSpanRole(frame('tool_execution_start', MessageCompletion.INTERRUPTED))).toBe('result')
  })

  it('reads a start frame as the result for each completion LeapMux records, and as the request for none', () => {
    for (const completion of [MessageCompletion.COMPLETE, MessageCompletion.INTERRUPTED, MessageCompletion.ERROR])
      expect(ohMyPiSpanRole(frame('tool_execution_start', completion)), MessageCompletion[completion]).toBe('result')
    expect(ohMyPiSpanRole(frame('tool_execution_start', MessageCompletion.UNSPECIFIED))).toBe('request')
  })

  it('reads an update frame and a row with no type as no side of a span', () => {
    // The worker folds each update into the call's own rows, so an update row that an
    // earlier build stored is no side of the span.
    expect(ohMyPiSpanRole(frame('tool_execution_update'))).toBe('other')
    expect(ohMyPiSpanRole(input({ content: 'hello' }, undefined, AgentProvider.OH_MY_PI))).toBe('other')
    expect(ohMyPiSpanRole(input(undefined, undefined, AgentProvider.OH_MY_PI))).toBe('other')
  })
})

// An end frame states no arguments and no intent, so its row reads them from the start
// frame. A start frame never waits for its end.
describe('ohMyPiNeedsRequest', () => {
  it('answers false for a start frame', () => {
    expect(ohMyPiNeedsRequest(frame('tool_execution_start'))).toBe(false)
  })

  it('answers true for an end frame', () => {
    expect(ohMyPiNeedsRequest(frame('tool_execution_end'))).toBe(true)
  })

  // A stopped turn stores the start frame again as the closing row, with its own arguments.
  it('answers false for a retained start frame', () => {
    expect(ohMyPiNeedsRequest(frame('tool_execution_start', MessageCompletion.INTERRUPTED))).toBe(false)
  })

  it('answers false for a frame outside a tool span', () => {
    expect(ohMyPiNeedsRequest(frame('agent_end'))).toBe(false)
  })
})
