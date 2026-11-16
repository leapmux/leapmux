import { describe, expect, it } from 'vitest'
import { claudeSafeguardResults } from './claudeSafeguards'

const request = {
  safeguards: [
    { type: 'dangerous_tool_use', classifier_context: {} },
  ],
}

describe('claudeSafeguardResults', () => {
  it('keeps verdicts on their exact calls and preserves an explicit empty reason', () => {
    const result = claudeSafeguardResults(request, {
      toolCalls: [
        { id: 'allow', name: 'Read' },
        { id: 'deny', name: 'Bash' },
      ],
      toolSafety: {
        decisions: {
          deny: { decision: 'deny', reason: '' },
        },
      },
    })
    expect(result).toEqual([
      {
        type: 'dangerous_tool_use',
        status: {
          type: 'available',
          tool_uses: {
            allow: { type: 'evaluated', outcome: 'not_flagged' },
            deny: { type: 'evaluated', outcome: 'flagged', explanation: '' },
          },
        },
      },
    ])
  })

  it('preserves prototype-like call IDs as own data properties', () => {
    const result = claudeSafeguardResults(request, {
      toolCalls: [
        { id: '__proto__', name: 'Read' },
        { id: '출력', name: 'Bash' },
      ],
    })
    expect(JSON.parse(JSON.stringify(result))).toEqual([
      {
        type: 'dangerous_tool_use',
        status: {
          type: 'available',
          tool_uses: JSON.parse('{"__proto__":{"type":"evaluated","outcome":"not_flagged"},"출력":{"type":"evaluated","outcome":"not_flagged"}}'),
        },
      },
    ])
  })

  it('returns an empty available result when the model makes no tool call', () => {
    expect(claudeSafeguardResults(request, {})).toEqual([
      {
        type: 'dangerous_tool_use',
        status: { type: 'available', tool_uses: {} },
      },
    ])
  })

  it.each([
    undefined,
    null,
    {},
    { safeguards: null },
    { safeguards: false },
    { safeguards: [] },
    {
      safeguards: [
        { type: 'another-check' },
      ],
    },
  ])('adds no verdict for an unrequested check: %j', (body) => {
    expect(claudeSafeguardResults(body, {})).toBeUndefined()
    expect(() => claudeSafeguardResults(body, { toolSafety: {} })).toThrow('requires Anthropic dangerous_tool_use')
  })

  it('rejects ambiguous call identities before emitting a verdict', () => {
    expect(() => claudeSafeguardResults(request, {
      toolCalls: [
        { id: 'same', name: 'Read' },
        { id: 'same', name: 'Bash' },
      ],
    })).toThrow('unique tool call IDs')
  })
})
