import { describe, expect, it } from 'vitest'
import { AgentProvider, MessageCompletion } from '~/generated/proto/leapmux/v1/agent_pb'
import { input } from '../testUtils'
import { ohMyPiRelatedMessages, ohMyPiSpanRole } from './spanRole'

const frame = (type: string, completion?: MessageCompletion) => ({ ...input({ type, toolCallId: 'c', toolName: 'bash' }, undefined, AgentProvider.OH_MY_PI), ...(completion !== undefined ? { completion } : {}) })

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

describe('ohMyPiRelatedMessages', () => {
  it('asks for the other side of the span', () => {
    expect(ohMyPiRelatedMessages(frame('tool_execution_start'))).toEqual(['result'])
    expect(ohMyPiRelatedMessages(frame('tool_execution_end'))).toEqual(['request'])
    expect(ohMyPiRelatedMessages(frame('agent_end'))).toEqual([])
  })

  it('asks a retained start frame for its request, because it is the result', () => {
    expect(ohMyPiRelatedMessages(frame('tool_execution_start', MessageCompletion.INTERRUPTED))).toEqual(['request'])
  })
})
