import type { ChatRowWidth } from './chatRowWidth'
import { createRenderEffect, createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createChatRowWidth } from './chatRowWidth'

const SETTLE_MS = 150

describe('createChatRowWidth', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  /** The unit under an owner of its own, so that a test can dispose it. */
  function createTestRowWidth(): { rowWidth: ChatRowWidth, dispose: () => void } {
    return createRoot(dispose => ({
      rowWidth: createChatRowWidth(SETTLE_MS),
      dispose,
    }))
  }

  it('holds no width, and allows no measure, before the first report', () => {
    const { rowWidth, dispose } = createTestRowWidth()

    expect(rowWidth.held()).toBeUndefined()
    expect(rowWidth.measurable()).toBe(false)
    dispose()
  })

  it('starts no wait for the first width', () => {
    const { rowWidth, dispose } = createTestRowWidth()

    rowWidth.report(640)

    expect(rowWidth.held()).toBe(640)
    expect(rowWidth.measurable()).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
    dispose()
  })

  it('allows no measure at a new width until it held for the settle time', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)

    rowWidth.report(700)
    expect(rowWidth.held()).toBe(700)
    expect(rowWidth.measurable()).toBe(false)

    vi.advanceTimersByTime(SETTLE_MS - 1)
    expect(rowWidth.measurable()).toBe(false)

    vi.advanceTimersByTime(1)
    expect(rowWidth.measurable()).toBe(true)
    dispose()
  })

  it('starts the wait again for each new width', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)
    rowWidth.report(700)
    vi.advanceTimersByTime(SETTLE_MS - 1)

    // A resize reports a width in each frame: the wait counts from the last one.
    rowWidth.report(710)
    vi.advanceTimersByTime(SETTLE_MS - 1)
    expect(rowWidth.measurable()).toBe(false)

    vi.advanceTimersByTime(1)
    expect(rowWidth.measurable()).toBe(true)
    expect(rowWidth.held()).toBe(710)
    dispose()
  })

  it('keeps the held width, allows no measure, and starts no wait for a report of 0', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)

    rowWidth.report(0)

    expect(rowWidth.held()).toBe(640)
    expect(rowWidth.measurable()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    dispose()
  })

  it('starts no wait when the same width returns after 0', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)
    rowWidth.report(0)

    rowWidth.report(640)

    expect(rowWidth.held()).toBe(640)
    expect(rowWidth.measurable()).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
    dispose()
  })

  it('waits when a new width follows 0', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)
    rowWidth.report(0)

    rowWidth.report(700)
    expect(rowWidth.held()).toBe(700)
    expect(rowWidth.measurable()).toBe(false)

    vi.advanceTimersByTime(SETTLE_MS)
    expect(rowWidth.measurable()).toBe(true)
    dispose()
  })

  it('keeps a wait that runs through a report of 0, and ends it at the settle time', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)
    rowWidth.report(700)

    rowWidth.report(0)
    vi.advanceTimersByTime(SETTLE_MS)
    // The width of 700 held, but the pane has no layout box now.
    expect(rowWidth.measurable()).toBe(false)

    rowWidth.report(700)
    expect(rowWidth.measurable()).toBe(true)
    dispose()
  })

  it('shows no render effect a new width as measurable before the settle time', () => {
    const seen: [number | undefined, boolean][] = []
    const { rowWidth, dispose } = createRoot((dispose) => {
      const rowWidth = createChatRowWidth(SETTLE_MS)
      // A hidden copy renders in a render effect. It must never run with a new width and
      // a settled state of the width before.
      createRenderEffect(() => {
        seen.push([
          rowWidth.held(),
          rowWidth.measurable(),
        ])
      })
      return { rowWidth, dispose }
    })

    rowWidth.report(640)
    rowWidth.report(700)
    vi.advanceTimersByTime(SETTLE_MS)

    expect(seen).toEqual([
      [undefined, false],
      [640, true],
      [700, false],
      [700, true],
    ])
    dispose()
  })

  it('cancels a wait that runs when its owner is disposed', () => {
    const { rowWidth, dispose } = createTestRowWidth()
    rowWidth.report(640)
    rowWidth.report(700)
    expect(vi.getTimerCount()).toBe(1)

    dispose()

    expect(vi.getTimerCount()).toBe(0)
    vi.advanceTimersByTime(SETTLE_MS)
    expect(rowWidth.measurable()).toBe(false)
  })
})
