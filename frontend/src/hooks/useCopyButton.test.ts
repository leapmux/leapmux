/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useCopyButton } from './useCopyButton'

const copyTextToClipboard = vi.hoisted(() => vi.fn(async (_text: string) => true))
vi.mock('~/lib/clipboard', () => ({ copyTextToClipboard }))

describe('useCopyButton', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    copyTextToClipboard.mockImplementation(async () => true)
  })

  it('flashes copied after a successful write', async () => {
    await createRoot(async (dispose) => {
      const { copied, copy } = useCopyButton(() => 'payload')
      expect(copied()).toBe(false)

      await copy()

      expect(copyTextToClipboard).toHaveBeenCalledWith('payload')
      expect(copied()).toBe(true)
      dispose()
    })
  })

  // The feedback has to track the WRITE, not the click. A non-secure origin
  // exposes no clipboard at all, and the previous version -- which called
  // `navigator.clipboard.writeText` inside a bare try/catch -- was one small
  // refactor away from flashing "Copied!" over a clipboard that never changed.
  it('does not flash copied when the write does not land', async () => {
    copyTextToClipboard.mockImplementation(async () => false)
    await createRoot(async (dispose) => {
      const { copied, copy } = useCopyButton(() => 'payload')

      await copy()

      expect(copied()).toBe(false)
      dispose()
    })
  })

  it('does not touch the clipboard when there is nothing to copy', async () => {
    await createRoot(async (dispose) => {
      const { copied, copy } = useCopyButton(() => undefined)

      await copy()

      expect(copyTextToClipboard).not.toHaveBeenCalled()
      expect(copied()).toBe(false)
      dispose()
    })
  })

  it('resets the flash after the timeout elapses', async () => {
    vi.useFakeTimers()
    try {
      await createRoot(async (dispose) => {
        const { copied, copy } = useCopyButton(() => 'payload')
        await copy()
        expect(copied()).toBe(true)

        vi.advanceTimersByTime(2000)

        expect(copied()).toBe(false)
        dispose()
      })
    }
    finally {
      vi.useRealTimers()
    }
  })
})
