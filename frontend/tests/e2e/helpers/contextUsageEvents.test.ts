import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { SESSION_INFO_KEY } from '../../../src/generated/contracts/session-info'
import { NOTIFICATION_TYPE } from '../../../src/generated/contracts/worker-vocab'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { contextUsageReading } from './contextUsageEvents'

function messageEvent(content: Uint8Array, options: { replay?: boolean, seq?: bigint, compression?: ContentCompression } = {}) {
  const message = create(AgentChatMessageSchema, {
    seq: options.seq ?? -1n,
    content,
    contentCompression: options.compression ?? ContentCompression.NONE,
  })
  return create(AgentEventSchema, { agentId: 'agent-1', replay: options.replay ?? false, event: { case: 'agentMessage', value: message } })
}

function infoEvent(info: Record<string, unknown>, options: { replay?: boolean, seq?: bigint } = {}) {
  return messageEvent(new TextEncoder().encode(JSON.stringify({ type: NOTIFICATION_TYPE.AgentSessionInfo, info })), options)
}

describe('contextUsageReading', () => {
  it('reads the usage map of a live session-info update, including an empty one', () => {
    expect(contextUsageReading(infoEvent({ [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 25 } }))).toEqual({ usage_percent: 25 })
    expect(contextUsageReading(infoEvent({ [SESSION_INFO_KEY.ContextUsage]: {} }))).toEqual({})
  })

  it('skips an update without usage, a replayed update, and a stored message', () => {
    expect(contextUsageReading(infoEvent({}))).toBeUndefined()
    expect(contextUsageReading(infoEvent({ [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 25 } }, { replay: true }))).toBeUndefined()
    expect(contextUsageReading(infoEvent({ [SESSION_INFO_KEY.ContextUsage]: { usage_percent: 25 } }, { seq: 7n }))).toBeUndefined()
  })

  it('skips empty content, content that is not JSON, and a notification of another type', () => {
    expect(contextUsageReading(messageEvent(new Uint8Array()))).toBeUndefined()
    expect(contextUsageReading(messageEvent(new TextEncoder().encode('{not json')))).toBeUndefined()
    expect(contextUsageReading(messageEvent(new TextEncoder().encode(JSON.stringify({ type: 'other', info: { [SESSION_INFO_KEY.ContextUsage]: {} } }))))).toBeUndefined()
  })

  it('skips an event that is not a chat message', () => {
    expect(contextUsageReading(create(AgentEventSchema, { agentId: 'agent-1', event: { case: 'inputQueueChanged', value: {} } }))).toBeUndefined()
  })

  it('fails on invalid compressed content, and keeps the decoder error as the cause', () => {
    let failure: unknown
    try {
      contextUsageReading(messageEvent(new Uint8Array([0xFF]), { compression: ContentCompression.ZSTD }))
    }
    catch (error) {
      failure = error
    }
    expect(failure).toMatchObject({ message: 'The Worker sent invalid compressed usage content.' })
    expect((failure as Error).cause).toBeDefined()
  })
})
