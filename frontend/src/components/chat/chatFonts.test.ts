import type { ChatFonts } from './chatFonts'
import { createRoot, createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { FONT_LOAD_MAX_RETRIES, FONT_LOAD_RETRY_BASE_MS } from '~/lib/fontFaces'
import { deferred } from '~/test-support/async'
import { CHAT_FONT_LOAD_LIMIT_MS, createChatFonts } from './chatFonts'

/** The four shorthands that the unit loads for `family`. */
function specsOf(family: string): string[] {
  return [
    `normal normal 1em ${family}`,
    `normal bold 1em ${family}`,
    `italic normal 1em ${family}`,
    `italic bold 1em ${family}`,
  ]
}

describe('createChatFonts', () => {
  // jsdom implements no `document.fonts`, so each case installs the API that it needs.
  let originalFonts: PropertyDescriptor | undefined
  let disposeRoot: (() => void) | undefined

  function installFonts(api: { check: (spec: string) => boolean, load: (spec: string) => Promise<unknown> }): void {
    Object.defineProperty(document, 'fonts', { configurable: true, value: api })
  }

  function mount(families: () => readonly string[]): ChatFonts {
    return createRoot((dispose) => {
      disposeRoot = dispose
      return createChatFonts(families)
    })
  }

  /** Let the settled promises of the loads run their callbacks. Works under fake timers. */
  async function flushLoads(): Promise<void> {
    await vi.advanceTimersByTimeAsync(0)
  }

  beforeEach(() => {
    vi.useFakeTimers()
    originalFonts = Object.getOwnPropertyDescriptor(document, 'fonts')
  })

  afterEach(() => {
    disposeRoot?.()
    disposeRoot = undefined
    vi.useRealTimers()
    if (originalFonts)
      Object.defineProperty(document, 'fonts', originalFonts)
    else
      delete (document as { fonts?: unknown }).fonts
  })

  it('counts the fonts as ready and loaded when the document has no font API', () => {
    delete (document as { fonts?: unknown }).fonts
    const fonts = mount(() => ['Sans', 'Mono'])
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(true)
  })

  it('loads nothing when every face is available', () => {
    const load = vi.fn(() => Promise.resolve())
    installFonts({
      check: () => true,
      load,
    })
    const fonts = mount(() => ['Sans', 'Mono'])
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(true)
    expect(load).not.toHaveBeenCalled()
  })

  it('turns ready and loaded when the last face arrives', async () => {
    const loads = new Map<string, ReturnType<typeof deferred<void>>>()
    const load = vi.fn((spec: string) => {
      const pending = deferred<void>()
      loads.set(spec, pending)
      return pending.promise
    })
    installFonts({
      check: () => false,
      load,
    })
    const fonts = mount(() => ['Sans', 'Mono'])

    expect(fonts.ready()).toBe(false)
    expect(fonts.loaded()).toBe(false)
    expect(load.mock.calls.map(call => call[0])).toEqual([
      ...specsOf('Sans'),
      ...specsOf('Mono'),
    ])

    const specs = [
      ...loads.keys(),
    ]
    for (const spec of specs.slice(0, -1))
      loads.get(spec)!.resolve()
    await flushLoads()
    expect(fonts.ready()).toBe(false)
    expect(fonts.loaded()).toBe(false)

    loads.get(specs.at(-1)!)!.resolve()
    await flushLoads()
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(true)
  })

  it('gives up a face that fails at each try, and leaves the fonts not loaded', async () => {
    const failing = specsOf('Mono')[1]!
    const load = vi.fn((spec: string) => (spec === failing
      ? Promise.reject(new Error('fetch failed'))
      : Promise.resolve()))
    installFonts({
      check: () => false,
      load,
    })
    const fonts = mount(() => ['Mono'])

    await flushLoads()
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(false)

    await vi.runAllTimersAsync()
    expect(fonts.loaded()).toBe(false)
    const triesOf = (spec: string) => load.mock.calls.filter(call => call[0] === spec).length
    expect(triesOf(failing)).toBe(1 + FONT_LOAD_MAX_RETRIES)
    expect(triesOf(specsOf('Mono')[0]!)).toBe(1)

    // The face used all its retries: no later timer tries it again.
    await vi.advanceTimersByTimeAsync(FONT_LOAD_RETRY_BASE_MS * 2 ** (FONT_LOAD_MAX_RETRIES + 1))
    expect(triesOf(failing)).toBe(1 + FONT_LOAD_MAX_RETRIES)
  })

  it('turns ready at the limit for slow fonts, and loaded when they arrive', async () => {
    const loads: ReturnType<typeof deferred<void>>[] = []
    installFonts({
      check: () => false,
      load: () => {
        const pending = deferred<void>()
        loads.push(pending)
        return pending.promise
      },
    })
    const fonts = mount(() => ['Mono'])

    await vi.advanceTimersByTimeAsync(CHAT_FONT_LOAD_LIMIT_MS - 1)
    expect(fonts.ready()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(false)

    for (const pending of loads)
      pending.resolve()
    await flushLoads()
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(true)
  })

  it('starts again when the families change, and ignores the loads of the run before', async () => {
    const loads = new Map<string, ReturnType<typeof deferred<void>>>()
    installFonts({
      check: () => false,
      load: (spec) => {
        const pending = deferred<void>()
        loads.set(spec, pending)
        return pending.promise
      },
    })
    const [families, setFamilies] = createSignal<readonly string[]>(['First'])
    const fonts = mount(families)
    expect(fonts.ready()).toBe(false)

    setFamilies(['Second'])
    for (const spec of specsOf('First'))
      loads.get(spec)!.resolve()
    await flushLoads()
    expect(fonts.ready()).toBe(false)
    expect(fonts.loaded()).toBe(false)

    // The limit of the first run is cleared: the fonts turn ready only at the limit of
    // the second run, or when its loads settle.
    for (const spec of specsOf('Second'))
      loads.get(spec)!.resolve()
    await flushLoads()
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(true)
  })

  it('keeps its states when the families change to an equal list', async () => {
    const load = vi.fn(() => new Promise<void>(() => {}))
    installFonts({
      check: () => false,
      load,
    })
    const [families, setFamilies] = createSignal<readonly string[]>(['Mono'])
    mount(families)
    expect(load).toHaveBeenCalledTimes(4)

    setFamilies(['Mono'])
    await flushLoads()
    expect(load).toHaveBeenCalledTimes(4)
  })

  it('skips an empty family', () => {
    const load = vi.fn((_spec: string) => new Promise<void>(() => {}))
    installFonts({
      check: () => false,
      load,
    })
    mount(() => ['', 'Mono'])
    expect(load.mock.calls.map(call => call[0])).toEqual(specsOf('Mono'))
  })

  it('counts the fonts as ready and loaded when every family is empty', () => {
    const load = vi.fn(() => Promise.resolve())
    installFonts({
      check: () => false,
      load,
    })
    const fonts = mount(() => ['', ''])
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(true)
    expect(load).not.toHaveBeenCalled()
  })

  it('survives a family that the font parser refuses', async () => {
    const refuse = (): never => {
      throw new SyntaxError('unparseable font shorthand')
    }
    installFonts({ check: refuse, load: refuse })
    let fonts: ChatFonts | undefined
    expect(() => {
      fonts = mount(() => ['not a valid family'])
    }).not.toThrow()

    await flushLoads()
    expect(fonts!.ready()).toBe(true)
    expect(fonts!.loaded()).toBe(false)
    await vi.runAllTimersAsync()
    expect(fonts!.loaded()).toBe(false)
  })

  it('turns loaded when a face that failed once loads at its second try', async () => {
    const failing = specsOf('Mono')[2]!
    let failures = 0
    installFonts({
      check: () => false,
      load: (spec) => {
        if (spec === failing && failures++ === 0)
          return Promise.reject(new Error('fetch failed'))
        return Promise.resolve()
      },
    })
    const fonts = mount(() => ['Mono'])

    await flushLoads()
    expect(fonts.ready()).toBe(true)
    expect(fonts.loaded()).toBe(false)

    await vi.advanceTimersByTimeAsync(FONT_LOAD_RETRY_BASE_MS - 1)
    expect(fonts.loaded()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(fonts.loaded()).toBe(true)
  })

  it('clears its timers when its owner is disposed', async () => {
    installFonts({
      check: () => false,
      load: () => new Promise<void>(() => {}),
    })
    const fonts = mount(() => ['Mono'])
    disposeRoot!()
    disposeRoot = undefined

    expect(vi.getTimerCount()).toBe(0)
    await vi.advanceTimersByTimeAsync(CHAT_FONT_LOAD_LIMIT_MS)
    expect(fonts.ready()).toBe(false)
  })
})
