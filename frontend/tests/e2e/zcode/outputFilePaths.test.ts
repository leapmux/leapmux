import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import { AgentChatMessageSchema, ContentCompression } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { readZcodeNativeOutput } from './outputFilePaths'

const sessionId = 'native-session'
const callId = 'native-call'
const path = '/native/zcode/artifacts/native-session/native-call-tool-result-11111111-1111-4111-8111-111111111111.txt'
const frame = { type: 'tool.updated', sessionId, payload: { kind: 'result', toolCallId: callId, result: { success: true, content: 'native preview' } } }
const native = { sessionId, messageId: 'native-message', data: { type: 'tool', callID: callId, tool: 'GetWorkflowRun', state: { status: 'completed', metadata: { serialization: { budgetStrategy: 'artifact', artifactPath: path } } } } }
const provider = { type: frame.type, payload: { kind: 'result', toolCallId: callId }, nativeTool: native }

function snapshot(original: unknown = frame, supplement: unknown = { provider }): NativeMessageSnapshot {
  return { agentId: 'native-agent', agentSessionId: sessionId, messages: [create(AgentChatMessageSchema, {
    id: 'native-row',
    agentSessionId: sessionId,
    spanId: callId,
    spanType: 'GetWorkflowRun',
    contentCompression: ContentCompression.NONE,
    content: new TextEncoder().encode(JSON.stringify(original)),
    supplementalContentCompression: ContentCompression.NONE,
    supplementalContent: new TextEncoder().encode(JSON.stringify(supplement)),
  })] }
}

describe('readZcodeNativeOutput', () => {
  it('keeps the exact native path and preview without reading an output file', () => {
    const input = snapshot()
    const receipt = readZcodeNativeOutput(input, callId, 'GetWorkflowRun')
    expect(receipt.paths).toEqual([path])
    expect(receipt.previewText).toBe('native preview')
    expect(receipt.frame).toEqual(frame)
    expect(receipt.content).toEqual(input.messages[0]?.content)
  })

  it.each(['session', 'call', 'tool', 'path', 'status', 'strategy'])('refuses a contradictory native %s field', (field) => {
    const changed = structuredClone(provider)
    if (field === 'session')
      changed.nativeTool.sessionId = 'foreign'
    if (field === 'call')
      changed.nativeTool.data.callID = 'foreign'
    if (field === 'tool')
      changed.nativeTool.data.tool = 'Read'
    if (field === 'path')
      changed.nativeTool.data.state.metadata.serialization.artifactPath = '/foreign/file.txt'
    if (field === 'status')
      changed.nativeTool.data.state.status = 'running'
    if (field === 'strategy')
      changed.nativeTool.data.state.metadata.serialization.budgetStrategy = 'inline'
    expect(() => readZcodeNativeOutput(snapshot(frame, { provider: changed }), callId, 'GetWorkflowRun')).toThrow()
  })

  it.each([null, {}, provider, { provider: null }, { provider: false }, { provider: { ...provider, payload: { kind: 'result', toolCallId: 'foreign' } } }])('refuses an absent or unowned provider section: %j', (supplement) => {
    expect(() => readZcodeNativeOutput(snapshot(frame, supplement), callId, 'GetWorkflowRun')).toThrow()
  })

  it.each(['', 'foreign-session'])('refuses a missing or foreign Worker session: %j', (agentSessionId) => {
    const input = snapshot()
    const row = input.messages[0]
    if (!row)
      throw new Error('The native fixture requires its original row.')
    row.agentSessionId = agentSessionId
    expect(() => readZcodeNativeOutput(input, callId, 'GetWorkflowRun')).toThrow()
  })

  it('refuses a native frame from another session', () => {
    expect(() => readZcodeNativeOutput(snapshot({ ...frame, sessionId: 'foreign' }), callId, 'GetWorkflowRun')).toThrow()
  })

  it('refuses duplicate native result rows', () => {
    const input = snapshot()
    const row = input.messages[0]
    if (!row)
      throw new Error('The native fixture requires its original row.')
    input.messages.push(row)
    expect(() => readZcodeNativeOutput(input, callId, 'GetWorkflowRun')).toThrow()
  })

  it('ignores the removed body store when it contains other text', () => {
    const receipt = readZcodeNativeOutput(snapshot(frame, { provider: { ...provider, outputFiles: { 'zcode-artifact://native-session/opaque': 'data:text/plain;base64,Rk9SR0VE' } } }), callId, 'GetWorkflowRun')
    expect(receipt.previewText).toBe('native preview')
  })
})
