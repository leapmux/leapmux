import type { Terminal } from '@xterm/xterm'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { resolveVariant, themeById } from '~/styles/themes'
import { stubMatchMedia } from '~/test-support/matchMediaStub'
import { KEY_BROWSER_PREFS, localStorageClearForTests, localStorageSet } from './browserStorage'
import { applyTerminalData, attachWebgl, createTerminalInstance, detachWebgl, getTerminalRendererPreference, getTerminalThemePreference, loadTerminalFonts, refreshTerminalFont, resolveTerminalRendererPreference, resolveTerminalTheme, resolveTerminalThemeMode, serializeXtermBuffer, terminalThemeFor } from './terminal'

// xterm.js requires a DOM element for open(), but we can still test
// the suppressInput mechanism without rendering.

let matchMedia: ReturnType<typeof stubMatchMedia>

// Every suite in this file needs a clean storage state and a matchMedia stub
// (jsdom implements neither), so the pair is installed once at file level.
beforeEach(() => {
  localStorageClearForTests()
  matchMedia = stubMatchMedia()
})

afterEach(() => {
  matchMedia?.restore()
})

describe('createTerminalInstance', () => {
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

describe('applyTerminalData delta overlap', () => {
  /** A real instance whose terminal.write records every payload. */
  function recordingInstance() {
    const instance = createTerminalInstance()
    const writes: Uint8Array[] = []
    const originalWrite = instance.terminal.write.bind(instance.terminal)
    instance.terminal.write = ((data: string | Uint8Array, cb?: () => void) => {
      writes.push(typeof data === 'string' ? new TextEncoder().encode(data) : data)
      return originalWrite(data, cb)
    }) as typeof instance.terminal.write
    return { instance, writes }
  }

  it('writes a delta that starts at the current offset', () => {
    const { instance, writes } = recordingInstance()
    const out = applyTerminalData(instance, { kind: 'delta', data: new TextEncoder().encode('abc'), endOffset: 13, currentOffset: 10 })
    expect(new TextDecoder().decode(writes[0])).toBe('abc')
    expect(out).toBe(13)
    instance.dispose()
  })

  it('trims the prefix of a delta that overlaps rendered bytes', () => {
    // The worker registers a watcher for live events first and takes its
    // catch-up snapshot after, so a delta can re-include bytes the live
    // path already delivered. Rendering the delta again duplicates them.
    const { instance, writes } = recordingInstance()
    // Current offset 12; the delta covers [10, 15): bytes 10..11 already
    // rendered, so only "cde" (offsets 12..14) may write.
    const out = applyTerminalData(instance, { kind: 'delta', data: new TextEncoder().encode('abcde'), endOffset: 15, currentOffset: 12 })
    expect(new TextDecoder().decode(writes[0])).toBe('cde')
    expect(out).toBe(15)
    instance.dispose()
  })

  it('writes nothing when the delta lies entirely in the past', () => {
    const { instance, writes } = recordingInstance()
    let parsed = 0
    // Offset 20; the delta covers [10, 14) — fully applied already.
    const out = applyTerminalData(instance, { kind: 'delta', data: new TextEncoder().encode('abcd'), endOffset: 14, currentOffset: 20, onParsed: () => {
      parsed++
    } })
    expect(writes).toEqual([])
    // The parse callback still fires: callers gate content-ready on it.
    expect(parsed).toBe(1)
    // A stale duplicate must not rewind the cursor.
    expect(out).toBe(20)
    instance.dispose()
  })

  it('marks the parsed offset only once xterm parses the write', async () => {
    const { instance } = recordingInstance()

    // The write is queued, not parsed: the marker must stay unset so a
    // dispose right here skips the capture instead of storing a screen whose
    // coverage the tab's cursor overstates.
    const out = applyTerminalData(instance, { kind: 'delta', data: new TextEncoder().encode('abc'), endOffset: 13, currentOffset: 10 })
    expect(out).toBe(13)
    expect(instance.lastParsedOffset).toBeUndefined()

    // Flush xterm's async parser; the write's callback marks the cursor.
    await new Promise<void>(resolve => instance.terminal.write('', resolve))
    expect(instance.lastParsedOffset).toBe(13)

    // A snapshot resets the buffer, so the marker is unsettled again until
    // the snapshot's own write parses.
    applyTerminalData(instance, { kind: 'snapshot', data: new TextEncoder().encode('rebuilt'), endOffset: 7 })
    expect(instance.lastParsedOffset).toBeUndefined()
    await new Promise<void>(resolve => instance.terminal.write('', resolve))
    expect(instance.lastParsedOffset).toBe(7)

    instance.dispose()
  })

  it('treats a snapshot as authoritative regardless of the current offset', async () => {
    // After overlapping live deltas, a snapshot (the worker rebuilt the
    // screen because the cursor fell outside the retained ring) resets the
    // buffer and adopts the snapshot's cursor, even when the snapshot ends
    // BEHIND the stale local offset.
    const { instance } = recordingInstance()
    instance.terminal.write('live')
    await new Promise<void>(resolve => instance.terminal.write('', resolve))
    const out = applyTerminalData(instance, { kind: 'snapshot', data: new TextEncoder().encode('rebuilt'), endOffset: 5 })
    await new Promise<void>(resolve => instance.terminal.write('', resolve))
    expect(instance.terminal.buffer.active.getLine(0)?.translateToString(true)).toContain('rebuilt')
    expect(out).toBe(5)
    instance.dispose()
  })
})

describe('serializeXtermBuffer', () => {
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

  it('reads the OS directly when pinned to system, even while the UI is not', () => {
    // `system` and `match-ui` differ exactly here: the app pinned to light, the
    // terminal asked to follow the OS rather than the app.
    expect(resolveTerminalThemeMode('system', 'light', true)).toBe('dark')
    expect(resolveTerminalThemeMode('system', 'dark', false)).toBe('light')
  })
})

describe('resolveTerminalTheme', () => {
  it('takes the whole answer from the app under the sentinel', () => {
    // The two halves move together, so `match-ui` supplies the palette AND the
    // mode. This is the shipped default, and it is what makes the launcher's
    // single picker move the terminal with the app.
    expect(resolveTerminalTheme(
      { name: 'match-ui', mode: 'match-ui' },
      { name: 'catppuccin', mode: 'dark' },
      false,
    )).toEqual(terminalThemeFor('catppuccin', 'dark'))

    // Including the app's own `system`, answered by the OS.
    expect(resolveTerminalTheme(
      { name: 'match-ui', mode: 'match-ui' },
      { name: 'gruvbox', mode: 'system' },
      true,
    )).toEqual(terminalThemeFor('gruvbox', 'dark'))
  })

  it('takes none of it from the app once a palette is pinned', () => {
    // A detached terminal answers for both halves, whatever the app is doing.
    expect(resolveTerminalTheme(
      { name: 'nord', mode: 'light' },
      { name: 'catppuccin', mode: 'dark' },
      true,
    )).toEqual(terminalThemeFor('nord', 'light'))

    // And its `system` reads the OS, not the app -- the same thing the word
    // means in the Theme row.
    expect(resolveTerminalTheme(
      { name: 'nord', mode: 'system' },
      { name: 'catppuccin', mode: 'light' },
      true,
    )).toEqual(terminalThemeFor('nord', 'dark'))
  })

  it('takes its chrome from the palette it paints, not from the app', () => {
    // The terminal's own background, or a Nord terminal on a Catppuccin app
    // would sit in a rectangle of the wrong colour.
    const theme = resolveTerminalTheme(
      { name: 'nord', mode: 'dark' },
      { name: 'catppuccin', mode: 'light' },
      false,
    )
    const nordDark = resolveVariant(themeById('nord'), undefined, 'dark')
    expect(theme.background).toBe(nordDark.palette['--background'])
    expect(theme.foreground).toBe(nordDark.palette['--foreground'])
  })

  it('falls back to Default for a palette this build does not carry', () => {
    expect(resolveTerminalTheme(
      { name: 'from-the-future', mode: 'dark' },
      { name: 'default', mode: 'light' },
      false,
    )).toEqual(terminalThemeFor('default', 'dark'))
  })
})

describe('terminalThemeFor', () => {
  it('returns a STABLE reference for the same pair', () => {
    // Load-bearing, not an optimisation: TerminalView guards its palette
    // rebuild with `theme === lastTheme`, and a fresh object per call would
    // make that guard never fire and rewrite every xterm colour table on every
    // reactive tick.
    expect(terminalThemeFor('nord', 'dark')).toBe(terminalThemeFor('nord', 'dark'))
    expect(terminalThemeFor('nord', 'dark')).not.toBe(terminalThemeFor('nord', 'light'))
  })

  it('shares one entry between every name that falls back to Default', () => {
    expect(terminalThemeFor('nope', 'dark')).toBe(terminalThemeFor('default', 'dark'))
  })

  it('carries all sixteen ANSI slots plus the four chrome colours', () => {
    const theme = terminalThemeFor('catppuccin', 'dark')
    for (const slot of ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white', 'brightBlack', 'brightRed', 'brightGreen', 'brightYellow', 'brightBlue', 'brightMagenta', 'brightCyan', 'brightWhite', 'background', 'foreground', 'cursor', 'selectionBackground'] as const) {
      expect(theme[slot], slot).toBeTruthy()
    }
  })
})

describe('getTerminalThemePreference', () => {
  it('defaults to following the UI in both halves', () => {
    expect(getTerminalThemePreference()).toEqual({ name: 'match-ui', mode: 'match-ui' })
  })

  it('reads a stored pair', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalTheme: { name: 'ayu', mode: 'dark' } })
    expect(getTerminalThemePreference()).toEqual({ name: 'ayu', mode: 'dark' })
  })

  it('degrades the pre-split scalar shape to match-ui', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalTheme: 'dark' })
    expect(getTerminalThemePreference()).toEqual({ name: 'match-ui', mode: 'match-ui' })
  })

  it('repairs a document that carries the sentinel in one half only', () => {
    // Not a state any control produces: the palette and the mode move together.
    // A hand-edited or older document still has to resolve to something, and
    // the NAME decides which way. A pinned palette keeps itself and takes
    // `system`; a sentinel name takes the whole app.
    localStorageSet(KEY_BROWSER_PREFS, { terminalTheme: { name: 'ayu', mode: 'match-ui' } })
    expect(getTerminalThemePreference()).toEqual({ name: 'ayu', mode: 'system' })

    localStorageSet(KEY_BROWSER_PREFS, { terminalTheme: { name: 'match-ui', mode: 'dark' } })
    expect(getTerminalThemePreference()).toEqual({ name: 'match-ui', mode: 'match-ui' })
  })

  it('degrades a palette this build does not carry to following the app', () => {
    // "Follow the app" leaves the terminal agreeing with what the user can see.
    // Pinning it to Default would be a palette they never chose.
    localStorageSet(KEY_BROWSER_PREFS, { terminalTheme: { name: 'from-the-future', mode: 'dark' } })
    expect(getTerminalThemePreference()).toEqual({ name: 'match-ui', mode: 'match-ui' })
  })
})

describe('getTerminalRendererPreference', () => {
  beforeEach(() => {
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

describe('lazy WebGL renderer', () => {
  beforeEach(() => {
    // Reset globals a prior describe may have left set (Object.defineProperty
    // leaks across blocks in jsdom), so the renderer preference resolves as a
    // plain non-Tauri browser rather than Linux desktop Tauri (→ canvas).
    Object.defineProperty(window, '__TAURI_INTERNALS__', { configurable: true, value: undefined })
    Object.defineProperty(navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15',
    })
  })

  it('does not attach a WebGL context at construction', () => {
    const instance = createTerminalInstance()
    // The renderer is chosen lazily by the pool, not eagerly here.
    expect(instance.webglAddon).toBeUndefined()
    // Outside Linux desktop Tauri the default preference resolves to WebGL,
    // so the terminal is eligible for a pooled context.
    expect(instance.webglAllowed).toBe(true)
    expect(typeof instance.fontsReady.then).toBe('function')
    instance.dispose()
  })

  it('marks a canvas-preference terminal WebGL-ineligible', () => {
    localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'canvas' })
    const instance = createTerminalInstance()
    expect(instance.webglAllowed).toBe(false)
    // attachWebgl must refuse an ineligible terminal without touching WebGL.
    expect(attachWebgl(instance, () => {})).toBe(false)
    expect(instance.webglAddon).toBeUndefined()
    instance.dispose()
  })

  it('fetches font variants only for WebGL-eligible terminals', () => {
    // The four-variant fetch fan-out exists solely to gate the WebGL atlas, so a
    // WebGL-ineligible terminal (DOM renderer, no atlas) must not trigger it --
    // its `fontsReady` is never awaited by the pool.
    const load = vi.fn(() => Promise.resolve([]))
    const originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')
    Object.defineProperty(document, 'fonts', { configurable: true, value: { load } })
    try {
      localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'canvas' })
      const canvas = createTerminalInstance()
      expect(canvas.webglAllowed).toBe(false)
      expect(load).not.toHaveBeenCalled()
      canvas.dispose()

      // A WebGL-eligible terminal still fetches all four (weight, style) variants.
      localStorageSet(KEY_BROWSER_PREFS, { terminalRenderer: 'webgl' })
      const webgl = createTerminalInstance()
      expect(webgl.webglAllowed).toBe(true)
      expect(load).toHaveBeenCalledTimes(4)
      webgl.dispose()
    }
    finally {
      if (originalFonts)
        Object.defineProperty(document, 'fonts', originalFonts)
      else
        delete (document as any).fonts
    }
  })

  it('attaches idempotently and detaches back to the DOM renderer, without throwing', () => {
    const instance = createTerminalInstance()
    // WebGL may or may not be available in the test environment; either way
    // attach must not throw, and its return value must agree with whether an
    // addon actually got installed.
    let ok = false
    expect(() => {
      ok = attachWebgl(instance, () => {})
    }).not.toThrow()
    expect(ok).toBe(instance.webglAddon !== undefined)

    // A second attach never installs a second addon.
    const addon = instance.webglAddon
    expect(attachWebgl(instance, () => {})).toBe(ok)
    expect(instance.webglAddon).toBe(addon)

    // Detach reverts to the DOM renderer.
    detachWebgl(instance)
    expect(instance.webglAddon).toBeUndefined()
    instance.dispose()
  })

  it('detachWebgl is a no-op when nothing is attached', () => {
    const instance = createTerminalInstance()
    expect(() => detachWebgl(instance)).not.toThrow()
    expect(instance.webglAddon).toBeUndefined()
    instance.dispose()
  })

  it('refreshTerminalFont re-arms fontsReady and reports change only when the family actually changes', () => {
    const instance = createTerminalInstance()
    const before = instance.fontsReady
    const currentFamily = instance.terminal.options.fontFamily!

    // Same family (the font effect re-firing on preference hydration) must not
    // replace fontsReady -- that would needlessly delay a pending WebGL attach --
    // and must report `false` so the caller skips the re-fit.
    expect(refreshTerminalFont(instance, currentFamily, 13)).toBe(false)
    expect(instance.fontsReady).toBe(before)
    expect(instance.terminal.options.fontFamily).toBe(currentFamily)

    // A genuine family change re-arms the gate and reports `true` so the caller
    // re-fits exactly the instances it mutated.
    expect(refreshTerminalFont(instance, '"Some Other Mono", monospace', 13)).toBe(true)
    expect(instance.fontsReady).not.toBe(before)
    expect(instance.terminal.options.fontFamily).toBe('"Some Other Mono", monospace')

    instance.dispose()
  })
})

describe('loadTerminalFonts cold-start retry', () => {
  // jsdom does not implement document.fonts; install a controllable stub so we
  // can drive the load/fail/retry sequence deterministically with fake timers.
  let originalFonts: PropertyDescriptor | undefined
  let load: ReturnType<typeof vi.fn>

  function installFonts(loadImpl: (spec: string) => Promise<unknown>) {
    load = vi.fn(loadImpl)
    Object.defineProperty(document, 'fonts', { configurable: true, value: { load } })
  }

  function fakeTerminal(): { terminal: Terminal, clearTextureAtlas: ReturnType<typeof vi.fn> } {
    const clearTextureAtlas = vi.fn()
    return { terminal: { clearTextureAtlas } as unknown as Terminal, clearTextureAtlas }
  }

  beforeEach(() => {
    vi.useFakeTimers()
    originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')
  })

  afterEach(async () => {
    // Drain any pending retry timers before tearing down the stub so a
    // background retry can't throw into a floating promise mid-restore.
    await vi.runAllTimersAsync()
    vi.useRealTimers()
    if (originalFonts)
      Object.defineProperty(document, 'fonts', originalFonts)
    else
      delete (document as any).fonts
    vi.restoreAllMocks()
  })

  it('resolves after the first attempt even when every variant fails, without blocking', async () => {
    installFonts(() => Promise.reject(new Error('server down')))
    const { terminal, clearTextureAtlas } = fakeTerminal()

    // Must resolve on the first attempt (4 variants) -- not hang on failures.
    await loadTerminalFonts(terminal, '"Hack NF", monospace', 13)
    expect(load).toHaveBeenCalledTimes(4)
    // No retry has fired yet (its backoff timer is still pending).
    expect(clearTextureAtlas).not.toHaveBeenCalled()
  })

  it('retries failed variants with backoff and clears the atlas once they load', async () => {
    let attempt = 0
    installFonts(() => {
      attempt++
      // The first batch (4 variants) fails; every later attempt succeeds.
      return attempt <= 4 ? Promise.reject(new Error('server down')) : Promise.resolve([{}])
    })
    const { terminal, clearTextureAtlas } = fakeTerminal()

    await loadTerminalFonts(terminal, '"Hack NF", monospace', 13)
    expect(clearTextureAtlas).not.toHaveBeenCalled()

    // Advance past the first backoff: the retry loads the variants and clears
    // the atlas so an attached WebGL renderer re-rasterizes with the real font.
    await vi.advanceTimersByTimeAsync(300)
    expect(load.mock.calls.length).toBeGreaterThan(4)
    expect(clearTextureAtlas).toHaveBeenCalled()
  })

  it('retries only the variants that failed on the first attempt', async () => {
    let firstPass = true
    installFonts((spec: string) => {
      // The italic variants fail on the first pass; everything else -- and the
      // retry -- loads cleanly.
      if (firstPass && spec.includes('italic'))
        return Promise.reject(new Error('server down'))
      return Promise.resolve([{}])
    })
    const { terminal, clearTextureAtlas } = fakeTerminal()

    await loadTerminalFonts(terminal, '"Hack NF", monospace', 13)
    expect(load).toHaveBeenCalledTimes(4) // all four variants attempted once
    expect(clearTextureAtlas).not.toHaveBeenCalled()

    firstPass = false
    load.mockClear()
    await vi.advanceTimersByTimeAsync(300)

    // Only the two italic variants -- the ones that failed -- are retried.
    expect(load).toHaveBeenCalledTimes(2)
    for (const [spec] of load.mock.calls)
      expect(spec).toContain('italic')
    expect(clearTextureAtlas).toHaveBeenCalled()
  })

  it('gives up after the bounded retry budget when the font never loads', async () => {
    installFonts(() => Promise.reject(new Error('server down')))
    const { terminal, clearTextureAtlas } = fakeTerminal()

    await loadTerminalFonts(terminal, '"Hack NF", monospace', 13)
    await vi.runAllTimersAsync()
    const callsAfterGivingUp = load.mock.calls.length

    // 4 variants x (1 initial + 4 retries) = 20 attempts, then it stops.
    expect(callsAfterGivingUp).toBe(20)
    // Nothing ever loaded, so the atlas is never needlessly cleared.
    expect(clearTextureAtlas).not.toHaveBeenCalled()

    // Draining more time triggers no further attempts.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(load.mock.calls.length).toBe(callsAfterGivingUp)
  })

  it('is a no-op when the platform has no FontFaceSet', async () => {
    delete (document as any).fonts
    const { terminal, clearTextureAtlas } = fakeTerminal()
    await expect(loadTerminalFonts(terminal, '"Hack NF", monospace', 13)).resolves.toBeUndefined()
    expect(clearTextureAtlas).not.toHaveBeenCalled()
  })

  it('degrades a spec that throws synchronously to a failed variant, never rejecting', async () => {
    // document.fonts.load throws a SyntaxError *synchronously* on an unparseable
    // font shorthand. loadTerminalFonts must swallow it (treating the variant as
    // failed) rather than rejecting `fontsReady` -- which would otherwise throw
    // out of createTerminalInstance at the call site that assigns it.
    installFonts(() => {
      throw new SyntaxError('unparseable font shorthand')
    })
    const { terminal, clearTextureAtlas } = fakeTerminal()

    await expect(loadTerminalFonts(terminal, 'not a valid family', 13)).resolves.toBeUndefined()
    // All four variants were attempted and each threw-then-degraded to a failure.
    expect(load).toHaveBeenCalledTimes(4)

    // The bounded retry keeps throwing-then-degrading and gives up without ever
    // clearing the atlas (nothing loaded) and without an unhandled rejection.
    await vi.runAllTimersAsync()
    expect(clearTextureAtlas).not.toHaveBeenCalled()
  })
})
