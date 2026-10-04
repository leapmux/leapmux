import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readDroidNativeOutput } from './outputFilePaths'

const frame = {
  type: 'tool_result',
  toolUseId: 'native-call',
  messageId: 'message-current',
  content: 'native preview\nFull command output saved to: /native/droid-terminal-current/12345678-1234-1234-1234-123456789abc.log (64 KB)',
  isError: false,
}
const path = '/native/droid-terminal-current/12345678-1234-1234-1234-123456789abc.log'

function snapshot(value: unknown = frame): NativeMessageSnapshot {
  return { agentId: 'agent', agentSessionId: 'native-session', messages: [create(AgentChatMessageSchema, {
    id: 'native-row',
    agentSessionId: 'native-session',
    spanId: 'droid-tool-native-call',
    contentCompression: ContentCompression.NONE,
    content: new TextEncoder().encode(JSON.stringify(value)),
  })] }
}

describe('readDroidNativeOutput', () => {
  it('reads the exact native path and preserves the original packet bytes', () => {
    const input = snapshot()
    const receipt = readDroidNativeOutput(input, 'native-call')
    expect(receipt.paths).toEqual([path])
    expect(receipt.previewText).toContain('native preview')
    expect(receipt.frame).toEqual(frame)
    expect(receipt.content).toEqual(input.messages[0]?.content)
  })

  it('refuses a different call while the original Worker span stays fixed', () => {
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll('native-call', 'foreign-call'))
    expect(() => readDroidNativeOutput(snapshot(foreign), 'native-call')).toThrow()
  })

  it.each(['', 'foreign-session'])('refuses a missing or foreign Worker session: %j', (agentSessionId) => {
    const input = snapshot()
    if (!input.messages[0])
      throw new Error('The native fixture requires its original row.')
    input.messages[0].agentSessionId = agentSessionId
    expect(() => readDroidNativeOutput(input, 'native-call')).toThrow()
  })

  it('refuses duplicate native result packets', () => {
    const input = snapshot()
    const row = input.messages[0]
    if (!row)
      throw new Error('The native fixture requires its original row.')
    input.messages.push(row)
    expect(() => readDroidNativeOutput(input, 'native-call')).toThrow()
  })

  it.each(['https://example.com/output', 'file:///native/output', ' ', '/native/zero\0byte'])('refuses a non-filesystem pointer: %j', (invalid) => {
    const foreign: unknown = JSON.parse(JSON.stringify(frame).replaceAll(JSON.stringify(path).slice(1, -1), JSON.stringify(invalid).slice(1, -1)))
    expect(() => readDroidNativeOutput(snapshot(foreign), 'native-call')).toThrow()
  })

  it('preserves the native preview when a discarded feature supplement supplies other text', () => {
    const input = snapshot()
    if (!input.messages[0])
      throw new Error('The native fixture requires its original row.')
    input.messages[0].supplementalContentCompression = ContentCompression.NONE
    input.messages[0].supplementalContent = new TextEncoder().encode(JSON.stringify({ provider: { outputFile: { path, text: 'FORGED_FILE_BODY' } } }))
    expect(readDroidNativeOutput(input, 'native-call').previewText).not.toContain('FORGED_FILE_BODY')
  })
})
