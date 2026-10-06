import type { MockModelStep } from './mockModelScript'
import { describe, expect, it } from 'vitest'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { applyClaudeChildHandback, claudeDeliveredChildResponse, claudeToolResultText } from './claudeChildResponse'
import { claudeSubagentHandbackToolCall, claudeSubagentHandbackToolDefinition, readToolCall } from './providerToolCalls'

const nativeCatalog = { tools: [claudeSubagentHandbackToolDefinition()] }

function deliveredRequest(report: string, result: unknown = { success: true, message: 'Report delivered to your caller.' }) {
  return {
    ...nativeCatalog,
    messages: [
      { role: 'assistant', content: [{ type: 'tool_use', id: 'delivered-report', name: 'SubagentHandback', input: { message: report } }] },
      { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'delivered-report', content: [{ type: 'text', text: JSON.stringify(result) }] }] },
      { role: 'system', content: [{ type: 'text', text: 'The native agent catalog remains available.' }] },
    ],
  }
}

describe('claudeToolResultText', () => {
  it('reads a string content and the text of one text block', () => {
    expect(claudeToolResultText('Direct text.')).toBe('Direct text.')
    expect(claudeToolResultText('')).toBe('')
    expect(claudeToolResultText([{ type: 'text', text: 'Block text.' }])).toBe('Block text.')
  })

  it.each([
    [],
    [{ type: 'text', text: 'First.' }, { type: 'text', text: 'Second.' }],
    [{ type: 'image', source: {} }],
    [{ type: 'text', text: 7 }],
    ['Text.'],
    { type: 'text', text: 'Not in a list.' },
    null,
    undefined,
  ])('states no text for any other content: %j', (content) => {
    expect(claudeToolResultText(content)).toBeUndefined()
  })
})

describe('claudeDeliveredChildResponse', () => {
  it.each([
    { label: 'Unicode and whitespace', report: '  Exact report.\n실제 내용 🧪\t  ' },
    { label: 'large', report: 'Original report line.\n'.repeat(8_192) },
  ])('keeps the exact $label report and does not mutate the request', ({ report }) => {
    const body = deliveredRequest(report)
    const original = JSON.stringify(body)
    expect(claudeDeliveredChildResponse(body)).toEqual({ text: report })
    expect(JSON.stringify(body)).toBe(original)
  })

  it('accepts the native direct-string tool result without changing its report', () => {
    const body = deliveredRequest('The actual report.')
    const direct = { ...body, messages: [body.messages[0], { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'delivered-report', content: JSON.stringify({ success: true, message: 'Report delivered to your caller.' }) }] }] }
    expect(claudeDeliveredChildResponse(direct)).toEqual({ text: 'The actual report.' })
  })

  it.each([null, {}, { tools: [] }, { messages: [] }, { messages: null }])('rejects the absent or malformed child request: %j', (body) => {
    expect(claudeDeliveredChildResponse(body)).toBeUndefined()
  })

  it.each([
    { label: 'failed delivery', result: { success: false, message: 'The caller is no longer running.' } },
    { label: 'missing success', result: { message: 'Report delivered to your caller.' } },
    { label: 'unrelated success', result: { success: true, message: 'A different native operation completed.' } },
    { label: 'null result', result: null },
    { label: 'array result', result: [] },
    { label: 'zero result', result: 0 },
  ])('rejects the $label boundary', ({ result }) => {
    expect(claudeDeliveredChildResponse(deliveredRequest('Original report.', result))).toBeUndefined()
  })

  it.each(['', ' \n\t'])('rejects an empty original report: %j', (report) => {
    expect(claudeDeliveredChildResponse(deliveredRequest(report))).toBeUndefined()
  })

  it('rejects a result with a different native tool-use ID', () => {
    const body = deliveredRequest('The actual report.')
    const mismatched = { ...body, messages: [body.messages[0], { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'other-report', content: JSON.stringify({ success: true, message: 'Report delivered to your caller.' }) }] }] }
    expect(claudeDeliveredChildResponse(mismatched)).toBeUndefined()
  })

  it('rejects malformed JSON and explicit native tool failure', () => {
    const body = deliveredRequest('The actual report.')
    for (const result of [
      { type: 'tool_result', tool_use_id: 'delivered-report', content: '{not JSON' },
      { type: 'tool_result', tool_use_id: 'delivered-report', is_error: true, content: JSON.stringify({ success: true, message: 'Report delivered to your caller.' }) },
    ]) {
      expect(claudeDeliveredChildResponse({ ...body, messages: [body.messages[0], { role: 'user', content: [result] }] })).toBeUndefined()
    }
  })

  it('does not use an earlier handback when the latest user supplies new input', () => {
    const body = deliveredRequest('The actual report.')
    expect(claudeDeliveredChildResponse({ ...body, messages: [...body.messages, { role: 'user', content: 'Do the next actual task.' }] })).toBeUndefined()
  })

  it('rejects another native tool and ambiguous duplicate call IDs', () => {
    const body = deliveredRequest('The actual report.')
    const call = { type: 'tool_use', id: 'delivered-report', name: 'SubagentHandback', input: { message: 'The actual report.' } }
    for (const content of [[{ ...call, name: 'Read' }], [call, call]])
      expect(claudeDeliveredChildResponse({ ...body, messages: [{ role: 'assistant', content }, body.messages[1]] })).toBeUndefined()
  })

  it('rejects a root catalog and malformed intervening history', () => {
    const body = deliveredRequest('The actual report.')
    expect(claudeDeliveredChildResponse({ ...body, tools: [] })).toBeUndefined()
    expect(claudeDeliveredChildResponse({ ...body, messages: [body.messages[0], null, body.messages[1]] })).toBeUndefined()
    expect(claudeDeliveredChildResponse({ ...body, messages: [body.messages[0], { role: 'assistant', content: 'A later assistant turn.' }, body.messages[1]] })).toBeUndefined()
  })
})

describe('applyClaudeChildHandback', () => {
  it('does not repeat a handback after its matching native result confirms delivery', () => {
    const report = 'The exact original child report.\nKeep this text.'
    const body = {
      ...nativeCatalog,
      messages: [
        { role: 'assistant', content: [{ type: 'tool_use', id: 'delivered-report', name: 'SubagentHandback', input: { message: report } }] },
        { role: 'user', content: [{ type: 'tool_result', tool_use_id: 'delivered-report', content: [{ type: 'text', text: JSON.stringify({ success: true, message: 'Report delivered to your caller.' }) }] }] },
        { role: 'system', content: [{ type: 'text', text: 'The native agent catalog remains available.' }] },
      ],
    }
    const step = { text: report }
    expect(applyClaudeChildHandback(body, step, 'final-continuation')).toBe(step)
  })

  it('returns a new step and keeps the entire original report and response metadata', () => {
    const text = '  Original child report.\nSecond line: 실제 내용 🧪\t  '
    const step = Object.freeze({ text, reasoning: 'The original reasoning.', usage: { outputTokens: 0 }, stream: { chunkChars: 2, delayMs: 0 }, gate: 'held-child' })
    const result = applyClaudeChildHandback(nativeCatalog, step, 'native-response')
    expect(result).not.toBe(step)
    expect(result).toEqual({ ...step, toolCalls: [claudeSubagentHandbackToolCall('native-response-handback', text)] })
    expect(step).not.toHaveProperty('toolCalls')
    expect(result.stream).toBe(step.stream)
    expect(result.usage).toBe(step.usage)
  })

  it.each<MockModelStep>([
    { text: '' },
    { text: ' \n\t' },
    { reasoning: 'Reasoning without a final report.' },
    { error: { status: 400, message: 'The native model request failed.' } },
    { text: 'Read progress.', toolCalls: [readToolCall(AgentProvider.CLAUDE_CODE, 'read', '/private/source.txt')] },
    { toolCalls: [claudeSubagentHandbackToolCall('already-delivered', 'The existing report.')] },
  ])('preserves a nonfinal or existing native response: %j', (step) => {
    expect(applyClaudeChildHandback(nativeCatalog, step, 'response')).toBe(step)
  })

  it.each([null, {}, { tools: null }, { tools: [] }, { tools: ['SubagentHandback'] }, { tools: [{ type: 'function', function: { name: 'SubagentHandback' } }] }, { tools: [{ name: 'SubagentHandback' }] }, { tools: [{ name: 'SubagentHandback', input_schema: { type: 'object', properties: { message: { type: 'string' } }, required: ['message', 'recipient'], additionalProperties: false } }] }])('does not change a root or malformed native tool catalog: %j', (body) => {
    const step = { text: 'The original root response.' }
    expect(applyClaudeChildHandback(body, step, 'response')).toBe(step)
  })

  it('keeps an empty tool array valid and gives each native response a distinct call ID', () => {
    const step = { text: 'The original child report.', toolCalls: [] }
    expect(applyClaudeChildHandback(nativeCatalog, step, 'first').toolCalls?.[0]?.id).toBe('first-handback')
    expect(applyClaudeChildHandback(nativeCatalog, step, 'second').toolCalls?.[0]?.id).toBe('second-handback')
    expect(step.toolCalls).toEqual([])
  })

  it('keeps every character of a large original report', () => {
    const text = 'Original report line.\n'.repeat(8_192)
    const result = applyClaudeChildHandback(nativeCatalog, { text }, 'large-report')
    expect(result.text).toBe(text)
    expect(result.toolCalls?.[0]?.arguments?.message).toBe(text)
  })
})
