import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { ampInferenceOf } from '../helpers/ampSurface'
import { ampToolResult } from './toolResult'

function request(run: unknown): MockModelRequestRecord {
  return {
    protocol: 'anthropic-messages',
    path: '/actors/native-thread',
    body: ampInferenceOf([
      { role: 'assistant', content: [{ type: 'tool_use', id: 'native-call', input: { command: 'exit 7; REQUEST_ONLY_MARKER' } }] },
      { role: 'user', content: [{ type: 'tool_result', toolUseID: 'native-call', run }] },
    ]).body,
  }
}

describe('ampToolResult', () => {
  it('reads the executor result by native call ID without matching its request command', () => {
    const result = JSON.stringify({ output: 'ACTUAL_STDERR_MARKER', exitCode: 7 })
    expect(ampToolResult(request({ status: 'done', result }), 'native-call')).toEqual({ text: result, exitCode: 7 })
    expect(ampToolResult(request({ status: 'done', result }), 'native-call').text).not.toContain('REQUEST_ONLY_MARKER')
  })

  it('preserves an empty native output and zero exit code without guessing a failure flag', () => {
    const result = JSON.stringify({ output: '', exitCode: 0 })
    expect(ampToolResult(request({ status: 'done', result }), 'native-call')).toEqual({ text: result, exitCode: 0 })
  })

  it('preserves the native executor error status', () => {
    expect(ampToolResult(request({ status: 'error', error: { message: 'Native executor refused the call.' } }), 'native-call'))
      .toEqual({ text: '{"message":"Native executor refused the call."}', failed: true })
  })

  it('keeps native status fields absent when the executor states none', () => {
    expect(ampToolResult(request({ result: 'Native plain result.' }), 'native-call')).toEqual({ text: 'Native plain result.' })
  })

  it.each([null, {}, { status: 'done' }, { result: null }])('refuses an absent native result: %j', (run) => {
    expect(() => ampToolResult(request(run), 'native-call')).toThrow(/executor run|no result/)
  })

  it('refuses an unrelated call ID, and keeps the count of the shared reader as the cause', () => {
    expect(() => ampToolResult(request({ result: 'Native result.' }), 'another-call')).toThrow(expect.objectContaining({
      message: 'Amp returned no unique executor run for another-call.',
      cause: expect.objectContaining({ message: expect.stringContaining('contains 0 results for another-call') }),
    }))
  })

  it('refuses duplicate results for the same native call ID', () => {
    const duplicate = request({ result: 'Native result.' })
    duplicate.body = ampInferenceOf([
      { role: 'user', content: [
        { type: 'tool_result', toolUseID: 'native-call', run: { result: 'First result.' } },
        { type: 'tool_result', toolUseID: 'native-call', run: { result: 'Second result.' } },
      ] },
    ]).body
    expect(() => ampToolResult(duplicate, 'native-call')).toThrow('no unique executor run')
  })

  it.each(['{', '[]', 'null'])('refuses malformed executor content: %s', (content) => {
    const malformed = request({ result: 'Native result.' })
    malformed.body = { messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'native-call', content }] }] }
    expect(() => ampToolResult(malformed, 'native-call')).toThrow(/invalid JSON|no result/)
  })

  it.each(['7', Number.NaN, 1.5])('refuses an invalid native exit code: %s', (exitCode) => {
    expect(() => ampToolResult(request({ result: { output: 'Native output.', exitCode } }), 'native-call')).toThrow('invalid native exit code')
  })
})
