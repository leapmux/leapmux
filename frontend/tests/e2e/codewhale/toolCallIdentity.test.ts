import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleNativeCallId } from './toolCallIdentity'

const text = new TextEncoder()

function snapshot(messages: Array<{ spanId: string, content: unknown }>): NativeMessageSnapshot {
  return {
    agentId: 'agent-1',
    agentSessionId: 'thr_1',
    messages: messages.map(({ spanId, content }, index) => {
      const message = create(AgentChatMessageSchema, {
        id: `m-${index}`,
        seq: BigInt(index + 1),
        agentSessionId: 'thr_1',
        spanId,
        content: text.encode(JSON.stringify(content)),
      })
      message.contentCompression = ContentCompression.NONE
      return message
    }),
  }
}

function itemFrame(callId: string, toolUseId: string): unknown {
  return { event: 'item.completed', payload: { item: { metadata: { provider_tool_use_id: callId, tool_use_id: toolUseId } } } }
}

describe('codewhaleNativeCallId', () => {
  it('resolves the runtime id of the one item that answers the scripted call', () => {
    expect(codewhaleNativeCallId(snapshot([
      { spanId: 'other', content: itemFrame('foreign', 'other') },
      { spanId: 'native-7', content: itemFrame('scripted', 'native-7') },
    ]), 'scripted')).toBe('native-7')
  })

  it('refuses a scripted call with no item or with two', () => {
    expect(() => codewhaleNativeCallId(snapshot([]), 'scripted')).toThrow('one native item')
    expect(() => codewhaleNativeCallId(snapshot([
      { spanId: 'a', content: itemFrame('scripted', 'a') },
      { spanId: 'b', content: itemFrame('scripted', 'b') },
    ]), 'scripted')).toThrow('one native item')
  })

  it('refuses an item whose stored span disagrees with its stated id', () => {
    expect(() => codewhaleNativeCallId(snapshot([
      { spanId: 'a', content: itemFrame('scripted', 'b') },
    ]), 'scripted')).toThrow('inconsistent')
  })
})
