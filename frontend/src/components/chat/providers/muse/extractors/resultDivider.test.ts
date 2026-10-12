import { describe, expect, it } from 'vitest'
import { museResultDivider } from './resultDivider'

describe('museResultDivider', () => {
  it('keeps an unknown native outcome visible without inventing an error', () => {
    const frame = { method: 'turn/completed', params: { terminal: 'futureFinal', durationMs: 0 } }
    expect(museResultDivider(frame)).toEqual({ label: 'Turn ended (0ms) — Unknown Muse outcome: futureFinal' })
    expect(frame.params.terminal).toBe('futureFinal')
  })

  it.each([
    ['completed', 'Turn ended — Native reason', undefined],
    ['cancelled', 'Turn interrupted — Native reason', undefined],
    ['failed', 'Turn failed — Native reason', true],
  ])('keeps the native %s outcome and cause', (terminal, label, isError) => {
    const result = museResultDivider({ method: 'turn/completed', params: { terminal, error: { message: 'Native reason' } } })
    expect(result?.label).toBe(label)
    expect(result?.isError).toBe(isError)
  })

  it.each([null, {}, { method: 'other' }])('ignores a frame with no native turn completion: %s', (frame) => {
    expect(museResultDivider(frame)).toBeNull()
  })
})
