import type { ClassifiedEntry } from './chatEntryCache'
import type { MessageCategory } from './messageClassifier'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { render, screen } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { makeMessage } from '~/test-support/messageFactory'
import { installControllableResizeObserver, triggerResizeObserverForSync } from '~/test-support/resizeObserverStub'
import { ChatHiddenPremeasure } from './chatHiddenPremeasure'
import { bandRow, bandRowThought, bleedRow } from './messageStyles.css'
import { prepareMessage } from './rowPreparation'
import { COL_SPACING, CONTAINER_PAD_RIGHT, ROW_BLEED_LEFT_VAR, rowBleedLeftStyle, spanLinesReservedWidth } from './widgets/SpanLines.geometry'

type PremeasureCategory = Exclude<MessageCategory, { kind: 'notification' | 'control_response' }>

function entryWithSpanLines(
  lineCount: number,
  kind: PremeasureCategory['kind'] = 'user_text',
  source: MessageSource = MessageSource.AGENT,
): ClassifiedEntry {
  const parsedSpanLines = Array.from({ length: lineCount }, (_, i) => ({
    span_id: `s${i}`,
    color: i + 1,
    type: 'active' as const,
  }))
  const message = makeMessage({ id: 'm1', seq: 1n, spanId: 'span-1', source, spanLines: JSON.stringify(parsedSpanLines) })
  return {
    ...prepareMessage(message),
    // Geometry tests select the category independently of their bubble content.
    category: { kind },
    parsedSpanLines,
    freshness: { revisionKey: 'fixture-revision', isChildTranscript: false, settingsLabelRevision: '' },
    spanLinesRef: message.spanLines,
    settingsLabelDependencies: [],
  }
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
  kind: PremeasureCategory['kind'],
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
  // The root publishes what a reader -- a screen reader, or an e2e measurement --
  // needs to tell a hidden COPY of a row from the row itself. Both markers are
  // load-bearing and neither is visible from inside a row, so nothing else fails
  // if one is dropped: the copy simply starts passing for the real thing.
  it.each(['load', 'error'])('keeps an empty image row unready until its %s event', (eventName) => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (handler: FrameRequestCallback) => frames.push(handler))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    installControllableResizeObserver()
    let unmount: (() => void) | undefined
    try {
      let complete = false
      const onMeasure = vi.fn(() => true)
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry: entryWithSpanLines(0, 'tool_result'), item: { id: 'empty', hasSpanLines: false } }]}
          contentWidthPx={400}
          renderBubble={() => <img alt="pending" />}
          onMeasure={onMeasure}
        />
      ))
      unmount = rendered.unmount
      const row = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      const image = row.querySelector('img')!
      vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 0))
      Object.defineProperty(image, 'complete', { get: () => complete })
      frames.shift()?.(0)
      expect(onMeasure).not.toHaveBeenCalled()
      complete = true
      image.dispatchEvent(new Event(eventName))
      frames.shift()?.(16)
      expect(onMeasure).toHaveBeenCalledWith('empty', 0, undefined, expect.any(Number), true)
    }
    finally {
      unmount?.()
      vi.unstubAllGlobals()
    }
  })

  it('skips a hidden view and measures its empty row after layout returns', () => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (handler: FrameRequestCallback) => frames.push(handler))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    installControllableResizeObserver()
    let unmount: (() => void) | undefined
    try {
      const onMeasure = vi.fn(() => true)
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry: entryWithSpanLines(0, 'tool_result'), item: { id: 'empty', hasSpanLines: false } }]}
          contentWidthPx={400}
          renderBubble={() => null}
          onMeasure={onMeasure}
        />
      ))
      unmount = rendered.unmount
      const root = rendered.container.firstElementChild as HTMLElement
      const row = root.firstElementChild as HTMLElement
      root.style.display = 'none'
      const rectangle = vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 0, 0))
      frames.shift()?.(0)
      expect(onMeasure).not.toHaveBeenCalled()
      root.style.display = ''
      root.style.visibility = 'hidden'
      rectangle.mockReturnValue(new DOMRect(0, 0, 400, 0))
      triggerResizeObserverForSync(row)
      frames.shift()?.(16)
      expect(onMeasure).toHaveBeenCalledWith('empty', 0, undefined, expect.any(Number), true)
    }
    finally {
      unmount?.()
      vi.unstubAllGlobals()
    }
  })

  it('measures only the current empty candidate after replacement before the frame', () => {
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (handler: FrameRequestCallback) => frames.push(handler))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    let unmount: (() => void) | undefined
    try {
      const entry = entryWithSpanLines(0, 'tool_result')
      const item = { id: 'empty', heightKey: 'live', hasSpanLines: false }
      const [candidates, setCandidates] = createSignal([{ entry, item }])
      const onMeasure = vi.fn(() => true)
      const rendered = render(() => (
        <ChatHiddenPremeasure candidates={candidates()} contentWidthPx={400} renderBubble={() => null} onMeasure={onMeasure} />
      ))
      unmount = rendered.unmount
      const oldRow = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      vi.spyOn(oldRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 100))
      setCandidates([{ entry, item: { ...item } }])
      const currentRow = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      expect(oldRow.isConnected).toBe(false)
      vi.spyOn(currentRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 0))
      frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledTimes(1)
      expect(onMeasure).toHaveBeenCalledWith('empty', 0, 'live', expect.any(Number), true)
    }
    finally {
      unmount?.()
      vi.unstubAllGlobals()
    }
  })

  it('marks its root as a hidden copy, reachable from any bubble inside it', () => {
    const { row } = renderPremeasureRow('user_text', MessageSource.USER)
    const root = row.parentElement!
    expect(root.getAttribute('aria-hidden')).toBe('true')
    expect(root.getAttribute('data-chat-premeasure-root')).toBe('true')
    // The query a chat measurement actually runs: from the bubble UP. It is how
    // measureBubbleEdges names a premeasure copy instead of reporting the same
    // "not inside the chat scroll container" a remounted row produces
    // (tests/e2e/helpers/ui.ts, and https://github.com/leapmux/leapmux/issues/402).
    expect(screen.getByTestId('bubble').closest('[data-chat-premeasure-root="true"]')).toBe(root)
  })

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
          entry: { ...entryWithSpanLines(0), message: { id: 'm2', seq: 2n, spanId: 'span-2' } as AgentChatMessage } as ClassifiedEntry,
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
      // Two candidates mounted above, so both rows exist; the guards are type-level alone.
      const row1 = rowEls[0]
      const row2 = rowEls[1]
      if (row1 === undefined || row2 === undefined)
        throw new Error('expected the two premeasure rows to mount')
      vi.spyOn(row1, 'getBoundingClientRect').mockImplementation(() => ({ height: height1 }) as DOMRect)
      vi.spyOn(row2, 'getBoundingClientRect').mockImplementation(() => ({ height: height2 }) as DOMRect)

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
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }
    globalThis.cancelAnimationFrame = vi.fn<typeof cancelAnimationFrame>()
    installControllableResizeObserver()
    const observe = vi.spyOn(ResizeObserver.prototype, 'observe')
    const disconnect = vi.spyOn(ResizeObserver.prototype, 'disconnect')
    let unmount: (() => void) | undefined
    try {
      const onMeasure = vi.fn(() => true)
      let height = 10
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry, item }]}
          contentWidthPx={400}
          renderBubble={() => <img alt="deferred" src="data:image/png;base64,iVBORw0KGgo=" />}
          onMeasure={onMeasure}
        />
      ))
      unmount = rendered.unmount
      const row = rendered.container.firstElementChild?.firstElementChild
      if (!(row instanceof HTMLElement))
        throw new Error('The image premeasure row did not mount.')
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => new DOMRect(0, 0, 400, height))
      let imageComplete = false
      const img = rendered.container.querySelector('img')
      if (!img)
        throw new Error('The deferred image did not mount.')
      Object.defineProperty(img, 'complete', {
        configurable: true,
        get: () => imageComplete,
      })

      frames.shift()?.(0)

      expect(onMeasure).toHaveBeenCalledWith('m1', 10, 'k1', expect.any(Number), false)
      expect(observe).toHaveBeenCalledWith(row)
      expect(disconnect).toHaveBeenCalledTimes(1)
      expect(disconnect.mock.contexts[0]).toBe(observe.mock.contexts[0])

      height = 42
      imageComplete = true
      img.dispatchEvent(new Event('load', { bubbles: true }))
      frames.shift()?.(16)

      expect(onMeasure).toHaveBeenLastCalledWith('m1', 42, 'k1', expect.any(Number), true)
    }
    finally {
      unmount?.()
      observe.mockRestore()
      disconnect.mockRestore()
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
    globalThis.requestAnimationFrame = (cb: FrameRequestCallback) => {
      frames.push(cb)
      return frames.length
    }
    globalThis.cancelAnimationFrame = vi.fn<typeof cancelAnimationFrame>()
    installControllableResizeObserver()
    const observe = vi.spyOn(ResizeObserver.prototype, 'observe')
    const disconnect = vi.spyOn(ResizeObserver.prototype, 'disconnect')
    let unmount: (() => void) | undefined
    try {
      const onMeasure = vi.fn()
      let height = 12
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[{ entry, item }]}
          contentWidthPx={400}
          renderBubble={() => <div>row</div>}
          onMeasure={onMeasure}
        />
      ))
      unmount = rendered.unmount
      const row = rendered.container.firstElementChild?.firstElementChild
      if (!(row instanceof HTMLElement))
        throw new Error('The resize premeasure row did not mount.')
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => new DOMRect(0, 0, 400, height))

      frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledWith('m1', 12, 'k1', expect.any(Number), true)

      height = 36
      expect(observe).toHaveBeenCalledWith(row)
      triggerResizeObserverForSync(row)
      frames.shift()?.(16)

      expect(onMeasure).toHaveBeenLastCalledWith('m1', 36, 'k1', expect.any(Number), true)
      unmount()
      unmount = undefined
      expect(disconnect).toHaveBeenCalledTimes(1)
      expect(disconnect.mock.contexts[0]).toBe(observe.mock.contexts[0])
    }
    finally {
      unmount?.()
      observe.mockRestore()
      disconnect.mockRestore()
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

  it('measures the current positive-height row after a same-key candidate replacement before its first frame', () => {
    const originalRaf = globalThis.requestAnimationFrame
    const originalCancelRaf = globalThis.cancelAnimationFrame
    const originalResizeObserver = globalThis.ResizeObserver
    const frames = new Map<number, FrameRequestCallback>()
    const readEvents: unknown[] = []
    const recordRead = (event: Event) => {
      if (event instanceof CustomEvent)
        readEvents.push(event.detail)
    }
    window.addEventListener('leapmux:chat-premeasure', recordRead)
    vi.stubEnv('LEAPMUX_DEV', '1')
    let nextFrameId = 0
    globalThis.requestAnimationFrame = (handler: FrameRequestCallback): number => {
      const id = ++nextFrameId
      frames.set(id, handler)
      return id
    }
    globalThis.cancelAnimationFrame = (id: number): void => {
      frames.delete(id)
    }
    installControllableResizeObserver()
    const observe = vi.spyOn(ResizeObserver.prototype, 'observe')
    let unmount: (() => void) | undefined
    try {
      const entry = entryWithSpanLines(0, 'assistant_text')
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'same-native-key' }
      const [candidates, setCandidates] = createSignal([{ entry, item }])
      const onMeasure = vi.fn(() => true)
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={candidates()}
          contentWidthPx={752}
          renderBubble={() => <p>ACTUAL_ASSISTANT42</p>}
          onMeasure={onMeasure}
        />
      ))
      unmount = rendered.unmount
      const oldRow = rendered.container.querySelector('[data-chat-premeasure-root]')?.firstElementChild
      if (!(oldRow instanceof HTMLElement))
        throw new Error('The first actual premeasure row did not mount.')
      vi.spyOn(oldRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 752, 11))
      expect(frames.size).toBe(1)
      expect(onMeasure).not.toHaveBeenCalled()

      setCandidates([{ entry, item: { ...item } }])
      const currentRow = rendered.container.querySelector('[data-chat-premeasure-root]')?.firstElementChild
      if (!(currentRow instanceof HTMLElement))
        throw new Error('The replacement actual premeasure row did not mount.')
      expect(currentRow).not.toBe(oldRow)
      expect(oldRow.isConnected).toBe(false)
      expect(currentRow.isConnected).toBe(true)
      const currentRectangle = vi.spyOn(currentRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 752, 88))
      expect(observe).toHaveBeenCalledWith(currentRow)
      expect(frames.size).toBe(1)
      const pending = [...frames.values()]
      frames.clear()
      for (const handler of pending)
        handler(0)

      expect(onMeasure).toHaveBeenCalledTimes(1)
      expect(onMeasure).toHaveBeenCalledWith('m1', 88, 'same-native-key', expect.any(Number), true)
      expect(readEvents).toEqual([{
        phase: 'read',
        id: 'm1',
        seq: '1',
        height: 88,
        heightKey: 'same-native-key',
        connected: true,
        settled: true,
      }])
      currentRectangle.mockReturnValue(new DOMRect(0, 0, 752, 101))
      triggerResizeObserverForSync(currentRow)
      expect(frames.size).toBe(1)
      const resized = [...frames.values()]
      frames.clear()
      for (const handler of resized)
        handler(16)
      expect(onMeasure).toHaveBeenCalledTimes(2)
      expect(onMeasure).toHaveBeenLastCalledWith('m1', 101, 'same-native-key', expect.any(Number), true)
      expect(readEvents).toHaveLength(2)
    }
    finally {
      unmount?.()
      observe.mockRestore()
      window.removeEventListener('leapmux:chat-premeasure', recordRead)
      vi.unstubAllEnvs()
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
