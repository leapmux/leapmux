import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { readCodexExecResult } from './execResult'

function response(output: unknown): MockModelRequestRecord {
  return { protocol: 'openai-responses', path: '/v1/responses', body: { input: [
    { type: 'custom_tool_call', call_id: 'actual-exec', name: 'exec', input: 'const result = await tools.exec_command({cmd:"actual command"});text(JSON.stringify(result))' },
    { type: 'custom_tool_call_output', call_id: 'actual-exec', output },
  ] } }
}
function blocks(value: unknown) {
  return [{ type: 'input_text', text: 'Script completed\nWall time 0.1 seconds\nOutput:\n' }, { type: 'input_text', text: JSON.stringify(value) }]
}

describe('readCodexExecResult', () => {
  it.each([0, 7, -1])('reads actual code-mode metadata with exit %s from native content blocks', (exitCode) => {
    expect(readCodexExecResult(response(blocks({ output: 'COMPUTED42\n', exit_code: exitCode, wall_time_seconds: 0 })), 'actual-exec'))
      .toEqual({ text: 'COMPUTED42\n', exitCode, failed: exitCode !== 0 })
  })
  it('preserves a genuinely unfinished native session without inventing exit zero', () => {
    expect(readCodexExecResult(response(blocks({ output: 'PARTIAL_NATIVE_OUTPUT', session_id: 42 })), 'actual-exec')).toEqual({ text: 'PARTIAL_NATIVE_OUTPUT' })
  })
  it('preserves an empty native output string and explicit exit zero', () => {
    expect(readCodexExecResult(response(blocks({ output: '', exit_code: 0 })), 'actual-exec')).toEqual({ text: '', exitCode: 0, failed: false })
  })
  it.each([
    { output: 'ACTUAL', exit_code: '7' },
    { output: 'ACTUAL', exit_code: null },
    { output: 'ACTUAL', exit_code: 0.5 },
    { output: 'ACTUAL', exit_code: Number.MAX_SAFE_INTEGER + 1 },
    { output: 'ACTUAL' },
    { output: ['content block'], exit_code: 0 },
    { output: false, exit_code: 0 },
    { output: null, exit_code: 0 },
    { exit_code: 0 },
    { error: 'The actual native command was interrupted.' },
  ])('rejects a malformed or status-free native command result: %j', (value) => {
    expect(() => readCodexExecResult(response(blocks(value)), 'actual-exec')).toThrow()
  })
  it('rejects two result objects in the same native code-mode output', () => {
    expect(() => readCodexExecResult(response([...blocks({ output: 'ONE', exit_code: 0 }), { type: 'input_text', text: '{"output":"TWO","exit_code":7}' }]), 'actual-exec')).toThrow()
  })
  it('does not read native status from another call or the scripted arguments', () => {
    const request: MockModelRequestRecord = { protocol: 'openai-responses', path: '/v1/responses', body: { input: [
      { type: 'custom_tool_call', call_id: 'actual-exec', input: '{"output":"ARGUMENT_ONLY","exit_code":7}' },
      { type: 'custom_tool_call_output', call_id: 'other-exec', output: blocks({ output: 'OTHER_RESULT', exit_code: 7 }) },
    ] } }
    expect(() => readCodexExecResult(request, 'actual-exec')).toThrow()
  })
})
