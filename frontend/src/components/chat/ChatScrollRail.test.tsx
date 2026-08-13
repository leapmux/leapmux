import type { ChatScrollRailProps } from './ChatScrollRail'
import type { VirtualItem } from './useChatVirtualizer'
import type { ChatRailData } from '~/stores/chatMessageMarks'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { popoverCardPadding } from '~/styles/popover.css'
import { POINTER_CLOSE_DELAY_MS, SCRUB_WARM_DEBOUNCE_MS } from './chatDotPreview'
import { resolveScrollbarOwner } from './chatRailPolicy'
import { ChatScrollRail } from './ChatScrollRail'
import * as styles from './ChatScrollRail.css'
import { SCRUB_SEEK_DEBOUNCE_MS } from './chatScrollRailDrag'
import { rowStartSeqs } from './chatScrollRailGeometry'

// jsdom does no layout, so clientHeight is 0 everywhere. Force a fixed viewport height
// for the whole test file so the rail measures a non-zero height and the scroll metrics
// are meaningful. Restored after each test.
const VIEWPORT_H = 400
let clientHeightSpy: PropertyDescriptor | undefined

/** Flush pending microtasks (a macrotask runs after every queued microtask). */
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 0))

/** Run drag pointermove frames synchronously; jsdom has no real frame clock. */
function installImmediateRaf() {
  vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => {
    cb(0)
    return 1
  })
  vi.stubGlobal('cancelAnimationFrame', vi.fn())
}

beforeEach(() => {
  clientHeightSpy = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientHeight')
  Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => VIEWPORT_H })
})
afterEach(() => {
  if (clientHeightSpy)
    Object.defineProperty(HTMLElement.prototype, 'clientHeight', clientHeightSpy)
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

/**
 * The rail element with a rect jsdom cannot supply: top 0, height 400 (== VIEWPORT_H), so a
 * pointer's clientY maps 1:1 onto the rail-relative Y every press handler computes from it.
 * Every test that dispatches a pointer event at the rail needs this, so it lives at file scope
 * rather than being re-inlined per test.
 */
function railWithRect(container: HTMLElement): HTMLElement {
  const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
  rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
  return rail
}

/** A scroll container with defined scroll geometry (clientHeight comes from the prototype spy). */
function makeScrollEl(scrollTop = 0, scrollHeight = 500): HTMLDivElement {
  const el = document.createElement('div')
  Object.defineProperty(el, 'scrollHeight', { value: scrollHeight, configurable: true })
  el.scrollTop = scrollTop
  return el
}

// Accepts flat rail-field overrides (loaded/minSeq/maxSeq/marks/window*) for test convenience and
// assembles them into the single `rail: ChatRailData` prop, so existing `baseProps({ minSeq, maxSeq,
// marks })` call sites keep working after the prop shape moved to a ChatRailData object.
type BasePropsOverrides = Partial<Omit<ChatScrollRailProps, 'rail'>> & Partial<ChatRailData>

function baseProps(overrides: BasePropsOverrides = {}): ChatScrollRailProps {
  const items: VirtualItem[] = [1n, 2n, 3n, 4n, 5n].map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
  const { loaded, minSeq, maxSeq, marks, windowFirstSeq, windowLastSeq, hidden, ...rest } = overrides
  const rail: ChatRailData = {
    loaded: loaded ?? true,
    minSeq: minSeq ?? 1n,
    maxSeq: maxSeq ?? 5n,
    marks: marks ?? [
      { seq: 2n, type: MarkType.USER_MESSAGE },
      { seq: 4n, type: MarkType.CONTROL_RESPONSE },
    ],
    windowFirstSeq: windowFirstSeq ?? 1n,
    windowLastSeq: windowLastSeq ?? 5n,
  }
  const totalHeight = rest.totalHeight ?? 500
  // `hidden` is now resolved by the host (ChatView), not the rail. Default it here the same way
  // ChatView does -- one resolveScrollbarOwner call off the flat fields (VIEWPORT_H is the
  // content-box height the clientHeight spy reports) -- so the pre-existing render/hide tests keep
  // their intent; a test that drives hiding reactively passes `hidden` explicitly instead.
  const resolvedHidden = resolveScrollbarOwner({
    loaded: rail.loaded,
    itemCount: items.length,
    rowSeqs: rowStartSeqs(items),
    range: { minSeq: rail.minSeq, maxSeq: rail.maxSeq },
    hasMoreOlder: rest.hasMoreOlder ?? false,
    hasMoreNewer: rest.hasMoreNewer ?? false,
    totalHeight,
    viewportHeight: VIEWPORT_H,
  }) !== 'rail'
  return {
    scrollEl: makeScrollEl(),
    items,
    offsetOfIndex: i => i * 100, // totalHeight 500, 5 rows of 100px
    totalHeight: 500,
    geometryVersion: 0,
    railRowSeqs: rowStartSeqs(items), // ChatView computes this once and passes it down (see F2)
    rail,
    hidden: hidden ?? resolvedHidden,
    // Default to "the reader is scrolling", so every pre-existing test renders the rail in
    // its visible state and keeps its original intent; the auto-hide tests pass false.
    scrollActive: true,
    hasMoreOlder: false,
    hasMoreNewer: false,
    onJumpToSeq: vi.fn(),
    previewScrollTo: vi.fn(),
    ...rest,
  }
}

describe('chatScrollRail', () => {
  it('renders nothing when not loaded', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps({ loaded: false })} />)
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
  })

  it('renders nothing for an empty conversation (maxSeq 0)', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps({ minSeq: 0n, maxSeq: 0n, marks: [] })} />)
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
  })

  it('disconnects the rail ResizeObserver when the rail hides', async () => {
    const instances: { disconnect: ReturnType<typeof vi.fn> }[] = []
    class MockResizeObserver {
      disconnect = vi.fn()
      observe = vi.fn()

      constructor() {
        instances.push(this)
      }
    }
    vi.stubGlobal('ResizeObserver', MockResizeObserver)

    const [hidden, setHidden] = createSignal(false)
    const base = baseProps()
    const { container } = render(() => <ChatScrollRail {...base} hidden={hidden()} />)
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).not.toBeNull()
    const disconnectsBeforeHide = instances.reduce((sum, ro) => sum + ro.disconnect.mock.calls.length, 0)

    setHidden(true)
    await Promise.resolve()

    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
    const disconnectsAfterHide = instances.reduce((sum, ro) => sum + ro.disconnect.mock.calls.length, 0)
    expect(disconnectsAfterHide).toBeGreaterThan(disconnectsBeforeHide)
  })

  it('renders a dot per mark, centered on its seq band, with data attributes', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    expect(dots.length).toBe(2)
    // Dots sit on the thumb-CENTRE axis: fixed thumb 24px -> centre travels [12, 388].
    // dotFraction(2)=0.3 -> 12+0.3*376=124.8; dotFraction(4)=0.7 -> 275.2.
    expect((dots[0] as HTMLElement).style.top).toBe('125px')
    expect(dots[0].getAttribute('data-seq')).toBe('2')
    expect(dots[0].getAttribute('data-mark-type')).toBe(String(MarkType.USER_MESSAGE))
    expect((dots[1] as HTMLElement).style.top).toBe('275px')
    expect(dots[1].getAttribute('data-seq')).toBe('4')
    expect(dots[1].getAttribute('data-mark-type')).toBe(String(MarkType.CONTROL_RESPONSE))
  })

  it('keeps the same dot DOM nodes when maxSeq bumps without moving a dot pixel (no per-row rebuild)', () => {
    // maxSeq ticks up on every persisted row during a streaming turn. On a long history a
    // +1 seq bump rounds to the same dot pixels, so the content-compared dots memo must keep
    // the SAME array reference -- else <For> tears down and rebuilds every dot's Tooltip.
    const [maxSeq, setMaxSeq] = createSignal(100_000n)
    const marks = [
      { seq: 50_000n, type: MarkType.USER_MESSAGE },
      { seq: 75_000n, type: MarkType.CONTROL_RESPONSE },
    ]
    const base = baseProps({ minSeq: 1n, marks, hasMoreOlder: true, hasMoreNewer: true })
    const { container } = render(() => (
      <ChatScrollRail {...base} rail={{ ...base.rail, maxSeq: maxSeq() }} />
    ))
    const before = Array.from(container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]'))
    expect(before.length).toBe(2)
    setMaxSeq(100_001n) // a streamed row bumps the live tail; the dots don't move
    const after = Array.from(container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]'))
    expect(after.length).toBe(2)
    // Same element instances => <For> reused the rows, so the Tooltips were not rebuilt.
    expect(after[0]).toBe(before[0])
    expect(after[1]).toBe(before[1])
  })

  it('clusters marks that round to the same rail pixel into one dot carrying a count', () => {
    // Three marks a few seqs apart in a huge history collapse to the same pixel -- they
    // become ONE cluster of count 3 (not dropped), so none of the three is lost.
    const marks = [
      { seq: 500n, type: MarkType.USER_MESSAGE },
      { seq: 501n, type: MarkType.USER_MESSAGE },
      { seq: 502n, type: MarkType.USER_MESSAGE },
    ]
    const { container } = render(() => <ChatScrollRail {...baseProps({ minSeq: 1n, maxSeq: 100_000n, marks })} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    expect(dots.length).toBe(1)
    expect(dots[0].getAttribute('data-count')).toBe('3')
    expect(dots[0].getAttribute('aria-label')).toBe('3 messages')
    // The cluster gets the extra-ring variant class so it reads as multiple.
    expect((dots[0] as HTMLElement).className).toContain(styles.dotCluster)
  })

  it('jumps to the cluster member nearest the pixel centre on click, and warms it', () => {
    const onJumpToSeq = vi.fn()
    const warmPreview = vi.fn()
    // seqs 500..502 in [1, 100000] all round to the same pixel on the thumb-centre axis; of
    // the three, 502's exact position is nearest that pixel centre -> representative seq 502.
    const marks = [
      { seq: 500n, type: MarkType.USER_MESSAGE },
      { seq: 501n, type: MarkType.USER_MESSAGE },
      { seq: 502n, type: MarkType.USER_MESSAGE },
    ]
    const { container } = render(() => <ChatScrollRail {...baseProps({ minSeq: 1n, maxSeq: 100_000n, marks, onJumpToSeq, warmPreview })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    expect(dot.getAttribute('data-seq')).toBe('502')
    fireEvent.pointerEnter(dot)
    expect(warmPreview).toHaveBeenCalledWith(502n)
    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onJumpToSeq).toHaveBeenCalledWith(502n)
  })

  it('fires onJumpToSeq with the dot seq on a dot pointerdown', () => {
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true }))
    expect(onJumpToSeq).toHaveBeenCalledWith(2n)
  })

  it('jumps to the dot seq on keyboard Enter / Space (keyboard activation), ignoring other keys', () => {
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    dot.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onJumpToSeq).toHaveBeenLastCalledWith(2n)
    dot.dispatchEvent(new KeyboardEvent('keydown', { key: ' ', bubbles: true }))
    expect(onJumpToSeq).toHaveBeenLastCalledWith(2n)
    expect(onJumpToSeq).toHaveBeenCalledTimes(2) // exactly one jump per activation, no double
    dot.dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))
    expect(onJumpToSeq).toHaveBeenCalledTimes(2) // an unrelated key does nothing
  })

  it('attaches a rejection handler to a keyboard seek, so that path cannot leak one', async () => {
    // Every seek in the component goes through one normalising entry point. The keyboard path
    // discards the result, so a RAW call here would leave a rejected promise with no handler --
    // a console error for the reader, and an unhandled-rejection failure for any suite that
    // stubs onJumpToSeq to reject (which this file's pointer-path tests already do).
    //
    // Asserted on the thenable rather than on a real rejection: Promise.resolve() assimilates a
    // thenable by calling its `then` with BOTH callbacks, so a non-undefined second argument is
    // direct proof that a rejection handler exists. Waiting for Node to report a real unhandled
    // rejection would pass whether or not the handler is there, which proves nothing.
    const then = vi.fn()
    const onJumpToSeq = vi.fn(() => ({ then } as unknown as Promise<boolean>))
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement

    dot.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onJumpToSeq).toHaveBeenCalledWith(2n)
    await tick()
    expect(then).toHaveBeenCalledTimes(1)
    expect(typeof then.mock.calls[0][1]).toBe('function') // the onRejected half
  })

  it('ignores a keyboard activation while a drag is live (no rival seek)', () => {
    // A dot keeps keyboard focus while a pointer scrubs the rail, so Enter mid-scrub would fire
    // exactly the rival jump the two press paths already guard against -- racing the drag's
    // live-scroll and its release seek.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement

    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 300, pointerId: 1 }))
    onJumpToSeq.mockClear()
    dot.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onJumpToSeq).not.toHaveBeenCalled()

    // The guard lifts with the drag: the next activation jumps normally.
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 300, pointerId: 1 }))
    dot.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    expect(onJumpToSeq).toHaveBeenCalledWith(2n)
  })

  it.each([
    ['the track', (c: HTMLElement) => railWithRect(c)],
    ['a dot', (c: HTMLElement) => c.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement],
  ])('ignores a SECONDARY-button press on %s', (_name, target) => {
    // Every press now captures the pointer and owns the rail until its pointerup -- and a context
    // menu can swallow the pointerup of a right press, which would leave the rail owned by a
    // gesture that never ends and reject every later press. A secondary press must not start one,
    // and must keep its default so the context menu still opens.
    const setPointerCapture = vi.fn()
    HTMLElement.prototype.setPointerCapture = setPointerCapture
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const el = target(container)

    const event = new PointerEvent('pointerdown', { bubbles: true, cancelable: true, button: 2, clientY: 300, pointerId: 1 })
    el.dispatchEvent(event)
    expect(onJumpToSeq).not.toHaveBeenCalled()
    expect(setPointerCapture).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(false)
  })

  it('claims a press it REJECTS, so a rival press cannot focus a dot and strand its card', () => {
    // A rejected dot press that keeps its default focuses the dot button, and the focus opens the
    // dot's preview card -- with no pointerleave on touch to ever close it. That pins
    // activeDot() non-null, which holds the whole rail lit for the rest of the session.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const rail = railWithRect(container)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement

    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 300, pointerId: 1 }))
    const rival = new PointerEvent('pointerdown', { bubbles: true, cancelable: true, clientY: 120, pointerId: 2 })
    dot.dispatchEvent(rival)
    expect(rival.defaultPrevented).toBe(true)
  })

  it('frees the rail for the next press when a drag loses pointer capture', () => {
    // The pointerup of a captured drag does not always come back to the rail (a context menu
    // takes it, or the browser revokes the capture). Without a lostpointercapture teardown the
    // "a drag is live" guard would then reject every later press for the life of the component.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)

    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 300, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('lostpointercapture', { bubbles: true, clientY: 300, pointerId: 1 }))
    onJumpToSeq.mockClear()

    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 300, pointerId: 2 }))
    expect(onJumpToSeq).toHaveBeenCalledTimes(1) // the rail still works
  })

  it('renders the thumb from the computed seq-space rect', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb).toBeTruthy()
    // The seq-space visible span affects top projection, but rendered thumb height is fixed.
    expect(thumb.style.height).toBe('24px')
    expect(thumb.style.top).toBe('0px')
  })

  it('consumes the railRowSeqs prop rather than recomputing it (no thumb when it is null)', () => {
    // F2: ChatView computes rowStartSeqs ONCE and hands it down. The rail must use that prop, so a
    // null railRowSeqs (no server anchor) drops the thumb even though rowStartSeqs(items) would be
    // non-null -- a rail that recomputed from `items` would (wrongly) still render a thumb here.
    const { container } = render(() => <ChatScrollRail {...baseProps({ railRowSeqs: null })} />)
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).not.toBeNull() // rail still shown
    expect(container.querySelector('[data-testid="chat-scroll-rail-thumb"]')).toBeNull() // but no thumb
  })

  it('renders the fixed thumb flush to the bottom at the true bottom edge', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl: makeScrollEl(100, 500) })} />)
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb).toBeTruthy()
    expect(thumb.style.height).toBe('24px')
    expect(thumb.style.top).toBe('376px')
  })

  it('insets the track to the thumb-centre travel range (ends = where the thumb centre reaches)', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    // thumb 24px -> thumbHalf 12, so the track is inset 12px at top AND bottom (its ends
    // sit where the thumb centre can reach, not at the rail edges).
    const track = container.querySelector(`.${styles.track}`) as HTMLElement
    expect(track).toBeTruthy()
    expect(track.style.top).toBe('12px')
    expect(track.style.bottom).toBe('12px')
  })

  it('maps a track click (below the thumb) to a seq via onJumpToSeq', () => {
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    // The thumb spans 0..24px, so click at y=360 (track region) maps near the bottom -> seq 5.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360 }))
    expect(onJumpToSeq).toHaveBeenCalledWith(5n)
  })

  it('forwards a wheel over the rail to the chat scroll container (no dead zone)', () => {
    // scrollEl: scrollTop 0, scrollHeight 500, clientHeight 400 -> max scrollTop 100.
    const scrollEl = makeScrollEl(0, 500)
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl })} />)
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 60 }))
    expect(scrollEl.scrollTop).toBe(60)
    // A large downward wheel clamps at the max scroll rather than overrunning.
    rail.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 9999 }))
    expect(scrollEl.scrollTop).toBe(100)
  })

  it('forwards wheel intent to the chat scroll container so edge pagination still runs', () => {
    const scrollEl = makeScrollEl(100, 500)
    const onScrollWheel = vi.fn()
    scrollEl.addEventListener('wheel', onScrollWheel)
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl })} />)
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement

    rail.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 60 }))

    expect(onScrollWheel).toHaveBeenCalledTimes(1)
    expect(onScrollWheel.mock.calls[0][0].deltaY).toBe(60)
  })

  it('normalizes line-mode and page-mode wheel deltas to pixels', () => {
    // Line mode (deltaMode 1): 3 lines * WHEEL_LINE_PX(16) = 48px.
    const lineEl = makeScrollEl(0, 500)
    const { container: c1 } = render(() => <ChatScrollRail {...baseProps({ scrollEl: lineEl })} />)
    ;(c1.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement)
      .dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 3, deltaMode: 1 }))
    expect(lineEl.scrollTop).toBe(48)
    // Page mode (deltaMode 2): 1 page * clientHeight(400), clamped at max scroll (100).
    const pageEl = makeScrollEl(0, 500)
    const { container: c2 } = render(() => <ChatScrollRail {...baseProps({ scrollEl: pageEl })} />)
    ;(c2.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement)
      .dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 1, deltaMode: 2 }))
    expect(pageEl.scrollTop).toBe(100)
  })

  it('drags the thumb: live-scrolls in-window on grab and seeks the mapped seq on release', () => {
    // jsdom has no pointer capture; stub it so startDrag doesn't throw.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const previewScrollTo = vi.fn()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ previewScrollTo, onJumpToSeq })} />
    ))
    const rail = railWithRect(container)
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    previewScrollTo.mockClear()
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    // Move to the axis midpoint y=200 -> fraction 0.5 -> seqF = 1 + 0.5*4 = 3.
    // Seq 3 is in the loaded window, so the drag live-scrolls to its content-Y (row 2 top = 200px).
    expect(previewScrollTo).toHaveBeenCalledWith(200)
    // Release at the same y -> fractionToSeq(0.5, 1, 5) = 3.
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenCalledWith(3n)
  })

  it('fires onSeekInterrupt on every grab, and again once the press becomes a scrub', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onSeekInterrupt = vi.fn()
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onSeekInterrupt })} />
    ))
    const rail = railWithRect(container)
    // Every press is a grab now, so every press abandons a PRIOR release's still-fetching
    // out-of-window seek -- it must not land and yank the viewport under this gesture.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    expect(onSeekInterrupt).toHaveBeenCalledTimes(1)
    // Dragging past the slop abandons a second seek: the one THIS press just issued, which the
    // reader now scrubbed away from.
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(onSeekInterrupt).toHaveBeenCalledTimes(2)
    // Engaging is one-way: later moves do not keep re-firing it.
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 150, pointerId: 1 }))
    expect(onSeekInterrupt).toHaveBeenCalledTimes(2)
  })

  it('a track press jumps to the pressed point AND scrubs on from there', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const previewScrollTo = vi.fn()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewScrollTo, onJumpToSeq })} />)
    const rail = railWithRect(container)
    // Press the bare track near the bottom (the thumb spans 0..24) -> jump to seq 5 at once.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenCalledWith(5n)
    // The thumb centres UNDER the press (top = 360 - half the 24px thumb), rather than holding a
    // within-thumb offset the way a thumb grab does -- the reader pressed a position.
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb.style.top).toBe('348px')
    // Without lifting: the same press now scrubs. This is the gesture a finger has no way to
    // spend two presses on.
    previewScrollTo.mockClear()
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(thumb.style.top).toBe('188px') // follows the pointer
    expect(previewScrollTo).toHaveBeenCalledWith(200) // seq 3 is in-window -> live-scroll
    // The release lands on the scrubbed-to position, not on the pressed one.
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenLastCalledWith(3n)
    expect(onJumpToSeq).toHaveBeenCalledTimes(2)
  })

  it('a track press released without a scrub seeks exactly once', () => {
    // The press already jumped. A second seek from the release would fetch the same page twice.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    // A finger drifts a pixel or two as it lifts; that is still a tap, not a scrub.
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 363, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 363, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    expect(onJumpToSeq).toHaveBeenCalledWith(5n)
    // And the thumb stays on the point the press jumped to, rather than sliding to the drift.
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb.style.top).toBe('348px')
  })

  it('holds a tapped thumb until the PRESS jump lands, without seeking again', async () => {
    // A tap fires no release seek, so the hold has to pin the thumb on the press's own jump
    // instead. Without that, the thumb would snap back to its pre-tap position the instant the
    // finger lifts and then jump again when the fetch landed -- two moves for one tap.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    let landSeek!: (scrolled: boolean) => void
    const onJumpToSeq = vi.fn(() => new Promise<boolean>((r) => {
      landSeek = r
    }))
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onJumpToSeq, hasMoreOlder: true, hasMoreNewer: true })} />
    ))
    const rail = railWithRect(container)
    // Tap the track: press and release on the same pixel.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 360, pointerId: 1 }))
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb.className).toContain(styles.thumbDragging) // pinned while the fetch is in flight
    expect(thumb.style.top).toBe('348px') // and pinned on the TAPPED point
    expect(onJumpToSeq).toHaveBeenCalledTimes(1) // the release added no second seek
    // The pin SURVIVES the settle a resolved seek clears on: it still waits for the
    // press's fetch, not clearing itself the moment the pointer lifted.
    await tick()
    expect(thumb.className).toContain(styles.thumbDragging)
    // The press's own seek resolves, so the pin hands off rather than sticking forever.
    landSeek(false)
    await tick()
    expect(thumb.className).not.toContain(styles.thumbDragging)
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
  })

  it('hands the thumb off ONE FRAME after a seek that reports it scrolled', async () => {
    // A landing follows a seek that scrolled, so the pin must survive until the
    // metrics-derived thumb has caught up to it -- one animation frame. Clearing on the seek's
    // microtask instead would flash the thumb back for that frame.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    // A DEFERRED rAF: the hold's hand-off frame must be observable as a separate step.
    const rafQueue: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => rafQueue.push(cb))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    const flushRaf = () => rafQueue.splice(0).forEach(cb => cb(0))
    const onJumpToSeq = vi.fn(() => Promise.resolve(true)) // the landing scrolled
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onJumpToSeq, hasMoreOlder: true, hasMoreNewer: true })} />
    ))
    const rail = railWithRect(container)
    // Grab the thumb and release far away: the release position alone engages the scrub.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb.className).toContain(styles.thumbDragging)
    await tick()
    // The seek resolved, but the hand-off waits for the frame -- unlike a seek that scrolled
    // NOWHERE, which clears on the spot (covered by its own test below).
    expect(thumb.className).toContain(styles.thumbDragging)
    flushRaf()
    expect(thumb.className).not.toContain(styles.thumbDragging)
  })

  it('clears the pinned thumb when a seek REJECTS, instead of sticking in the dragging state', async () => {
    // A rejected jump (the fetch failed, the tab went away mid-flight) resolves the hold's
    // hand-off to "did not scroll". Without that the thumb would stay pinned at the release
    // position for the rest of the session, and the rejection would go unhandled.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn(() => Promise.reject(new Error('fetch failed')))
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onJumpToSeq, hasMoreOlder: true, hasMoreNewer: true })} />
    ))
    const rail = railWithRect(container)
    // A TAP on the track: the hold waits on the PRESS's seek, which is the one that rejects.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 360, pointerId: 1 }))
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    expect(thumb.className).toContain(styles.thumbDragging)
    await tick()
    expect(thumb.className).not.toContain(styles.thumbDragging)
  })

  it('survives a settle seek that rejects, and keeps scrubbing', async () => {
    // The settle seek is fire-and-forget -- no hold awaits it -- so it is the one seek whose
    // rejection nothing else would handle. An unhandled rejection here would surface as a
    // console error mid-scrub (and fails this suite outright), and the scrub must carry on.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    vi.useFakeTimers()
    // A DEFERRED rAF (drained by flushRaf), not installImmediateRaf: this test dispatches two
    // moves, and a synchronous rAF leaves the coalescer's rafId set from its own return value,
    // which swallows the second push. See the fast-scrub coalescing test below.
    const rafQueue: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => rafQueue.push(cb))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    const flushRaf = () => rafQueue.splice(0).forEach(cb => cb(0))
    const onJumpToSeq = vi.fn(() => Promise.reject(new Error('fetch failed')))
    const previewScrollTo = vi.fn()
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({
        onJumpToSeq,
        previewScrollTo,
        maxSeq: 100_000n,
        marks: [],
        windowFirstSeq: 99_000n,
        windowLastSeq: 100_000n,
        hasMoreOlder: true,
      })}
      />
    ))
    const rail = railWithRect(container)
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    flushRaf()
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    flushRaf()
    vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(0) // let the rejection settle
    // The scrub is unharmed: the thumb still tracks the pointer.
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    const beforeTop = thumb.style.top
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 300, pointerId: 1 }))
    flushRaf()
    expect(thumb.style.top).not.toBe(beforeTop)
  })

  it('still jumps when the browser refuses pointer capture', () => {
    // The press action stands on its own. A browser that rejects setPointerCapture (an
    // already-released pointer, a stale id) loses the SCRUB, but a press that stopped jumping
    // as well would leave the rail with no working press at all.
    HTMLElement.prototype.setPointerCapture = vi.fn(() => {
      throw new DOMException('pointer is no longer active', 'NotFoundError')
    })
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    expect(() => {
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    }).not.toThrow()
    expect(onJumpToSeq).toHaveBeenCalledWith(5n)
    // The failed grab also freed the guard, so the NEXT press is not locked out.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 2 }))
    expect(onJumpToSeq).toHaveBeenCalledTimes(2)
  })

  it('a dot press jumps to the dot seq, centres the thumb on the FINGER, and scrubs on', () => {
    // On a coarse pointer each dot carries a 24px hit circle, so on a marked conversation most
    // of the rail is dot rather than track. A dot press that could not scrub would leave a
    // finger with nowhere to start the gesture.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="2"]') as HTMLElement
    // Press the dot slightly off its centre (a fingertip is wider than the 6px dot).
    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 131, pointerId: 1 }))
    // The jump takes the dot's OWN seq, not the fraction under the finger.
    expect(onJumpToSeq).toHaveBeenCalledWith(2n)
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    // The thumb centres on the PRESS (131), not on the dot it hit (125). The drag keeps the
    // finger-to-thumb offset it starts with, so centring on the dot would hold the thumb 6px off
    // the finger for the whole scrub below -- and up to 12px, half a coarse hit circle, in
    // general. Only a dot press could do that, so it also gave one gesture two different feels.
    expect(thumb.style.top).toBe('119px') // 131 - half the 24px thumb
    // The same press scrubs on to the other dot and lands there.
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 281, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 281, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenLastCalledWith(4n)
  })

  it('tracks the finger 1:1 after a dot press, with no residual offset from the dot', () => {
    // The regression this guards: the thumb used to anchor on the dot while the grab offset
    // anchored on the finger, so every later frame of the scrub stayed off by the gap between
    // them. Press well off the dot centre, then move a known distance and check the thumb moved
    // exactly that far AND sits under the finger -- which pins both halves at once.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const rail = railWithRect(container)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="2"]') as HTMLElement
    // Read the top as a NUMBER: the pixel goes through a [0,1] fraction and back, so the
    // round-trip lands a float ULP off the whole pixel it means.
    const thumbTop = () => Number.parseFloat((container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement).style.top)

    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 137, pointerId: 1 }))
    expect(thumbTop()).toBeCloseTo(125, 6) // 137 - 12: under the finger, 12px below the dot at 125
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 237, pointerId: 1 }))
    expect(thumbTop()).toBeCloseTo(225, 6) // moved exactly the finger's 100px, still centred on it
  })

  it('a dot press released without a scrub seeks its dot exactly once', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="2"]') as HTMLElement
    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 125, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 125, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    expect(onJumpToSeq).toHaveBeenCalledWith(2n)
  })

  it('opens the dot preview on a dot press, with no hover to open it', () => {
    // A touch has no hover: the card must come from the press itself (the thumb lands on the
    // dot, so the scrub target resolves to it) or a finger would never see a preview.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const previewFor = (seq: bigint) => (seq === 2n ? 'pressed message two' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    railWithRect(container) // the component reads the rail's rect on the dot press below
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="2"]') as HTMLElement
    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 125, pointerId: 1 }))
    const previews = container.querySelectorAll('[data-testid="chat-scroll-rail-preview"]')
    expect(previews.length).toBe(1)
    expect(previews[0]).toHaveTextContent('pressed message two')
  })

  it('ignores a dot press while a drag is live (no rival seek)', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ onJumpToSeq })} />)
    const rail = railWithRect(container)
    // A first pointer grabs the thumb (spans 0..24) -> a live drag is in progress.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    // A SECOND finger lands on a dot mid-drag: without the guard it would fire a rival jump
    // that races the drag's live-scroll and its release seek.
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="4"]') as HTMLElement
    dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 275, pointerId: 2 }))
    expect(onJumpToSeq).not.toHaveBeenCalled()
  })

  it('seeks an out-of-window position the scrub settles on, so the view follows the thumb', () => {
    // Out of the loaded window there is nothing to live-scroll to. Without the settle seek the
    // thumb and the dot preview would move over a transcript that never follows.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    vi.useFakeTimers()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    // A long history whose loaded window holds only its tail (seqs 1..5 of 1..100000).
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({
        onJumpToSeq,
        maxSeq: 100_000n,
        marks: [],
        windowFirstSeq: 99_000n,
        windowLastSeq: 100_000n,
        hasMoreOlder: true,
      })}
      />
    ))
    const rail = railWithRect(container)
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    onJumpToSeq.mockClear() // the grab itself is a thumb grab: it jumps nowhere
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(onJumpToSeq).not.toHaveBeenCalled() // a fly-over must not fetch
    vi.advanceTimersByTime(SCRUB_SEEK_DEBOUNCE_MS)
    // Rail fraction 0.5 over [1, 100000] -> seq 50000ish; assert it landed inside the history
    // rather than pinning an exact rounding.
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    const [seq] = onJumpToSeq.mock.calls[0] as [bigint]
    expect(seq).toBeGreaterThan(49_000n)
    expect(seq).toBeLessThan(51_000n)
  })

  it('ignores a second pointerdown on the track while a thumb-drag is live (no rival seek)', () => {
    // jsdom has no pointer capture; stub it so startDrag doesn't throw.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onJumpToSeq })} />
    ))
    const rail = railWithRect(container)
    // A first pointer grabs the thumb (spans 0..24) -> a live drag is in progress.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    // A SECOND pointer lands on the TRACK (below the thumb) mid-drag: without the drag guard it
    // would fire a rival onJumpToSeq that races the drag's live-scroll and its release seek.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 2 }))
    expect(onJumpToSeq).not.toHaveBeenCalled()
    // The original drag still releases normally into exactly ONE seek.
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
  })

  it('holds the thumb through an ambient metrics change while the seek is in flight (no early flash)', async () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    // A pending (out-of-window) seek: it awaits a fetch before the landing scrolls.
    let landSeek!: (scrolled: boolean) => void
    const onJumpToSeq = vi.fn(() => new Promise<boolean>((r) => {
      landSeek = r
    }))
    const [geometryVersion, setGeometryVersion] = createSignal(0)
    // hasMoreOlder/Newer so the thumb isn't a full-height (hidden) thumb, and there's remote
    // history the drag could target out-of-window (where the flash was worst).
    const scrollEl = makeScrollEl(0, 500)
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ scrollEl, onJumpToSeq, hasMoreOlder: true, hasMoreNewer: true })} geometryVersion={geometryVersion()} />
    ))
    const rail = railWithRect(container)
    // Grab the fixed thumb (spans 0..24) and release at a DIFFERENT position.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    // Held after release: the thumb did NOT revert to its metrics-derived (pre-drag) position.
    expect(thumb.className).toContain(styles.thumbDragging)
    // The window swaps / streaming commits WHILE the seek's fetch is still in flight -- an
    // ambient metrics change. The OLD hold cleared on the first such change, flashing the thumb
    // back before the landing; the fix keeps it pinned until the seek itself resolves.
    scrollEl.scrollTop = 100
    setGeometryVersion(1)
    await Promise.resolve()
    expect(thumb.className).toContain(styles.thumbDragging) // still held -- no early hand-off
    // The seek finally resolves (here scrolled=false: the landing had nowhere to scroll), so the
    // hold clears on the seek's own resolution and the thumb hands off rather than staying stuck.
    landSeek(false)
    await tick()
    expect(thumb.className).not.toContain(styles.thumbDragging)
  })

  it('clears the held thumb when the release-seek scrolls nowhere (no stuck dragging state)', async () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    // The seek resolves false: the landing produced no scroll (target already at this scrollTop,
    // or no landable row), so no landing scroll -- and thus no clear frame -- will come. The
    // hold must clear on the seek's own resolution instead, or the thumb stays stuck dragging.
    const onJumpToSeq = vi.fn(() => Promise.resolve(false))
    const scrollEl = makeScrollEl(0, 500)
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ scrollEl, onJumpToSeq, hasMoreOlder: true, hasMoreNewer: true })} />
    ))
    const rail = railWithRect(container)
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
    const thumb = container.querySelector('[data-testid="chat-scroll-rail-thumb"]') as HTMLElement
    // Held immediately after release (the anti-flash hold is armed)...
    expect(thumb.className).toContain(styles.thumbDragging)
    expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    // ...but once the seek resolves with scrolled=false, the fallback clears the hold even
    // though no metrics change ever fires -- so the thumb can't stay stuck dragging forever.
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(thumb.className).not.toContain(styles.thumbDragging)
  })

  it('ignores a second concurrent grab while a drag is live (no orphaned listener set)', () => {
    const capture = vi.fn()
    HTMLElement.prototype.setPointerCapture = capture
    const { container } = render(() => <ChatScrollRail {...baseProps({ hasMoreOlder: true, hasMoreNewer: true })} />)
    const rail = railWithRect(container)
    // First grab on the fixed thumb (spans 0..24) captures pointer 1.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    expect(capture).toHaveBeenCalledTimes(1)
    // A second finger lands on the thumb mid-drag: the guard drops it (no rival capture/listeners).
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 14, pointerId: 2 }))
    expect(capture).toHaveBeenCalledTimes(1)
    // The first drag releases, freeing the guard; a fresh grab then captures again.
    rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 14, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 3 }))
    expect(capture).toHaveBeenCalledTimes(2)
  })

  it('cancels an active thumb drag when the rail hides', async () => {
    const capture = vi.fn()
    const release = vi.fn()
    HTMLElement.prototype.setPointerCapture = capture
    HTMLElement.prototype.releasePointerCapture = release
    installImmediateRaf()
    const [hidden, setHidden] = createSignal(false)
    const base = baseProps({ hasMoreOlder: true, hasMoreNewer: true })
    const { container } = render(() => (
      <ChatScrollRail {...base} hidden={hidden()} />
    ))
    let rail = railWithRect(container)

    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    expect(capture).toHaveBeenCalledTimes(1)

    setHidden(true)
    await Promise.resolve()

    expect(release).toHaveBeenCalledWith(1)
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()

    setHidden(false)
    await Promise.resolve()

    rail = railWithRect(container)
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 2 }))
    expect(capture).toHaveBeenCalledTimes(2)
  })

  it('reveals the preview card for the dot the thumb passes over while dragging, and warms it after it settles', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    vi.useFakeTimers()
    installImmediateRaf() // override the faked rAF with a synchronous one for the drag frames
    const warmPreview = vi.fn()
    const previewFor = (seq: bigint) => (seq === 2n ? 'scrubbed message two' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ warmPreview, previewFor })} />)
    const rail = railWithRect(container)
    // No card until a drag is in progress (and nothing is hovered).
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
    // Grab the fixed thumb, then scrub to the seq-2 dot at y=125.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 125, pointerId: 1 }))
    const preview = container.querySelectorAll('[data-testid="chat-scroll-rail-preview"]')
    expect(preview.length).toBe(1) // never two cards
    expect(preview[0]).toHaveTextContent('scrubbed message two')
    // The scrub warm is DEBOUNCED: nothing is fetched until the thumb settles on the dot, so a
    // fast fly-over doesn't fire a GetAgentMessage RPC per dot crossed.
    expect(warmPreview).not.toHaveBeenCalled()
    vi.advanceTimersByTime(SCRUB_WARM_DEBOUNCE_MS)
    expect(warmPreview).toHaveBeenCalledWith(2n)
  })

  it('coalesces a fast scrub: only the dot the thumb settles on is warmed, not the ones flown over', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    vi.useFakeTimers()
    // A DEFERRED rAF (drained by flushRaf) rather than the synchronous installImmediateRaf: the
    // coalescer resets its rafId inside its dispatch, so back-to-back moves in one synchronous run
    // need a real frame boundary between them for BOTH to apply (a synchronous rAF leaves rafId
    // set from its own return value, swallowing the second push).
    const rafQueue: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => rafQueue.push(cb))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())
    const flushRaf = () => rafQueue.splice(0).forEach(cb => cb(0))
    const warmPreview = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ warmPreview })} />)
    const rail = railWithRect(container)
    // Grab the thumb, pass OVER the seq-2 dot (y=125), then move on to seq-4 (y=275) BEFORE the
    // debounce elapses -- the second move supersedes the first dot's pending warm.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    flushRaf()
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 125, pointerId: 1 }))
    flushRaf()
    vi.advanceTimersByTime(SCRUB_WARM_DEBOUNCE_MS - 20) // not yet settled on seq-2
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 275, pointerId: 1 }))
    flushRaf()
    vi.advanceTimersByTime(SCRUB_WARM_DEBOUNCE_MS)
    expect(warmPreview).toHaveBeenCalledTimes(1)
    expect(warmPreview).toHaveBeenCalledWith(4n) // only the settled dot, never the flown-over seq-2
  })

  it('shows no preview card when the dragging thumb is between dots', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const rail = railWithRect(container)
    // y=200 -> thumb centre 200; dots sit at 125 and 275, both >12px away -> no scrub target.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('shows ONE card (the scrub target wins) when a dot is hovered while scrubbing', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const previewFor = (seq: bigint) => (seq === 2n ? 'scrub target two' : seq === 4n ? 'hovered four' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const rail = railWithRect(container)
    // Scrub over the seq-2 dot (thumb centre 125), then also hover the seq-4 dot.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 125, pointerId: 1 }))
    const dot4 = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="4"]') as HTMLElement
    fireEvent.pointerEnter(dot4)
    const previews = container.querySelectorAll('[data-testid="chat-scroll-rail-preview"]')
    expect(previews.length).toBe(1) // exactly one card, no double
    expect(previews[0]).toHaveTextContent('scrub target two') // scrub wins over the hover
    expect(previews[0]).not.toHaveTextContent('hovered four')
  })

  it('warms a mark preview on dot hover, keyed by the dot seq', () => {
    const warmPreview = vi.fn()
    const { container } = render(() => <ChatScrollRail {...baseProps({ warmPreview })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    expect(warmPreview).toHaveBeenCalledWith(2n)
  })

  it('labels each dot for accessibility by its mark type', () => {
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    expect(dots[0].getAttribute('aria-label')).toBe('Your message')
    expect(dots[1].getAttribute('aria-label')).toBe('Your response')
  })

  it('describes a focused dot by the preview card its focus opened', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'the first message' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    expect(dots[0].getAttribute('aria-describedby')).toBeNull()

    // This rail gives FOCUS its own open channel, so the card is a surface the keyboard reaches by
    // design. Without the description a screen-reader user hears "Your message" and never the
    // message -- the whole content the feature exists to show.
    fireEvent.focus(dots[0])
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]')!
    expect(card.id).not.toBe('')
    expect(dots[0].getAttribute('aria-describedby')).toBe(card.id)
    expect(card.textContent).toContain('the first message')
    // Only the dot the card actually describes claims it.
    expect(dots[1].getAttribute('aria-describedby')).toBeNull()

    fireEvent.blur(dots[0])
    expect(dots[0].getAttribute('aria-describedby')).toBeNull()
  })

  it('hides the rail when the whole conversation is loaded and fits (thumb would be full)', () => {
    // clientHeight (400) >= totalHeight requires content that fits; use a short total.
    const items: VirtualItem[] = [1n, 2n].map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
    const { container } = render(() => (
      <ChatScrollRail
        {...baseProps({
          items,
          offsetOfIndex: i => i * 100,
          totalHeight: 200, // < clientHeight (400): no scroll
          scrollEl: makeScrollEl(0, 400),
          minSeq: 1n,
          maxSeq: 2n,
          windowFirstSeq: 1n,
          windowLastSeq: 2n,
          hasMoreOlder: false,
          hasMoreNewer: false,
        })}
      />
    ))
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
  })

  it('keeps the rail visible when a big seq gap makes the seq-space share ~1 but the content overflows in pixels', () => {
    // Whole conversation loaded (no more older/newer), only two visible server rows: a tiny
    // seq-1 row and a huge seq-1000 row (seqs 2..999 deleted/hidden absorb into the gap). The
    // seq-space visibleFraction rounds to ~1 (the loaded window covers ~the whole seq range),
    // but the content overflows the viewport in PIXELS (2020px > 400px), so a scrollbar IS
    // needed. The rail must stay visible -- else, with the native scrollbar hidden by
    // hideNativeScrollbar, the viewport would have no usable scrollbar at all.
    const items: VirtualItem[] = [1n, 1000n].map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
    const { container } = render(() => (
      <ChatScrollRail
        {...baseProps({
          items,
          offsetOfIndex: i => i * 20, // row0 [0,20], row1 [20,2020]
          totalHeight: 2020, // >> clientHeight (400): overflows in pixels
          scrollEl: makeScrollEl(0, 2020),
          minSeq: 1n,
          maxSeq: 1000n,
          windowFirstSeq: 1n,
          windowLastSeq: 1000n,
          hasMoreOlder: false,
          hasMoreNewer: false,
        })}
      />
    ))
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).not.toBeNull()
  })

  it('keeps the rail visible when the loaded content fits but older history remains off-window', () => {
    // The pixel-fits self-hide is gated on the WHOLE conversation being loaded
    // (!hasMoreOlder && !hasMoreNewer). Here the loaded window fits the viewport in pixels
    // (200px < 400px) yet older history exists off-window, so the rail must stay visible --
    // it is the only way to jump into that unloaded history.
    const items: VirtualItem[] = [10n, 11n, 12n, 13n, 14n].map((seq, i) => ({ id: `m${i}`, hasSpanLines: false, seq }))
    const { container } = render(() => (
      <ChatScrollRail
        {...baseProps({
          items,
          offsetOfIndex: i => i * 40,
          totalHeight: 200, // < clientHeight (400): the loaded window fits in pixels
          scrollEl: makeScrollEl(0, 400),
          minSeq: 1n, // whole-history floor is far below the loaded window
          maxSeq: 14n,
          windowFirstSeq: 10n,
          windowLastSeq: 14n,
          hasMoreOlder: true,
          hasMoreNewer: false,
        })}
      />
    ))
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).not.toBeNull()
  })
})

describe('chatScrollRail dot preview card', () => {
  /** Hover the first dot -- the card opens IMMEDIATELY (no show-delay), and returns it. */
  function hoverFirstDot(container: HTMLElement): HTMLElement | null {
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    return container.querySelector('[data-testid="chat-scroll-rail-preview"]')
  }

  it('insets the preview card by the shared popover-card padding, not by a copy of it', () => {
    // This card is where that value came from, and every card popover in the app now carries
    // the same class. A literal here instead would be the second source of truth that the
    // shared class exists to remove. jsdom loads no stylesheet, so the class list is the only
    // thing a unit test can see -- the resolved pixels are asserted in
    // tests/e2e/036-dropdown-popover.spec.ts.
    expect(styles.previewCard.split(' ')).toContain(popoverCardPadding)
  })

  it('places the card on the Y the controller resolved for its dot', () => {
    // The clamp that keeps a near-edge card off the overflow-hidden wrapper reads the card's
    // MEASURED height, and jsdom implements neither layout nor ResizeObserver -- so here the card
    // reports height 0 and the clamp correctly resolves to the dot's own Y. What this pins is the
    // WIRING: the rail hands cardTopPx to the card and the card writes it. The clamp's own
    // arithmetic, which needs a height to be worth asserting, is unit-tested against injected
    // heights in chatDotPreview.test.ts.
    const marks = [{ seq: 1n, type: MarkType.USER_MESSAGE }]
    const { container } = render(() => <ChatScrollRail {...baseProps({ minSeq: 1n, maxSeq: 100_000n, marks })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    expect(dot.style.top).toBe('12px') // with the fixed 24px thumb, the seq-1 dot sits at ~12px
    fireEvent.pointerEnter(dot)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    expect(card.style.top).toBe('12px')
  })

  it('opens the card immediately on hover and renders the resolved preview as markdown', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? '**jump** to this message' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const card = hoverFirstDot(container)! // no timers advanced -- it's immediate
    expect(card).toHaveTextContent('jump to this message')
    // The markdown is rendered, not shown as raw source: **jump** becomes a bold element.
    const strong = card.querySelector('strong, b')
    expect(strong?.textContent).toBe('jump')
    expect(card.textContent).not.toContain('**')
  })

  it('keeps the card open for the close delay after the pointer leaves the dot, then closes it', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'hi there' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
    fireEvent.pointerLeave(dot)
    // The card sits a gutter away from the rail, so the pointer is over neither for a moment.
    // Closing on the leave would put the card's selectable text out of reach.
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS - 1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
    vi.advanceTimersByTime(1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('keeps the card open while the pointer rests on it, and closes it a delay after it leaves', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'hi there' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    // The pointer crosses to the card: leave the dot, arrive on the card.
    fireEvent.pointerLeave(dot)
    fireEvent.pointerEnter(card)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10) // a reader takes as long as they like
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    fireEvent.pointerLeave(card)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS - 1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
    vi.advanceTimersByTime(1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('holds the card open while a press inside it drags a selection past its edge', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)
    // Press inside the card, then drag out of it -- what selecting to the end of a line does.
    fireEvent.pointerDown(card)
    fireEvent.pointerLeave(card)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    // Closing here would destroy the selection the reader is still making.
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    // The button comes up outside the card, so nothing holds it: the usual delay, then closed.
    fireEvent.pointerUp(window)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS - 1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
    vi.advanceTimersByTime(1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('keeps a FOCUSED dot card open when the pointer visits the card and leaves again', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'hi there' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    // A keyboard reader tabbed to the dot, then reached for the mouse.
    fireEvent.focus(dot)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    fireEvent.pointerEnter(card)
    fireEvent.pointerLeave(card)
    // The pointer let go, but focus never did. One shared channel would have closed the card
    // here and left a focused dot with nothing to show.
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    fireEvent.blur(dot)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('a press inside the card selects text instead of starting a rail drag', () => {
    const capture = vi.fn()
    HTMLElement.prototype.setPointerCapture = capture
    const onJumpToSeq = vi.fn()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor, onJumpToSeq })} />)
    railWithRect(container)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    // The card renders INSIDE the rail, so an unhandled press here would reach the rail's own
    // pointerdown and grab the thumb at the card's Y.
    const press = new PointerEvent('pointerdown', { bubbles: true, cancelable: true, clientY: 40, pointerId: 1 })
    card.dispatchEvent(press)
    expect(capture).not.toHaveBeenCalled()
    expect(onJumpToSeq).not.toHaveBeenCalled()
    // And the default stands, because the default here IS the text selection.
    expect(press.defaultPrevented).toBe(false)
  })

  /** Open the first dot's card, then move the pointer across the gutter onto the card. */
  function cardUnderPointer(container: HTMLElement): { dot: HTMLElement, card: HTMLElement } {
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    fireEvent.pointerLeave(dot)
    fireEvent.pointerEnter(card)
    return { dot, card }
  }

  /** The first text node inside `el` -- the only kind of node a selection can anchor in. */
  function textNodeIn(el: HTMLElement): Node {
    const node = document.createTreeWalker(el, NodeFilter.SHOW_TEXT).nextNode()
    if (!node)
      throw new Error('the element holds no text to select')
    return node
  }

  /** A message in the transcript the card floats over: real text, outside the rail. */
  function transcriptText(container: HTMLElement): HTMLElement {
    const el = document.createElement('p')
    el.textContent = 'a message the card lies over'
    container.append(el)
    return el
  }

  /**
   * Put a real, non-collapsed document selection anchored in `from`'s text and reaching `to`'s.
   *
   * Both ends are given, because WHERE a drag starts and ends is the whole question here. The
   * browser extends a selection to wherever the pointer goes, so a select-to-the-end-of-a-line
   * drag that starts in the card routinely ENDS outside it.
   *
   * setBaseAndExtent, not a Range: a Range must run forwards in document order, and it silently
   * COLLAPSES when it does not -- which would leave these tests asserting against an empty
   * selection that holds nothing, whichever way the code behaved. A real drag has no such rule,
   * and anchor-then-focus is the shape the card's own hold reads.
   */
  function selectTextFrom(from: HTMLElement, to: HTMLElement = from) {
    const anchor = textNodeIn(from)
    const focus = textNodeIn(to)
    const selection = document.getSelection()!
    selection.setBaseAndExtent(anchor, 0, focus, focus.textContent!.length)
    expect(selection.isCollapsed, 'the test needs a real, non-empty selection').toBe(false)
    fireEvent(document, new Event('selectionchange'))
  }

  /** Collapse the document selection, as the reader's next click anywhere would. */
  function clearSelection() {
    document.getSelection()!.removeAllRanges()
    fireEvent(document, new Event('selectionchange'))
  }

  it('opens no press hold for a SECONDARY button, whose pointerup a context menu can swallow', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)

    // A right-press to reach the browser's own "Copy". The native menu regularly eats the matching
    // pointerup, so a hold opened here would never end -- pinning the card over the transcript and
    // the whole rail lit for the rest of the session. beginRailPress guards the rail the same way.
    card.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, cancelable: true, button: 2, pointerId: 1 }))
    fireEvent.pointerLeave(card)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('ignores a rival pointer\'s release while THIS pointer is still selecting', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)
    card.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, cancelable: true, button: 0, pointerId: 1 }))
    fireEvent.pointerLeave(card)

    // A pen tap or a second finger elsewhere, on the hybrid devices where the card stays
    // interactive. Its release must not end a press it never started, or the card closes under a
    // selection drag that pointer 1 is still making.
    window.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerId: 2 }))
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    window.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, pointerId: 1 }))
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('takes the hold from a PRESS on a card that opened under a stationary cursor', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    // A keyboard reader tabs to the dot; the card mounts under a cursor that is ALREADY parked
    // where it appears, so no pointerenter ever fires and the card never learns it is hovered.
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.focus(dot)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement

    // The press is the first evidence the pointer is on the card. Without it the card takes no
    // hold, so the blur below drops the only channel holding it and the card goes mid-selection.
    fireEvent.pointerDown(card)
    fireEvent.blur(dot)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    // And the press must leave the card HOVERED, not merely re-opened: releasing without moving
    // leaves the pointer sitting on the card, and a card that forgot that would close under it.
    fireEvent.pointerUp(window)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    // Leaving is what releases it, exactly as for a card the pointer arrived on normally.
    fireEvent.pointerLeave(card)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('holds the card for as long as it holds the reader\'s SELECTION, not for a fixed delay', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)

    // Select to the end of a line: press inside, drag past the card's edge, release outside. The
    // browser extends the selection to where the pointer went, so it ENDS on the rail outside the
    // card -- a hold that demanded both ends inside would miss the very drag it exists for.
    fireEvent.pointerDown(card)
    selectTextFrom(card, transcriptText(container))
    fireEvent.pointerLeave(card)
    fireEvent.pointerUp(window)

    // Closing on that release would destroy the selection with the text nodes it points at,
    // before the reader could reach a keyboard and copy it.
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()

    // The reader's next click anywhere collapses the selection, and the card lets go.
    clearSelection()
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS - 1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
    vi.advanceTimersByTime(1)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('takes no selection hold from a selection that started somewhere else', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)

    // A selection made in the transcript can sweep across an open card, and its range then covers
    // the card's text. That is the reader selecting the MESSAGE, not the card, so the card must
    // still close when they leave it -- keying on the anchor rather than the range is what
    // separates the two.
    fireEvent.pointerDown(card)
    selectTextFrom(transcriptText(container), card)
    fireEvent.pointerLeave(card)
    fireEvent.pointerUp(window)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('closes on a release that left NO selection behind, with no extra hold to wait out', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)

    // A plain click inside the card selects nothing. The selection hold must not latch on an
    // empty selection, or a card would outlive every press that merely touched it.
    fireEvent.pointerDown(card)
    fireEvent.pointerLeave(card)
    fireEvent.pointerUp(window)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('keeps the card on ITS dot while a selection drag crosses the dots beside it', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'message two' : seq === 4n ? 'message four' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    fireEvent.pointerEnter(dots[0])
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    fireEvent.pointerLeave(dots[0])
    fireEvent.pointerEnter(card)

    fireEvent.pointerDown(card)
    // The card's right edge is a gutter away from the dots, so selecting to the end of a line
    // drags the pointer straight across them. Re-targeting the card here would swap its body under
    // the reader's own selection -- and the selection dies with the text nodes it pointed at.
    fireEvent.pointerLeave(card)
    fireEvent.pointerEnter(dots[1])
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toHaveTextContent('message two')
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toHaveTextContent('message four')

    // Once the press ends the dots have their say again, so this is a hold, not a permanent lock.
    fireEvent.pointerUp(window)
    fireEvent.pointerEnter(dots[1])
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toHaveTextContent('message four')
  })

  it('switches the card to another dot at once, with no close delay in between', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'message two' : seq === 4n ? 'message four' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    fireEvent.pointerEnter(dots[0])
    fireEvent.pointerLeave(dots[0])
    fireEvent.pointerEnter(dots[1])
    const cards = container.querySelectorAll('[data-testid="chat-scroll-rail-preview"]')
    expect(cards.length).toBe(1) // never two, and never the old one for a moment longer
    expect(cards[0]).toHaveTextContent('message four')
    // The first dot's pending close must not take the second dot's card down with it.
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS * 10)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toHaveTextContent('message four')
  })

  it('moves the SAME card element to the new dot\'s Y, and sends its scroll back to the top', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'message two' : seq === 4n ? 'message four' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    fireEvent.pointerEnter(dots[0])
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    expect(card.style.top).toBe('125px')
    card.scrollTop = 150 // the reader scrolled deep into this dot's preview

    fireEvent.pointerLeave(dots[0])
    fireEvent.pointerEnter(dots[1])

    // <Show> is not keyed, so the same element carries the next dot. Both of these would pass
    // silently if the card froze its props at creation: the card would sit at the first dot's Y,
    // still scrolled into the first dot's text, describing the second dot's message.
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBe(card)
    expect(card).toHaveTextContent('message four')
    expect(card.style.top).toBe('275px')
    expect(card.scrollTop).toBe(0)
  })

  it('leaves the card\'s scroll alone when a streaming turn re-anchors the SAME dot', async () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'message two' : undefined)
    const [marks, setMarks] = createSignal([{ seq: 2n, type: MarkType.USER_MESSAGE }])
    const { container } = render(() => <ChatScrollRail {...baseProps({ marks: marks(), previewFor })} />)
    fireEvent.pointerEnter(container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    card.scrollTop = 150

    // The same seq re-clusters to a fresh object as maxSeq ticks (see chatDotPreview.reanchor).
    setMarks([{ seq: 2n, type: MarkType.CONTROL_RESPONSE }])
    await tick()

    // Resetting here would yank the card out from under a reader who is mid-read, once per
    // persisted row of a streaming turn. The reset keys on the SEQ for exactly this reason.
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBe(card)
    expect(card.scrollTop).toBe(150)
  })

  /** Open the first dot's card and give it a scroll geometry jsdom cannot supply. */
  function cardWithScrollHeight(container: HTMLElement, scrollHeight: number): HTMLElement {
    fireEvent.pointerEnter(container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    // clientHeight comes from the file-wide prototype spy (400), so scrollHeight above it means
    // "the preview is taller than the card" and below it means "the preview fits".
    Object.defineProperty(card, 'scrollHeight', { value: scrollHeight, configurable: true })
    return card
  }

  it('scrolls the preview card, not the transcript, when the card has room to scroll', () => {
    const scrollEl = makeScrollEl(100, 5000)
    const previewFor = (seq: bigint) => (seq === 2n ? 'a long preview' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl, previewFor })} />)
    const forwarded = vi.fn()
    scrollEl.addEventListener('wheel', forwarded)
    const card = cardWithScrollHeight(container, 1000) // taller than the 400px card -> 600px of room

    card.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 100 }))

    // The rail's wheel forwarder never sees it, so the transcript stays where the reader left it
    // instead of scrolling out from under the card they are reading.
    expect(forwarded).not.toHaveBeenCalled()
    expect(scrollEl.scrollTop).toBe(100)
  })

  it('forwards the wheel to the transcript when the preview card has nothing left to scroll', () => {
    const scrollEl = makeScrollEl(100, 5000)
    const previewFor = (seq: bigint) => (seq === 2n ? 'a short preview' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl, previewFor })} />)
    const card = cardWithScrollHeight(container, 200) // shorter than the card -> the preview fits

    card.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 100 }))

    // A card that is not a scroller must not become a dead zone -- the same reason the rail
    // forwards a wheel over its own strip.
    expect(scrollEl.scrollTop).toBe(200)
  })

  it('measures the card room in the direction the wheel actually goes', () => {
    // The two directions read DIFFERENT room. A card scrolled to its bottom has none left
    // downward but plenty upward, and one formula for both would either trap the wheel at an end
    // or hand the transcript a wheel the card could still use.
    const scrollEl = makeScrollEl(100, 5000)
    const previewFor = (seq: bigint) => (seq === 2n ? 'a long preview' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl, previewFor })} />)
    const card = cardWithScrollHeight(container, 600) // 600 - 400 = 200px of travel
    card.scrollTop = 200 // ...and the reader is already at the bottom of it

    // Down: nothing left, so the transcript takes it.
    card.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 100 }))
    expect(scrollEl.scrollTop).toBe(200)

    // Up: 200px of room back to the top, so the card keeps it.
    card.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: -100 }))
    expect(scrollEl.scrollTop).toBe(200)
  })

  it('keeps a purely horizontal wheel off the rail, which could only swallow it', () => {
    const scrollEl = makeScrollEl(100, 5000)
    const previewFor = (seq: bigint) => (seq === 2n ? 'a long preview' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ scrollEl, previewFor })} />)
    const forwarded = vi.fn()
    scrollEl.addEventListener('wheel', forwarded)
    const card = cardWithScrollHeight(container, 200) // a preview that FITS -- no vertical room at all

    const sideways = new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaX: 100, deltaY: 0 })
    card.dispatchEvent(sideways)

    // The card scrolls on one axis only, so it has nothing to do with a sideways wheel -- but
    // handing it to the rail's forwarder is strictly worse than keeping it. That forwarder applies
    // deltaY ONLY and calls preventDefault, so the wheel would move nothing AND lose the browser's
    // own horizontal scroll of the wide content under the card. Stop it here instead.
    expect(forwarded).not.toHaveBeenCalled()
    expect(scrollEl.scrollTop).toBe(100) // the transcript stays exactly where the reader left it
    expect(sideways.defaultPrevented).toBe(false) // and the browser keeps its own sideways scroll
  })

  it('ends the press hold on a pointercancel, not only on a pointerup', () => {
    vi.useFakeTimers()
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)
    fireEvent.pointerDown(card)
    fireEvent.pointerLeave(card)

    // A system gesture takes the pointer away mid-selection. No pointerup will ever arrive, so a
    // hold that waited only for one would pin the card open for the rest of the session.
    fireEvent.pointerCancel(window)
    vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('drops its window press listeners when the card is torn down mid-press', async () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const [marks, setMarks] = createSignal([{ seq: 2n, type: MarkType.USER_MESSAGE }])
    const { container } = render(() => <ChatScrollRail {...baseProps({ marks: marks(), previewFor })} />)
    fireEvent.pointerEnter(container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement)
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement

    // Spy across ONE step at a time and restore immediately, so this asserts the identity of the
    // card's own handler rather than a count of every window listener in the app -- a count would
    // break the day anything else registers one, and a spy left installed would follow the rest
    // of this file (the project sets no restoreMocks).
    const handlersFor = (spy: { mock: { calls: unknown[][] } }) =>
      spy.mock.calls.filter(([type]) => type === 'pointerup').map(([, fn]) => fn)

    const added = vi.spyOn(window, 'addEventListener')
    fireEvent.pointerDown(card)
    const pressHandlers = handlersFor(added)
    added.mockRestore()
    expect(pressHandlers).toHaveLength(1)

    // The pressed mark's message is deleted, so the card unmounts under the finger and no
    // pointerup of its own ever runs. Its listener must not outlive it.
    const removed = vi.spyOn(window, 'removeEventListener')
    setMarks([])
    await Promise.resolve()
    const droppedHandlers = handlersFor(removed)
    removed.mockRestore()

    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
    expect(droppedHandlers).toContain(pressHandlers[0])
  })

  it('gives the dots back to the pointer when the card is torn down mid-press', async () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : seq === 4n ? 'message four' : undefined)
    const [marks, setMarks] = createSignal([
      { seq: 2n, type: MarkType.USER_MESSAGE },
      { seq: 4n, type: MarkType.USER_MESSAGE },
    ])
    const { container } = render(() => <ChatScrollRail {...baseProps({ marks: marks(), previewFor })} />)
    const dots = container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]')
    fireEvent.pointerEnter(dots[0])
    const card = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    fireEvent.pointerDown(card)

    // The pressed mark's message is deleted, so the card unmounts under the finger and no
    // pointerup of its own ever runs. The press LOCK it took on the rail's dots must come off with
    // it -- otherwise the dots stand down for a press that can never end, and the rail stops
    // showing any card at all for the rest of the session.
    setMarks([{ seq: 4n, type: MarkType.USER_MESSAGE }])
    await tick()
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()

    fireEvent.pointerEnter(container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toHaveTextContent('message four')
  })

  it('opens ONE press hold however many times the press repeats', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'select me' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const { card } = cardUnderPointer(container)

    // A pen and a mouse can both be down on the card at once, and the browser is free to deliver
    // a second pointerdown. A second hold would register a second listener pair that the first
    // release never drops, and the card would stay held by a press nothing can end.
    const added = vi.spyOn(window, 'addEventListener')
    fireEvent.pointerDown(card)
    fireEvent.pointerDown(card)
    const pressHandlers = added.mock.calls.filter(([type]) => type === 'pointerup')
    added.mockRestore()
    expect(pressHandlers).toHaveLength(1)
  })

  it('closes the card when the hovered dot disappears', async () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'stale preview' : undefined)
    const [marks, setMarks] = createSignal([{ seq: 2n, type: MarkType.USER_MESSAGE }])
    const { container } = render(() => <ChatScrollRail {...baseProps({ marks: marks(), previewFor })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toHaveTextContent('stale preview')

    setMarks([])
    await Promise.resolve()

    expect(container.querySelector('[data-testid="chat-scroll-rail-dot"]')).toBeNull()
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('shows a loading line while the preview is unresolved', () => {
    const previewFor = () => undefined // never resolves within the test
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    expect(hoverFirstDot(container)).toHaveTextContent('Loading preview…')
  })

  it('falls back to the mark-type label when the preview resolved empty', () => {
    const previewFor = () => '' // resolved, but no previewable text
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    expect(hoverFirstDot(container)).toHaveTextContent('Your message')
  })

  it('shows an aggregate "N messages" header plus the representative preview for a cluster', () => {
    // seqs 500..502 in [1, 100000] collapse to one pixel -> a cluster of 3; on the centre axis
    // 502 is nearest the pixel centre -> representative 502.
    const marks = [
      { seq: 500n, type: MarkType.USER_MESSAGE },
      { seq: 501n, type: MarkType.USER_MESSAGE },
      { seq: 502n, type: MarkType.USER_MESSAGE },
    ]
    const previewFor = (seq: bigint) => (seq === 502n ? 'the nearest message' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ minSeq: 1n, maxSeq: 100_000n, marks, previewFor })} />)
    const card = hoverFirstDot(container)!
    expect(card).toHaveTextContent('3 messages') // the aggregate header
    expect(card).toHaveTextContent('the nearest message') // the representative's preview
  })

  // The floating auto-hide. Only the CLASS is observable here: vitest inserts no stylesheet
  // for a .css.ts import and jsdom evaluates no media query, so the opacity, the
  // pointer-events, and the media scoping that make the class do anything are E2E-only (see
  // tests/e2e/047b-chat-scroll-rail-autohide.spec.ts).
  describe('railIdle auto-hide', () => {
    /** The rail root, with a concrete rect so pointer hit-tests resolve (jsdom does no layout). */
    it('marks the rail idle when the activity window closes and un-marks it when it reopens', () => {
      const [scrollActive, setScrollActive] = createSignal(true)
      const { container } = render(() => <ChatScrollRail {...baseProps()} scrollActive={scrollActive()} />)
      const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
      expect(rail.className).not.toContain(styles.railIdle)

      setScrollActive(false)
      expect(rail.className).toContain(styles.railIdle)

      setScrollActive(true)
      expect(rail.className).not.toContain(styles.railIdle)
    })

    it('adds the idle class without remounting the rail or its dots', () => {
      // The guard against implementing idle as a <Show>: an unmount would disconnect the
      // rail's ResizeObserver, cancel any live drag, and rebuild every dot's tooltip on
      // every single idle transition.
      const [scrollActive, setScrollActive] = createSignal(true)
      const { container } = render(() => <ChatScrollRail {...baseProps()} scrollActive={scrollActive()} />)
      const railBefore = container.querySelector('[data-testid="chat-scroll-rail"]')
      const dotsBefore = Array.from(container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]'))
      expect(dotsBefore.length).toBe(2)

      setScrollActive(false)

      expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBe(railBefore)
      const dotsAfter = Array.from(container.querySelectorAll('[data-testid="chat-scroll-rail-dot"]'))
      expect(dotsAfter[0]).toBe(dotsBefore[0])
      expect(dotsAfter[1]).toBe(dotsBefore[1])
    })

    it('keeps the rail visible while a thumb drag is live, however long the window has been shut', () => {
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const [scrollActive, setScrollActive] = createSignal(true)
      const { container } = render(() => <ChatScrollRail {...baseProps()} scrollActive={scrollActive()} />)
      const rail = railWithRect(container)
      expect(rail.className).not.toContain(styles.railIdle)

      // Grab the fixed thumb (spans 0..24) while the rail is lit, THEN shut the activity window:
      // the drag itself must keep the rail visible until release, however long the window stays shut.
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      setScrollActive(false)
      expect(rail.className).not.toContain(styles.railIdle)
    })

    it('stays visible through the drag-release hold, not just the pointer lifecycle', async () => {
      // Keyed on drag() rather than the pointer being down: createDragReleaseHold keeps the
      // thumb pinned until the release seek settles, and the rail must stay lit that whole
      // time or it blinks out from under a scrub that is still landing.
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      let landSeek!: (scrolled: boolean) => void
      const onJumpToSeq = vi.fn(() => new Promise<boolean>((r) => {
        landSeek = r
      }))
      const [scrollActive, setScrollActive] = createSignal(true)
      const { container } = render(() => (
        <ChatScrollRail
          {...baseProps({ onJumpToSeq, hasMoreOlder: true, hasMoreNewer: true })}
          scrollActive={scrollActive()}
        />
      ))
      const rail = railWithRect(container)
      // Grab while lit, THEN shut the window so the only thing keeping the rail visible is the hold.
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
      setScrollActive(false)
      // The pointer is up, but the seek has not landed: still held, so still lit.
      expect(rail.className).not.toContain(styles.railIdle)

      landSeek(false)
      await tick()
      expect(rail.className).toContain(styles.railIdle)
    })

    it('keeps the rail visible while a dot preview is open, on hover and on keyboard focus', () => {
      vi.useFakeTimers()
      const { container } = render(() => <ChatScrollRail {...baseProps({ scrollActive: false })} />)
      const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
      const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
      expect(rail.className).toContain(styles.railIdle)

      fireEvent.pointerEnter(dot)
      expect(rail.className).not.toContain(styles.railIdle)
      fireEvent.pointerLeave(dot)
      // The card is still open for the close delay, and the rail must not fade out from under it
      // -- the reader is on their way to the card the whole time.
      expect(rail.className).not.toContain(styles.railIdle)
      vi.advanceTimersByTime(POINTER_CLOSE_DELAY_MS)
      expect(rail.className).toContain(styles.railIdle)

      // activeDot() folds focus in with hover, which is what lets the rail skip a CSS
      // :focus-within rule -- pin that here so a refactor can't quietly drop it.
      fireEvent.focus(dot)
      expect(rail.className).not.toContain(styles.railIdle)

      // Focus OUT must null activeDot() so the rail fades again -- without this a broken onBlur
      // would pin the rail lit forever after a keyboard tab-away. No delay on this one: focus
      // moves in discrete steps, so there is no gap for the reader to cross.
      fireEvent.blur(dot)
      expect(rail.className).toContain(styles.railIdle)
    })

    it('reopens the host window from the rail own traffic: pointerdown and focusin', () => {
      // pointerdown always reopens (a track-click jump wants its fade tail), and focusin bubbles
      // from a dot button so tabbing through dots keeps the rail lit. pointermove is covered by
      // its own test below (it reopens only while faded, not on every move).
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const onActivity = vi.fn()
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity })} />)
      const rail = railWithRect(container)

      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      expect(onActivity).toHaveBeenCalledTimes(1)

      const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
      fireEvent.focusIn(dot)
      expect(onActivity).toHaveBeenCalledTimes(2)
    })

    it('reopens the host window when a drag ENDS, so a long scrub still gets a fade tail', async () => {
      // The window the grab opened has a fixed idle timeout, and nothing re-arms it mid-drag: the
      // pointer is captured (the scroll container sees nothing), the rail's own pointermove
      // relight is inert while idle() is false, and the drag's live-scroll writes are
      // programmatic. A touch scrub easily outlasts that timeout, so without a relight at the end
      // the rail would fade out from under the finger the moment it lifts.
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const onActivity = vi.fn()
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity })} />)
      const rail = railWithRect(container)

      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      onActivity.mockClear()
      rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
      expect(onActivity).not.toHaveBeenCalled() // no per-move timer churn
      rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
      // The re-arm keys on the rail releasing ITSELF, not on the pointer lifting: the release
      // hold outlives the pointer, and it is one of the states that outrank the host's window.
      await tick()
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('defers the reopen until a SLOW release seek lands, so a fetch cannot outlast the window', async () => {
      // The case a re-arm at pointerup gets wrong. An out-of-window release seek fetches a page,
      // and the rail holds itself lit for the whole fetch (the release hold keeps drag()
      // non-null). A window re-armed when the finger lifted would already be closing by the time
      // that hold clears, so the rail would snap dark with no fade tail -- the very failure the
      // re-arm exists to prevent, just moved later. Re-arm when the hold ends instead.
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const onActivity = vi.fn()
      let landSeek: (scrolled: boolean) => void = () => {}
      const onJumpToSeq = vi.fn(() => new Promise<boolean>((resolve) => {
        landSeek = resolve
      }))
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity, onJumpToSeq })} />)
      const rail = railWithRect(container)

      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
      onActivity.mockClear()
      rail.dispatchEvent(new PointerEvent('pointerup', { bubbles: true, clientY: 200, pointerId: 1 }))
      await tick()
      // Still fetching: the rail holds itself lit, so a window opened now would only burn down.
      expect(onActivity).not.toHaveBeenCalled()

      landSeek(false) // resolved with no landing scroll -> the hold clears at once
      await tick()
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('reopens the host window when a dot card closes, so tabbing away gets a fade tail too', async () => {
      // The card is the third state that outranks the host's window (see idle()), and it can
      // stay open past the idle timeout just as easily as a drag can. The re-arm keys on the
      // whole override set, so this needs no separate wiring -- which is the point of keying it
      // there rather than on the pointer lifecycle.
      vi.useFakeTimers()
      const onActivity = vi.fn()
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity })} />)
      const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement

      fireEvent.pointerEnter(dot)
      expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
      onActivity.mockClear()
      fireEvent.pointerLeave(dot)
      // The re-arm keys on the card actually CLOSING, not on the pointer leaving the dot: the
      // close delay is one more stretch during which the rail holds itself lit.
      await vi.advanceTimersByTimeAsync(POINTER_CLOSE_DELAY_MS - 1)
      expect(onActivity).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1)
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('reopens the host window when a drag is CANCELLED (a system gesture stole the pointer)', () => {
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const onActivity = vi.fn()
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity })} />)
      const rail = railWithRect(container)

      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      onActivity.mockClear()
      rail.dispatchEvent(new PointerEvent('pointercancel', { bubbles: true, clientY: 200, pointerId: 1 }))
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('reopens the host window from a pointermove only while the rail is faded, not on every move', () => {
      // A captured thumb drag retargets its pointermove to the rail, so the host's scroll
      // container sees nothing -- the FIRST move onto a faded strip must relight it. But once lit
      // (by the drag itself, an open dot, or the now-open host window) further moves would only
      // re-arm a timer the next move cancels again: dozens of clearTimeout + setTimeout pairs per
      // second of dragging. So pointermove reopens the window ONLY while idle, then goes quiet.
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      // onActivity is wired to a real scrollActive signal, mirroring ChatView: a call flips the
      // rail to its lit state so the second pointermove sees a visible rail and no-ops.
      const [scrollActive, setScrollActive] = createSignal(false)
      const onActivity = vi.fn(() => setScrollActive(true))
      const { container } = render(() => (
        <ChatScrollRail {...baseProps({ onActivity })} scrollActive={scrollActive()} />
      ))
      const rail = railWithRect(container)
      expect(rail.className).toContain(styles.railIdle)

      // Faded: the first pointermove relights it.
      rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
      expect(onActivity).toHaveBeenCalledTimes(1)
      expect(rail.className).not.toContain(styles.railIdle)

      // Now lit: further moves are no-ops (no redundant timer churn).
      rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 210, pointerId: 1 }))
      rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 220, pointerId: 1 }))
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('relights directly on a wheel over the faded rail, not only via the re-dispatched event', () => {
      // onRailWheel forwards the delta to the scroll container by re-dispatching a wheel, which
      // indirectly relights the rail via the scroller's passive listener. That chain is fragile
      // (a capture-phase listener or a dropped bubbles flag breaks it), so the wheel also calls
      // onActivity directly. Pin that here so a refactor of the indirect path cannot strand a
      // faded rail under a wheel.
      const onActivity = vi.fn()
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity, scrollActive: false })} />)
      const rail = railWithRect(container)
      expect(rail.className).toContain(styles.railIdle)

      rail.dispatchEvent(new WheelEvent('wheel', { bubbles: true, deltaY: 40 }))
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('reports activity even for a grab it rejects, so a stray second finger cannot fade it', () => {
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const onActivity = vi.fn()
      const onJumpToSeq = vi.fn()
      const { container } = render(() => <ChatScrollRail {...baseProps({ onActivity, onJumpToSeq })} />)
      const rail = railWithRect(container)
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
      onActivity.mockClear()

      // A second finger on the track mid-drag: the jump is correctly refused, but the touch
      // is still the reader on the rail, so the window must reopen anyway.
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 2 }))
      expect(onJumpToSeq).not.toHaveBeenCalled()
      expect(onActivity).toHaveBeenCalledTimes(1)
    })

    it('renders nothing at all when hidden, idle or not', () => {
      // hidden and idle compose one way only: hidden wins and unmounts, so there is never a
      // mounted-but-idle rail on a viewport that has no rail to show.
      const { container } = render(() => <ChatScrollRail {...baseProps({ hidden: true, scrollActive: false })} />)
      expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()
    })

    it('rejects a press on a faded rail so you cannot click what you cannot see', () => {
      // A press onto a faded rail only REVEALS it (via onActivity); the grab is refused so a
      // stray press into the invisible strip cannot start a drag or fire a jump. The NEXT press
      // -- rail now lit -- acts. On a coarse pointer the idle rail is pointer-events:none, so
      // this guard only ever applies to a fine pointer.
      //
      // onActivity is wired to a real scrollActive signal, mirroring ChatView: a call flips the
      // rail to its lit state so the second press -- now against a visible rail -- goes through.
      HTMLElement.prototype.setPointerCapture = vi.fn()
      installImmediateRaf()
      const [scrollActive, setScrollActive] = createSignal(false)
      const onActivity = vi.fn(() => setScrollActive(true))
      const onJumpToSeq = vi.fn()
      const { container } = render(() => (
        <ChatScrollRail {...baseProps({ onActivity, onJumpToSeq })} scrollActive={scrollActive()} />
      ))
      const rail = railWithRect(container)
      expect(rail.className).toContain(styles.railIdle)

      // First press: faded -> rejected. onActivity fires (and lights the rail), but no grab
      // and no jump.
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
      expect(onActivity).toHaveBeenCalledTimes(1)
      expect(onJumpToSeq).not.toHaveBeenCalled()
      expect(rail.className).not.toContain(styles.railIdle)

      // Second press on the now-lit rail: the track jump goes through.
      rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 2 }))
      expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    })

    it('rejects a press on a faded dot but reveals it, so the next press jumps', () => {
      // The same "can't click what you can't see" rule covers dots: a press on a faded dot only
      // reveals the rail; the jump happens on the next press. onActivity flips scrollActive, so
      // the second press sees a lit rail.
      const [scrollActive, setScrollActive] = createSignal(false)
      const onActivity = vi.fn(() => setScrollActive(true))
      const onJumpToSeq = vi.fn()
      const { container } = render(() => (
        <ChatScrollRail {...baseProps({ onActivity, onJumpToSeq })} scrollActive={scrollActive()} />
      ))
      const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
      const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
      expect(rail.className).toContain(styles.railIdle)

      dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerId: 1 }))
      expect(onActivity).toHaveBeenCalledTimes(1)
      expect(onJumpToSeq).not.toHaveBeenCalled()
      expect(rail.className).not.toContain(styles.railIdle)

      dot.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, pointerId: 2 }))
      expect(onJumpToSeq).toHaveBeenCalledTimes(1)
    })
  })
})
