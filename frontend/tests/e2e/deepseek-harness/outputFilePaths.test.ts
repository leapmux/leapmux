import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readDeepseekHarnessNativeOutput } from './outputFilePaths'

const path = '/owned/dsh-spill/0123456789ab/0123456789ab-bash.txt'
const preview = `(Output omitted. Full formatted result stored at: ${path}. Use read_file.)`
function fixture(text = preview, callId = 'call', sessionId = 'session') {
  const frame = { type: 'tool/result', data: { message: { toolCallId: callId, content: [{ type: 'text', text }], isError: false } } }
  return { agentId: 'agent', agentSessionId: sessionId, messages: [create(AgentChatMessageSchema, { id: 'result', spanId: callId, spanType: 'bash', agentSessionId: sessionId, content: new TextEncoder().encode(JSON.stringify(frame)), contentCompression: ContentCompression.NONE })] }
}

describe('readDeepseekHarnessNativeOutput', () => {
  it('reads exact native paths and original preview bytes without a content supplement', () => {
    const snapshot = fixture()
    const before = snapshot.messages[0]!.content.slice()
    const result = readDeepseekHarnessNativeOutput(snapshot, 'call')
    expect(result.paths).toEqual([path])
    expect(result.previewText).toBe(preview)
    expect(result.supplement).toBeUndefined()
    expect(snapshot.messages[0]!.content).toEqual(before)
  })

  it('ignores a root or provider complete-text receipt', () => {
    const snapshot = fixture()
    const receipt = { outputFile: { sessionId: 'session', toolCallId: 'call', files: [{ path, text: 'Unowned complete bytes' }] } }
    snapshot.messages[0]!.supplementalContent = new TextEncoder().encode(JSON.stringify({ ...receipt, provider: receipt }))
    snapshot.messages[0]!.supplementalContentCompression = ContentCompression.NONE
    expect(readDeepseekHarnessNativeOutput(snapshot, 'call').previewText).toBe(preview)
  })

  it('refuses another call, Worker session, span, or duplicate final record', () => {
    const snapshot = fixture()
    expect(() => readDeepseekHarnessNativeOutput(snapshot, 'foreign')).toThrow('exactly one')
    expect(() => readDeepseekHarnessNativeOutput({ ...snapshot, agentSessionId: 'foreign' }, 'call')).toThrow('exactly one')
    snapshot.messages[0]!.spanId = 'foreign'
    expect(() => readDeepseekHarnessNativeOutput(snapshot, 'call')).toThrow('exactly one')
    const duplicate = fixture()
    duplicate.messages.push(duplicate.messages[0]!)
    expect(() => readDeepseekHarnessNativeOutput(duplicate, 'call')).toThrow('exactly one')
  })

  it('preserves empty native text and zero exit status without reading a file', () => {
    expect(readDeepseekHarnessNativeOutput(fixture(''), 'call').previewText).toBe('')
    expect(readDeepseekHarnessNativeOutput(fixture('native preview\n[exit code: 0]'), 'call').previewText).toBe('native preview')
  })

  it.each(['(Output omitted. Full formatted result stored at: file:///owned/result.txt. Use read_file.)', '(Output omitted. Full formatted result stored at: opaque-id. Use read_file.)'])('does not turn a URI or ID into a path: %s', (text) => {
    expect(readDeepseekHarnessNativeOutput(fixture(text), 'call').paths).toEqual([])
  })
})

describe('native formatted path punctuation', () => {
  it.each([
    '/native/project. notes/dsh-spill/0123456789ab/0123456789ab-bash.txt',
    String.raw`C:\native\project. notes\dsh-spill\0123456789ab\0123456789ab-bash.txt`,
  ])('keeps the original native path and preview independently: %j', (nativePath) => {
    const nativePreview = `(Output omitted. Full formatted result stored at: ${nativePath}. Use read_file.)`
    const snapshot = fixture(nativePreview)
    const before = snapshot.messages[0]?.content.slice()
    const receipt = readDeepseekHarnessNativeOutput(snapshot, 'call')
    expect(receipt.paths).toEqual([nativePath])
    expect(receipt.previewText).toBe(nativePreview)
    expect(snapshot.messages[0]?.content).toEqual(before)
  })
})
