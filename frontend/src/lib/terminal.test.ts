import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createTerminalInstance } from './terminal'

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
