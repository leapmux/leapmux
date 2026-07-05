import type { Accessor, Setter } from 'solid-js'
import type { DotCluster } from './chatRailPolicy'
import { createEffect, createMemo, createSignal, onCleanup, untrack } from 'solid-js'
import { clamp } from '~/lib/clamp'
import { dotClusterEqual, nearestDotWithin } from './chatRailPolicy'
import * as styles from './ChatScrollRail.css'
import { centerAxisY } from './chatScrollRailGeometry'

// ---------------------------------------------------------------------------
// Scroll-rail dot preview
//
// The rail's "which dot the single popover describes + when to warm its preview" state machine:
// a scrub target (the dot the dragging thumb is over) takes precedence over a hovered/focused
// dot, the popover is placed on the dot's centre-axis Y (clamped off the rail edges), and the
// preview is warmed instantly on hover/focus but DEBOUNCED during a scrub -- so dragging across a
// dense rail doesn't fire a GetAgentMessage per fly-over dot. Extracted from ChatScrollRail (where
// it was a signal, three memos, an effect, and a debounce timer tangled through the component) so
// the subtle precedence/debounce/cleanup behaviour is one named, unit-tested unit -- the sibling
// of createRailMetrics / createDragReleaseHold / createThumbDrag.
// ---------------------------------------------------------------------------

/** Rail px within which a dragging thumb counts as "over" a dot, revealing its scrub preview. */
const SCRUB_PREVIEW_RANGE_PX = 12

/**
 * How long the thumb must settle on a dot mid-scrub before its preview is fetched. A hover/focus
 * warms instantly; a SCRUB debounces, because dragging across a dense rail passes many dots and
 * warming each would fire a GetAgentMessage RPC per out-of-window mark crossed. Reset on every
 * scrub dot change, so only a dot the thumb lingers near is fetched -- the fly-over dots coalesce.
 */
export const SCRUB_WARM_DEBOUNCE_MS = 120

export interface DotPreviewDeps {
  /** The rail's current dot clusters (ascending by topPx). */
  dots: Accessor<DotCluster[]>
  /** The live drag fraction (null when not dragging), so a scrub can take popover precedence. */
  drag: Accessor<number | null>
  /** The rail's pixel height, for the centre-axis mapping and the popover clamp. */
  railHeight: Accessor<number>
  /** The current thumb height (px), for mapping a drag fraction to the thumb-centre rail-Y. */
  thumbHeightPx: Accessor<number>
  /** Kick off resolving a mark's preview (dot hover/focus or scrub-settle). Deduped upstream. */
  warmPreview?: (seq: bigint) => void
}

// Named ...Controller (not DotPreview) to avoid clashing with the sibling DotPreview presentation
// component in ChatScrollRailPreview -- this is the state machine that decides WHICH dot that
// component renders, not the component itself.
export interface DotPreviewController {
  /** The dot the single preview popover describes: the scrub target while dragging, else hover/focus. */
  activeDot: Accessor<DotCluster | null>
  /** The popover's rail-Y, clamped so a dot near the top/bottom doesn't clip past the wrapper. */
  popoverTopPx: Accessor<number>
  /** Set/clear the hovered/focused dot (from a dot's pointer/focus handlers and the rail teardown). */
  setHoverDot: Setter<DotCluster | null>
}

/**
 * Create the rail's dot-preview state machine (see the module header). Must be created within an
 * owner scope (it wires createEffect + onCleanup); ChatScrollRail creates it once at component top
 * level, exactly like its createRailMetrics / createDragReleaseHold siblings.
 */
export function createDotPreview(deps: DotPreviewDeps): DotPreviewController {
  // The dot the pointer is hovering / focusing (null when none). The rail shows ONE preview
  // popover for the "active" dot -- a scrub target takes precedence over this (see activeDot).
  const [hoverDot, setHoverDot] = createSignal<DotCluster | null>(null)

  // While dragging, the dot the thumb is currently over (nearest within range), so a scrub --
  // mouse OR touch -- reveals each marked message's preview as the thumb passes it. Null when
  // not dragging or the thumb is between dots.
  const scrubDot = createMemo(() => {
    const f = deps.drag()
    if (f === null)
      return null
    const rh = deps.railHeight()
    if (rh <= 0)
      return null
    const y = centerAxisY(f, rh, deps.thumbHeightPx()) // the thumb centre's rail-Y at this drag fraction
    return nearestDotWithin(deps.dots(), y, SCRUB_PREVIEW_RANGE_PX)
  })

  // The dot the single preview popover describes: the scrub target while dragging (it takes
  // precedence, so hovering a dot mid-scrub can't open a second popover), else the hovered/
  // focused dot. One source -> one popover, shown immediately (no hover delay).
  const activeDot = createMemo(() => scrubDot() ?? hoverDot())

  createEffect(() => {
    const h = hoverDot()
    if (!h)
      return
    const currentDots = deps.dots()
    // The exact hovered cluster is still present -- leave the popover alone.
    if (currentDots.some(d => dotClusterEqual(d, h)))
      return
    // The hovered cluster changed identity but may still stand for the same seq: a streaming
    // turn ticks maxSeq and re-rounds its topPx by a pixel, or a fresh mark lands in its pixel
    // and bumps the count. <For> is reference-keyed, so it tears the hovered button down and
    // mounts a new one -- and removing the element under the cursor fires no pointerleave, so
    // hoverDot still holds the STALE cluster. Re-anchor to the current cluster for the same seq
    // so the popover the reader is looking at FOLLOWS the dot instead of vanishing out from
    // under them; clear only when that seq is genuinely gone (its message was deleted/reseq'd).
    setHoverDot(currentDots.find(d => d.seq === h.seq) ?? null)
  })

  // The popover's rail-Y: centred on the active dot but clamped so a dot near the rail's top
  // or bottom doesn't push the card past the overflow-hidden wrapper and clip it.
  const popoverTopPx = createMemo(() => {
    const d = activeDot()
    if (!d)
      return 0
    const rh = deps.railHeight()
    const half = Math.min(styles.PREVIEW_POPOVER_MAX_H_PX / 2, rh / 2)
    return clamp(d.topPx, half, rh - half)
  })

  // Warm the active dot's preview so the popover fills in. A HOVER/focus warms instantly; a SCRUB
  // (thumb drag) debounces -- dragging across a dense rail changes activeDot dot-by-dot, and
  // warming each would fire a GetAgentMessage RPC per out-of-window mark it crosses. The effect
  // tracks ONLY activeDot (its scrub source, scrubDot, dedups per dot, so it changes when the
  // thumb reaches a NEW dot, not on every drag pixel); the scrub-vs-hover classification reads
  // drag() untracked so a fraction change doesn't itself reset the debounce.
  let scrubWarmTimer: ReturnType<typeof setTimeout> | undefined
  const clearScrubWarmTimer = () => {
    if (scrubWarmTimer !== undefined) {
      clearTimeout(scrubWarmTimer)
      scrubWarmTimer = undefined
    }
  }
  onCleanup(clearScrubWarmTimer)
  createEffect(() => {
    const d = activeDot()
    // A new active dot supersedes any pending scrub warm (the thumb moved on before it settled).
    clearScrubWarmTimer()
    if (!d)
      return
    if (untrack(() => deps.drag()) === null) {
      deps.warmPreview?.(d.seq) // hover/focus: instant
      return
    }
    // Scrub: warm only once the thumb has settled on this dot for the debounce window.
    scrubWarmTimer = setTimeout(() => {
      scrubWarmTimer = undefined
      deps.warmPreview?.(d.seq)
    }, SCRUB_WARM_DEBOUNCE_MS)
  })

  return { activeDot, popoverTopPx, setHoverDot }
}
