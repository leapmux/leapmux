import { describe, expect, it, vi } from 'vitest'
import { copyTextToClipboard } from './clipboard'

function stubClipboard(value: unknown) {
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value })
}

describe('copyTextToClipboard', () => {
  it('writes non-empty text to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard({ writeText })

    await expect(copyTextToClipboard('hello')).resolves.toBe(true)

    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('skips empty strings (avoids clobbering the clipboard on deselect)', async () => {
    const writeText = vi.fn()
    stubClipboard({ writeText })

    await expect(copyTextToClipboard('')).resolves.toBe(false)

    expect(writeText).not.toHaveBeenCalled()
  })

  it('swallows clipboard errors so callers do not have to', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    stubClipboard({ writeText })

    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
  })

  // A non-secure origin -- plain `http://` on a LAN -- exposes no
  // `navigator.clipboard` at all, so an unguarded `navigator.clipboard.writeText`
  // throws a TypeError rather than rejecting.
  it('reports failure when the platform exposes no clipboard', async () => {
    stubClipboard(undefined)

    await expect(copyTextToClipboard('hello')).resolves.toBe(false)
  })
})
