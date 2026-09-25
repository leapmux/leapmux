import { describe, expect, it, vi } from 'vitest'
import { acpDividerReader, acpResultDivider, acpStopReasonDivider } from './resultDivider'

// A wrapped turn end has to label the same way an unwrapped one does. The
// classifier and this renderer read `stopReason` through one shared unwrap, so
// a row that classifies as `result_divider` always produces a model here.
describe('acpResultDivider', () => {
  it('labels a wrapped turn end', () => {
    expect(acpResultDivider({
      id: 'msg-1',
      role: 'result',
      content: { stopReason: 'end_turn', usage: { totalTokens: 100 } },
    })).toEqual({ label: 'Turn ended' })
  })

  it('qualifies a wrapped non-end_turn stop reason', () => {
    expect(acpResultDivider({ role: 'result', content: { stopReason: 'max_tokens' } }))
      .toEqual({ label: 'Turn ended (max_tokens)' })
  })

  it('reports a wrapped cancel as interrupted', () => {
    expect(acpResultDivider({ role: 'result', content: { stopReason: 'cancelled' } }))
      .toEqual({ label: 'Turn interrupted' })
  })
})

// A provider that ends a turn it started by itself states the protocol's own words,
// so the divider reads the same as the divider of a prompt.
describe('acpStopReasonDivider', () => {
  it.each([
    ['end_turn', 'Turn ended'],
    ['max_tokens', 'Turn ended (max_tokens)'],
    ['cancelled', 'Turn interrupted'],
    ['', 'Turn ended'],
  ])('labels the stop reason %j', (reason, label) => {
    expect(acpStopReasonDivider(reason)).toEqual({ label })
  })
})

describe('acpDividerReader', () => {
  const agentTurnEnd = (parent: Record<string, unknown>) => parent.method === 'vendor/end_turn' ? String(parent.reason ?? '') : undefined

  it('reads the provider\'s own turn end, and the prompt response otherwise', () => {
    const read = acpDividerReader(agentTurnEnd)
    expect(read({ method: 'vendor/end_turn', reason: 'max_tokens' })).toEqual({ label: 'Turn ended (max_tokens)' })
    expect(read({ stopReason: 'cancelled' })).toEqual({ label: 'Turn interrupted' })
    expect(read('not an object')).toBeNull()
  })

  // The hook answers undefined for "not a turn end" and the empty string for a turn
  // end that states no reason. The wrapped `cancelled` below is what a truthiness
  // test would fall through to, so the label shows which of the two answered.
  it('reads an empty stop reason from the hook as a plain end, not as no answer', () => {
    const read = acpDividerReader(() => '')
    expect(read({ role: 'result', content: { stopReason: 'cancelled' } })).toEqual({ label: 'Turn ended' })
  })

  it('asks the hook only about an object', () => {
    const agentTurnEnd = vi.fn(() => 'end_turn')
    const read = acpDividerReader(agentTurnEnd)
    expect(read(null)).toBeNull()
    expect(read('end_turn')).toBeNull()
    expect(read([{ stopReason: 'end_turn' }])).toBeNull()
    expect(agentTurnEnd).not.toHaveBeenCalled()
  })

  it('reads the prompt response alone for a provider with no turn end of its own', () => {
    expect(acpDividerReader()({ stopReason: 'end_turn' })).toEqual({ label: 'Turn ended' })
    expect(acpDividerReader()({ method: 'vendor/end_turn', reason: 'x' })).toEqual({ label: 'Turn ended' })
  })
})
