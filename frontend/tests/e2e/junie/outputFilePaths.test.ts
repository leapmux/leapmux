import { describe, expect, it } from 'vitest'
import { MessageCompletion } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { nativeOutputSnapshot } from '../helpers/nativeOutputReaderCases'
import { junieHostTerminalPreview, junieNativeNoticePath, readJunieNativeOutputPaths } from './outputFilePaths'

/** The error of the notice reader for a notice with no log path that Junie writes. */
const NO_NOTICE_PATH = 'The Junie output notice has no unique native log path.'

describe('junieNativeNoticePath', () => {
  it.each(['truncated', 'summarized'])('reads the exact native %s notice', (kind) => {
    expect(junieNativeNoticePath(`[Command output exceeded the display limit and has been ${kind}. See full log at: /private/project/.output.txt. Important information may only be in the full log.]`)).toBe('/private/project/.output.txt')
  })

  it('reads the captured session-owned native terminal log path', () => {
    const path = '/private/.junie/sessions/session-261002-102701-7le2/task-261002-102702-1ch6/terminal-output/terminal-output-8897314953674343425.txt'
    expect(junieNativeNoticePath(`[Command output exceeded the display limit and has been truncated. See full log at: ${path}. Important information (test results, errors) may only be in the full log.]`)).toBe(path)
  })

  it('rejects a native terminal log from another captured session', () => {
    const path = '/private/custom-profile/sessions/session-current/task-current/terminal-output/terminal-output-123.txt'
    const notice = `[Command output exceeded the display limit and has been truncated. See full log at: ${path}. Important information]`
    expect(junieNativeNoticePath(notice, 'session-current')).toBe(path)
    expect(() => junieNativeNoticePath(notice, 'session-foreign')).toThrow('another session')
    expect(() => junieNativeNoticePath(notice, '')).toThrow('another session')
  })

  it.each(['/private/native/terminal-output-123.txt', '/private/.junie/sessions/session-current/task-current/terminal-output/foreign.txt', '/private/.junie/sessions/session-current/task-current/terminal-output/terminal-output-abc.txt'])('rejects an unrelated terminal log path %s', (path) => {
    expect(() => junieNativeNoticePath(`[Command output exceeded the display limit and has been truncated. See full log at: ${path}. Important information]`)).toThrow(NO_NOTICE_PATH)
  })
  it.each(['plain /private/.output.txt', '[Command output exceeded the display limit and has been truncated. See full log at: relative/.output.txt. Important information]', '[Command output exceeded the display limit and has been truncated. See full log at: /private/foreign.txt. Important information]'])('rejects an invalid native log reference %s', (text) => {
    expect(() => junieNativeNoticePath(text)).toThrow(NO_NOTICE_PATH)
  })
})

const callId = '8b4a1496-1260-4d91-a8bc-4a3a56240fbe'
const output = 'NATIVE_FIRST42\nNATIVE_MIDDLE77\nNATIVE_LAST42'
const nativeFrame = { sessionUpdate: 'tool_call', toolCallId: callId, kind: 'execute', status: 'in_progress', content: [{ type: 'terminal', terminalId: callId }] }
const retained = { provider: { sessionUpdate: nativeFrame.sessionUpdate, toolCallId: callId, status: nativeFrame.status, terminals: { [callId]: { output, exitCode: 0, truncated: false } } } }

function stored(frame: unknown = nativeFrame, supplement: unknown = retained) {
  return nativeOutputSnapshot([{ frame, spanId: callId, spanType: 'execute', completion: MessageCompletion.COMPLETE, supplement }], { agentSessionId: 'session-current' })
}

describe('junieHostTerminalPreview', () => {
  it('reads the current pointer-only receipt and preserves its native preview', () => {
    const sessionId = 'session-261003-225241-1cba'
    const taskId = 'task-261003-225242-1bq0'
    const path = `/owned/junie/sessions/${sessionId}/${taskId}/terminal-output/terminal-output-123.txt`
    const frame = { sessionUpdate: 'tool_call_update', toolCallId: callId, kind: 'execute', status: 'completed', content: [], rawInput: { command: 'node exact-script.js', cwd: '/owned/project' }, rawOutput: { output }, _meta: { terminal_exit: { terminal_id: callId, exit_code: 0, signal: null } } }
    const receipt = { sessionId, toolCallId: callId, taskId, command: 'node exact-script.js', cwd: '/owned/project', path, exitCode: 0 }
    const supplement = { provider: { sessionUpdate: frame.sessionUpdate, toolCallId: callId, status: frame.status, outputFilePath: receipt } }
    const value = stored(frame, supplement)
    value.agentSessionId = sessionId
    value.messages[0]!.agentSessionId = sessionId
    const before = value.messages[0]!.content.slice()
    const result = readJunieNativeOutputPaths(value, callId)
    expect(result.paths).toEqual([path])
    expect(result.previewText).toBe(output)
    expect(value.messages[0]!.content).toEqual(before)
    expect(receipt).not.toHaveProperty('output')
    expect(receipt).not.toHaveProperty('nativeOutput')
  })

  it('reads retained native host preview bytes with the actual terminal identity and zero exit status', () => {
    expect(junieHostTerminalPreview(stored(), callId)).toBe(output)
  })

  it('retains an empty successful terminal output as an empty string', () => {
    const supplement = { provider: { ...retained.provider, terminals: { [callId]: { output: '', exitCode: 0, truncated: false } } } }
    expect(junieHostTerminalPreview(stored(nativeFrame, supplement), callId)).toBe('')
  })

  // A supplement whose identity differs from the frame, or that is absent, owns no terminal of the row. A terminal of
  // the row with an unsuccessful field reaches the field check.
  it.each([
    [{ provider: { ...retained.provider, toolCallId: 'foreign' } }, 'requires one exact retained native terminal'],
    [{ provider: { ...retained.provider, status: 'completed' } }, 'requires one exact retained native terminal'],
    [{ provider: { ...retained.provider, terminals: { [callId]: { output, exitCode: 1, truncated: false } } } }, 'lacks exact successful native terminal fields'],
    [{ provider: { ...retained.provider, terminals: { [callId]: { output: false, exitCode: 0, truncated: false } } } }, 'lacks exact successful native terminal fields'],
    [{ provider: { ...retained.provider, terminals: { [callId]: { output, exitCode: 0, truncated: false, signal: 'SIGTERM' } } } }, 'lacks exact successful native terminal fields'],
    [{}, 'requires one exact retained native terminal'],
  ])('rejects foreign or unsuccessful native host fields %j', (supplement, error) => {
    expect(() => junieHostTerminalPreview(stored(nativeFrame, supplement), callId)).toThrow(error)
  })

  it('rejects a terminal reference that disagrees with the actual call', () => {
    const frame = { ...nativeFrame, content: [{ type: 'terminal', terminalId: 'foreign' }] }
    expect(() => junieHostTerminalPreview(stored(frame), callId)).toThrow('different native terminal')
  })

  it('rejects duplicate complete retained rows', () => {
    const snapshot = stored()
    snapshot.messages.push(snapshot.messages[0]!)
    expect(() => junieHostTerminalPreview(snapshot, callId)).toThrow('one exact retained')
  })
})

describe('native clipped host preview', () => {
  it('keeps a native clipped preview without treating it as complete file bytes', () => {
    const supplement = { provider: { ...retained.provider, terminals: { [callId]: { output: 'Native clipped preview', exitCode: 0, truncated: true } } } }
    expect(junieHostTerminalPreview(stored(nativeFrame, supplement), callId)).toBe('Native clipped preview')
  })
})
