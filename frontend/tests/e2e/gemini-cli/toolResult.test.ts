import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { describe, expect, it } from 'vitest'
import { readGeminiToolOutput } from './toolResult'

function request(response: unknown): MockModelRequestRecord {
  return {
    protocol: 'google-generative-language',
    path: '/v1beta/models/gemini-2.5-pro:streamGenerateContent',
    body: {
      contents: [
        { role: 'model', parts: [{ functionCall: { id: 'native-call', name: 'run_shell_command', args: { output: 'ARGUMENT_ONLY_OUTPUT' } } }] },
        { role: 'user', parts: [
          { functionResponse: { id: 'other-call', name: 'run_shell_command', response: { output: 'OTHER_CALL_OUTPUT' } } },
          { functionResponse: { id: 'native-call', name: 'run_shell_command', response } },
        ] },
      ],
    },
  }
}

describe('readGeminiToolOutput', () => {
  it('reads decoded output from the exact native call and preserves the model-only path line', () => {
    const output = '<tool_output_masked>\nOutput: native excerpt\nOutput too large. Full output available at: /private/native/output.txt\n</tool_output_masked>'
    const result = readGeminiToolOutput(request({ output, future: false }), 'native-call')
    expect(result).toBe(output)
    expect(/^Output too large\. Full output available at: (.+)$/m.exec(result)?.[1]).toBe('/private/native/output.txt')
    expect(result).not.toContain('ARGUMENT_ONLY_OUTPUT')
    expect(result).not.toContain('OTHER_CALL_OUTPUT')
  })

  it.each([
    { name: 'empty', output: '' },
    { name: 'quoted Unicode', output: 'Native "quoted" output\n\tΩ' },
    { name: 'large', output: 'Ω'.repeat(32_768) },
  ])('preserves $name native output without JSON escapes', ({ output }) => {
    expect(readGeminiToolOutput(request({ output }), 'native-call')).toBe(output)
  })

  it('rejects another call instead of selecting a repeated tool name', () => {
    expect(() => readGeminiToolOutput(request({ output: 'NATIVE_OUTPUT' }), 'missing-call')).toThrow()
  })

  it('rejects a model-role result instead of treating it as native tool output', () => {
    const record: MockModelRequestRecord = { protocol: 'google-generative-language', path: '/mock', body: { contents: [{ role: 'model', parts: [{ functionResponse: { id: 'native-call', response: { output: 'MODEL_ONLY_OUTPUT' } } }] }] } }
    expect(() => readGeminiToolOutput(record, 'native-call')).toThrow()
  })

  it.each([undefined, null, false, 0, '', '{"output":"SCALAR_JSON_IMITATION"}', [], {}, { output: null }, { output: false }, { output: 0 }, { output: [] }, { error: 'Native tool failed' }].map(response => [response]))('rejects an absent or malformed output: %j', (response) => {
    expect(() => readGeminiToolOutput(request(response), 'native-call')).toThrow()
  })

  it.each([null, {}, { contents: false }, { contents: [{ role: 'user', parts: false }] }])('rejects malformed native request content: %j', (body) => {
    expect(() => readGeminiToolOutput({ protocol: 'google-generative-language', path: '/mock', body }, 'native-call')).toThrow()
  })

  it('rejects duplicated native call results', () => {
    const result = { functionResponse: { id: 'native-call', response: { output: 'NATIVE_OUTPUT' } } }
    expect(() => readGeminiToolOutput({ protocol: 'google-generative-language', path: '/mock', body: { contents: [{ role: 'user', parts: [result, result] }] } }, 'native-call')).toThrow()
  })

  it('rejects absent requests and another model protocol', () => {
    expect(() => readGeminiToolOutput(undefined, 'native-call')).toThrow()
    expect(() => readGeminiToolOutput({ protocol: 'openai-chat-completions', path: '/mock', body: { messages: [{ role: 'tool', tool_call_id: 'native-call', content: 'OTHER_PROTOCOL_OUTPUT' }] } }, 'native-call')).toThrow()
  })
})
