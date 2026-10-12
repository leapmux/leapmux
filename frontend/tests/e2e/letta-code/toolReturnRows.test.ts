import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentStatusChangeSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { AgentEventSchema } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { liveToolReturnRow, toolReturnRow } from './toolReturnRows'

const TOOL_RETURN = JSON.stringify({ message_type: 'tool_return_message', tool_call_id: 'view-image', tool_return: 'Viewed the image.' })
const ASSISTANT = JSON.stringify({ message_type: 'assistant_message', content: 'The image is blue.' })

/** A stored row whose content is `text`, with no compression. */
function row(text: string) {
  return create(AgentChatMessageSchema, { content: new TextEncoder().encode(text), contentCompression: ContentCompression.NONE })
}

/** A watched event that carries `text` as a message row. */
function messageEvent(text: string, replay: boolean) {
  return create(AgentEventSchema, { agentId: 'letta-agent', replay, event: { case: 'agentMessage', value: row(text) } })
}

describe('toolReturnRow', () => {
  it('returns the raw content of a tool result row', () => {
    expect(toolReturnRow(row(TOOL_RETURN))).toBe(TOOL_RETURN)
  })

  it('returns undefined for a row of another message type', () => {
    expect(toolReturnRow(row(ASSISTANT))).toBeUndefined()
  })

  it('returns undefined for a row whose content does not decode', () => {
    const undecodable = create(AgentChatMessageSchema, { content: new TextEncoder().encode(TOOL_RETURN), contentCompression: ContentCompression.UNSPECIFIED })
    expect(toolReturnRow(undecodable)).toBeUndefined()
  })
})

describe('liveToolReturnRow', () => {
  it('returns the raw content of a live tool result row', () => {
    expect(liveToolReturnRow(messageEvent(TOOL_RETURN, false))).toBe(TOOL_RETURN)
  })

  it('skips a replayed tool result row', () => {
    expect(liveToolReturnRow(messageEvent(TOOL_RETURN, true))).toBeUndefined()
  })

  it('skips a live row of another message type', () => {
    expect(liveToolReturnRow(messageEvent(ASSISTANT, false))).toBeUndefined()
  })

  it('skips an event that carries no message row', () => {
    const statusChange = create(AgentEventSchema, { agentId: 'letta-agent', event: { case: 'statusChange', value: create(AgentStatusChangeSchema, {}) } })
    expect(liveToolReturnRow(statusChange)).toBeUndefined()
  })
})
