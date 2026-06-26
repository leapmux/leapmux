import { monotonicNow } from './chatScrollGeometry'

/**
 * How long (ms) a programmatic-write marker stays eligible to match its echoing
 * scroll event. A browser delivers the `scroll` event for a synchronous scrollTop
 * write asynchronously -- usually within a frame, but under load it can be several
 * frames late. The marker must outlive that delivery so a late echo is still
 * recognized as ours (the bug a fixed one-frame deadline had: the echo arriving after
 * the frame was misread as a user gesture, capturing an anchor / firing a false
 * fling). It must NOT linger indefinitely either -- a write that produces NO echo
 * (scrollTop didn't actually move) would otherwise leave the marker forever, swallowing
 * a much-later genuine user scroll to the same pixel. ~150ms comfortably covers
 * worst-case delivery while keeping the stale-marker window short.
 */
const ECHO_MARKER_TTL_MS = 150

/**
 * Upper bound on concurrently-pending programmatic-write markers. A re-pin burst arms
 * one per write that MOVES scrollTop; only a couple are ever pending at once (a write
 * plus a same-handler re-stick), so a small ring covers it. The cap is a backstop that
 * evicts the oldest if a pathological burst overflows it.
 */
const MAX_ECHO_MARKERS = 8

interface EchoMarker {
  /** The scrollTop value this programmatic write left. */
  top: number
  /** The clock reading at the write, so a marker whose echo never arrived ages out. */
  at: number
  /** Monotonic generation, so a consumer removes the exact marker its event matched. */
  gen: number
}

/**
 * The programmatic-scroll guard: tracks the scrollTop values the hook last wrote
 * itself so the scroll events they echo back are recognized as ours, not user
 * gestures. Matching by POSITION + RECENCY (not a frame-delayed boolean) is what keeps a
 * genuine user scroll from being swallowed during a rapid re-pin burst, AND keeps a
 * late-delivered echo from being misread as a user gesture.
 *
 * Markers live in a small RING, not a single slot: a re-stick fired DURING the handling
 * of an earlier write's echo arms a SECOND pending marker, and two writes <1px apart in
 * separate frames each have their own echo still in flight. A single slot would clobber
 * the first, so consuming one echo would strand the other to be read as a user gesture.
 * The ring keeps each pending echo distinct; `consumeEcho` removes the one its event
 * matched (by generation) and spares the rest. Extracted from the scroll hook as a
 * self-contained unit -- its only dependencies are the scroll element and a clock
 * (injected for tests; defaults to monotonicNow).
 */
export function createProgrammaticScrollGuard(
  getEl: () => HTMLDivElement | undefined,
  // Called after each programmatic write that MOVES scrollTop, with the post-write
  // (clamped) scrollTop, so a consumer (the scroll-velocity tracker) can advance its
  // baseline past a displacement that isn't a user gesture.
  onMark?: (pos: number) => void,
  // The clock used to age out a marker whose echo never arrives. Injected so a test
  // can drive the TTL deterministically.
  now: () => number = monotonicNow,
) {
  const markers: EchoMarker[] = []
  // Monotonic generation, bumped on every mark, so a consumer can name the exact marker
  // its event matched even when a fresher one was armed mid-handler.
  let writeGen = 0

  const isFresh = (m: EchoMarker): boolean => now() - m.at < ECHO_MARKER_TTL_MS

  // Drop expired markers (a write that produced no echo, or whose echo never arrived) so
  // the ring holds only still-eligible ones rather than filling with stale entries.
  const pruneStale = () => {
    for (let i = markers.length - 1; i >= 0; i--) {
      if (!isFresh(markers[i]))
        markers.splice(i, 1)
    }
  }

  /**
   * Mark the current (post-write, possibly browser-clamped) scrollTop as our own
   * programmatic write. The marker is matched by position + recency: it stays eligible
   * until its echo is consumed (consumeEcho), aged out (ECHO_MARKER_TTL_MS), or evicted
   * when a long burst overflows the ring -- so an echo delivered a few frames late is
   * still recognized, while a write that produced no echo ages out instead of lingering.
   */
  const mark = () => {
    const el = getEl()
    if (!el)
      return
    pruneStale()
    writeGen++
    markers.push({ top: el.scrollTop, at: now(), gen: writeGen })
    if (markers.length > MAX_ECHO_MARKERS)
      markers.shift()
    onMark?.(el.scrollTop)
  }

  /** Write scrollTop without it being interpreted as a user gesture. */
  const write = (top: number) => {
    const el = getEl()
    if (!el)
      return
    const before = el.scrollTop
    el.scrollTop = top
    // Only arm a marker when the write actually MOVED scrollTop. A write that lands on
    // the current (browser-clamped) position fires no scroll event, so the marker would
    // never be consumed -- it would instead linger the full TTL and swallow a genuine
    // user scroll that happens to land within 1px of that pixel.
    if (el.scrollTop !== before)
      mark()
  }

  // The index of a fresh marker within 1px of the current scrollTop, or -1. Searched
  // oldest-first so the earliest still-pending echo is the one matched/consumed (FIFO),
  // which keeps a same-position pair from consuming each other out of order.
  const matchIndex = (): number => {
    const el = getEl()
    if (!el)
      return -1
    for (let i = 0; i < markers.length; i++) {
      if (isFresh(markers[i]) && Math.abs(el.scrollTop - markers[i].top) < 1)
        return i
    }
    return -1
  }

  /**
   * True when the current scroll event is our own programmatic write echoing back: a
   * scroll event landing within 1px of a position we recently wrote, while that marker
   * is still fresh. Read fresh on each call -- a re-stick can issue a new programmatic
   * write between two reads, so a cached snapshot would be stale.
   */
  const isEcho = (): boolean => matchIndex() >= 0

  /**
   * The generation of the marker the CURRENT scroll position matches (0 = none). The
   * handler snapshots this at the START of an event -- before a mid-handler re-stick can
   * arm a fresher marker -- so it names THIS event's own marker, and `consumeEcho` then
   * removes exactly that one rather than the freshest write (whose echo is still pending).
   */
  const matchedEchoGen = (): number => {
    const i = matchIndex()
    return i >= 0 ? markers[i].gen : 0
  }

  /**
   * Consume the marker the just-handled scroll event matched (named by `gen`), so a
   * LATER user gesture that coincidentally lands within 1px of the same pixel is
   * recognized as a real scroll rather than a second echo. Removes only that one marker,
   * sparing a separate marker armed by a mid-handler re-stick -- its echo is still
   * pending. A non-positive `gen` (the event matched nothing) is a no-op.
   */
  const consumeEcho = (gen: number) => {
    if (gen <= 0)
      return
    const i = markers.findIndex(m => m.gen === gen)
    if (i >= 0)
      markers.splice(i, 1)
  }

  return { mark, write, isEcho, matchedEchoGen, consumeEcho }
}
