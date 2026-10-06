import { describe, expect, it } from 'vitest'
import { claudeUltracodeEnabled } from './ultracodeRequest'

const ON = 'Ultracode is on: use the native workflow policy.'
const OFF = 'Ultracode is off — the native standard policy applies again.'

function request(messages: unknown[], system?: unknown) {
  return { protocol: 'anthropic-messages' as const, body: { messages, ...(system === undefined ? {} : { system }) } }
}

describe('claudeUltracodeEnabled', () => {
  it('reads an enabled instruction before a later token reminder', () => {
    expect(claudeUltracodeEnabled(request([
      { role: 'system', content: [{ type: 'text', text: `Native environment.\n\n${ON}` }] },
      { role: 'user', content: 'Reply after reload.' },
      { role: 'system', content: [{ type: 'text', text: '<total_tokens>15000000 tokens left</total_tokens>' }] },
    ]))).toBe(true)
  })

  it('uses the later native disabled state after an older enabled instruction', () => {
    expect(claudeUltracodeEnabled(request([
      { role: 'system', content: ON },
      { role: 'assistant', content: 'The earlier turn ended.' },
      { role: 'system', content: OFF },
      { role: 'system', content: '<total_tokens>10000 tokens left</total_tokens>' },
    ]))).toBe(false)
  })

  it('uses the later native enabled state after an older disabled instruction', () => {
    expect(claudeUltracodeEnabled(request([
      { role: 'system', content: OFF },
      { role: 'system', content: ON },
    ]))).toBe(true)
    expect(claudeUltracodeEnabled(request([{ role: 'system', content: 'Ultracode is still on — use the native workflow policy.' }]))).toBe(true)
  })

  it('ignores matching user text assistant text tool schemas and tool results', () => {
    const native = request([
      { role: 'system', content: 'Native environment without Ultracode.' },
      { role: 'user', content: ON },
      { role: 'assistant', content: ON },
      { role: 'tool', content: ON },
      { role: 'system', content: [{ type: 'tool_result', text: ON }] },
    ])
    Object.assign(native.body, { tools: [{ description: ON }], output_config: { effort: 'xhigh' } })
    expect(claudeUltracodeEnabled(native)).toBe(false)
  })

  it('reads top-level system text before later native message states', () => {
    expect(claudeUltracodeEnabled(request([], [{ type: 'text', text: ON }]))).toBe(true)
    expect(claudeUltracodeEnabled(request([{ role: 'system', content: OFF }], ON))).toBe(false)
  })

  it('rejects absent empty and malformed native instructions', () => {
    for (const [body, error] of [
      [null, 'The Ultracode proof requires an actual native Claude model request.'],
      [[], 'The Ultracode proof requires an actual native Claude model request.'],
      [{}, 'The native Ultracode request contains no message array.'],
      [{ messages: null }, 'The native Ultracode request contains no message array.'],
      [{ messages: [] }, 'The native Ultracode request contains no system text.'],
      [{ messages: [null] }, 'The native Ultracode request contains an invalid message.'],
      [{ messages: [{ role: 'system', content: null }] }, 'The native Claude system instruction has invalid content.'],
      [{ messages: [{ role: 'system', content: [{ type: 'text' }] }] }, 'The native Claude system text block contains no text.'],
      [{ messages: [{ role: 'system', content: '' }] }, 'The native Ultracode request contains no system text.'],
    ] as const)
      expect(() => claudeUltracodeEnabled({ protocol: 'anthropic-messages', body }), JSON.stringify(body)).toThrow(error)
    expect(() => claudeUltracodeEnabled({ protocol: 'openai-responses', body: { system: ON, messages: [] } })).toThrow('native Claude')
  })
})
