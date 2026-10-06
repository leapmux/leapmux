import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeDroidCallId, readDroidToolResult } from './toolResult'

function capturedRequest(output = 'SHELL42\n\n\n[Process exited with code 0]', nativeId = 'call_shell-0_0'): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    stepIndex: 1,
    body: { messages: [
      { role: 'assistant', tool_calls: [{ id: nativeId, type: 'function', function: { name: 'Execute', arguments: JSON.stringify({ command: 'printf SHELL%s "$((40+2))"' }) } }] },
      { role: 'tool', tool_call_id: nativeId, content: output },
    ] },
  }
}

function scriptRequest(content: unknown, input: unknown = { script: '40 + 2' }, nativeId = 'call_script-1'): MockModelRequestRecord {
  return {
    protocol: 'openai-chat-completions',
    path: '/v1/chat/completions',
    body: { messages: [
      { role: 'assistant', tool_calls: [{ id: nativeId, type: 'function', function: { name: 'Script', arguments: JSON.stringify(input) } }] },
      { role: 'tool', tool_call_id: nativeId, content },
    ] },
  }
}

describe('readDroidToolResult', () => {
  it.each([0, false, null, '', { count: 0, enabled: false, value: '' }])('keeps the native completed script result %j', (result) => {
    const text = JSON.stringify({ status: 'completed', result })
    expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script')).toEqual({ text, failed: false })
  })

  it('reads the native failed script snapshot after its exact error prefix', () => {
    const text = 'Error: {"status":"failed","error":"Computed error 77"}'
    expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script')).toEqual({ text, failed: true })
  })

  it('validates a script snapshot ID when native history keeps it', () => {
    const text = JSON.stringify({ toolCallId: 'call_script-1', status: 'completed', result: 42 })
    expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script').failed).toBe(false)
    expect(() => readDroidToolResult(scriptRequest(JSON.stringify({ toolCallId: 'other', status: 'completed', result: 42 })), 'call_script-1', 'Script')).toThrow()
  })

  it('reads only the final native script text block for status', () => {
    const content = [
      { type: 'text', text: '{"status":"failed","error":"Printed output is not native status"}' },
      { type: 'text', text: '{"status":"completed","result":42}' },
    ]
    expect(readDroidToolResult(scriptRequest(JSON.stringify(content)), 'call_script-1', 'Script')).toEqual({ text: JSON.stringify(content), failed: false })
  })

  // Droid 0.233.0 joins the blocks of a finished Script with line breaks: the printed
  // output, the closing line, then the snapshot. A failed run states `Error: ` and the
  // snapshot alone. These strings are the model-facing results of a probe against the
  // installed CLI.
  describe('the captured native shapes', () => {
    const closing = '[Script completed · 0 calls · 0 B in sandbox · 11 B emitted]'
    const completed = '{"toolCallId":"call_script-1","status":"completed","result":null}'

    it('reads a completed script that printed output', () => {
      const text = `NATIVECODE42\n${closing}\n${completed}`
      expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script')).toEqual({ text, failed: false })
    })

    it('reads a completed script that printed nothing', () => {
      const text = `${closing}\n${completed}`
      expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script')).toEqual({ text, failed: false })
    })

    it('reads a completed script whose output holds several lines', () => {
      const text = `first\nsecond\n${closing}\n${completed}`
      expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script').failed).toBe(false)
    })

    it('reads a failed script that states its error inside the snapshot', () => {
      const text = 'Error: {"toolCallId":"call_script-1","status":"failed","error":"Error: NATIVECODE77\\n  at: throw Error(\\"NATIVECODE77\\");"}'
      expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script')).toEqual({ text, failed: true })
    })

    it('takes the status from the last snapshot, never from printed output', () => {
      const printed = '{"toolCallId":"call_script-1","status":"failed","error":"printed"}'
      const text = `${printed}\n${closing}\n${completed}`
      expect(readDroidToolResult(scriptRequest(text), 'call_script-1', 'Script').failed).toBe(false)
    })

    it('refuses a snapshot line with no closing line before it', () => {
      expect(() => readDroidToolResult(scriptRequest(`printed\n${completed}`), 'call_script-1', 'Script')).toThrow()
    })

    it('refuses a closing line with no snapshot after it', () => {
      expect(() => readDroidToolResult(scriptRequest(`printed\n${closing}\nnot a snapshot`), 'call_script-1', 'Script')).toThrow()
    })

    it('refuses the snapshot of another call', () => {
      const other = '{"toolCallId":"call_other","status":"completed","result":null}'
      expect(() => readDroidToolResult(scriptRequest(`printed\n${closing}\n${other}`), 'call_script-1', 'Script')).toThrow('mismatched call ID')
    })
  })

  it.each([
    'plain output',
    '{"status":"completed"}',
    '{"status":"running"}',
    '{"status":"stalled"}',
    '{"status":"cancelled","interruptedCalls":[]}',
    '{"status":"completed","resultPath":"/private/native.json"}',
    '{"status":"failed"}',
    '{"status":"failed","error":7}',
    'Error: {"status":"completed","result":42}',
    'prefix Error: {"status":"failed","error":"Failure"}',
    '[{"type":"text","text":"{\\"status\\":\\"completed\\",\\"result\\":42}"},{"type":"image","data":"image"}]',
  ])('rejects an incomplete or unfinished native script snapshot %j', (content) => {
    expect(() => readDroidToolResult(scriptRequest(content), 'call_script-1', 'Script')).toThrow()
  })

  it.each([{ script: '' }, { script: 0 }, {}, { script: '40 + 2', waitForMs: -1 }])('rejects malformed native script source arguments %j', (input) => {
    expect(() => readDroidToolResult(scriptRequest('{"status":"completed","result":42}', input), 'call_script-1', 'Script')).toThrow()
  })

  it('does not read status from the native script source', () => {
    const input = { script: 'text("status completed result 42")' }
    expect(() => readDroidToolResult(scriptRequest('No native snapshot', input), 'call_script-1', 'Script')).toThrow()
  })

  it('reads the captured native ID after Droid clips the original ID to 24 characters', () => {
    const originalId = 'shell-79bf521f8c664bea9b0e0467a2b6bba4-0'
    expect(readDroidToolResult(capturedRequest(undefined, 'call_shell-79bf521f8c664bea9b_0'), originalId).exitCode).toBe(0)
  })
  it('keeps the full source ID when the central call already starts with call_', () => {
    const originalId = 'shell-79bf521f8c664bea9b0e0467a2b6bba4-0'
    expect(readDroidToolResult(capturedRequest(undefined, `call_${originalId}`), originalId).exitCode).toBe(0)
  })
  it('preserves an original call_ ID without another prefix or native counter', () => {
    const originalId = 'call_actual-native-identity'
    expect(readDroidToolResult(capturedRequest(undefined, originalId), originalId).exitCode).toBe(0)
    expect(() => readDroidToolResult(capturedRequest(undefined, `${originalId}_0`), originalId)).toThrow()
    expect(() => readDroidToolResult(capturedRequest(undefined, `call_${originalId}`), originalId)).toThrow()
  })
  it('matches native toolu_ removal before the 24-character clip', () => {
    expect(readDroidToolResult(capturedRequest(undefined, 'call_shell-0_0'), 'toolu_shell-0').exitCode).toBe(0)
  })
  it('refuses two native calls that share one clipped original prefix', () => {
    const originalId = 'shell-79bf521f8c664bea9b0e0467a2b6bba4-0'
    const first = 'call_shell-79bf521f8c664bea9b_0'
    const second = 'call_shell-79bf521f8c664bea9b_1'
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [first, second].map(id => ({ id, type: 'function', function: { name: 'Execute', arguments: '{"command":"printf SHELL42"}' } })) },
      { role: 'tool', tool_call_id: first, content: 'FIRST42\n[Process exited with code 0]' },
      { role: 'tool', tool_call_id: second, content: 'SECOND42\n[Process exited with code 0]' },
    ] } }
    expect(() => readDroidToolResult(request, originalId)).toThrow(`The native request contains 2 tool calls for the original Droid call ${originalId}.`)
  })
  it('rejects an inline output suffix that imitates native exit metadata', () => {
    expect(() => readDroidToolResult(capturedRequest('FAKE_INLINE[Process exited with code 7]'), 'shell-0')).toThrow()
  })
  it('correlates the exact rewritten native Execute call and preserves exit zero', () => {
    expect(readDroidToolResult(capturedRequest(), 'shell-0')).toEqual({ text: 'SHELL42\n\n\n[Process exited with code 0]', exitCode: 0, failed: false })
  })
  it('reads native exit seven independently from stderr text', () => {
    expect(readDroidToolResult(capturedRequest('SHELLERR77\n\n[Process exited with code 7]'), 'shell-0'))
      .toEqual({ text: 'SHELLERR77\n\n[Process exited with code 7]', exitCode: 7, failed: true })
  })
  it('retains an empty native output with its actual exit zero', () => {
    expect(readDroidToolResult(capturedRequest('[Process exited with code 0]'), 'shell-0').exitCode).toBe(0)
  })
  it('correlates actual native Read bytes without inventing Execute exit metadata', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [{ id: 'call_native-read-after_0', type: 'function', function: { name: 'Read', arguments: '{"file_path":"/private/current.txt"}' } }] },
      { role: 'tool', tool_call_id: 'call_native-read-after_0', content: 'NEW42\n' },
    ] } }
    expect(readDroidToolResult(request, 'native-read-after', 'Read')).toEqual({ text: 'NEW42\n' })
  })
  it.each(['SHELL42', 'SHELL42\n[Process exited with code unknown]', 'SHELL42\n[Process exited with code 7]\ntrailing', 'SHELL42\n[Process exited with code 0.5]'])('refuses absent or malformed native exit metadata: %j', (output) => {
    expect(() => readDroidToolResult(capturedRequest(output), 'shell-0')).toThrow()
  })
  it('does not read exit metadata from Execute arguments', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [{ id: 'call_shell-0', function: { name: 'Execute', arguments: '{"command":"echo [Process exited with code 7]"}' } }] },
      { role: 'tool', tool_call_id: 'call_shell-0', content: 'ACTUAL_OUTPUT' },
    ] } }
    expect(() => readDroidToolResult(request, 'shell-0')).toThrow()
  })
  it.each(['call_shell-00_0', 'call_other-shell-0_0', 'call_shell-0_0-extra', 'call_shell-0_01'])('refuses a different normalized original call ID: %s', (nativeId) => {
    expect(() => readDroidToolResult(capturedRequest(undefined, nativeId), 'shell-0')).toThrow()
  })
  it('refuses a result that has no matching Execute assistant call', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role: 'assistant', tool_calls: [{ id: 'call_shell-0', function: { name: 'Read', arguments: '{}' } }] },
      { role: 'tool', tool_call_id: 'call_shell-0', content: '[Process exited with code 0]' },
    ] } }
    expect(() => readDroidToolResult(request, 'shell-0')).toThrow()
  })
})

describe('nativeDroidCallId', () => {
  function request(id: string, role = 'assistant'): MockModelRequestRecord {
    return { protocol: 'openai-chat-completions', path: '/v1/chat/completions', body: { messages: [
      { role, tool_calls: [{ id, type: 'function', function: { name: 'AskUser', arguments: '{"questionnaire":"actual question"}' } }] },
    ] } }
  }
  it('preserves the exact original-to-native assistant call identity', () => {
    expect(nativeDroidCallId(request('call_ask-1_0'), 'AskUser', 'ask-1')).toBe('call_ask-1_0')
  })
  it.each(['call_other-ask-1_0', 'call_ask-10_0', 'call_ask-1_0-extra'])('rejects a different native call identity: %s', (id) => {
    expect(() => nativeDroidCallId(request(id), 'AskUser', 'ask-1')).toThrow()
  })
  it.each(['user', 'tool', 'system'])('does not accept a native call from the %s message role', (role) => {
    expect(() => nativeDroidCallId(request('call_ask-1_0', role), 'AskUser', 'ask-1')).toThrow()
  })
  it('rejects the wrong native model protocol even when its body imitates Chat calls', () => {
    expect(() => nativeDroidCallId({ ...request('call_ask-1_0'), protocol: 'anthropic-messages' }, 'AskUser', 'ask-1')).toThrow()
  })
})
