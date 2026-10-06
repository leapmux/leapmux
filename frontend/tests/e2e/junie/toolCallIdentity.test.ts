import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { junieNativeOutputFileCallId } from './toolCallIdentity'

const id = '8b4a1496-1260-4d91-a8bc-4a3a56240fbe'
const command = 'node actual-computed-script.js'
const workingDirectory = '/owned/project'
const frame = { sessionUpdate: 'tool_call', toolCallId: id, kind: 'execute', status: 'in_progress', rawInput: { command, cwd: workingDirectory } }
function message(value: unknown = frame, spanId = id) {
  return create(AgentChatMessageSchema, { id: 'native-row', spanId, spanType: 'execute', contentCompression: ContentCompression.NONE, content: new TextEncoder().encode(JSON.stringify(value)) })
}
function snapshot() {
  return { agentId: 'agent', agentSessionId: 'session-261002-102701-7le2', messages: [message()] }
}

describe('junieNativeOutputFileCallId', () => {
  it('reads the native UUID from the exact command and Worker span', () => {
    expect(junieNativeOutputFileCallId(snapshot(), command, workingDirectory)).toBe(id)
    expect(junieNativeOutputFileCallId(snapshot(), command, workingDirectory)).not.toBe('native-full-output')
  })

  it('keeps one identity when a retained closing row repeats the original native frame', () => {
    const stored = snapshot()
    stored.messages.push(message())
    expect(junieNativeOutputFileCallId(stored, command, workingDirectory)).toBe(id)
  })

  // A row of another command is no candidate, so it leaves no call. A row of the command must carry its call ID in its span.
  it.each([
    [{ agentId: '' }, 'requires its started native session and exact command'],
    [{ agentSessionId: '' }, 'requires its started native session and exact command'],
    [{ messages: [] }, 'requires one actual ACP call'],
    [{ messages: [message({ ...frame, rawInput: { command: 'another-command' } })] }, 'requires one actual ACP call'],
    [{ messages: [message(frame, 'foreign-span')] }, 'has inconsistent native tool identity'],
    [{ messages: [message({ ...frame, toolCallId: '' })] }, 'has inconsistent native tool identity'],
  ])('rejects absent or inconsistent native identity in case %#', (change, error) => {
    expect(() => junieNativeOutputFileCallId({ ...snapshot(), ...change }, command, workingDirectory)).toThrow(error)
  })

  it('rejects two actual native calls that both ran the same command', () => {
    const stored = snapshot()
    stored.messages.push(message({ ...frame, toolCallId: 'another-native-id' }, 'another-native-id'))
    expect(() => junieNativeOutputFileCallId(stored, command, workingDirectory)).toThrow('one actual ACP call')
  })

  it('rejects an empty command', () => {
    expect(() => junieNativeOutputFileCallId(snapshot(), '', workingDirectory)).toThrow('exact command')
  })

  it('rejects a different or absent working directory', () => {
    expect(() => junieNativeOutputFileCallId(snapshot(), command, '/another/project')).toThrow('one actual ACP call')
    expect(() => junieNativeOutputFileCallId(snapshot(), command, '')).toThrow('exact command')
    const stored = snapshot()
    stored.messages = [message({ ...frame, rawInput: { command } })]
    expect(() => junieNativeOutputFileCallId(stored, command, workingDirectory)).toThrow('one actual ACP call')
  })
})
