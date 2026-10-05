import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { OPENCODE_PERMISSION_REJECTION, openCodeToolEnding } from './permissionDenial'

const encoder = new TextEncoder()

/** The failed frame that OpenCode sends for a call whose permission the reader rejected. */
const rejectedFrame = {
  sessionUpdate: 'tool_call_update',
  toolCallId: 'denied-1',
  status: 'failed',
  kind: 'execute',
  content: [{ type: 'content', content: { type: 'text', text: OPENCODE_PERMISSION_REJECTION } }],
  rawOutput: { error: OPENCODE_PERMISSION_REJECTION },
}

function message(id: string, frame: unknown, overrides: { agentSessionId?: string, spanId?: string } = {}) {
  return create(AgentChatMessageSchema, {
    id,
    agentSessionId: overrides.agentSessionId ?? 'native-session',
    spanId: overrides.spanId ?? 'denied-1',
    contentCompression: ContentCompression.NONE,
    content: encoder.encode(JSON.stringify(frame)),
  })
}

function snapshot(...messages: ReturnType<typeof message>[]): NativeMessageSnapshot {
  return { agentId: 'parent', agentSessionId: 'native-session', messages }
}

describe('openCodeToolEnding', () => {
  it('reads the exact native refusal of a rejected permission', () => {
    expect(openCodeToolEnding(snapshot(message('result', rejectedFrame)), 'denied-1')).toEqual({
      status: 'failed',
      text: [OPENCODE_PERMISSION_REJECTION],
      error: OPENCODE_PERMISSION_REJECTION,
    })
  })

  it('skips the opening call and the progress updates of the same call', () => {
    const opening = { sessionUpdate: 'tool_call', toolCallId: 'denied-1', status: 'pending' }
    const progress = { sessionUpdate: 'tool_call_update', toolCallId: 'denied-1', status: 'in_progress' }
    expect(openCodeToolEnding(snapshot(message('open', opening), message('progress', progress), message('result', rejectedFrame)), 'denied-1').status).toBe('failed')
  })

  it('ignores a final frame of the same call ID in another native session', () => {
    const foreign = message('foreign', { ...rejectedFrame, status: 'completed', rawOutput: { output: 'FOREIGN' } }, { agentSessionId: 'other-session' })
    expect(openCodeToolEnding(snapshot(foreign, message('result', rejectedFrame)), 'denied-1').status).toBe('failed')
  })

  it('omits the error of a frame whose raw output has none', () => {
    const completed = { ...rejectedFrame, status: 'completed', content: [], rawOutput: { output: '' } }
    expect(openCodeToolEnding(snapshot(message('result', completed)), 'denied-1')).toEqual({ status: 'completed', text: [] })
  })

  it('refuses an absent final frame', () => {
    expect(() => openCodeToolEnding(snapshot(), 'denied-1')).toThrow('has 0 final frames')
  })

  it('refuses two final frames for one call', () => {
    expect(() => openCodeToolEnding(snapshot(message('first', rejectedFrame), message('second', rejectedFrame)), 'denied-1')).toThrow('has 2 final frames')
  })

  it('refuses a frame in the call span that identifies another call', () => {
    expect(() => openCodeToolEnding(snapshot(message('result', { ...rejectedFrame, toolCallId: 'other-call' })), 'denied-1')).toThrow('identifies another call')
  })

  it('refuses a frame that holds no object', () => {
    expect(() => openCodeToolEnding(snapshot(message('result', ['not', 'an', 'object'])), 'denied-1')).toThrow('must contain an object')
  })

  it('requires an exact call and a nonempty agent and session identity', () => {
    const source = snapshot(message('result', rejectedFrame))
    expect(() => openCodeToolEnding(source, '')).toThrow('exact agent, session, and call ID')
    expect(() => openCodeToolEnding({ ...source, agentId: ' ' }, 'denied-1')).toThrow('exact agent, session, and call ID')
    expect(() => openCodeToolEnding({ ...source, agentSessionId: '' }, 'denied-1')).toThrow('exact agent, session, and call ID')
  })
})
