import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { nativeControlFrame } from './nativeControlWatch'

function controlEvent(payload: string, options: { changed?: boolean, requestId?: string } = {}) {
  return create(AgentEventSchema, {
    agentId: 'agent-1',
    event: {
      case: options.changed ? 'controlResponseChanged' : 'controlRequest',
      value: {
        agentId: 'agent-1',
        requestId: options.requestId ?? 'native-control-1',
        payload: new TextEncoder().encode(payload),
        responseState: options.changed ? ControlResponseState.COMPLETED : ControlResponseState.READY,
      },
    },
  })
}

/** The error that the selector throws for one event, so a test can read its cause. */
function selectorFailure(event: ReturnType<typeof controlEvent>): Error {
  try {
    nativeControlFrame(event)
  }
  catch (error) {
    return error as Error
  }
  throw new Error('The selector accepted the event.')
}

describe('nativeControlFrame', () => {
  it('captures a real control request and its later completed response state', () => {
    expect(nativeControlFrame(controlEvent('{"type":"native-permission","enabled":false,"count":0,"input":""}'))).toEqual({
      requestId: 'native-control-1',
      payload: { type: 'native-permission', enabled: false, count: 0, input: '' },
      responseState: ControlResponseState.READY,
    })
    expect(nativeControlFrame(controlEvent('{"type":"native-permission"}', { changed: true }))).toEqual({
      requestId: 'native-control-1',
      payload: { type: 'native-permission' },
      responseState: ControlResponseState.COMPLETED,
    })
  })

  it('skips a response-state update that contains no native payload, but refuses an empty request payload', () => {
    expect(nativeControlFrame(controlEvent('', { changed: true }))).toBeUndefined()
    expect(selectorFailure(controlEvent('')).message).toBe('The Worker sent an invalid native control frame.')
  })

  it('skips an event that is not a control', () => {
    expect(nativeControlFrame(create(AgentEventSchema, { agentId: 'agent-1', event: { case: 'inputQueueChanged', value: {} } }))).toBeUndefined()
  })

  it('refuses a control with no request ID', () => {
    expect(selectorFailure(controlEvent('{}', { requestId: '' })).cause).toMatchObject({ message: 'The native control frame has no request ID.' })
  })

  it.each(['{invalid', 'null', '[]', '"text"', '0'])('refuses malformed or non-object native control content: %s', (payload) => {
    const failure = selectorFailure(controlEvent(payload))
    expect(failure.message).toBe('The Worker sent an invalid native control frame.')
    expect(failure.cause).toBeInstanceOf(Error)
  })
})
