import type { AgentChatMessage } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, AgentProvider, ContentCompression, MessageSource } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codexCommandStarted } from './commandStart'

const sessionId = 'native-thread'
const encoder = new TextEncoder()

function storedMessage(spanType: string, body: unknown, overrides: { agentSessionId?: string } = {}): AgentChatMessage {
  return create(AgentChatMessageSchema, {
    id: 'message-1',
    seq: 1n,
    source: MessageSource.AGENT,
    agentProvider: AgentProvider.CODEX,
    spanId: 'exec-1',
    spanType,
    agentSessionId: overrides.agentSessionId ?? sessionId,
    contentCompression: ContentCompression.NONE,
    content: encoder.encode(JSON.stringify(body)),
  })
}

const item = (status: string, type = 'commandExecution') => ({ threadId: sessionId, turnId: 'turn-1', item: { type, id: 'exec-1', status } })

describe('codexCommandStarted', () => {
  it('reads the start of a native command item', () => {
    expect(codexCommandStarted([storedMessage('commandExecution', item('inProgress'))], sessionId)).toBe(true)
  })

  it('stays true after the item completed, because its start stays stored', () => {
    const messages = [storedMessage('commandExecution', item('inProgress')), storedMessage('commandExecution', item('completed'))]
    expect(codexCommandStarted(messages, sessionId)).toBe(true)
  })

  it.each([
    { label: 'no message', messages: [] },
    { label: 'only a completion', messages: [storedMessage('commandExecution', item('completed'))] },
    { label: 'a file change item', messages: [storedMessage('fileChange', item('inProgress', 'fileChange'))] },
    { label: 'a span of another type that holds a command body', messages: [storedMessage('exec', item('inProgress'))] },
    { label: 'a command item of another session', messages: [storedMessage('commandExecution', item('inProgress'), { agentSessionId: 'other-thread' })] },
    { label: 'a body with another thread', messages: [storedMessage('commandExecution', { ...item('inProgress'), threadId: 'other-thread' })] },
    { label: 'a body without an item', messages: [storedMessage('commandExecution', { threadId: sessionId })] },
  ])('does not read a start from $label', ({ messages }) => {
    expect(codexCommandStarted(messages, sessionId)).toBe(false)
  })

  it('refuses an absent session ID', () => {
    expect(() => codexCommandStarted([], '')).toThrow('requires its session ID')
  })

  it('refuses a body that is not JSON', () => {
    const broken = create(AgentChatMessageSchema, {
      id: 'message-2',
      seq: 2n,
      source: MessageSource.AGENT,
      agentProvider: AgentProvider.CODEX,
      spanType: 'commandExecution',
      agentSessionId: sessionId,
      contentCompression: ContentCompression.NONE,
      content: encoder.encode('not json'),
    })
    expect(() => codexCommandStarted([broken], sessionId)).toThrow('invalid JSON')
  })
})
