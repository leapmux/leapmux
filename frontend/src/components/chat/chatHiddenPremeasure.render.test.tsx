import type { ClassifiedEntry } from './chatEntryCache'
import type { MessageCategory } from './messageClassifier'
import type { RenderReadiness } from './renderReadiness'
import type { VirtualItem } from './useChatVirtualizer'
import type { AgentChatMessage } from '~/generated/proto/leapmux/v1/agent_pb'
import { cleanup, render, screen } from '@solidjs/testing-library'
import { createSignal, Show } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MessageSource } from '~/generated/proto/leapmux/v1/agent_pb'
import { createManualFrames } from '~/test-support/manualFrames'
import { makeMessage } from '~/test-support/messageFactory'
import { installControllableResizeObserver, triggerResizeObserverForSync } from '~/test-support/resizeObserverStub'
import { ChatHiddenPremeasure, PREMEASURE_IMAGE_SETTLE_LIMIT_MS } from './chatHiddenPremeasure'
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
  const message = makeMessage({
    id: 'm1',
    seq: 1n,
    spanId: 'span-1',
    source,
    spanLines: JSON.stringify(parsedSpanLines),
  })
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
      candidates={[
        { entry, item },
      ]}
      contentWidthPx={400}
      renderBubble={() => <div data-testid="bubble">bubble</div>}
      onMeasure={vi.fn()}
    />
  ))
  const bubble = screen.getByTestId('bubble')
  return {
    row: premeasureRowOf(bubble),
    column: bubble.parentElement!,
    unmount,
  }
}

/**
 * Queue each animation frame for the test to run by hand, run `body`, then
 * unmount everything it rendered and restore the globals and the spies. The
 * unmount runs first, so a row that cancels its frame on disposal still reaches
 * the stub.
 */
function withFrameQueue(body: (frames: FrameRequestCallback[]) => void): void {
  const frames: FrameRequestCallback[] = []
  vi.stubGlobal('requestAnimationFrame', (handler: FrameRequestCallback) => frames.push(handler))
  vi.stubGlobal('cancelAnimationFrame', vi.fn())
  try {
    body(frames)
  }
  finally {
    cleanup()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  }
}

describe('chat hidden premeasure rendering', () => {
  it.each(['load', 'error'])('keeps an empty image row unready until its %s event', (eventName) => {
    withFrameQueue((frames) => {
      installControllableResizeObserver()
      let complete = false
      const onMeasure = vi.fn()
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[
            {
              entry: entryWithSpanLines(0, 'tool_result'),
              item: { id: 'empty', hasSpanLines: false },
            },
          ]}
          contentWidthPx={400}
          renderBubble={() => <img alt="pending" />}
          onMeasure={onMeasure}
        />
      ))
      const row = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      const image = row.querySelector('img')!
      vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 0))
      Object.defineProperty(image, 'complete', {
        get: () => complete,
      })
      frames.shift()?.(0)
      expect(onMeasure).not.toHaveBeenCalled()
      complete = true
      image.dispatchEvent(new Event(eventName))
      frames.shift()?.(16)
      expect(onMeasure).toHaveBeenCalledWith('empty', 0, undefined, expect.any(Number))
    })
  })

  it('commits no height while a Markdown body is pending, and measures once it is ready without a resize', () => {
    withFrameQueue((frames) => {
      installControllableResizeObserver()
      const [pending, setPending] = createSignal(true)
      const onMeasure = vi.fn()
      // eslint-disable-next-line solid/reactivity -- the scope reads the accessor inside its own memo
      const renderBubble = vi.fn((_entry: ClassifiedEntry, readiness: RenderReadiness) => {
        readiness.track(pending)
        return <div data-testid="bubble">body</div>
      })
      render(() => (
        <ChatHiddenPremeasure
          candidates={[
            {
              entry: entryWithSpanLines(0, 'assistant_text'),
              item: { id: 'markdown', hasSpanLines: false, heightKey: 'k1' },
            },
          ]}
          contentWidthPx={400}
          renderBubble={renderBubble}
          onMeasure={onMeasure}
        />
      ))
      const row = premeasureRowOf(screen.getByTestId('bubble'))
      vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 120))

      // The row has a height, but its Markdown is not final: nothing commits.
      while (frames.length > 0)
        frames.shift()?.(0)
      expect(onMeasure).not.toHaveBeenCalled()

      // The body becomes ready. No resize fires, but the row measures anyway.
      setPending(false)
      expect(frames).toHaveLength(1)
      frames.shift()?.(16)
      expect(onMeasure).toHaveBeenCalledOnce()
      expect(onMeasure).toHaveBeenCalledWith('markdown', 120, 'k1', expect.any(Number))
    })
  })

  // A diff view that switches its layout keeps the old one in view while the new
  // one mounts behind it: the copy shows complete content, but its height is about
  // to change.
  it('commits no height while a body replaces its content, and measures once the copy shows the new content', () => {
    withFrameQueue((frames) => {
      installControllableResizeObserver()
      const [replacing, setReplacing] = createSignal(true)
      const onMeasure = vi.fn()
      render(() => (
        <ChatHiddenPremeasure
          candidates={[
            {
              entry: entryWithSpanLines(0, 'tool_result'),
              item: { id: 'diff', hasSpanLines: false, heightKey: 'k1' },
            },
          ]}
          contentWidthPx={400}
          renderBubble={(_entry, readiness) => {
            readiness.trackReplacing(replacing)
            return <div data-testid="bubble">body</div>
          }}
          onMeasure={onMeasure}
        />
      ))
      const row = premeasureRowOf(screen.getByTestId('bubble'))
      vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 80))

      while (frames.length > 0)
        frames.shift()?.(0)
      expect(onMeasure).not.toHaveBeenCalled()

      setReplacing(false)
      expect(frames).toHaveLength(1)
      frames.shift()?.(16)
      expect(onMeasure).toHaveBeenCalledOnce()
      expect(onMeasure).toHaveBeenCalledWith('diff', 80, 'k1', expect.any(Number))
    })
  })

  it('measures nothing again for a load event of an element that is not an image', () => {
    withFrameQueue((frames) => {
      installControllableResizeObserver()
      const onMeasure = vi.fn()
      render(() => (
        <ChatHiddenPremeasure
          candidates={[
            {
              entry: entryWithSpanLines(0, 'assistant_text'),
              item: { id: 'media', hasSpanLines: false, heightKey: 'k1' },
            },
          ]}
          contentWidthPx={400}
          renderBubble={() => (
            <div data-testid="bubble">
              <video />
              <img alt="settled" />
            </div>
          )}
          onMeasure={onMeasure}
        />
      ))
      const row = premeasureRowOf(screen.getByTestId('bubble'))
      vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 40))
      for (const image of row.querySelectorAll('img')) {
        Object.defineProperty(image, 'complete', {
          get: () => true,
        })
      }
      while (frames.length > 0)
        frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledOnce()

      row.querySelector('video')!.dispatchEvent(new Event('load'))
      expect(frames).toHaveLength(0)

      row.querySelector('img')!.dispatchEvent(new Event('load'))
      expect(frames).toHaveLength(1)
    })
  })

  it('measures when an image that mounts with a ready body settles', () => {
    // A Markdown body renders in the worker, so its image mounts after the row.
    withFrameQueue((frames) => {
      installControllableResizeObserver()
      const [pending, setPending] = createSignal(true)
      const onMeasure = vi.fn()
      let height = 24
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[
            {
              entry: entryWithSpanLines(0, 'assistant_text'),
              item: { id: 'late', hasSpanLines: false, heightKey: 'k1' },
            },
          ]}
          contentWidthPx={400}
          renderBubble={(_entry, readiness) => {
            readiness.track(pending)
            return (
              <Show when={!pending()}>
                <img alt="late" src="data:image/png;base64,iVBORw0KGgo=" />
              </Show>
            )
          }}
          onMeasure={onMeasure}
        />
      ))
      const row = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => new DOMRect(0, 0, 400, height))
      while (frames.length > 0)
        frames.shift()?.(0)
      expect(row.querySelector('img')).toBeNull()

      setPending(false)
      const image = row.querySelector('img')!
      let complete = false
      Object.defineProperty(image, 'complete', {
        get: () => complete,
      })
      frames.shift()?.(16)
      // The image still decodes, and it can change the height of the row: the copy
      // reports nothing yet.
      expect(onMeasure).not.toHaveBeenCalled()

      height = 64
      complete = true
      image.dispatchEvent(new Event('load'))
      frames.shift()?.(32)
      expect(onMeasure).toHaveBeenCalledOnce()
      expect(onMeasure).toHaveBeenLastCalledWith('late', 64, 'k1', expect.any(Number))
    })
  })

  it('hands each row copy its own readiness scope', () => {
    const scopes: RenderReadiness[] = []
    const entry = entryWithSpanLines(0)
    render(() => (
      <ChatHiddenPremeasure
        candidates={[
          {
            entry,
            item: { id: 'a', hasSpanLines: false },
          },
          {
            entry,
            item: { id: 'b', hasSpanLines: false },
          },
        ]}
        contentWidthPx={400}
        renderBubble={(_entry, readiness) => {
          scopes.push(readiness)
          return null
        }}
        onMeasure={vi.fn()}
      />
    ))

    expect(scopes).toHaveLength(2)
    expect(scopes[0]).not.toBe(scopes[1])
  })

  it('skips a hidden view and measures its empty row after layout returns', () => {
    withFrameQueue((frames) => {
      installControllableResizeObserver()
      const onMeasure = vi.fn()
      const rendered = render(() => (
        <ChatHiddenPremeasure
          candidates={[
            {
              entry: entryWithSpanLines(0, 'tool_result'),
              item: { id: 'empty', hasSpanLines: false },
            },
          ]}
          contentWidthPx={400}
          renderBubble={() => null}
          onMeasure={onMeasure}
        />
      ))
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
      expect(onMeasure).toHaveBeenCalledWith('empty', 0, undefined, expect.any(Number))
    })
  })

  it('measures only the current empty candidate after replacement before the frame', () => {
    withFrameQueue((frames) => {
      const entry = entryWithSpanLines(0, 'tool_result')
      const item = { id: 'empty', heightKey: 'live', hasSpanLines: false }
      const [candidates, setCandidates] = createSignal([
        { entry, item },
      ])
      const onMeasure = vi.fn()
      const rendered = render(() => (
        <ChatHiddenPremeasure candidates={candidates()} contentWidthPx={400} renderBubble={() => null} onMeasure={onMeasure} />
      ))
      const oldRow = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      vi.spyOn(oldRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 100))
      setCandidates([
        {
          entry,
          item: { ...item },
        },
      ])
      const currentRow = rendered.container.firstElementChild!.firstElementChild as HTMLElement
      expect(oldRow.isConnected).toBe(false)
      vi.spyOn(currentRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 0))
      frames.shift()?.(0)
      expect(onMeasure).toHaveBeenCalledTimes(1)
      expect(onMeasure).toHaveBeenCalledWith('empty', 0, 'live', expect.any(Number))
    })
  })

  // The root publishes what a reader -- a screen reader, or an e2e measurement --
  // needs to tell a hidden COPY of a row from the row itself. Both markers are
  // load-bearing and neither is visible from inside a row, so nothing else fails
  // if one is dropped: the copy simply starts passing for the real thing.
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

  it('reports no height while an image loads, and measures once when it loaded', () => {
    withFrameQueue((frames) => {
      const onMeasure = vi.fn()
      let height = 10
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const { container } = render(() => (
        <ChatHiddenPremeasure
          candidates={[
            { entry, item },
          ]}
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
      // A height of the copy without its image would be wrong by the height of the image.
      expect(onMeasure).not.toHaveBeenCalled()

      height = 42
      imageComplete = true
      container.querySelector('img')!.dispatchEvent(new Event('load', { bubbles: true }))
      frames.shift()?.(16)

      expect(onMeasure).toHaveBeenCalledOnce()
      expect(onMeasure).toHaveBeenLastCalledWith('m1', 42, 'k1', expect.any(Number))
    })
  })

  it('reports no height for a size-reserved image while it decodes', () => {
    withFrameQueue((frames) => {
      const onMeasure = vi.fn()
      const entry = entryWithSpanLines(0)
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
      const { container } = render(() => (
        <ChatHiddenPremeasure
          candidates={[
            { entry, item },
          ]}
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
      // verifier, so the hidden row cannot report its height yet.
      let imageComplete = false
      Object.defineProperty(container.querySelector('img')!, 'complete', {
        configurable: true,
        get: () => imageComplete,
      })

      frames.shift()?.(0)
      expect(onMeasure).not.toHaveBeenCalled()

      // The image decoded at the reserved size. Nothing resizes, so only the event of
      // the image measures the copy.
      imageComplete = true
      container.querySelector('img')!.dispatchEvent(new Event('load', { bubbles: true }))
      frames.shift()?.(16)
      expect(onMeasure).toHaveBeenCalledOnce()
      expect(onMeasure).toHaveBeenCalledWith('m1', 24, 'k1', expect.any(Number))
    })
  })

  it('measures a whole band in ONE shared frame: all rect reads happen before any commit', () => {
    withFrameQueue((frames) => {
      const height1 = 10
      let height2 = 20
      const calls: Array<[string, number]> = []
      const onMeasure = vi.fn((id: string, height: number) => {
        calls.push([id, height])
        // Simulate the commit dirtying layout (the offset-map rebuild + spacer write a
        // real height commit triggers): if row 2's rect were read AFTER this commit -- the
        // old per-row interleaving -- it would observe the dirtied 999, not its real 20.
        height2 = 999
        return true
      })
      const candidates = [
        {
          entry: entryWithSpanLines(0),
          item: { id: 'm1', hasSpanLines: false, heightKey: 'k1' } as VirtualItem,
        },
        {
          entry: {
            ...entryWithSpanLines(0),
            message: { id: 'm2', seq: 2n, spanId: 'span-2' } as AgentChatMessage,
          } as ClassifiedEntry,
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
      expect(calls).toEqual([
        ['m1', 10],
        ['m2', 20],
      ])
    })
  })

  it('remeasures when hidden row layout changes after the first frame', () => {
    const originalResizeObserver = globalThis.ResizeObserver
    installControllableResizeObserver()
    // `withFrameQueue` restores the two spies.
    const observe = vi.spyOn(ResizeObserver.prototype, 'observe')
    const disconnect = vi.spyOn(ResizeObserver.prototype, 'disconnect')
    try {
      withFrameQueue((frames) => {
        const onMeasure = vi.fn()
        let height = 12
        const entry = entryWithSpanLines(0)
        const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'k1' }
        const rendered = render(() => (
          <ChatHiddenPremeasure
            candidates={[
              { entry, item },
            ]}
            contentWidthPx={400}
            renderBubble={() => <div>row</div>}
            onMeasure={onMeasure}
          />
        ))
        const row = rendered.container.firstElementChild?.firstElementChild
        if (!(row instanceof HTMLElement))
          throw new TypeError('The resize premeasure row did not mount.')
        vi.spyOn(row, 'getBoundingClientRect').mockImplementation(() => new DOMRect(0, 0, 400, height))

        frames.shift()?.(0)
        expect(onMeasure).toHaveBeenCalledWith('m1', 12, 'k1', expect.any(Number))

        height = 36
        expect(observe).toHaveBeenCalledWith(row)
        triggerResizeObserverForSync(row)
        frames.shift()?.(16)

        expect(onMeasure).toHaveBeenLastCalledWith('m1', 36, 'k1', expect.any(Number))
        rendered.unmount()
        expect(disconnect).toHaveBeenCalledTimes(1)
        expect(disconnect.mock.contexts[0]).toBe(observe.mock.contexts[0])
      })
    }
    finally {
      if (originalResizeObserver)
        globalThis.ResizeObserver = originalResizeObserver
      else
        Reflect.deleteProperty(globalThis, 'ResizeObserver')
    }
  })

  it('measures the current positive-height row after a same-key candidate replacement before its first frame', () => {
    const originalResizeObserver = globalThis.ResizeObserver
    // Frames that a cancel removes, unlike the queue of `withFrameQueue`.
    const frames = createManualFrames()
    const readEvents: unknown[] = []
    const recordRead = (event: Event) => {
      if (event instanceof CustomEvent)
        readEvents.push(event.detail)
    }
    window.addEventListener('leapmux:chat-premeasure', recordRead)
    vi.stubEnv('LEAPMUX_DEV', '1')
    vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
    vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
    installControllableResizeObserver()
    const observe = vi.spyOn(ResizeObserver.prototype, 'observe')
    let unmount: (() => void) | undefined
    try {
      const entry = entryWithSpanLines(0, 'assistant_text')
      const item: VirtualItem = { id: 'm1', hasSpanLines: false, heightKey: 'same-native-key' }
      const [candidates, setCandidates] = createSignal([
        { entry, item },
      ])
      const onMeasure = vi.fn()
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
        throw new TypeError('The first actual premeasure row did not mount.')
      vi.spyOn(oldRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 752, 11))
      expect(frames.pendingFrames()).toBe(1)
      expect(onMeasure).not.toHaveBeenCalled()

      setCandidates([
        {
          entry,
          item: { ...item },
        },
      ])
      const currentRow = rendered.container.querySelector('[data-chat-premeasure-root]')?.firstElementChild
      if (!(currentRow instanceof HTMLElement))
        throw new TypeError('The replacement actual premeasure row did not mount.')
      expect(currentRow).not.toBe(oldRow)
      expect(oldRow.isConnected).toBe(false)
      expect(currentRow.isConnected).toBe(true)
      const currentRectangle = vi.spyOn(currentRow, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 752, 88))
      expect(observe).toHaveBeenCalledWith(currentRow)
      expect(frames.pendingFrames()).toBe(1)
      frames.runFrames()

      expect(onMeasure).toHaveBeenCalledTimes(1)
      expect(onMeasure).toHaveBeenCalledWith('m1', 88, 'same-native-key', expect.any(Number))
      expect(readEvents).toEqual([
        {
          phase: 'read',
          id: 'm1',
          seq: '1',
          height: 88,
          heightKey: 'same-native-key',
          connected: true,
          settled: true,
          measurable: true,
        },
      ])
      currentRectangle.mockReturnValue(new DOMRect(0, 0, 752, 101))
      triggerResizeObserverForSync(currentRow)
      expect(frames.pendingFrames()).toBe(1)
      frames.runFrames()
      expect(onMeasure).toHaveBeenCalledTimes(2)
      expect(onMeasure).toHaveBeenLastCalledWith('m1', 101, 'same-native-key', expect.any(Number))
      expect(readEvents).toHaveLength(2)
    }
    finally {
      unmount?.()
      observe.mockRestore()
      window.removeEventListener('leapmux:chat-premeasure', recordRead)
      vi.unstubAllEnvs()
      vi.unstubAllGlobals()
      if (originalResizeObserver)
        globalThis.ResizeObserver = originalResizeObserver
      else
        Reflect.deleteProperty(globalThis, 'ResizeObserver')
    }
  })
  describe('the limit of the wait for an image', () => {
    afterEach(() => {
      vi.useRealTimers()
    })

    /**
     * Mount one copy whose content is an image that never loads, in a row of 30px, with
     * the frames of `withFrameQueue`, which the test runs by hand. `setHeightKey` changes
     * the key of the candidate: the new key gives a new candidate object, so the copy of
     * the key before is disposed, and a new copy mounts.
     */
    function mountCopyWithStuckImage(frames: FrameRequestCallback[]) {
      const onMeasure = vi.fn()
      const entry = entryWithSpanLines(0)
      const [heightKey, setHeightKey] = createSignal('k1')
      // One candidate object for each key, as the premeasure queue keeps them.
      const candidates = () => [
        {
          entry,
          item: {
            id: 'm1',
            hasSpanLines: false,
            heightKey: heightKey(),
          } as VirtualItem,
        },
      ]
      // Every row and image of this mount: no row has a rect before its first frame.
      vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 30))
      vi.spyOn(HTMLImageElement.prototype, 'complete', 'get').mockReturnValue(false)
      render(() => (
        <ChatHiddenPremeasure
          candidates={candidates()}
          contentWidthPx={400}
          renderBubble={() => <img alt="stuck" src="data:image/png;base64,iVBORw0KGgo=" />}
          onMeasure={onMeasure}
        />
      ))
      const runFrames = () => {
        while (frames.length > 0)
          frames.shift()?.(0)
      }
      return { onMeasure, runFrames, setHeightKey }
    }

    it('reports the height that the copy has when an image never settles', () => {
      vi.useFakeTimers()
      withFrameQueue((frames) => {
        const { onMeasure, runFrames } = mountCopyWithStuckImage(frames)
        runFrames()
        expect(onMeasure).not.toHaveBeenCalled()

        vi.advanceTimersByTime(PREMEASURE_IMAGE_SETTLE_LIMIT_MS - 1)
        runFrames()
        expect(onMeasure).not.toHaveBeenCalled()

        // One slow image must not keep its row out of the list.
        vi.advanceTimersByTime(1)
        runFrames()
        expect(onMeasure).toHaveBeenCalledOnce()
        expect(onMeasure).toHaveBeenCalledWith('m1', 30, 'k1', expect.any(Number))
      })
    })

    it('starts the wait again for the copy of a new key, and reports nothing for the key before it', () => {
      vi.useFakeTimers()
      withFrameQueue((frames) => {
        const { onMeasure, runFrames, setHeightKey } = mountCopyWithStuckImage(frames)
        runFrames()
        expect(vi.getTimerCount()).toBe(1)

        vi.advanceTimersByTime(PREMEASURE_IMAGE_SETTLE_LIMIT_MS - 1000)
        // The copy of the first key is disposed with its timer, and a new copy mounts.
        setHeightKey('k2')
        expect(vi.getTimerCount()).toBe(0)
        runFrames()
        // The limit of the first key would pass here.
        vi.advanceTimersByTime(1000)
        runFrames()
        expect(onMeasure).not.toHaveBeenCalled()

        // The limit of the new copy started with its own first read.
        vi.advanceTimersByTime(PREMEASURE_IMAGE_SETTLE_LIMIT_MS - 1000)
        runFrames()
        expect(onMeasure).toHaveBeenCalledOnce()
        expect(onMeasure).toHaveBeenCalledWith('m1', 30, 'k2', expect.any(Number))
      })
    })

    it('clears the limit timer when the copy unmounts', () => {
      vi.useFakeTimers()
      withFrameQueue((frames) => {
        const { onMeasure, runFrames } = mountCopyWithStuckImage(frames)
        runFrames()
        expect(vi.getTimerCount()).toBe(1)

        cleanup()

        expect(vi.getTimerCount()).toBe(0)
        expect(onMeasure).not.toHaveBeenCalled()
      })
    })
  })

  it('reads a later copy when a frame runs inside its request', () => {
    // The frame of the vitest setup runs its callback inside requestAnimationFrame. A
    // shared frame that keeps the cancel function of such a frame never schedules again.
    vi.stubGlobal('requestAnimationFrame', (handler: FrameRequestCallback) => {
      handler(0)
      return 1
    })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue(new DOMRect(0, 0, 400, 30))
    try {
      const onMeasure = vi.fn()
      const first = {
        entry: entryWithSpanLines(0),
        item: { id: 'm1', hasSpanLines: false, heightKey: 'k1' } as VirtualItem,
      }
      const second = {
        entry: {
          ...entryWithSpanLines(0),
          message: { id: 'm2', seq: 2n, spanId: 'span-2' } as AgentChatMessage,
        } as ClassifiedEntry,
        item: { id: 'm2', hasSpanLines: false, heightKey: 'k2' } as VirtualItem,
      }
      const [candidates, setCandidates] = createSignal([first])
      render(() => (
        <ChatHiddenPremeasure
          candidates={candidates()}
          contentWidthPx={400}
          renderBubble={() => <div>bubble</div>}
          onMeasure={onMeasure}
        />
      ))
      expect(onMeasure).toHaveBeenCalledWith('m1', 30, 'k1', expect.any(Number))

      setCandidates([first, second])

      expect(onMeasure).toHaveBeenCalledWith('m2', 30, 'k2', expect.any(Number))
    }
    finally {
      cleanup()
      vi.unstubAllGlobals()
      vi.restoreAllMocks()
    }
  })
})
