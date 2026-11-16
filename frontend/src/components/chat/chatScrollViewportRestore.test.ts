import type { AdmissionTarget } from './chatRowAdmission'
import type { ChatScrollState, ChatScrollVirtualizer, ScrollContext } from './useChatScroll'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createManualFrames } from '~/test-support/manualFrames'
import { createViewportRestore } from './chatScrollViewportRestore'
import { makeScrollContext, virtualizerNoOps, withContentAnchors } from './useChatScroll.testkit'

/**
 * A manual animation frame queue in place of the global one, until `afterEach` removes it.
 * handleResize schedules its flush on a frame, and schedules it again while an animation
 * runs. `runFrames()` runs the frames requested so far: a frame that one of them requests
 * waits for the next call, as at a real frame boundary.
 */
function stubFrames() {
  const frames = createManualFrames()
  vi.stubGlobal('requestAnimationFrame', frames.requestFrame)
  vi.stubGlobal('cancelAnimationFrame', frames.cancelFrame)
  return frames
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/** A virtualizer stub whose anchors resolve to the given scroll coordinates. */
function stubVirt(opts: { totalHeight: number, forAnchor: number | null, nearAnchor: number | null }): ChatScrollVirtualizer {
  return withContentAnchors({
    ...virtualizerNoOps(),
    totalHeight: () => opts.totalHeight,
    geometryVersion: () => 0,
    updateViewport: vi.fn(),
    anchorAt: vi.fn(() => null),
    scrollTopForAnchor: vi.fn(() => opts.forAnchor),
    scrollTopNearAnchor: vi.fn(() => opts.nearAnchor),
  })
}

/**
 * A viewport restore over stubbed deps. `placed` records what each `placeAt` resolver
 * returned, in order: a `null` is a placement whose anchor did not resolve. The context
 * shows each requested place at once (see makeScrollContext), so a restore that asks for
 * a place records its placement in `placed` also. The loaded rows of the context agree
 * with the stub virtualizer: a row is loaded when `forAnchor` resolves it, and the loaded
 * row nearest a seq is `near` when `nearAnchor` resolves. `context` replaces members of
 * the context.
 */
function harness(opts: {
  saved?: ChatScrollState | undefined
  totalHeight?: number
  forAnchor?: number | null
  nearAnchor?: number | null
  followRequested?: boolean
  lastAnchor?: () => ScrollAnchor | null
  /** Whether the hook's restoreAnchor placed the view. Default: false. */
  anchorRestored?: boolean
  context?: Partial<ScrollContext>
} = {}) {
  const el = { scrollTop: 0, scrollHeight: 5000, clientHeight: 500 } as HTMLDivElement
  // `followRequested` is the request for the tail; a test changes it between the
  // ResizeObserver callback and the flush.
  const state = { animating: false, followRequested: opts.followRequested ?? false }
  const placed: Array<number | null> = []
  const placeAt = vi.fn((resolve: () => number | null): boolean | null => {
    const top = resolve()
    placed.push(top)
    return top === null
      ? null
      : true
  })
  const recordPosition = vi.fn()
  const extras = {
    stickToBottom: vi.fn(() => true),
    forceScrollToBottom: vi.fn(),
    clearSavedViewportScroll: vi.fn(),
    savedViewportScroll: () => opts.saved,
    restoreAnchor: vi.fn((_anchor: ScrollAnchor | null) => opts.anchorRestored ?? false),
    lastAnchor: vi.fn(opts.lastAnchor ?? (() => null)),
    onGeometrySettled: vi.fn(),
  }
  const forAnchor = opts.forAnchor === undefined
    ? 1200
    : opts.forAnchor
  const nearAnchor = opts.nearAnchor === undefined
    ? 1200
    : opts.nearAnchor
  const ctx = makeScrollContext({
    getEl: () => el,
    virt: stubVirt({
      totalHeight: opts.totalHeight ?? 5000,
      forAnchor,
      nearAnchor,
    }),
    loadedRows: {
      has: () => forAnchor !== null,
      nearestId: () => nearAnchor !== null
        ? 'near'
        : undefined,
      first: () => undefined,
    },
    isAnimating: () => state.animating,
    followRequested: () => state.followRequested,
    placeAt,
    recordPosition,
    ...opts.context,
  })
  const restore = createViewportRestore(ctx, extras)
  // prevClientHeight defaults to 0 (hidden); el.clientHeight is 500 -> wasHidden.
  return {
    restore,
    ctx,
    state,
    placed,
    placeAt,
    recordPosition,
    extras,
    el,
  }
}

const SAVED_ANCHOR: ChatScrollState = {
  anchor: { id: 'r1', offsetWithinRow: 10 },
  atBottom: false,
  hasMoreNewer: false,
}

describe('chatScrollViewportRestore handleResize', () => {
  it('defers a hidden->visible restore while an animation runs, then restores once it ends', () => {
    const frames = stubFrames()
    const { restore, state, placed } = harness({ saved: SAVED_ANCHOR })
    state.animating = true

    // A hidden->visible resize fires while a scroll animation is in flight.
    restore.handleResize()
    frames.runFrames() // the flush sees isAnimating() -> re-schedules instead of restoring
    expect(placed).toEqual([])

    // The animation is still running across another frame: still deferred, NOT dropped.
    frames.runFrames()
    expect(placed).toEqual([])

    // Animation ends; the re-scheduled flush now performs the restore.
    state.animating = false
    frames.runFrames()
    expect(placed).toEqual([1200])
  })

  it('restores immediately when no animation is running', () => {
    const frames = stubFrames()
    const { restore, placed, extras } = harness({ saved: SAVED_ANCHOR })
    restore.handleResize()
    frames.runFrames()
    expect(placed).toEqual([1200])
    expect(extras.clearSavedViewportScroll).toHaveBeenCalledOnce()
  })

  it('passes the anchor of the ResizeObserver callback to the restore', () => {
    const frames = stubFrames()
    const anchorA: ScrollAnchor = { id: 'a', offsetWithinRow: 1 }
    const anchorB: ScrollAnchor = { id: 'b', offsetWithinRow: 2 }
    let current = anchorA
    // No saved state and no request: the show takes the recheck path. The test does
    // not call initClientHeight, so the pane counts as hidden before the resize.
    const { restore, extras, recordPosition } = harness({
      saved: undefined,
      lastAnchor: () => current,
    })
    restore.handleResize()
    // A scroll event of the browser replaces the anchor of the hook before the flush.
    current = anchorB
    frames.runFrames()
    expect(extras.restoreAnchor).toHaveBeenCalledWith(anchorA)
    expect(recordPosition).toHaveBeenCalledOnce()
  })

  it('records no second write after a restore of the anchor that placed the view', () => {
    const frames = stubFrames()
    // A restore that placed the view recorded its own write. A second record would end
    // the anchor that the hook holds for a row that does not show its height yet.
    const anchor: ScrollAnchor = { id: 'a', offsetWithinRow: 1 }
    const { restore, extras, recordPosition } = harness({
      saved: undefined,
      lastAnchor: () => anchor,
      anchorRestored: true,
    })
    restore.handleResize()
    frames.runFrames()
    expect(extras.restoreAnchor).toHaveBeenCalledWith(anchor)
    expect(recordPosition).not.toHaveBeenCalled()
  })

  it('sticks on a resize while the reader requests the tail', () => {
    const frames = stubFrames()
    const { restore, extras } = harness({ saved: undefined, followRequested: true })
    restore.initClientHeight()
    restore.handleResize()
    frames.runFrames()
    expect(extras.stickToBottom).toHaveBeenCalledOnce()
    expect(extras.restoreAnchor).not.toHaveBeenCalled()
  })

  it('keeps the request of the ResizeObserver callback for a show', () => {
    const frames = stubFrames()
    const { restore, ctx, extras, recordPosition, state } = harness({ saved: undefined, followRequested: true })
    restore.handleResize()
    // The browser moves the pane that it showed, and the scroll event of that move ends the
    // request before the flush.
    state.followRequested = false
    frames.runFrames()
    // The show asks for the tail, and the hook sticks for it.
    expect(vi.mocked(ctx.requestPlace).mock.calls).toEqual([
      [
        { kind: 'tail' },
      ],
    ])
    expect(extras.stickToBottom).not.toHaveBeenCalled()
    expect(extras.restoreAnchor).not.toHaveBeenCalled()
    expect(recordPosition).not.toHaveBeenCalled()
  })

  it('records the position on a resize of a visible reader with no request', () => {
    const frames = stubFrames()
    const { restore, extras, recordPosition } = harness({ saved: undefined })
    restore.initClientHeight()
    restore.handleResize()
    frames.runFrames()
    expect(extras.stickToBottom).not.toHaveBeenCalled()
    // Only a show restores the anchor: the pane was visible before this resize.
    expect(extras.restoreAnchor).not.toHaveBeenCalled()
    expect(recordPosition).toHaveBeenCalledOnce()
  })
})

/**
 * A hidden->visible restore of a NON-bottom save whose anchor doesn't resolve. The
 * priority is: anchor or nearest survivor -> raw-top (while still all-hidden) ->
 * hasMoreNewer tail-snap -> top. The key invariant: an all-hidden reader who scrolled
 * UP while windowed away (hasMoreNewer) must be RESTORED to their raw offset, not
 * yanked to the live tail by the tail-snap.
 */
describe('chatScrollViewportRestore restore priority', () => {
  function priorityHarness(opts: {
    totalHeight: number
    rawScrollTop?: number
    hasMoreNewer: boolean
    // A saved anchor whose exact row no longer resolves (trimmed); `nearAnchorTop` is
    // what scrollTopNearAnchor recovers for it (null = no surviving row to land on).
    anchor?: ChatScrollState['anchor']
    nearAnchorTop?: number | null
  }) {
    const saved: ChatScrollState = {
      ...(opts.anchor !== undefined
        ? { anchor: opts.anchor }
        : {}),
      atBottom: false,
      hasMoreNewer: opts.hasMoreNewer,
      ...(opts.rawScrollTop !== undefined
        ? { rawScrollTop: opts.rawScrollTop }
        : {}),
    }
    return harness({
      saved,
      totalHeight: opts.totalHeight,
      forAnchor: null,
      nearAnchor: opts.nearAnchorTop ?? null,
    })
  }

  it('restores the raw offset of an all-hidden scrolled-up reader instead of snapping to the live tail', () => {
    const frames = stubFrames()
    // All-hidden window (totalHeight 0), a saved raw offset, and hasMoreNewer set.
    const { restore, placed, extras } = priorityHarness({
      totalHeight: 0,
      rawScrollTop: 800,
      hasMoreNewer: true,
    })
    restore.handleResize()
    frames.runFrames()
    // raw-top restore wins over the hasMoreNewer tail-snap.
    expect(placed).toEqual([800])
    expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
  })

  it('snaps to the live tail when the window is no longer all-hidden and no anchor recovers', () => {
    const frames = stubFrames()
    // Virtual rows appeared (totalHeight > 0) and no saved anchor to recover, so the
    // raw offset is stale (restoreFromRawTop declines) and the tail-snap stands.
    const { restore, placed, extras } = priorityHarness({
      totalHeight: 3000,
      rawScrollTop: 800,
      hasMoreNewer: true,
    })
    restore.handleResize()
    frames.runFrames()
    expect(extras.forceScrollToBottom).toHaveBeenCalled()
    expect(placed).toEqual([])
  })

  it('recovers to the nearest surviving row when the anchored row was trimmed away', () => {
    const frames = stubFrames()
    // A scrolled-up reader whose anchor row was trimmed as the window advanced: the
    // exact anchor no longer resolves, the window isn't all-hidden, and newer messages
    // exist -- but instead of the live-tail snap, restore lands on the nearest survivor.
    const { restore, ctx, placed, extras } = priorityHarness({
      totalHeight: 3000,
      hasMoreNewer: true,
      anchor: { id: 'gone', offsetWithinRow: 10, seq: 42n },
      nearAnchorTop: 250,
    })
    restore.handleResize()
    frames.runFrames()
    // One step resolves the anchor to its place, the nearest survivor, and places it:
    // at the top of that row, with the seq of the saved anchor.
    expect(vi.mocked(ctx.requestPlace).mock.calls).toEqual([
      [
        {
          kind: 'row',
          anchor: { id: 'near', seq: 42n, offsetWithinRow: 0 },
        },
      ],
    ])
    expect(placed).toEqual([250])
    expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
  })

  it('falls back to the live-tail snap when no surviving row resolves for the trimmed anchor', () => {
    const frames = stubFrames()
    const { restore, placed, extras } = priorityHarness({
      totalHeight: 3000,
      hasMoreNewer: true,
      anchor: { id: 'gone', offsetWithinRow: 10, seq: 42n },
      nearAnchorTop: null, // e.g. an empty window -- nothing to land on
    })
    restore.handleResize()
    frames.runFrames()
    expect(extras.forceScrollToBottom).toHaveBeenCalled()
    // The anchor gives no place, so nothing asks for a placement.
    expect(placed).toEqual([])
  })

  it('places the view at the top when nothing resolves and no newer page exists', () => {
    const frames = stubFrames()
    const { restore, placed, extras } = priorityHarness({ totalHeight: 3000, hasMoreNewer: false })
    restore.handleResize()
    frames.runFrames()
    expect(placed).toEqual([0])
    expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
  })
})

describe('chatScrollViewportRestore requested places', () => {
  /** The place and the `show` argument of each `requestPlace` call of the context, in order. */
  const requests = (ctx: ScrollContext): [AdmissionTarget, (() => void) | undefined][] =>
    vi.mocked(ctx.requestPlace).mock.calls.map(([place, show]) => [place, show])

  it('asks for the place of a saved anchor whose row is loaded, and clears the saved state', () => {
    const frames = stubFrames()
    const place: AdmissionTarget = {
      kind: 'row',
      anchor: { id: 'r1', offsetWithinRow: 10 },
    }
    const { restore, ctx, extras } = harness({
      saved: SAVED_ANCHOR,
      context: {
        placeForAnchor: vi.fn(() => place),
      },
    })
    restore.handleResize()
    frames.runFrames()
    expect(ctx.placeForAnchor).toHaveBeenCalledWith(SAVED_ANCHOR.anchor)
    expect(extras.clearSavedViewportScroll).toHaveBeenCalledOnce()
    // No `show`: the hook places a row place at its anchor.
    expect(requests(ctx)).toEqual([
      [place, undefined],
    ])
  })

  it('jumps to the latest page for a saved anchor with no place in a window with newer pages', () => {
    const frames = stubFrames()
    const { restore, ctx, extras } = harness({
      saved: { ...SAVED_ANCHOR, hasMoreNewer: true },
      context: {
        placeForAnchor: vi.fn(() => undefined),
      },
    })
    restore.handleResize()
    frames.runFrames()
    expect(extras.forceScrollToBottom).toHaveBeenCalledOnce()
    expect(ctx.requestPlace).not.toHaveBeenCalled()
    expect(extras.clearSavedViewportScroll).toHaveBeenCalledOnce()
  })

  it('restores no raw position while loaded rows exist and none is in the list', () => {
    const frames = stubFrames()
    // The total height is 0 only because no row joined the list yet.
    const { restore, ctx, placed } = harness({
      saved: { atBottom: false, hasMoreNewer: false, rawScrollTop: 40 },
      totalHeight: 0,
      context: {
        awaitingFirstView: vi.fn(() => true),
      },
    })
    restore.handleResize()
    frames.runFrames()
    expect(placed).not.toContain(40)
    expect(requests(ctx)).toEqual([
      [
        { kind: 'top' },
        undefined,
      ],
    ])
  })

  it('restores a raw position in a window with no visible row', () => {
    const frames = stubFrames()
    const { restore, ctx, placed } = harness({
      saved: { atBottom: false, hasMoreNewer: false, rawScrollTop: 40 },
      totalHeight: 0,
    })
    restore.handleResize()
    frames.runFrames()
    expect(placed).toEqual([40])
    expect(ctx.requestPlace).not.toHaveBeenCalled()
  })

  it('asks for the top when the saved state gives no anchor, no raw position, and no newer page', () => {
    const frames = stubFrames()
    const { restore, ctx } = harness({
      saved: { atBottom: false, hasMoreNewer: false },
    })
    restore.handleResize()
    frames.runFrames()
    expect(requests(ctx)).toEqual([
      [
        { kind: 'top' },
        undefined,
      ],
    ])
  })

  it('asks for the tail when a pane of a reader who requests the tail is shown', () => {
    const frames = stubFrames()
    const { restore, ctx, extras } = harness({ saved: undefined, followRequested: true })
    restore.handleResize()
    frames.runFrames()
    const [request] = requests(ctx)
    expect(requests(ctx)).toHaveLength(1)
    expect(request?.[0]).toEqual({ kind: 'tail' })
    // The request passes no placement: the hook sticks for the tail.
    expect(request?.[1]).toBeUndefined()
    expect(extras.stickToBottom).not.toHaveBeenCalled()
  })

  it('asks for the tail at a mount with a saved state at the bottom, and clears the saved state', () => {
    const { restore, ctx, extras } = harness({
      saved: { atBottom: true, hasMoreNewer: false },
    })
    restore.restoreOnMount()
    expect(requests(ctx).map(([place]) => place)).toEqual([
      { kind: 'tail' },
    ])
    expect(extras.clearSavedViewportScroll).toHaveBeenCalledOnce()
    expect(extras.forceScrollToBottom).not.toHaveBeenCalled()
  })

  it('writes no stick on a resize of a visible pane while a tail waits', () => {
    const frames = stubFrames()
    const { restore, ctx, extras, recordPosition } = harness({
      saved: undefined,
      followRequested: true,
      context: {
        showWaitingPlace: vi.fn((): 'waiting' => 'waiting'),
      },
    })
    restore.initClientHeight() // prevClientHeight = 500 -> the resize is of a visible pane
    restore.handleResize()
    frames.runFrames()
    // A stick would put the view at the bottom of the measured rows, and the wait would
    // move it again.
    expect(extras.stickToBottom).not.toHaveBeenCalled()
    expect(ctx.requestPlace).not.toHaveBeenCalled()
    expect(recordPosition).toHaveBeenCalledOnce()
  })

  it('restores no anchor at a show while a place waits', () => {
    const frames = stubFrames()
    const { restore, extras, recordPosition } = harness({
      saved: undefined,
      lastAnchor: () => ({ id: 'r3', offsetWithinRow: 4 }),
      anchorRestored: true,
      context: {
        showWaitingPlace: vi.fn((): 'waiting' => 'waiting'),
      },
    })
    restore.handleResize()
    frames.runFrames()
    expect(extras.restoreAnchor).not.toHaveBeenCalled()
    expect(recordPosition).toHaveBeenCalledOnce()
  })

  it('restores no anchor and records nothing at a show that showed the place that waited', () => {
    const frames = stubFrames()
    const { restore, extras, recordPosition } = harness({
      saved: undefined,
      lastAnchor: () => ({ id: 'r3', offsetWithinRow: 4 }),
      anchorRestored: true,
      context: {
        showWaitingPlace: vi.fn((): 'shown' => 'shown'),
      },
    })
    restore.handleResize()
    frames.runFrames()
    expect(extras.restoreAnchor).not.toHaveBeenCalled()
    expect(recordPosition).not.toHaveBeenCalled()
  })

  it('shows a place that no step of the flush showed, after the steps', () => {
    const frames = stubFrames()
    const { restore, ctx, placeAt, placed } = harness({
      saved: { atBottom: false, hasMoreNewer: false, rawScrollTop: 40 },
      totalHeight: 0,
    })
    restore.handleResize()
    frames.runFrames()
    expect(placed).toEqual([40])
    expect(ctx.showWaitingPlace).toHaveBeenCalledOnce()
    expect(vi.mocked(ctx.showWaitingPlace).mock.invocationCallOrder[0])
      .toBeGreaterThan(placeAt.mock.invocationCallOrder[0]!)
  })
})

describe('chatScrollViewportRestore paneHidden', () => {
  /** Set the height that the element of the harness reports. */
  const setClientHeight = (el: HTMLDivElement, height: number): void => {
    (el as { clientHeight: number }).clientHeight = height
  }

  it('counts the pane as hidden from a flush at a height of 0 until the flush of the show', () => {
    const frames = stubFrames()
    const { restore, el } = harness({ saved: undefined })
    restore.initClientHeight()
    expect(restore.paneHidden()).toBe(false)

    setClientHeight(el, 0)
    restore.handleResize()
    frames.runFrames()
    expect(restore.paneHidden()).toBe(true)

    // The browser gives the pane its layout box one frame before the flush of the show.
    setClientHeight(el, 500)
    restore.handleResize()
    expect(restore.paneHidden()).toBe(true)

    frames.runFrames()
    expect(restore.paneHidden()).toBe(false)
  })

  it('counts a pane that mounts with a height as shown', () => {
    const { restore } = harness({ saved: undefined })
    restore.initClientHeight()
    expect(restore.paneHidden()).toBe(false)
  })

  it('counts a pane that mounts with no height as hidden', () => {
    const { restore, el } = harness({ saved: undefined })
    setClientHeight(el, 0)
    restore.initClientHeight()
    expect(restore.paneHidden()).toBe(true)
  })

  it('counts the pane as hidden while the flush of a show waits for a scroll animation', () => {
    const frames = stubFrames()
    const { restore, state } = harness({ saved: undefined })
    state.animating = true
    restore.handleResize()

    // The flush takes another frame: it recorded no height yet.
    frames.runFrames()
    expect(restore.paneHidden()).toBe(true)

    state.animating = false
    frames.runFrames()
    expect(restore.paneHidden()).toBe(false)
  })
})

describe('chatScrollViewportRestore geometry settle', () => {
  it('re-arms the geometry-derived memos after a resize settles (no scroll event fires)', () => {
    const frames = stubFrames()
    const { restore, extras } = harness({ saved: undefined })
    restore.initClientHeight() // prevClientHeight = 500 -> the next resize is non-hidden
    restore.handleResize()
    frames.runFrames()
    // Geometry-derived memos read only when this ticks; a resize that clamped
    // scrollTop to an edge fired no scroll event, so the resize must tick it.
    expect(extras.onGeometrySettled).toHaveBeenCalled()
  })

  it('re-arms the geometry-derived memos after a restore at the mount', () => {
    const { restore, extras } = harness({
      saved: { atBottom: true, hasMoreNewer: true },
    })
    restore.restoreOnMount()
    // The jump to the latest page of an at-bottom save writes nothing that would re-arm them.
    expect(extras.forceScrollToBottom).toHaveBeenCalledOnce()
    expect(extras.onGeometrySettled).toHaveBeenCalledOnce()
  })
})
