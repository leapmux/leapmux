import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseChildTaskId } from './childIdentity'

const encoder = new TextEncoder()
const prompt = 'ACTUAL_CHILD_TASK\n\nLEAPMUXE2ESCENARIO:current-task'
function event(id = 'spawn', instructions = prompt) {
  return { sessionUpdate: 'tool_call', toolCallId: id, title: 'delegate', rawInput: { instructions }, _meta: { goose: { toolCall: { toolName: 'delegate', extensionName: 'summon' } } } }
}
function snapshot(...bodies: unknown[]): NativeMessageSnapshot {
  return { agentId: 'parent', agentSessionId: 'native-session', messages: bodies.map((body, index) => create(AgentChatMessageSchema, {
    id: `frame-${index}`,
    agentSessionId: 'native-session',
    contentCompression: ContentCompression.NONE,
    content: encoder.encode(JSON.stringify(body)),
  })) }
}
function onlyMessage(source: NativeMessageSnapshot) {
  expect(source.messages).toHaveLength(1)
  const message = source.messages[0]
  if (!message)
    throw new Error('The native Goose test fixture contains no message.')
  return message
}

describe('gooseChildTaskId', () => {
  it('uses the actual delegate call and prompt independently of its display title', () => {
    expect(gooseChildTaskId(snapshot(event('other'), { ...event(), title: 'Another native label' }), 'spawn', prompt)).toBe('spawn')
  })

  it.each(['', ' '])('refuses an invalid native session before decoding a matching delegate frame: "%s"', (session) => {
    const source = snapshot(event())
    source.agentSessionId = session
    const message = onlyMessage(source)
    message.agentSessionId = session
    expect(() => gooseChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
    message.content = encoder.encode('{broken')
    expect(() => gooseChildTaskId(source, 'spawn', prompt)).toThrow('nonempty session ID')
  })

  it('retains repeated native receipts with the same identity and prompt', () => {
    expect(gooseChildTaskId(snapshot(event(), event()), 'spawn', prompt)).toBe('spawn')
  })

  it('refuses absent calls, stale sessions, and another exact task prompt', () => {
    expect(() => gooseChildTaskId(snapshot(), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => gooseChildTaskId(snapshot(event('other')), 'spawn', prompt)).toThrow('no native parent receipt')
    expect(() => gooseChildTaskId(snapshot(event('spawn', 'WRONG_CHILD_PROMPT')), 'spawn', prompt)).toThrow('another child prompt')
    const stale = snapshot(event())
    onlyMessage(stale).agentSessionId = 'old-session'
    expect(() => gooseChildTaskId(stale, 'spawn', prompt)).toThrow('no native parent receipt')
  })

  it.each([null, {}, { goose: [] }, { goose: { toolCall: null } }, { goose: { toolCall: { toolName: 'shell', extensionName: 'developer' } } }])('refuses missing or unrelated native delegate metadata: %j', (_meta) => {
    expect(() => gooseChildTaskId(snapshot({ ...event(), _meta }), 'spawn', prompt)).toThrow('not a native summon delegate')
  })

  it('refuses conflicting receipts and invalid native bytes', () => {
    expect(() => gooseChildTaskId(snapshot(event(), event('spawn', 'OTHER_PROMPT')), 'spawn', prompt)).toThrow('another child prompt')
    const source = snapshot(event())
    onlyMessage(source).content = encoder.encode('{broken')
    expect(() => gooseChildTaskId(source, 'spawn', prompt)).toThrow('invalid JSON')
  })

  it('requires nonempty exact call and task identities', () => {
    expect(() => gooseChildTaskId(snapshot(event()), '', prompt)).toThrow('call ID and prompt')
    expect(() => gooseChildTaskId(snapshot(event()), 'spawn', '   ')).toThrow('call ID and prompt')
  })
})
