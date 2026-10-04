import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { openCodeShellOutcome } from './nativeShellOutcome'

const encoder = new TextEncoder()
function snapshot(exit: unknown = 7, status = 'completed', output = 'ACTUAL_STDERR\n'): NativeMessageSnapshot {
  return { agentId: 'parent', agentSessionId: 'native-session', messages: [create(AgentChatMessageSchema, {
    id: 'result',
    agentSessionId: 'native-session',
    spanId: 'shell-1',
    contentCompression: ContentCompression.NONE,
    content: encoder.encode(JSON.stringify({ sessionUpdate: 'tool_call_update', toolCallId: 'shell-1', status, rawOutput: { output, metadata: { exit } } })),
  })] }
}

describe('openCodeShellOutcome', () => {
  it.each([0, 7, -1])('retains native exit %s independently of model-facing prose', (exitCode) => {
    expect(openCodeShellOutcome(snapshot(exitCode), 'shell-1', 'ACTUAL_STDERR\n'))
      .toEqual({ text: 'ACTUAL_STDERR\n', exitCode, failed: exitCode !== 0 })
  })

  it('requires an exact call and a nonempty Worker agent/session identity', () => {
    expect(() => openCodeShellOutcome(snapshot(), '', 'ACTUAL_STDERR\n')).toThrow('exact agent, session, and call ID')
    expect(() => openCodeShellOutcome({ ...snapshot(), agentId: '' }, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('exact agent, session, and call ID')
    expect(() => openCodeShellOutcome({ ...snapshot(), agentSessionId: '' }, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('exact agent, session, and call ID')
  })

  it('retains an explicit native failed outcome', () => {
    expect(openCodeShellOutcome(snapshot(7, 'failed'), 'shell-1', 'ACTUAL_STDERR\n').failed).toBe(true)
  })

  it.each([undefined, null, '7', false, 0.5, Number.MAX_SAFE_INTEGER + 1, {}, []])('refuses an absent or malformed numeric native outcome: %j', (exit) => {
    const source = snapshot(exit)
    if (exit === undefined)
      source.messages[0]!.content = encoder.encode('{"sessionUpdate":"tool_call_update","toolCallId":"shell-1","status":"completed","rawOutput":{"output":"ACTUAL_STDERR\\n","metadata":{}}}')
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('safe integer exit code')
  })

  it('does not derive a missing code from output that mentions exit seven', () => {
    const source = snapshot(null, 'completed', 'Command exited with code 7.')
    expect(() => openCodeShellOutcome(source, 'shell-1', 'Command exited with code 7.')).toThrow('safe integer exit code')
  })

  it('refuses a stale session or another call even when its output matches', () => {
    const source = snapshot()
    source.messages[0]!.agentSessionId = 'old-session'
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('one completed')
    source.messages[0]!.agentSessionId = 'native-session'
    source.messages[0]!.spanId = 'other-call'
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('one completed')
  })

  it('refuses conflicting call identity inside a matching span', () => {
    const source = snapshot()
    source.messages[0]!.content = encoder.encode('{"sessionUpdate":"tool_call_update","toolCallId":"other","status":"completed"}')
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('another call')
  })

  it('refuses duplicate completed frames, output mismatch, and incomplete status', () => {
    const source = snapshot()
    const first = source.messages[0]
    if (!first)
      throw new Error('The native outcome fixture contains no initial result.')
    source.messages.push(create(AgentChatMessageSchema, { ...first, id: 'duplicate' }))
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('one completed')
    expect(() => openCodeShellOutcome(snapshot(), 'shell-1', 'OTHER_BYTES\n')).toThrow('different output bytes')
    expect(() => openCodeShellOutcome(snapshot(7, 'in_progress'), 'shell-1', 'ACTUAL_STDERR\n')).toThrow('one completed')
  })

  it('uses a matching Worker supplement without accepting a different call supplement', () => {
    const source = snapshot()
    source.messages[0]!.content = encoder.encode('{"sessionUpdate":"tool_call_update","toolCallId":"shell-1","status":"completed"}')
    source.messages[0]!.supplementalContentCompression = ContentCompression.NONE
    source.messages[0]!.supplementalContent = encoder.encode('{"provider":{"sessionUpdate":"tool_call_update","toolCallId":"shell-1","status":"completed","protocol":{"rawOutput":{"output":"ACTUAL_STDERR\\n","metadata":{"exit":7}}}}}')
    expect(openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n').exitCode).toBe(7)
    source.messages[0]!.supplementalContent = encoder.encode('{"provider":{"sessionUpdate":"tool_call_update","toolCallId":"other","status":"completed","protocol":{"rawOutput":{"output":"ACTUAL_STDERR\\n","metadata":{"exit":7}}}}}')
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('safe integer exit code')
  })

  it('refuses damaged native bytes and malformed relevant JSON', () => {
    const source = snapshot()
    source.messages[0]!.contentCompression = ContentCompression.ZSTD
    source.messages[0]!.content = new Uint8Array([0, 1])
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow()
    source.messages[0]!.contentCompression = ContentCompression.NONE
    source.messages[0]!.content = encoder.encode('{broken')
    expect(() => openCodeShellOutcome(source, 'shell-1', 'ACTUAL_STDERR\n')).toThrow('invalid JSON')
  })
})
