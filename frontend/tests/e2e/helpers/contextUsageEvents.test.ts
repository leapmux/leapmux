import { create, toBinary } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { SESSION_INFO_KEY } from '../../../src/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema, WatchEventsResponseSchema, WatchUpdateAckSchema } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { ContextUsageFrameCollector } from './contextUsageEvents'

function infoFrame(agentId: string, info: Record<string, unknown>, replay = false): Uint8Array {
  const message = create(AgentChatMessageSchema, {
    seq: -1n,
    content: new TextEncoder().encode(JSON.stringify({ type: NOTIFICATION_TYPE.AgentSessionInfo, info })),
    contentCompression: ContentCompression.NONE,
  })
  const event = create(AgentEventSchema, { agentId, replay, event: { case: 'agentMessage', value: message } })
  return toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: event } }))
}

describe('ContextUsageFrameCollector', () => {
  it('ignores missing, replayed and foreign usage updates', () => {
    const collector = new ContextUsageFrameCollector('agent-1')
    collector.accept(infoFrame('agent-1', {}))
    collector.accept(infoFrame('agent-1', { [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 25 } }, true))
    collector.accept(infoFrame('agent-2', { [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 25 } }))
    expect(collector.readings).toEqual([])
  })

  it('keeps each native update in arrival order, including equal values', () => {
    const collector = new ContextUsageFrameCollector('agent-1')
    collector.accept(infoFrame('agent-1', { [SESSION_INFO_KEY.ContextUsage]: {} }))
    const first = infoFrame('agent-1', { [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 25 } })
    const second = infoFrame('agent-1', { [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 50 } })
    collector.accept(first)
    collector.accept(first)
    collector.accept(second)
    expect(collector.readings).toEqual([{}, { usage_percent: 25 }, { usage_percent: 25 }, { usage_percent: 50 }])
  })

  it('requires an accepted subscription and reports malformed frames', () => {
    const collector = new ContextUsageFrameCollector('agent-1')
    const ack = create(WatchUpdateAckSchema, { updateId: 1n })
    collector.accept(toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'updateAck', value: ack } })))
    expect(collector.subscribed).toBe(true)
    collector.accept(new Uint8Array([0xFF]))
    expect(() => collector.assertHealthy()).toThrow('invalid usage watch frame')
  })

  it('reports invalid compressed content without throwing from the stream callback', () => {
    const collector = new ContextUsageFrameCollector('agent-1')
    const message = create(AgentChatMessageSchema, {
      seq: -1n,
      content: new Uint8Array([0xFF]),
      contentCompression: ContentCompression.ZSTD,
    })
    const event = create(AgentEventSchema, { agentId: 'agent-1', event: { case: 'agentMessage', value: message } })
    const frame = toBinary(WatchEventsResponseSchema, create(WatchEventsResponseSchema, { event: { case: 'agentEvent', value: event } }))
    expect(() => collector.accept(frame)).not.toThrow()
    expect(() => collector.assertHealthy()).toThrow('invalid compressed usage content')
  })
})
