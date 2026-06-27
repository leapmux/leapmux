import type { AgentChatMessage } from '~/generated/leapmux/v1/agent_pb'
import type { ScrollAnchor } from '~/stores/chatTypes'
import { createEffect } from 'solid-js'
import { distFromBottom } from './chatScrollGeometry'

/**
 * Fetching one raw page is a large mutation: it prepends/appends rows, re-pins the
 * viewport, and may immediately trigger an opposite-end trim. Do not do that for a
 * tiny shortfall just below the ideal buffer target, or slow scrolling can fall into a
 * fetch -> trim -> tiny-deficit -> fetch loop. The trim logic still keeps the full
 * target when content exists; this is only the lower refill watermark.
 */
const BUFFER_REFILL_HYSTERESIS_SCREENS = 0.5

/**
 * The scroll-buffer filler: pre-fetches RAW history pages to keep a buffer of
 * VISIBLE content beyond the viewport in each direction, so scrolling stays smooth
 * even in a hidden-heavy stretch (where a raw page adds almost no scroll height and
 * the old load-at-the-edge policy thrashed).
 *
 * Only visible rows have height, so the buffer is measured in pixels off the live
 * geometry: scrollTop is the visible content ABOVE the viewport, distFromBottom the
 * visible content BELOW. While either side has less than `bufferTargetPx` AND more
 * history to load, it pages that direction; loadOlder/loadNewer grow the window to
 * the raw CEILING (chat.store), so the buffer accumulates rather than sliding.
 *
 * The fill loops (each page lands -> the messages effect re-runs -> fill again) until
 * both buffers are satisfied or history is exhausted. When consecutive loads don't grow
 * the LOADED SIDE's own visible buffer (older -> content prepended ABOVE a stable ref
 * row, newer -> content appended BELOW it -- each measured off the geometry, so a
 * mid-fetch scroll in either direction, the opposite side's load, or the far-end trim
 * can't be miscredited as progress on the loaded side), it deprioritizes that side while
 * another deficient side can still surface visible content. If both sides are all-hidden,
 * the filler keeps paging the user's scroll direction until history is exhausted or the
 * store's raw window ceiling stops growth.
 *
 * Driven from BOTH a reactive effect (window changes) and handleScroll (the
 * viewport moving toward an edge shrinks that side's buffer). A self-contained unit.
 *
 * This is mechanism #2 of the NO-SKIP INVARIANT (see FLING_OVERSCAN_LOOKAHEAD_MS):
 * growing the loaded window ahead of the viewport is what lets a fast fling keep
 * moving. When a fling outruns it, the loaded-window scroll bound makes the view
 * STALL at the loading edge until the next page lands -- it never skips a message.
 */
export function createScrollBufferFiller(deps: {
  getEl: () => HTMLDivElement | undefined
  messages: () => AgentChatMessage[]
  bufferTargetPx: () => number
  hasOlder: () => boolean
  hasNewer: () => boolean
  fetchingOlder: () => boolean
  fetchingNewer: () => boolean
  onLoadOlder: () => void
  onLoadNewer: () => void
  lastScrollDir: () => 'older' | 'newer'
  // Pause the pre-fetch (a forced stop halts loading until the next scroll). The
  // explicit wheel/key-at-edge loads bypass the filler, so the user can still page.
  paused: () => boolean
  // Suppress ONLY the older-side auto-load (a viewport restore arms this so an
  // immediate older prepend can't yank the just-restored position; cleared once the
  // user explicitly scrolls to the very top). The newer side stays free -- an append
  // lands below the viewport and never disturbs it -- so a quiescent thread scrolled
  // toward its loaded bottom still pages forward instead of stalling.
  suppressOlder: () => boolean
  // BOTH sides' progress is measured by how much content a load added on its side of a
  // STABLE reference row, NOT by raw scrollTop / distFromBottom growth. captureAnchor()
  // snapshots the row at the viewport top when a load fires; contentAbove(anchor) is that
  // row's content-coordinate offset (the height ABOVE it) and contentBelow(anchor) is the
  // height BELOW it, both read from the geometry offset map. An OLDER load grows
  // contentAbove (a prepend); a NEWER load grows contentBelow (an append). The delta from
  // the captured baseline is the load's visible height -- immune to user scroll (we track
  // the ROW, not the viewport, so a mid-fetch scroll in EITHER direction can't mask a
  // productive load), to the OPPOSITE side's load (above vs below the ref never cross),
  // and to the far-end trim (it removes rows on the side away from the ref, which net out
  // of that side's measure). This replaces the old scrollTop / distFromBottom metrics,
  // which a mid-fetch scroll could drive past the baseline and mis-count a productive
  // load as no-progress; it also subsumes the old pendingFlingDrift correction (the
  // geometry offset reflects a prepend regardless of a deferred re-pin write). Either
  // returns null when the ref row was trimmed / the
  // virtualizer can't anchor -- which attributeLastLoadProgress reads as unmeasurable
  // PROGRESS (a trim implies the window grew), distinct from a captured-but-flat measure.
  captureAnchor: () => ScrollAnchor | null
  contentAbove: (anchor: ScrollAnchor) => number | null
  contentBelow: (anchor: ScrollAnchor) => number | null
  // True when the in-memory window is within a page of the RAW CEILING of server rows,
  // so it can no longer meaningfully GROW -- a fetch only shuffles the window. At the
  // ceiling a fetch on one side trims the opposite end to the ceiling, reaping that
  // side's buffer and re-arming its deficit: left unchecked the two sides ping-pong
  // fetches forever. So at the ceiling the filler pages ONLY the side the user is
  // scrolling TOWARD, and only when scrolled away from the live tail (its far end is
  // re-fetchable). The one-page margin (see chat.store.atWindowCeiling) keeps the last
  // allowed fetch from crossing the ceiling and dropping the live tail.
  atCeiling: () => boolean
}) {
  // Which side the last load paged, the captured reference row, and that side's content
  // measure at the moment of the load (older -> content ABOVE the ref, newer -> content
  // BELOW it), so the next fill can tell whether the load grew THAT side's visible buffer
  // (progress) or not (all hidden). `lastRefAnchor` is null when there was no last load.
  let lastLoadedSide: 'older' | 'newer' | null = null
  let lastLoadedMetric = 0
  let lastRefAnchor: ScrollAnchor | null = null
  // Per-side "this side is paging all-hidden content" flags, keyed by side so the
  // attribute/select logic indexes a side rather than branching on it. A side is
  // deprioritized once stuck so an all-hidden run on ONE side can't starve a still-
  // productive other side. Cleared when that side makes progress or on re-arm.
  const stuck: Record<'older' | 'newer', boolean> = { older: false, newer: false }

  // Reset per-window side-selection state once buffers are satisfied or a jump replaces
  // the message window.
  const rearm = () => {
    lastLoadedSide = null
    lastLoadedMetric = 0
    lastRefAnchor = null
    stuck.older = false
    stuck.newer = false
  }

  // At the ceiling the window can't grow, so a fetch only SHUFFLES it (a prepend forces
  // an opposite-end trim and vice versa). Paging is allowed there only TOWARD
  // lastScrollDir AND only when scrolled away from the live tail -- the far end it would
  // drop is then re-fetchable history, not the pinned live tail. Below the ceiling both
  // sides are free. Expressed positively (allowed) to avoid a double-negative at the use
  // site, and parameterized on the side so the two directions can't drift.
  const ceilingAllowed = (side: 'older' | 'newer') =>
    !deps.atCeiling() || (deps.hasNewer() && deps.lastScrollDir() === side)

  // Which sides are under the buffer target, have more to load, AND are allowed to
  // fetch right now (ceiling gate + the post-restore older suppression). Hidden rows
  // have zero height, so scrollTop / distFromBottom ARE the visible buffers above/below.
  const deficits = () => {
    const el = deps.getEl()
    if (!el)
      return { older: false, newer: false }
    const target = deps.bufferTargetPx()
    const triggerPx = Math.max(0, target - el.clientHeight * BUFFER_REFILL_HYSTERESIS_SCREENS)
    return {
      older: ceilingAllowed('older') && el.scrollTop < triggerPx && deps.hasOlder() && !deps.suppressOlder(),
      newer: ceilingAllowed('newer') && distFromBottom(el) < triggerPx && deps.hasNewer(),
    }
  }

  // The content measure for a side: how much content sits on THAT side of the captured
  // ref row (older -> ABOVE, newer -> BELOW), read from the geometry offset map. Both are
  // immune to user scroll (we track the ROW), the opposite side's load, and the far-end
  // trim. Null when the ref row was trimmed / the virtualizer can't anchor.
  const sideContent = (side: 'older' | 'newer'): number | null =>
    !lastRefAnchor ? null : (side === 'older' ? deps.contentAbove : deps.contentBelow)(lastRefAnchor)

  // Fold the LAST load's outcome into the per-side selection state, judged by THAT side's
  // own visible-content growth: a flat signal means the page was all hidden (mark that
  // side stuck so selection stops wasting loads on it while another side can make
  // progress); growth means it surfaced content (unstick that side).
  //
  // A null read splits two ways. When NO ref could be captured at the last load
  // (`lastRefAnchor` null -- an empty/unanchorable window), treat it like a flat
  // all-hidden load for side selection. But when a ref WAS captured and has since
  // resolved to null, the ref row was TRIMMED out of the window: a trim only fires
  // because the loaded side grew enough to force the opposite-end cap-trim, so the load
  // DID surface content -- unmeasurable PROGRESS, not a stall.
  const attributeLastLoadProgress = () => {
    if (!lastLoadedSide)
      return
    const now = sideContent(lastLoadedSide)
    const refTrimmed = lastRefAnchor !== null && now === null
    if (refTrimmed || (now != null && now > lastLoadedMetric)) {
      stuck[lastLoadedSide] = false
    }
    else {
      stuck[lastLoadedSide] = true
    }
  }

  // Which deficient side to page next (a pure read of the deficits + stuck flags + last
  // scroll dir). Prefer a deficient side that isn't yet known all-hidden (not stuck), so
  // an all-hidden run on one side can't starve a still-productive other side. Within
  // that, honor the user's last scroll direction (a content-fits / all-hidden window
  // reads as deficient at both edges). If BOTH deficient sides are stuck, keep paging
  // the preferred one.
  const selectSideToLoad = (older: boolean, newer: boolean): 'older' | 'newer' => {
    const olderEligible = older && !stuck.older
    const newerEligible = newer && !stuck.newer
    if (olderEligible || newerEligible)
      return olderEligible && (deps.lastScrollDir() === 'older' || !newerEligible) ? 'older' : 'newer'
    return older && (deps.lastScrollDir() === 'older' || !newer) ? 'older' : 'newer'
  }

  const fill = () => {
    const el = deps.getEl()
    // No fetch for a hidden/inactive tab (clientHeight 0), while one is in flight
    // (let it land and re-run the fill before deciding the next), or while paused by
    // a forced stop (resumes on the user's next scroll).
    if (!el || el.clientHeight === 0 || deps.paused() || deps.fetchingOlder() || deps.fetchingNewer())
      return

    const { older, newer } = deficits()
    if (!older && !newer) {
      // Genuinely satisfied / nothing to load, OR blocked at the ceiling. Reset side
      // selection state either way because no load is happening in this window state.
      rearm()
      return
    }
    // guard -> attribute -> select -> fire. Only ONE side is paged per fill; the two are
    // never raced together.
    attributeLastLoadProgress()
    const side = selectSideToLoad(older, newer)
    lastLoadedSide = side
    // Anchor to the row at the viewport top and baseline this side's content measure
    // (older -> content ABOVE the ref, newer -> content BELOW it) from the geometry offset
    // map, so the next fill's growth check is immune to user scroll, the opposite side's
    // load, and the far-end trim.
    lastRefAnchor = deps.captureAnchor()
    lastLoadedMetric = sideContent(side) ?? 0
    if (side === 'older')
      deps.onLoadOlder()
    else
      deps.onLoadNewer()
  }

  createEffect(() => {
    deps.messages() // re-run as the window content changes (a page landed)
    fill()
  })

  return { fill, rearm }
}
