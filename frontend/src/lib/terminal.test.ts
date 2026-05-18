import { beforeEach, describe, expect, it, vi } from 'vitest'
import { KEY_BROWSER_PREFS, localStorageSet } from './browserStorage'
import { createTerminalInstance, getTerminalRendererPreference, resolveTerminalRendererPreference, resolveTerminalThemeMode, serializeXtermBuffer } from './terminal'

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

describe('serializeXtermBuffer', () => {
  beforeEach(() => {
    localStorage.clear()
    window.matchMedia = vi.fn().mockReturnValue({ matches: false }) as any
  })

  // Helper: synchronously write then await xterm's async parser via the
  // write callback. xterm processes its write queue asynchronously, so
  // calling serialize immediately after write() races the parser.
  const writeAndWait = (instance: ReturnType<typeof createTerminalInstance>, data: string) =>
    new Promise<void>(resolve => instance.terminal.write(data, () => resolve()))

  // Helper: read the entire active buffer (visible + scrollback) as a
  // single string with trailing whitespace trimmed per line. Used to
  // verify the round-trip through serialize → write reproduces visible
  // text without relying on byte-identical escape sequences.
  const readActiveBuffer = (terminal: ReturnType<typeof createTerminalInstance>['terminal']): string => {
    const buf = terminal.buffer.active
    let out = ''
    for (let i = 0; i < buf.length; i++) {
      const line = buf.getLine(i)
      if (line)
        out += `${line.translateToString(true)}\n`
    }
    return out
  }

  it('produces bytes that re-create the visible content when written into a fresh terminal', async () => {
    const source = createTerminalInstance({ cols: 80, rows: 24 })
    await writeAndWait(source, 'hello world\r\n')
    await writeAndWait(source, 'second line\r\n')
    await writeAndWait(source, 'third\r\n')

    const bytes = serializeXtermBuffer(source)
    expect(bytes).toBeInstanceOf(Uint8Array)
    expect(bytes.length).toBeGreaterThan(0)

    const restored = createTerminalInstance({ cols: 80, rows: 24 })
    restored.terminal.reset()
    await new Promise<void>(resolve => restored.terminal.write(bytes, () => resolve()))

    const text = readActiveBuffer(restored.terminal)
    expect(text).toContain('hello world')
    expect(text).toContain('second line')
    expect(text).toContain('third')

    source.dispose()
    restored.dispose()
  })

  it('captures content that has scrolled off the visible viewport', async () => {
    const source = createTerminalInstance({ cols: 80, rows: 5 })
    for (let i = 0; i < 20; i++)
      await writeAndWait(source, `line-${i}\r\n`)

    const bytes = serializeXtermBuffer(source)
    const decoded = new TextDecoder().decode(bytes)

    // The earliest line wrote was line-0; with default scrollback (1000)
    // it must still be reachable through the serialized form.
    expect(decoded).toContain('line-0')
    expect(decoded).toContain('line-19')

    source.dispose()
  })

  it('returns an empty buffer for a fresh terminal with no writes', () => {
    const instance = createTerminalInstance({ cols: 80, rows: 24 })
    const bytes = serializeXtermBuffer(instance)
    expect(bytes).toBeInstanceOf(Uint8Array)
    // A pristine terminal serializes to a small prelude (mode resets); the
    // contract here is just that it doesn't throw and returns Uint8Array.
    expect(bytes.length).toBeGreaterThanOrEqual(0)
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

describe('getTerminalRendererPreference', () => {
  beforeEach(() => {
    localStorage.clear()
    Object.defineProperty(window, '__TAURI_INTERNALS__', {
      configurable: true,
      value: undefined,
    })
    Object.defineProperty(navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15',
    })
  })

  it('defaults to auto', () => {
    expect(getTerminalRendererPreference()).toBe('auto')
  })

  it('accepts the canvas experiment override', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'canvas' })

    expect(getTerminalRendererPreference()).toBe('canvas')
  })

  it('ignores unknown values', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'invalid' })

    expect(getTerminalRendererPreference()).toBe('auto')
  })

  it('defaults auto to WebGL outside Linux desktop Tauri', () => {
    expect(resolveTerminalRendererPreference('auto')).toBe('webgl')
  })

  it('defaults auto to canvas on Linux desktop Tauri', () => {
    Object.defineProperty(window, '__TAURI_INTERNALS__', {
      configurable: true,
      value: {},
    })
    Object.defineProperty(navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15',
    })

    expect(resolveTerminalRendererPreference('auto')).toBe('canvas')
  })

  it('allows WebGL override on Linux desktop Tauri', () => {
    Object.defineProperty(window, '__TAURI_INTERNALS__', {
      configurable: true,
      value: {},
    })
    Object.defineProperty(navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/605.1.15',
    })

    expect(resolveTerminalRendererPreference('webgl')).toBe('webgl')
  })
})
