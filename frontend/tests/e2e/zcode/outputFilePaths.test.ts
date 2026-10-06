import type { NativeMessageSnapshot } from '../helpers/nativeMessages'
import { describe, expect, it } from 'vitest'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { readZcodeNativeOutput } from './outputFilePaths'

const sessionId = 'native-session'
const callId = 'native-call'
const path = '/native/zcode/artifacts/native-session/native-call-tool-result-11111111-1111-4111-8111-111111111111.txt'
const frame = { type: 'tool.updated', sessionId, payload: { kind: 'result', toolCallId: callId, result: { success: true, content: 'native preview' } } }
const native = { sessionId, messageId: 'native-message', data: { type: 'tool', callID: callId, tool: 'GetWorkflowRun', state: { status: 'completed', metadata: { serialization: { budgetStrategy: 'artifact', artifactPath: path } } } } }
const provider = { type: frame.type, payload: { kind: 'result', toolCallId: callId }, nativeTool: native }
/** The error of the shared record reader for a snapshot with no record of the call in its session. */
const NO_RECORD = 'The native output requires exactly one accepted record in its Worker session and span.'
/** The error of the ZCode reader for a provider section that does not state the call, the session, and the path. */
const NO_OWNER = 'The native ZCode output pointer has no exact call and session owner.'
/** The error of the ZCode reader for a path outside the artifact directory of the session and the call. */
const FOREIGN_PATH = 'The native ZCode path belongs to another call or session.'

function snapshot(original: unknown = frame, supplement: unknown = { provider }): NativeMessageSnapshot {
  return nativeOutputSnapshot([{ frame: original, spanId: callId, spanType: 'GetWorkflowRun', supplement }], { agentId: 'native-agent', agentSessionId: sessionId })
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

  // A path field that is still a filesystem path passes the owner check, and the path check then refuses it.
  it.each([
    ['session', NO_OWNER],
    ['call', NO_OWNER],
    ['tool', NO_OWNER],
    ['path', FOREIGN_PATH],
    ['status', NO_OWNER],
    ['strategy', NO_OWNER],
  ])('refuses a contradictory native %s field', (field, error) => {
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
    expect(() => readZcodeNativeOutput(snapshot(frame, { provider: changed }), callId, 'GetWorkflowRun')).toThrow(error)
  })

  it.each([null, {}, provider, { provider: null }, { provider: false }, { provider: { ...provider, payload: { kind: 'result', toolCallId: 'foreign' } } }])('refuses an absent or unowned provider section: %j', (supplement) => {
    expect(() => readZcodeNativeOutput(snapshot(frame, supplement), callId, 'GetWorkflowRun')).toThrow(NO_OWNER)
  })

  it.each(['', 'foreign-session'])('refuses a missing or foreign Worker session: %j', (agentSessionId) => {
    const input = snapshot()
    const row = input.messages[0]
    if (!row)
      throw new Error('The native fixture requires its original row.')
    row.agentSessionId = agentSessionId
    expect(() => readZcodeNativeOutput(input, callId, 'GetWorkflowRun')).toThrow(NO_RECORD)
  })

  it('refuses a native frame from another session', () => {
    expect(() => readZcodeNativeOutput(snapshot({ ...frame, sessionId: 'foreign' }), callId, 'GetWorkflowRun')).toThrow(NO_RECORD)
  })

  it('refuses duplicate native result rows', () => {
    const input = snapshot()
    const row = input.messages[0]
    if (!row)
      throw new Error('The native fixture requires its original row.')
    input.messages.push(row)
    expect(() => readZcodeNativeOutput(input, callId, 'GetWorkflowRun')).toThrow(NO_RECORD)
  })

  it('ignores the removed body store when it contains other text', () => {
    const receipt = readZcodeNativeOutput(snapshot(frame, { provider: { ...provider, outputFiles: { 'zcode-artifact://native-session/opaque': 'data:text/plain;base64,Rk9SR0VE' } } }), callId, 'GetWorkflowRun')
    expect(receipt.previewText).toBe('native preview')
  })
})
