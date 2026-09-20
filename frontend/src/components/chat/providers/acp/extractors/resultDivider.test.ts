import { describe, expect, it } from 'vitest'
import { acpResultDivider } from './resultDivider'

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
