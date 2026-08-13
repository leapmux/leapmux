import type { ClassifiedEntry } from './chatEntryCache'
import type { MessageCategory } from './messageClassification'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { render, screen } from '@solidjs/testing-library'
import { describe, expect, it, vi } from 'vitest'
import { MessageSource } from '~/generated/leapmux/v1/agent_pb'
import { ChatHiddenPremeasure } from './chatHiddenPremeasure'
import { bandRow, bandRowThought, bleedRow } from './messageStyles.css'
import { COL_SPACING, CONTAINER_PAD_RIGHT, ROW_BLEED_LEFT_VAR, rowBleedLeftStyle, spanLinesReservedWidth } from './widgets/SpanLines.geometry'

function entryWithSpanLines(
  lineCount: number,
  kind: MessageCategory['kind'] = 'user_text',
  source: MessageSource = MessageSource.AGENT,
): ClassifiedEntry {
  return {
    msg: { id: 'm1', seq: 1n, spanId: 'span-1', source } as AgentChatMessage,
    category: { kind } as MessageCategory,
    parsedSpanLines: Array.from({ length: lineCount }, (_, i) => ({
      span_id: `s${i}`,
      color: i + 1,
      type: 'active' as const,
    })),
  } as ClassifiedEntry
}

/** The premeasure row element: the bubble's grandparent (row > reserved wrapper > bubble). */
function premeasureRowOf(bubble: HTMLElement): HTMLElement {
  const row = bubble.parentElement?.parentElement
  if (!row)
    throw new Error('premeasure row not found')
  return row
}

/**
 * Render one candidate through the premeasure root and hand back the two elements
 * every test below asserts on. The mount is identical across them -- only the kind,
 * the source and the rail count vary -- so it lives here rather than in each `it`.
 */
function renderPremeasureRow(
  kind: MessageCategory['kind'],
  source: MessageSource = MessageSource.AGENT,
  lineCount = 0,
): { row: HTMLElement, column: HTMLElement, unmount: () => void } {
  const entry = entryWithSpanLines(lineCount, kind, source)
  const item: VirtualItem = { id: 'm1', hasSpanLines: lineCount > 0, heightKey: 'k1' }
  const { unmount } = render(() => (
    <ChatHiddenPremeasure
      candidates={[{ entry, item }]}
      contentWidthPx={400}
      renderBubble={() => <div data-testid="bubble">bubble</div>}
      onMeasure={vi.fn()}
    />
  ))
  const bubble = screen.getByTestId('bubble')
  return { row: premeasureRowOf(bubble), column: bubble.parentElement!, unmount }
}

describe('chat hidden premeasure rendering', () => {
  it('reserves the same width as visible span-line columns', () => {
    expect(spanLinesReservedWidth(0)).toBe(CONTAINER_PAD_RIGHT)
    expect(spanLinesReservedWidth(1)).toBe(COL_SPACING + CONTAINER_PAD_RIGHT)
    expect(spanLinesReservedWidth(3)).toBe(3 * COL_SPACING + CONTAINER_PAD_RIGHT)
  })

  it('reserves span-line width without mounting SpanLines columns', () => {
    const { column } = renderPremeasureRow('user_text', MessageSource.AGENT, 3)
    expect(column.style.marginLeft).toBe(`${spanLinesReservedWidth(3)}px`)
    expect(column.previousElementSibling).toBeNull()
  })

  // The band's two borders and its vertical padding are part of the row's height,
  // so a premeasured band row must carry the same strip as the live row -- else
  // every band commits a height short by both borders.
  it('wears the band strip when the candidate is an assistant message', () => {
    const { row } = renderPremeasureRow('assistant_text')
    expect(row.classList.contains(bandRow)).toBe(true)
    expect(row.classList.contains(bandRowThought)).toBe(false)
    expect(row.dataset.band).toBe('text')
  })

  it('wears the dashed band strip when the candidate is a thought', () => {
    const { row } = renderPremeasureRow('assistant_thinking')
    expect(row.classList.contains(bandRow)).toBe(true)
    expect(row.classList.contains(bandRowThought)).toBe(true)
    expect(row.dataset.band).toBe('thought')
  })

  it('leaves a non-band candidate unbanded', () => {
    const { row } = renderPremeasureRow('tool_result')
    expect(row.classList.contains(bandRow)).toBe(false)
    expect(row.classList.contains(bleedRow)).toBe(false)
    expect(row.dataset.band).toBeUndefined()
  })

  it('widens a user message candidate, whose bubble bleeds to the right edge', () => {
    // The widening is decided by kind AND source here, exactly as in the live row.
    // A premeasure row that missed it would clip the bubble's bleed and measure a
    // different wrap than the list shows.
    const { row } = renderPremeasureRow('user_text', MessageSource.USER)
    expect(row.classList.contains(bleedRow)).toBe(true)
    expect(row.classList.contains(bandRow)).toBe(false)
  })

  it('publishes the distance to the panel edge, rails included, like the live row', () => {
    // The bleeding child inside reads this var to size its negative margin. If the
    // measured row published a different distance than the live one, the two would
    // wrap their text at different widths and the committed height would be wrong.
    for (const lineCount of [0, 3]) {
      const { column, unmount } = renderPremeasureRow('result_divider', MessageSource.AGENT, lineCount)
      expect(column.style.getPropertyValue(ROW_BLEED_LEFT_VAR))
        .toBe(rowBleedLeftStyle(lineCount)[ROW_BLEED_LEFT_VAR])
      // And it tracks the rails: the two counts must not resolve to the same distance.
      expect(column.style.marginLeft).toBe(`${spanLinesReservedWidth(lineCount)}px`)
      unmount()
    }
  })

  // A turn-end divider bleeds too, so the measured row must be as wide as the
  // live one -- a long divider label wraps differently at the narrower width.
  it('widens a turn-end divider candidate without banding it', () => {
    const { row } = renderPremeasureRow('result_divider')
    expect(row.classList.contains(bleedRow)).toBe(true)
    expect(row.classList.contains(bandRow)).toBe(false)
    expect(row.dataset.band).toBeUndefined()
  })

  it('remeasures when an image loads after the first premeasure frame', () => {
    const originalRaf = globalThis.requestAnimationFrame
    const originalCancelRaf = globalThis.cancelAnimationFrame
    const frames: FrameRequestCallback[] = []
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }) as typeof requestAnimationFrame
    globalThis.cancelAnimationFrame = vi.fn() as typeof cancelAnimationFrame
    try {
      const onMeasure = vi.fn()
      let height = 10
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const { container } = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry, item }]}
          contentWidthPx={400}
          renderBubble={() => <img alt="deferred" src="data:image/png;base64,iVBORw0KGgo=" />}
          onMeasure={onMeasure}
        />
      ))
      const row = container.firstElementChild?.firstElementChild as HTMLElement
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height }) as DOMRect)
      let imageComplete = false
      Object.defineProperty(container.querySelector('img')!, 'complete', {
        configurable: true,
        get: () => imageComplete,
      })

      frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledWith('m1', 10, 'k1', expect.any(Number), false)

      height = 42
      imageComplete = true
      container.querySelector('img')!.dispatchEvent(new Event('load', { bubbles: true }))
      frames.shift()?.(16)

      expect(onMeasure).toHaveBeenLastCalledWith('m1', 42, 'k1', expect.any(Number), true)
    }
    finally {
      if (originalRaf)
        globalThis.requestAnimationFrame = originalRaf
      else
        Reflect.deleteProperty(globalThis, 'requestAnimationFrame')
      if (originalCancelRaf)
        globalThis.cancelAnimationFrame = originalCancelRaf
      else
        Reflect.deleteProperty(globalThis, 'cancelAnimationFrame')
    }
  })

  it('keeps a size-reserved image unsettled while it decodes', () => {
    const originalRaf = globalThis.requestAnimationFrame
    const originalCancelRaf = globalThis.cancelAnimationFrame
    const frames: FrameRequestCallback[] = []
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }) as typeof requestAnimationFrame
    globalThis.cancelAnimationFrame = vi.fn() as typeof cancelAnimationFrame
    try {
      const onMeasure = vi.fn()
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const { container } = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry, item }]}
          contentWidthPx={400}
          renderBubble={() => (
            <img alt="reserved" data-size-reserved="1" src="data:image/png;base64,iVBORw0KGgo=" />
          )}
          onMeasure={onMeasure}
        />
      ))
      const row = container.firstElementChild?.firstElementChild as HTMLElement
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height: 24 }) as DOMRect)
      // Still decoding: the reservation might be revoked by the decode-time
      // verifier, so the hidden row cannot commit as settled yet.
      Object.defineProperty(container.querySelector('img')!, 'complete', {
        configurable: true,
        get: () => false,
      })

      frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledWith('m1', 24, 'k1', expect.any(Number), false)
    }
    finally {
      if (originalRaf)
        globalThis.requestAnimationFrame = originalRaf
      else
        Reflect.deleteProperty(globalThis, 'requestAnimationFrame')
      if (originalCancelRaf)
        globalThis.cancelAnimationFrame = originalCancelRaf
      else
        Reflect.deleteProperty(globalThis, 'cancelAnimationFrame')
    }
  })

  it('measures a whole band in ONE shared frame: all rect reads happen before any commit', () => {
    const originalRaf = globalThis.requestAnimationFrame
    const originalCancelRaf = globalThis.cancelAnimationFrame
    const frames: FrameRequestCallback[] = []
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }) as typeof requestAnimationFrame
    globalThis.cancelAnimationFrame = vi.fn() as typeof cancelAnimationFrame
    try {
      const height1 = 10
      let height2 = 20
      const calls: Array<[string, number]> = []
      const onMeasure = vi.fn((id: string, height: number) => {
        calls.push([id, height])
        // Simulate the commit dirtying layout (the offset-map rebuild + spacer write a
        // real primeHeight triggers): if row 2's rect were read AFTER this commit -- the
        // old per-row interleaving -- it would observe the dirtied 999, not its real 20.
        height2 = 999
        return true
      })
      const candidates = [
        { entry: entryWithSpanLines(0), item: { id: 'm1', hasSpanLines: false, heightKey: 'k1' } as VirtualItem },
        {
          entry: { ...entryWithSpanLines(0), msg: { id: 'm2', seq: 2n, spanId: 'span-2' } as AgentChatMessage } as ClassifiedEntry,
          item: { id: 'm2', hasSpanLines: false, heightKey: 'k2' } as VirtualItem,
        },
      ]
      const { container } = render(() => (
        <ChatHiddenPremeasure
          candidates={candidates}
          contentWidthPx={400}
          renderBubble={() => <div>bubble</div>}
          onMeasure={onMeasure}
        />
      ))
      const rowEls = Array.from(container.firstElementChild!.children) as HTMLElement[]
      vi.spyOn(rowEls[0], 'getBoundingClientRect').mockImplementation(() => ({ height: height1 }) as DOMRect)
      vi.spyOn(rowEls[1], 'getBoundingClientRect').mockImplementation(() => ({ height: height2 }) as DOMRect)

      // Both rows share one frame (not one rAF per row), and both commits see the
      // heights read against the SAME clean layout.
      expect(frames).toHaveLength(1)
      frames.shift()?.(0)
      expect(calls).toEqual([['m1', 10], ['m2', 20]])
    }
    finally {
      if (originalRaf)
        globalThis.requestAnimationFrame = originalRaf
      else
        Reflect.deleteProperty(globalThis, 'requestAnimationFrame')
      if (originalCancelRaf)
        globalThis.cancelAnimationFrame = originalCancelRaf
      else
        Reflect.deleteProperty(globalThis, 'cancelAnimationFrame')
    }
  })

  it('disconnects resize observation after an accepted unsettled image measurement', () => {
    const originalRaf = globalThis.requestAnimationFrame
    const originalCancelRaf = globalThis.cancelAnimationFrame
    const originalResizeObserver = globalThis.ResizeObserver
    const frames: FrameRequestCallback[] = []
    const observers: Array<{ callback: ResizeObserverCallback, disconnected: boolean }> = []
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }) as typeof requestAnimationFrame
    globalThis.cancelAnimationFrame = vi.fn() as typeof cancelAnimationFrame
    globalThis.ResizeObserver = class {
      private readonly record: { callback: ResizeObserverCallback, disconnected: boolean }

      constructor(callback: ResizeObserverCallback) {
        this.record = { callback, disconnected: false }
        observers.push(this.record)
      }

      observe() {}
      unobserve() {}
      disconnect() {
        this.record.disconnected = true
      }
    } as unknown as typeof ResizeObserver
    try {
      const onMeasure = vi.fn(() => true)
      let height = 10
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const { container } = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry, item }]}
          contentWidthPx={400}
          renderBubble={() => <img alt="deferred" src="data:image/png;base64,iVBORw0KGgo=" />}
          onMeasure={onMeasure}
        />
      ))
      const row = container.firstElementChild?.firstElementChild as HTMLElement
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height }) as DOMRect)
      let imageComplete = false
      const img = container.querySelector('img')!
      Object.defineProperty(img, 'complete', {
        configurable: true,
        get: () => imageComplete,
      })

      frames.shift()?.(0)

      expect(onMeasure).toHaveBeenCalledWith('m1', 10, 'k1', expect.any(Number), false)
      expect(observers[0].disconnected).toBe(true)

      height = 42
      imageComplete = true
      img.dispatchEvent(new Event('load', { bubbles: true }))
      frames.shift()?.(16)

      expect(onMeasure).toHaveBeenLastCalledWith('m1', 42, 'k1', expect.any(Number), true)
    }
    finally {
      if (originalRaf)
        globalThis.requestAnimationFrame = originalRaf
      else
        Reflect.deleteProperty(globalThis, 'requestAnimationFrame')
      if (originalCancelRaf)
        globalThis.cancelAnimationFrame = originalCancelRaf
      else
        Reflect.deleteProperty(globalThis, 'cancelAnimationFrame')
      if (originalResizeObserver)
        globalThis.ResizeObserver = originalResizeObserver
      else
        Reflect.deleteProperty(globalThis, 'ResizeObserver')
    }
  })

  it('remeasures when hidden row layout changes after the first frame', () => {
    const originalRaf = globalThis.requestAnimationFrame
    const originalCancelRaf = globalThis.cancelAnimationFrame
    const originalResizeObserver = globalThis.ResizeObserver
    const frames: FrameRequestCallback[] = []
    const observers: Array<{ callback: ResizeObserverCallback, disconnected: boolean }> = []
    globalThis.requestAnimationFrame = ((cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }) as typeof requestAnimationFrame
    globalThis.cancelAnimationFrame = vi.fn() as typeof cancelAnimationFrame
    globalThis.ResizeObserver = class {
      private readonly record: { callback: ResizeObserverCallback, disconnected: boolean }

      constructor(callback: ResizeObserverCallback) {
        this.record = { callback, disconnected: false }
        observers.push(this.record)
      }

      observe() {}
      unobserve() {}
      disconnect() {
        this.record.disconnected = true
      }
    } as unknown as typeof ResizeObserver
    try {
      const onMeasure = vi.fn()
      let height = 12
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const { container, unmount } = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry, item }]}
          contentWidthPx={400}
          renderBubble={() => <div>row</div>}
          onMeasure={onMeasure}
        />
      ))
      const row = container.firstElementChild?.firstElementChild as HTMLElement
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => ({ height }) as DOMRect)

      frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledWith('m1', 12, 'k1', expect.any(Number), true)

      height = 36
      observers[0].callback([], observers[0] as unknown as ResizeObserver)
      frames.shift()?.(16)

      expect(onMeasure).toHaveBeenLastCalledWith('m1', 36, 'k1', expect.any(Number), true)
      unmount()
      expect(observers[0].disconnected).toBe(true)
    }
    finally {
      if (originalRaf)
        globalThis.requestAnimationFrame = originalRaf
      else
        Reflect.deleteProperty(globalThis, 'requestAnimationFrame')
      if (originalCancelRaf)
        globalThis.cancelAnimationFrame = originalCancelRaf
      else
        Reflect.deleteProperty(globalThis, 'cancelAnimationFrame')
      if (originalResizeObserver)
        globalThis.ResizeObserver = originalResizeObserver
      else
        Reflect.deleteProperty(globalThis, 'ResizeObserver')
    }
  })
})
