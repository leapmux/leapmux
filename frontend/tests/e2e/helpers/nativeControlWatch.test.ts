import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { ControlResponseState } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { WatchEventsResponseSchema } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { NativeControlFrameCollector } from './nativeControlWatch'

function controlFrame(agentId: string, payload: string, changed = false): Uint8Array {
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
    event: {
      case: 'agentEvent',
      value: {
        agentId,
        event: {
          case: changed ? 'controlResponseChanged' : 'controlRequest',
          value: {
            agentId,
            requestId: 'native-control-1',
            payload: new TextEncoder().encode(payload),
            responseState: changed ? ControlResponseState.COMPLETED : ControlResponseState.READY,
          },
        },
      },
    },
  }))
}

describe('NativeControlFrameCollector', () => {
  it('acknowledges only the requested subscription update', () => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
      event: { case: 'updateAck', value: { updateId: 2n } },
    })))
    expect(collector.subscribed).toBe(false)
    collector.accept(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
      event: { case: 'updateAck', value: { updateId: 1n } },
    })))
    expect(collector.subscribed).toBe(true)
  })

  it('captures a real control request and its later completed response state', () => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(controlFrame('agent-1', '{"type":"native-permission","enabled":false,"count":0,"input":""}'))
    collector.accept(controlFrame('agent-1', '{"type":"native-permission"}', true))
    collector.assertHealthy()
    expect(collector.controls).toEqual([
      { requestId: 'native-control-1', payload: { type: 'native-permission', enabled: false, count: 0, input: '' }, responseState: ControlResponseState.READY },
      { requestId: 'native-control-1', payload: { type: 'native-permission' }, responseState: ControlResponseState.COMPLETED },
    ])
  })

  it('refuses a subscription that the Worker rejects', () => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, {
      event: { case: 'updateAck', value: { updateId: 1n, rejectedAgents: [{ entityId: 'agent-1' }] } },
    })))
    expect(collector.subscribed).toBe(false)
    expect(() => collector.assertHealthy()).toThrow('invalid native control frame')
    expect(collector.error?.cause).toMatchObject({ message: 'The Worker refused the native control subscription.' })
  })

  it('ignores a foreign agent before decoding its malformed control payload', () => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(controlFrame('another-agent', '{invalid'))
    collector.assertHealthy()
    expect(collector.controls).toEqual([])
  })

  it('ignores a response-state update that contains no native payload', () => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(controlFrame('agent-1', ''))
    expect(() => collector.assertHealthy()).toThrow('invalid native control frame')
    const healthy = new NativeControlFrameCollector('agent-1')
    healthy.accept(controlFrame('agent-1', '', true))
    healthy.assertHealthy()
    expect(healthy.controls).toEqual([])
  })

  it.each(['{invalid', 'null', '[]', '"text"', '0'])('refuses malformed or non-object native control content: %s', (payload) => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(controlFrame('agent-1', payload))
    expect(() => collector.assertHealthy()).toThrow('invalid native control frame')
    expect(collector.controls).toEqual([])
  })

  it('refuses a malformed protobuf frame', () => {
    const collector = new NativeControlFrameCollector('agent-1')
    collector.accept(Uint8Array.of(0xFF))
    expect(() => collector.assertHealthy()).toThrow('invalid native control frame')
  })
})
