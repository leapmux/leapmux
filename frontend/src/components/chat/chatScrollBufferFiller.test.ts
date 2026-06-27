import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import { createRoot } from 'solid-js'
import { describe, expect, it } from 'vitest'
import { createScrollBufferFiller } from './chatScrollBufferFiller'

// A minimal scroll element backed by mutable per-side buffer metrics: `scrollTop` is the
// visible content ABOVE the viewport (the older buffer) and `distBottom` the visible
// content BELOW (the newer buffer). scrollHeight is derived so distFromBottom(el)
// (= scrollHeight - scrollTop - clientHeight) equals distBottom. bufferTargetPx is huge,
// so BOTH sides stay deficient regardless of growth; per-side metric GROWTH is what
// distinguishes a visible-content page from an all-hidden one.
const CLIENT_HEIGHT = 100
const HUGE_TARGET = 1e9

/**
 * Build a filler over mutable dep state. `olderGrowsBy` / `newerGrowsBy` are the visible
 * height each load on that side adds to ITS OWN buffer; 0 models an all-hidden page. The
 * progress signals are content-anchored: `olderAbove` (content above the ref row, read by
 * contentAbove()) and `newerBelow` (content below it, read by contentBelow()). Each grows
 * ONLY when a load on that side surfaces visible content -- never from scrollTop /
 * distBottom (user scroll) or the opposite side -- modeling the geometry-offset
 * measurement. scrollTop/distBottom drive only the buffer DEFICIT (kept < the huge target,
 * so both sides stay deficient). Returns hooks to drive + observe the filler.
 */
function harness(opts: {
  olderGrowsBy?: number
  newerGrowsBy?: number
  bufferTargetPx?: number
  scrollTop?: number
  distBottom?: number
} = {}) {
  const olderGrowsBy = opts.olderGrowsBy ?? 0
  const newerGrowsBy = opts.newerGrowsBy ?? 0
  const bufferTargetPx = opts.bufferTargetPx ?? HUGE_TARGET
  let enabled = false
  let scrollTop = opts.scrollTop ?? 0
  let distBottom = opts.distBottom ?? 0
  // Content above / below the captured ref row (the geometry offsets contentAbove /
  // contentBelow read). Each grows ONLY when a load on that side prepends/appends visible
  // content -- never from scrollTop/distBottom -- so each side's progress signal is immune
  // to user scroll and to the opposite side's load.
  let olderAbove = 0
  let newerBelow = 0
  let olderLoads = 0
  let newerLoads = 0
  let lastScrollDir: 'older' | 'newer' = 'older'
  let hasOlder = true
  let hasNewer = true
  let atCeiling = false
  let suppressOlder = false
  // When set, contentAbove/contentBelow resolve null (the captured ref row was TRIMMED out
  // of the window) -- a churning window, which the filler reads as unmeasurable progress.
  // When `unanchorable` is set, captureAnchor itself returns null (an empty/unanchorable
  // window).
  let refTrimmed = false
  let unanchorable = false

  const getEl = (): HTMLDivElement | undefined =>
    enabled
      ? ({ scrollTop, clientHeight: CLIENT_HEIGHT, scrollHeight: scrollTop + CLIENT_HEIGHT + distBottom } as HTMLDivElement)
      : undefined

  const filler = createRoot(dispose =>
    Object.assign(
      createScrollBufferFiller({
        getEl,
        messages: () => [] as AgentChatMessage[],
        bufferTargetPx: () => bufferTargetPx,
        hasOlder: () => hasOlder,
        hasNewer: () => hasNewer,
        fetchingOlder: () => false,
        fetchingNewer: () => false,
        onLoadOlder: () => {
          olderLoads++
          olderAbove += olderGrowsBy // older prepend grows the ABOVE buffer (0 = all hidden)
        },
        onLoadNewer: () => {
          newerLoads++
          newerBelow += newerGrowsBy // a newer append grows the BELOW buffer (0 = all hidden)
        },
        lastScrollDir: () => lastScrollDir,
        paused: () => false,
        suppressOlder: () => suppressOlder,
        atCeiling: () => atCeiling,
        // A non-null ref token; the mock measures growth via the global olderAbove /
        // newerBelow (every prepend sits above, every append below, any captured row), so
        // the token's identity is irrelevant. `unanchorable` models an empty window where
        // no ref can be captured (anchorAt returns null).
        captureAnchor: () => unanchorable ? null : ({ id: 'ref', offsetWithinRow: 0 }),
        contentAbove: () => refTrimmed ? null : olderAbove,
        contentBelow: () => refTrimmed ? null : newerBelow,
      }),
      { dispose },
    ))

  return {
    filler,
    // The effect's creation-time fill() ran while el was disabled (a no-op); enable the
    // element now so manually-driven fill()s do real work from a clean baseline.
    enable: () => { enabled = true },
    setLastScrollDir: (d: 'older' | 'newer') => { lastScrollDir = d },
    setHas: (o: boolean, n: boolean) => {
      hasOlder = o
      hasNewer = n
    },
    setCeiling: (v: boolean) => { atCeiling = v },
    setSuppressOlder: (v: boolean) => { suppressOlder = v },
    setRefTrimmed: (v: boolean) => { refTrimmed = v },
    setUnanchorable: (v: boolean) => { unanchorable = v },
    setOlderBuffer: (px: number) => { scrollTop = px },
    setNewerBuffer: (px: number) => { distBottom = px },
    // Simulate a live-tail append BETWEEN fills: it grows the BELOW buffer (content + DOM)
    // without a filler load -- the growth that would fool a global-total progress signal
    // into crediting an all-hidden OLDER fetch.
    bumpTail: (px: number) => {
      newerBelow += px
      distBottom += px
    },
    // Simulate a USER scroll-up between fills: scrollTop falls, distBottom rises (content
    // moves from below the viewport to above it) -- but NO content is loaded, so the
    // content-anchored progress signals (olderAbove/newerBelow) must ignore it.
    scrollUpBy: (px: number) => {
      scrollTop = Math.max(0, scrollTop - px)
      distBottom += px
    },
    // The mirror: a USER scroll-down (scrollTop rises, distFromBottom falls). The
    // content-anchored newer signal must ignore it (the bug a raw distFromBottom had).
    scrollDownBy: (px: number) => {
      scrollTop += px
      distBottom = Math.max(0, distBottom - px)
    },
    counts: () => ({ olderLoads, newerLoads }),
  }
}

describe('chatscrollbufferfiller', () => {
  it('requires meaningful buffer depletion before fetching a full page', () => {
    const older = harness({ bufferTargetPx: 300, scrollTop: 260 })
    older.enable()
    older.setHas(true, false)
    older.setLastScrollDir('older')
    older.filler.fill()
    expect(older.counts().olderLoads).toBe(0)

    older.setOlderBuffer(249)
    older.filler.fill()
    expect(older.counts().olderLoads).toBe(1)

    const newer = harness({ bufferTargetPx: 300, distBottom: 260 })
    newer.enable()
    newer.setHas(false, true)
    newer.setLastScrollDir('newer')
    newer.filler.fill()
    expect(newer.counts().newerLoads).toBe(0)

    newer.setNewerBuffer(249)
    newer.filler.fill()
    expect(newer.counts().newerLoads).toBe(1)
  })

  it('keeps serving a productive newer side instead of wedging it behind an all-hidden older run', () => {
    // older loads never grow their buffer (scrollTop flat = all hidden); newer loads grow
    // theirs. A single shared counter would page older, climb to the cap, and halt BOTH
    // sides. The per-side model excludes the stuck older side and keeps newer flowing.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 50 })
    h.enable()
    for (let i = 0; i < 40; i++)
      h.filler.fill()

    const { olderLoads, newerLoads } = h.counts()
    // Older is probed once or twice, then excluded as stuck; newer carries the rest.
    expect(olderLoads).toBeLessThanOrEqual(2)
    expect(newerLoads).toBeGreaterThan(30)
  })

  it('counts a productive older load as progress even while the user scrolls UP mid-fetch', () => {
    // The older-progress signal is the content prepended above a stable ref row (geometry
    // offset), NOT raw scrollTop. A user actively scrolling UP during the fetch DROPS
    // scrollTop and would mask the prepend under a scrollTop metric -- making a
    // productive older load read as no-progress and wrongly deprioritizing it mid-scroll
    // through visible older history. The content-anchored measurement ignores the scroll,
    // so productive loads keep flowing. (This also subsumes the old fling-deferred-write
    // case: the geometry offset reflects the prepend regardless of whether the corrective
    // scrollTop write was deferred.)
    const h = harness({ olderGrowsBy: 80 })
    h.enable()
    h.setHas(true, false) // only older has more, so every fill pages older
    h.setLastScrollDir('older')
    for (let i = 0; i < 40; i++) {
      h.filler.fill()
      h.scrollUpBy(200) // the user keeps scrolling up between fills (scrollTop falls)
    }

    // Productive older loads keep flowing despite the scroll.
    expect(h.counts().olderLoads).toBe(40)
  })

  it('counts a productive newer load as progress even while the user scrolls DOWN mid-fetch', () => {
    // The mirror of the older case: the newer-progress signal is content appended BELOW a
    // stable ref row (geometry offset), NOT raw distFromBottom. A user scrolling DOWN
    // during the fetch DROPS distFromBottom and would mask the append under a
    // distFromBottom metric -- mis-counting a productive newer load as no-progress and
    // wrongly deprioritizing it. The content-anchored measurement ignores the scroll.
    const h = harness({ newerGrowsBy: 80 })
    h.enable()
    h.setHas(false, true) // only newer has more, so every fill pages newer
    h.setLastScrollDir('newer')
    for (let i = 0; i < 40; i++) {
      h.filler.fill()
      h.scrollDownBy(200) // the user keeps scrolling down between fills (distFromBottom falls)
    }

    expect(h.counts().newerLoads).toBe(40)
  })

  it('keeps paging the preferred side when both sides are all-hidden', () => {
    // Neither side grows its buffer: both go stuck, so with no productive side to prefer
    // the filler keeps paging the user's scroll direction until history ends.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    for (let fills = 0; fills < 40; fills++)
      h.filler.fill()

    const { olderLoads, newerLoads } = h.counts()
    expect(olderLoads + newerLoads).toBe(40)
    expect(olderLoads).toBeGreaterThan(30)
    expect(newerLoads).toBeGreaterThanOrEqual(1)
  })

  it('treats a trimmed captured ref as progress', () => {
    // A sustained page-up near the ceiling: every older load prepends content, forcing the
    // opposite-end cap-trim that evicts the captured ref row, so contentAbove resolves null.
    // A trimmed ref means the window GREW, so it must read as unmeasurable progress and
    // keep the side eligible.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    h.setHas(true, false) // older history exists; at the live tail
    h.setRefTrimmed(true) // every contentAbove read resolves null (ref trimmed each fill)
    for (let i = 0; i < 40; i++)
      h.filler.fill()
    expect(h.counts().olderLoads).toBe(40)
  })

  it('keeps paging an unanchorable all-hidden window until history state changes', () => {
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    h.setUnanchorable(true)
    for (let fills = 0; fills < 40; fills++)
      h.filler.fill()

    const { olderLoads, newerLoads } = h.counts()
    expect(olderLoads + newerLoads).toBe(40)
    expect(olderLoads).toBeGreaterThan(30)
    expect(newerLoads).toBeGreaterThanOrEqual(1)
  })

  it('does not credit a concurrent live-tail append to an all-hidden older load', () => {
    // The older run is all hidden (scrollTop flat), but a live tail streams in below
    // (distBottom grows between fills). A GLOBAL-total progress signal would credit that
    // tail growth to the older fetch and keep choosing older; the per-side metric marks
    // older stuck, so a productive newer side takes over.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 50 })
    h.enable()
    h.setHas(true, true)
    for (let fills = 0; fills < 40; fills++) {
      h.filler.fill()
      h.bumpTail(50) // a live-tail append grows the BELOW buffer between fills
    }

    const { olderLoads, newerLoads } = h.counts()
    expect(olderLoads).toBeLessThanOrEqual(2)
    expect(newerLoads).toBeGreaterThan(30)
  })

  it('re-arms per-side stuck state once both buffers are satisfied', () => {
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 50 })
    h.enable()
    for (let i = 0; i < 5; i++)
      h.filler.fill()
    const before = h.counts()
    expect(before.olderLoads).toBeLessThanOrEqual(2)

    // Both buffers satisfied (no more to load): the next fill re-arms. When older
    // becomes deficient again, the preferred older side is probed again rather than
    // remaining deprioritized from the prior window state.
    h.setHas(false, false)
    h.filler.fill()
    h.setHas(true, true)
    h.filler.fill()
    expect(h.counts().olderLoads).toBe(before.olderLoads + 1)
  })

  it('rearm() clears per-side stuck state for a jump window-replace', () => {
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 50 })
    h.enable()
    for (let i = 0; i < 5; i++)
      h.filler.fill()
    const before = h.counts()

    // A jump replaces the window; rearm() drops carried-over side-selection state.
    h.filler.rearm()
    h.filler.fill()
    expect(h.counts().olderLoads).toBe(before.olderLoads + 1)
  })

  it('keeps paging a below-ceiling all-hidden run', () => {
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    h.setHas(true, false) // older exists, at the live tail, NOT at the ceiling
    for (let fills = 0; fills < 40; fills++)
      h.filler.fill()

    expect(h.counts().olderLoads).toBe(40)
  })

  it('at the ceiling on the live tail, an older deficit does not fetch', () => {
    // The window can't grow (at ceiling) and the newest end is the pinned live tail, so an
    // older fetch is futile -- the filler fetches nothing.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    h.setHas(true, false) // older exists, at the live tail
    h.setCeiling(true)
    h.filler.fill()
    expect(h.counts().olderLoads).toBe(0)
  })

  it('a post-restore SUPPRESSED older side at the ceiling does not fetch', () => {
    // suppressOlder (armed by a near-top restore) gates the older PRE-FETCH.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    h.setHas(true, false)
    h.setCeiling(true)
    h.setSuppressOlder(true)
    h.filler.fill()
    expect(h.counts().olderLoads).toBe(0)
  })

  it('at the ceiling, a deficit on the side scrolled AWAY from does not fetch', () => {
    // At the ceiling, scrolled away (hasNewer) and parked at the very oldest (hasOlder
    // false), near the in-memory bottom (newer buffer thin). The newer deficit is real but
    // the user is scrolling OLDER, so the ceiling gate blocks newer by design.
    const h = harness({ olderGrowsBy: 0, newerGrowsBy: 0 })
    h.enable()
    h.setHas(false, true) // at the very oldest; newer history exists
    h.setCeiling(true)
    h.setLastScrollDir('older')
    h.filler.fill()
    expect(h.counts()).toEqual({ olderLoads: 0, newerLoads: 0 })
  })

  it('at the ceiling scrolled away, pages ONLY the lastScrollDir side (no ceiling ping-pong)', () => {
    // Scrolled away from the tail with both buffers deficient at the ceiling: paging
    // either side reaps the other's buffer. Only the side the user is scrolling toward
    // pages (dropping the re-fetchable far end); the opposite side never auto-fills.
    const older = harness({ olderGrowsBy: 50, newerGrowsBy: 50 })
    older.enable()
    older.setHas(true, true) // both sides have more (scrolled away from tail)
    older.setCeiling(true)
    older.setLastScrollDir('older')
    for (let i = 0; i < 20; i++)
      older.filler.fill()
    // The blocked side NEVER fills; the toward side pages EVERY iteration.
    expect(older.counts().newerLoads).toBe(0)
    expect(older.counts().olderLoads).toBe(20)

    const newer = harness({ olderGrowsBy: 50, newerGrowsBy: 50 })
    newer.enable()
    newer.setHas(true, true)
    newer.setCeiling(true)
    newer.setLastScrollDir('newer')
    for (let i = 0; i < 20; i++)
      newer.filler.fill()
    expect(newer.counts().olderLoads).toBe(0)
    expect(newer.counts().newerLoads).toBe(20)
  })
})
