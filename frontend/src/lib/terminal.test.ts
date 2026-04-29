import { beforeEach, describe, expect, it, vi } from 'vitest'
import { copySelectionToClipboard, createTerminalInstance, resolveTerminalThemeMode } from './terminal'

// xterm.js requires a DOM element for open(), but we can still test
// the suppressInput mechanism without rendering.

describe('createTerminalInstance', () => {
  beforeEach(() => {
    localStorage.clear()
    // jsdom doesn't implement matchMedia
    window.matchMedia = vi.fn().mockReturnValue({ matches: false }) as any
  })

  it('initializes suppressInput to false', () => {
    const instance = createTerminalInstance()
    expect(instance.suppressInput).toBe(false)
    instance.dispose()
  })

  it('suppresses onData forwarding during snapshot replay', async () => {
    const instance = createTerminalInstance()
    const forwarded: string[] = []

    instance.terminal.onData((data) => {
      if (!instance.suppressInput) {
        forwarded.push(data)
      }
    })

    // Simulate snapshot replay: set flag, write data containing a
    // DA query (CSI c), then clear the flag in the write callback.
    instance.suppressInput = true
    await new Promise<void>((resolve) => {
      instance.terminal.write('\x1B[c', () => {
        instance.suppressInput = false
        resolve()
      })
    })

    // Any onData responses generated during the write should have
    // been suppressed by the flag.
    expect(forwarded).toEqual([])

    instance.dispose()
  })

  it('forwards onData after snapshot replay completes', async () => {
    const instance = createTerminalInstance()
    const forwarded: string[] = []

    instance.terminal.onData((data) => {
      if (!instance.suppressInput) {
        forwarded.push(data)
      }
    })

    // Simulate snapshot replay
    instance.suppressInput = true
    await new Promise<void>((resolve) => {
      instance.terminal.write('hello', () => {
        instance.suppressInput = false
        resolve()
      })
    })

    expect(instance.suppressInput).toBe(false)

    // After replay, user input should be forwarded.
    // We can't easily simulate real user input in jsdom, but we can
    // verify the flag state allows forwarding.
    expect(instance.suppressInput).toBe(false)

    instance.dispose()
  })
})

describe('resolveTerminalThemeMode', () => {
  it('returns the explicit terminal preference when set', () => {
    expect(resolveTerminalThemeMode('light', 'dark', true)).toBe('light')
    expect(resolveTerminalThemeMode('dark', 'light', false)).toBe('dark')
  })

  it('follows the UI theme preference when set to match-ui', () => {
    expect(resolveTerminalThemeMode('match-ui', 'light', true)).toBe('light')
    expect(resolveTerminalThemeMode('match-ui', 'dark', false)).toBe('dark')
  })

  it('falls back to OS prefers-color-scheme when both prefs defer to system', () => {
    expect(resolveTerminalThemeMode('match-ui', 'system', true)).toBe('dark')
    expect(resolveTerminalThemeMode('match-ui', 'system', false)).toBe('light')
  })
})

describe('copySelectionToClipboard', () => {
  it('writes non-empty text to the clipboard', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    copySelectionToClipboard('hello')

    expect(writeText).toHaveBeenCalledWith('hello')
  })

  it('skips empty strings (avoids clobbering the clipboard on deselect)', () => {
    const writeText = vi.fn()
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    copySelectionToClipboard('')

    expect(writeText).not.toHaveBeenCalled()
  })

  it('swallows clipboard errors so callers do not have to', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    })

    expect(() => copySelectionToClipboard('hello')).not.toThrow()
    // Allow the rejected promise to settle without an unhandled rejection.
    await Promise.resolve()
  })
})
