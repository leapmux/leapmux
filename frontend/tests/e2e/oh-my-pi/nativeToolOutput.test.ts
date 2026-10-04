import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ohMyPiNativeOutput } from './nativeToolOutput'

const frame = { type: 'tool_execution_end', toolCallId: 'call', toolName: 'bash', isError: false, result: { content: [{ type: 'text', text: 'preview' }], details: { meta: { truncation: { artifactId: '0' } } } } }
function snapshot(value: unknown = frame): NativeMessageSnapshot {
  return { agentId: 'agent', agentSessionId: '/private/session.jsonl', messages: [create(AgentChatMessageSchema, { id: 'row', agentSessionId: '/private/session.jsonl', spanId: 'call', spanType: 'bash', contentCompression: ContentCompression.NONE, content: new TextEncoder().encode(JSON.stringify(value)) })] }
}
describe('ohMyPiNativeOutput', () => {
  it('keeps the opaque native identifier zero without a filesystem path', () => {
    const receipt = ohMyPiNativeOutput(snapshot(), 'call')
    expect(receipt.artifactId).toBe('0')
    expect(receipt.previewText).toBe('preview')
    expect(receipt).not.toHaveProperty('path')
  })
  it.each(['', '-1', '../other', '01', '1.2'])('rejects an invalid native artifact ID %s', (outputFileId) => {
    expect(() => ohMyPiNativeOutput(snapshot({ ...frame, result: { ...frame.result, details: { meta: { truncation: { artifactId: outputFileId } } } } }), 'call')).toThrow('opaque ID')
  })
  it.each([{ ...frame, toolCallId: 'other' }, { ...frame, toolName: 'eval' }, { ...frame, isError: true }])('rejects a foreign native completion %j', (value) => {
    expect(() => ohMyPiNativeOutput(snapshot(value), 'call')).toThrow()
  })
})
