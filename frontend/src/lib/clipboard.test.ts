import { describe, expect, it, vi } from 'vitest'
import { copyTextToClipboard } from './clipboard'

describe('copyTextToClipboard', () => {
  it('writes non-empty text to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    copyTextToClipboard('hello')

    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('skips empty strings (avoids clobbering the clipboard on deselect)', () => {
    const writeText = vi.fn()
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    copyTextToClipboard('')

    expect(writeText).not.toHaveBeenCalled()
  })

  it('swallows clipboard errors so callers do not have to', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    expect(() => copyTextToClipboard('hello')).not.toThrow()
    // Allow the rejected promise to settle without an unhandled rejection.
    await Promise.resolve()
  })
})
