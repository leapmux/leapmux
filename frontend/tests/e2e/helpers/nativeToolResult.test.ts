import type { MockModelRequestRecord } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { nativeToolResult, nativeToolResultContent } from './nativeToolResult'

function request(protocol: MockModelRequestRecord['protocol'], body: unknown): MockModelRequestRecord {
  return { protocol, path: '/mock', body }
}

describe('nativeToolResult', () => {
  it('keeps a native object distinct from a scalar string that imitates its JSON', () => {
    const content = { output: 'NATIVE_OUTPUT' }
    const serialized = JSON.stringify(content)
    const record = (response: unknown) => request('google-generative-language', {
      contents: [{ role: 'user', parts: [{ functionResponse: { id: 'color', response } }] }],
    })
    expect(nativeToolResultContent(record(content), 'color')).toBe(content)
    expect(nativeToolResultContent(record(serialized), 'color')).toBe(serialized)
    expect(nativeToolResult(record(content), 'color')).toBe(serialized)
    expect(nativeToolResult(record(serialized), 'color')).toBe(serialized)
  })

  it('keeps native text blocks distinct from a scalar string that imitates their JSON', () => {
    const content = [{ type: 'text', text: 'NATIVE_OUTPUT' }]
    const serialized = JSON.stringify(content)
    const record = (value: unknown) => request('anthropic-messages', {
      messages: [{ role: 'user', content: [{ type: 'tool_result', tool_use_id: 'color', content: value }] }],
    })
    expect(nativeToolResultContent(record(content), 'color')).toBe(content)
    expect(nativeToolResultContent(record(serialized), 'color')).toBe(serialized)
    expect(nativeToolResult(record(content), 'color')).toBe(serialized)
    expect(nativeToolResult(record(serialized), 'color')).toBe(serialized)
  })

  it.each([
    [{ output: 'ACTUAL_RESULT', zero: 0, disabled: false, empty: '' }, '{"output":"ACTUAL_RESULT","zero":0,"disabled":false,"empty":""}'],
    [false, 'false'],
    [0, '0'],
    ['', ''],
  ])('reads the exact Google response for the original model call ID: %j', (response, expected) => {
    const record = request('google-generative-language', { contents: [
      { role: 'model', parts: [{ functionCall: { id: 'color', name: 'read_file', args: { response: 'ARGUMENT_ONLY' } } }] },
      { role: 'user', parts: [{ functionResponse: { id: 'other', response: 'OTHER_ONLY' } }, { functionResponse: { id: 'color', name: 'read_file', response } }] },
    ] })
    expect(nativeToolResult(record, 'color')).toBe(expected)
  })

  it('rejects ambiguous and absent Google responses without matching a tool name', () => {
    const result = { functionResponse: { id: 'color', name: 'read_file', response: { output: 'Red' } } }
    expect(() => nativeToolResult(request('google-generative-language', { contents: [{ role: 'user', parts: [result, result] }] }), 'color')).toThrow('contains 2 results')
    expect(() => nativeToolResult(request('google-generative-language', { contents: [{ role: 'user', parts: [result] }] }), 'other')).toThrow('contains 0 results')
    expect(() => nativeToolResult(request('google-generative-language', { contents: [{ role: 'user', parts: [{ functionResponse: { id: 'color' } }] }] }), 'color')).toThrow('has no content')
  })

  it('reads only a Chat Completions tool result', () => {
    const record = request('openai-chat-completions', {
      messages: [
        { role: 'assistant', tool_calls: [{ id: 'color', function: { arguments: '{"options":["Blue","Green"]}' } }] },
        { role: 'tool', tool_call_id: 'color', content: '{"answer":"Green"}' },
      ],
    })
    expect(nativeToolResult(record, 'color')).toBe('{"answer":"Green"}')
    expect(nativeToolResult(record, 'color')).not.toContain('Blue')
  })

  it('reads only a Responses function output', () => {
    const record = request('openai-responses', {
      input: [
        { type: 'function_call', call_id: 'color', arguments: '{"options":["Blue","Red"]}' },
        { type: 'function_call_output', call_id: 'color', output: '{"answer":"Red"}' },
      ],
    })
    expect(nativeToolResult(record, 'color')).toBe('{"answer":"Red"}')
  })

  it('reads the actual Responses custom tool output from the approved shell command', () => {
    const output = [
      { type: 'input_text', text: 'Script completed\nWall time 0.2 seconds\nOutput:\n' },
      { type: 'input_text', text: 'NATIVECONTROL42\n' },
    ]
    const record = request('openai-responses', {
      input: [
        {
          type: 'custom_tool_call',
          status: 'completed',
          call_id: 'native-control-permission',
          name: 'exec',
          input: 'const result = await tools.exec_command({cmd: "printf NATIVECONTROL%s \\\"$((40 + 2))\\\""}); text(result.output)',
        },
        {
          type: 'custom_tool_call_output',
          id: 'ctco_01a0f52a-5fb3-7422-9132-d9065b1dc98b',
          call_id: 'native-control-permission',
          output,
        },
      ],
    })
    expect(nativeToolResult(record, 'native-control-permission')).toBe(JSON.stringify(output))
    expect(nativeToolResult(record, 'native-control-permission')).toContain('NATIVECONTROL42')
    expect(nativeToolResult(record, 'native-control-permission')).not.toContain('$((40 + 2))')
    expect(nativeToolResult(record, 'native-control-permission')).not.toContain('tools.exec_command')
  })

  it('selects only the requested custom result', () => {
    const record = request('openai-responses', {
      input: [
        { type: 'function_call', call_id: 'color', arguments: 'ARGUMENT_ONLY_BLUE' },
        { type: 'custom_tool_call', call_id: 'color', input: 'CUSTOM_ARGUMENT_ONLY_GREEN' },
        { type: 'custom_tool_call_output', call_id: 'other', output: 'WRONG_CALL_ONLY_ORANGE' },
        { type: 'custom_tool_call_output', call_id: 'color', output: 'SELECTED_RED' },
      ],
    })
    expect(nativeToolResult(record, 'color')).toBe('SELECTED_RED')
  })

  it.each([
    ['function_call_output', 'custom_tool_call_output'],
    ['custom_tool_call_output', 'function_call_output'],
    ['custom_tool_call_output', 'custom_tool_call_output'],
  ])('rejects duplicate Responses results across %s and %s', (firstType, secondType) => {
    const record = request('openai-responses', {
      input: [
        { type: firstType, call_id: 'color', output: 'Red' },
        { type: secondType, call_id: 'color', output: 'Blue' },
      ],
    })
    expect(() => nativeToolResult(record, 'color')).toThrow('contains 2 results')
  })

  it.each(['function_call_output', 'custom_tool_call_output'])('rejects a mismatched Responses %s call ID', (type) => {
    const record = request('openai-responses', { input: [{ type, call_id: 'other', output: 'Red' }] })
    expect(() => nativeToolResult(record, 'color')).toThrow('contains 0 results')
  })

  for (const type of ['function_call_output', 'custom_tool_call_output']) {
    it.each([
      ['', ''],
      [0, '0'],
      [false, 'false'],
      [[], '[]'],
    ])(`preserves empty and falsy Responses ${type} output %j`, (output, expected) => {
      const record = request('openai-responses', { input: [{ type, call_id: 'color', output }] })
      expect(nativeToolResult(record, 'color')).toBe(expected)
    })

    it.each([undefined, null])(`rejects absent Responses ${type} output %j`, (output) => {
      const record = request('openai-responses', { input: [{ type, call_id: 'color', output }] })
      expect(() => nativeToolResult(record, 'color')).toThrow('has no content')
    })
  }

  it('reads only an Anthropic tool result', () => {
    const record = request('anthropic-messages', {
      messages: [
        { role: 'assistant', content: [{ type: 'tool_use', id: 'color', input: { options: ['Blue', 'Green'] } }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'color', content: [{ type: 'text', text: 'Green' }] }] },
      ],
    })
    expect(nativeToolResult(record, 'color')).toContain('Green')
    expect(nativeToolResult(record, 'color')).not.toContain('Blue')
  })

  it.each(['assistant', 'system', undefined])('rejects an Anthropic result outside the user role: %j', (role) => {
    const record = request('anthropic-messages', {
      messages: [{ role, content: [{ type: 'tool_result', tool_use_id: 'color', content: 'WRONG_ROLE_ONLY' }] }],
    })
    expect(() => nativeToolResult(record, 'color')).toThrow('contains 0 results')
  })

  it('keeps the exact Anthropic user result when another role repeats its call ID', () => {
    const content = [{ type: 'text', text: 'ACTUAL_USER_RESULT' }]
    const record = request('anthropic-messages', {
      messages: [
        { role: 'assistant', content: [{ type: 'tool_result', tool_use_id: 'color', content: 'WRONG_ROLE_ONLY' }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'color', content }] },
      ],
    })
    expect(nativeToolResult(record, 'color')).toBe(JSON.stringify(content))
  })

  it('rejects invalid native results', () => {
    const record = request('openai-chat-completions', {
      messages: [{ role: 'tool', tool_call_id: 'color', content: 'Green' }],
    })
    expect(() => nativeToolResult(record, 'other')).toThrow('contains 0 results')
    expect(() => nativeToolResult(undefined, 'color')).toThrow('No native model request')
    const duplicate = request('openai-chat-completions', {
      messages: [
        { role: 'tool', tool_call_id: 'color', content: 'Green' },
        { role: 'tool', tool_call_id: 'color', content: 'Blue' },
      ],
    })
    expect(() => nativeToolResult(duplicate, 'color')).toThrow('contains 2 results')
  })

  it('rejects a result without content or an unsupported protocol', () => {
    expect(() => nativeToolResult(request('openai-chat-completions', {
      messages: [{ role: 'tool', tool_call_id: 'color' }],
    }), 'color')).toThrow('has no content')
    expect(() => nativeToolResult(request('aws-event-stream', {}), 'color')).toThrow('unavailable')
  })
})
