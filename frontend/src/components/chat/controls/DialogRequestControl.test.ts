import { describe, expect, it } from 'vitest'
import { dialogTimeoutHint } from './DialogRequestControl'

/**
 * A dialog that resolves itself is one the reader must be able to see a deadline on,
 * so the sentence has to state a deadline the reader can act on.
 */
describe('dialogTimeoutHint', () => {
  it('states a sub-second deadline in milliseconds', () => {
    // Rounding to whole seconds read "Auto-resolves in 0s if no response." for every
    // deadline under 500 ms, which tells the reader the dialog already expired.
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm', timeoutMs: 400 }))
      .toBe('Auto-resolves in 400ms if no response.')
  })

  it('states a longer deadline in whole seconds', () => {
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm', timeoutMs: 30_000 }))
      .toBe('Auto-resolves in 30s if no response.')
  })

  it('states nothing for a dialog with no deadline', () => {
    expect(dialogTimeoutHint({ title: 'Continue?', variant: 'confirm' })).toBeNull()
  })
})
