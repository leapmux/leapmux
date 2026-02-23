import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { describe, expect, it } from 'vitest'
import { ContentCompression, MessageRole } from '~/generated/leapmux/v1/agent_pb'
import { isAgentWorking } from '~/utils/agentState'

/** Encode a JSON object as message content bytes. */
function encode(obj: unknown): Uint8Array {
  return new TextEncoder().encode(JSON.stringify(obj))
}

/** Build a wrapper envelope (the standard message format). */
function wrap(messages: unknown[]): Uint8Array {
  return encode({ old_seqs: [], messages })
}

/** Build a minimal AgentChatMessage. */
function makeMsg(role: MessageRole, content?: Uint8Array): AgentChatMessage {
  return {
    $typeName: 'leapmux.v1.AgentChatMessage' as const,
    id: 'msg-1',
    role,
    seq: 1n,
    createdAt: '',
    updatedAt: '',
    deliveryError: '',
    content: content ?? new Uint8Array(),
    contentCompression: ContentCompression.NONE,
  } as AgentChatMessage
}

describe('isAgentWorking', () => {
  it('returns false for an empty messages array', () => {
    expect(isAgentWorking([])).toBe(false)
  })

  it('returns true when last message is USER role', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
    ])).toBe(true)
  })

  it('returns true when last message is ASSISTANT role', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
    ])).toBe(true)
  })

  it('returns false when last message is a turn-end RESULT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'result', subtype: 'turn_result' }])),
    ])).toBe(false)
  })

  it('skips settings_changed RESULT and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'settings_changed' }])),
    ])).toBe(true)
  })

  it('skips context_cleared RESULT and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'context_cleared' }])),
    ])).toBe(true)
  })

  it('skips multiple trailing notification RESULTs and finds preceding non-RESULT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.USER),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'context_cleared' }])),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'settings_changed' }])),
    ])).toBe(true)
  })

  it('skips notification RESULTs but finds turn-end RESULT underneath', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, wrap([{ type: 'result', subtype: 'turn_result' }])),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'settings_changed' }])),
    ])).toBe(false)
  })

  it('returns false when all messages are notification RESULTs', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, wrap([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('does not skip interrupted RESULT (it is a genuine turn end)', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'interrupted' }])),
    ])).toBe(false)
  })

  it('handles RESULT with unparseable content as a turn end', () => {
    const badContent = new TextEncoder().encode('not valid json')
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT, badContent),
    ])).toBe(false)
  })

  it('handles RESULT with empty content as a turn end', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.RESULT),
    ])).toBe(false)
  })

  it('skips LEAPMUX message and finds turn-end RESULT underneath', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, wrap([{ type: 'result', subtype: 'turn_result' }])),
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'settings_changed' }])),
    ])).toBe(false)
  })

  it('skips LEAPMUX message and finds preceding ASSISTANT', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.ASSISTANT),
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'settings_changed' }])),
    ])).toBe(true)
  })

  it('skips multiple trailing LEAPMUX messages', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, wrap([{ type: 'result', subtype: 'turn_result' }])),
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('returns false when all messages are LEAPMUX notifications', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })

  it('skips mixed LEAPMUX and notification RESULT messages', () => {
    expect(isAgentWorking([
      makeMsg(MessageRole.RESULT, wrap([{ type: 'result', subtype: 'turn_result' }])),
      makeMsg(MessageRole.RESULT, wrap([{ type: 'settings_changed' }])),
      makeMsg(MessageRole.LEAPMUX, wrap([{ type: 'context_cleared' }])),
    ])).toBe(false)
  })
})
