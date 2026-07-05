import type { ChatScrollRailProps } from './ChatScrollRail'
import type { VirtualItem } from './useChatVirtualizer'
import type { ChatRailData } from '~/stores/chatMessageMarks'
import { fireEvent, render } from '@solidjs/testing-library'
import { createSignal } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { MarkType } from '~/generated/leapmux/v1/agent_pb'
import { SCRUB_WARM_DEBOUNCE_MS } from './chatDotPreview'
import { resolveScrollbarOwner } from './chatRailPolicy'
import { ChatScrollRail } from './ChatScrollRail'
import * as styles from './ChatScrollRail.css'
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
    hasMoreOlder: false,
    hasMoreNewer: false,
    onJumpToSeq: vi.fn(),
    previewScrollTo: vi.fn(),
    ...rest,
  }
}

describe('chatscrollrail', () => {
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
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    // Give the rail a concrete rect (jsdom returns zeros otherwise).
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    // Concrete rail rect (top 0, height 400). The fixed thumb spans 0..24px at the top,
    // so its CENTRE axis is [12, 388].
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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

  it('fires onSeekInterrupt on a thumb grab (abandon a prior seek) but NOT on a track click', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onSeekInterrupt = vi.fn()
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onSeekInterrupt })} />
    ))
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
    // A track click (below the thumb at 0..24) supersedes any pending seek via its own jump,
    // so it must NOT also fire onSeekInterrupt.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 360, pointerId: 1 }))
    expect(onSeekInterrupt).not.toHaveBeenCalled()
    // A thumb grab (on the thumb at y=12) is manual control: abandon a prior release's
    // still-fetching out-of-window seek so it can't yank the viewport mid-scrub.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 2 }))
    expect(onSeekInterrupt).toHaveBeenCalledTimes(1)
  })

  it('ignores a second pointerdown on the track while a thumb-drag is live (no rival seek)', () => {
    // jsdom has no pointer capture; stub it so startDrag doesn't throw.
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const onJumpToSeq = vi.fn()
    const { container } = render(() => (
      <ChatScrollRail {...baseProps({ onJumpToSeq })} />
    ))
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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
    let rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })

    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    expect(capture).toHaveBeenCalledTimes(1)

    setHidden(true)
    await Promise.resolve()

    expect(release).toHaveBeenCalledWith(1)
    expect(container.querySelector('[data-testid="chat-scroll-rail"]')).toBeNull()

    setHidden(false)
    await Promise.resolve()

    rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 2 }))
    expect(capture).toHaveBeenCalledTimes(2)
  })

  it('reveals the preview popover for the dot the thumb passes over while dragging, and warms it after it settles', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    vi.useFakeTimers()
    installImmediateRaf() // override the faked rAF with a synchronous one for the drag frames
    const warmPreview = vi.fn()
    const previewFor = (seq: bigint) => (seq === 2n ? 'scrubbed message two' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ warmPreview, previewFor })} />)
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
    // No popover until a drag is in progress (and nothing is hovered).
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
    // Grab the fixed thumb, then scrub to the seq-2 dot at y=125.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 125, pointerId: 1 }))
    const preview = container.querySelectorAll('[data-testid="chat-scroll-rail-preview"]')
    expect(preview.length).toBe(1) // never two popovers
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
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
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

  it('shows no preview popover when the dragging thumb is between dots', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const { container } = render(() => <ChatScrollRail {...baseProps()} />)
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
    // y=200 -> thumb centre 200; dots sit at 125 and 275, both >12px away -> no scrub target.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 200, pointerId: 1 }))
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('shows ONE popover (the scrub target wins) when a dot is hovered while scrubbing', () => {
    HTMLElement.prototype.setPointerCapture = vi.fn()
    installImmediateRaf()
    const previewFor = (seq: bigint) => (seq === 2n ? 'scrub target two' : seq === 4n ? 'hovered four' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const rail = container.querySelector('[data-testid="chat-scroll-rail"]') as HTMLElement
    rail.getBoundingClientRect = () => ({ top: 0, left: 0, height: 400, width: 10, right: 10, bottom: 400, x: 0, y: 0, toJSON: () => ({}) })
    // Scrub over the seq-2 dot (thumb centre 125), then also hover the seq-4 dot.
    rail.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, clientY: 12, pointerId: 1 }))
    rail.dispatchEvent(new PointerEvent('pointermove', { bubbles: true, clientY: 125, pointerId: 1 }))
    const dot4 = container.querySelector('[data-testid="chat-scroll-rail-dot"][data-seq="4"]') as HTMLElement
    fireEvent.pointerEnter(dot4)
    const previews = container.querySelectorAll('[data-testid="chat-scroll-rail-preview"]')
    expect(previews.length).toBe(1) // exactly one popover, no double
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

describe('chatscrollrail dot preview popover', () => {
  /** Hover the first dot -- the popover opens IMMEDIATELY (no show-delay), and returns it. */
  function hoverFirstDot(container: HTMLElement): HTMLElement | null {
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    return container.querySelector('[data-testid="chat-scroll-rail-preview"]')
  }

  it('clamps the popover into the rail so a dot near the top edge does not clip past it', () => {
    // With the fixed 24px thumb, the seq-1 dot sits at ~12px,
    // above the popover's clamp floor. The popover top is pinned down to keep the card visible.
    const marks = [{ seq: 1n, type: MarkType.USER_MESSAGE }]
    const { container } = render(() => <ChatScrollRail {...baseProps({ minSeq: 1n, maxSeq: 100_000n, marks })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    expect(dot.style.top).toBe('12px') // the dot itself is near the top
    fireEvent.pointerEnter(dot)
    const popover = container.querySelector('[data-testid="chat-scroll-rail-preview"]') as HTMLElement
    expect(popover.style.top).toBe('100px') // clamped down from 12 (rail 400, half-height 100)
  })

  it('opens the popover immediately on hover and renders the resolved preview as markdown', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? '**jump** to this message' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const popover = hoverFirstDot(container)! // no timers advanced -- it's immediate
    expect(popover).toHaveTextContent('jump to this message')
    // The markdown is rendered, not shown as raw source: **jump** becomes a bold element.
    const strong = popover.querySelector('strong, b')
    expect(strong?.textContent).toBe('jump')
    expect(popover.textContent).not.toContain('**')
  })

  it('closes the popover when the pointer leaves the dot', () => {
    const previewFor = (seq: bigint) => (seq === 2n ? 'hi there' : undefined)
    const { container } = render(() => <ChatScrollRail {...baseProps({ previewFor })} />)
    const dot = container.querySelector('[data-testid="chat-scroll-rail-dot"]') as HTMLElement
    fireEvent.pointerEnter(dot)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).not.toBeNull()
    fireEvent.pointerLeave(dot)
    expect(container.querySelector('[data-testid="chat-scroll-rail-preview"]')).toBeNull()
  })

  it('closes the popover when the hovered dot disappears', async () => {
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
    const popover = hoverFirstDot(container)!
    expect(popover).toHaveTextContent('3 messages') // the aggregate header
    expect(popover).toHaveTextContent('the nearest message') // the representative's preview
  })
})
