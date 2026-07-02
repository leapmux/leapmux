import type { JSX } from 'solid-js'
import { createMemo } from 'solid-js'
import { fnv1a32Hex } from '~/lib/stringDigest'
import { rowSkeleton, rowSkeletonFill } from './ChatRowSkeleton.css'

// ---------------------------------------------------------------------------
// Skeleton row body: ONE full-height Oat skeleton block, alpha-masked into a
// stack of ragged text-length bars. The mask (an SVG data URI tiling
// vertically) does the work a per-line div stack used to do, at O(1) cost for
// ANY row height — the old stack needed one div per 24px and a line cap, so a
// row taller than the cap showed blank space below the last bar. Oat's
// shimmer animation lives on the block's background; the mask clips it into
// the bars, so every bar shimmers in phase.
// ---------------------------------------------------------------------------

/** Vertical rhythm per bar: Oat's 1rem `.line` height + a --space-2-ish gap. */
export const SKELETON_LINE_PITCH_PX = 24

/** Bar thickness within the pitch (matches Oat's `.line` height at 1rem). */
export const SKELETON_LINE_HEIGHT_PX = 16

/**
 * Bars per mask tile. The seeded ragged pattern repeats every this many lines
 * — enough variety that the repetition is imperceptible, small enough that
 * the data URI stays compact.
 */
export const SKELETON_TILE_LINES = 12

/** Width variation range: lines span 55%..95% like ragged text. */
const LINE_WIDTH_MIN_PERCENT = 55
const LINE_WIDTH_SPAN_PERCENT = 41

/**
 * Deterministic pseudo-random line width for (seed, index): the same row shows
 * the same pattern on every render (no flicker across reactive re-runs), and
 * tests can pin exact values. Seeded per row id so neighboring rows differ.
 */
export function skeletonLineWidthPercent(seed: string, index: number): number {
  return LINE_WIDTH_MIN_PERCENT + (Number.parseInt(fnv1a32Hex(`${seed}:${index}`), 16) % LINE_WIDTH_SPAN_PERCENT)
}

/**
 * The repeat-y alpha mask that clips the shimmer block into ragged bars: one
 * opaque rect per line, transparent gaps between. `viewBox` width 100 +
 * `preserveAspectRatio='none'` make each rect's width a PERCENT of the
 * element at whatever width it renders; the vertical axis maps 1:1 to px via
 * the mask-size the component sets.
 */
export function skeletonMaskSvg(seed: string): string {
  const rects = Array.from({ length: SKELETON_TILE_LINES }, (_, i) =>
    `<rect x='0' y='${i * SKELETON_LINE_PITCH_PX}' width='${skeletonLineWidthPercent(seed, i)}' height='${SKELETON_LINE_HEIGHT_PX}'/>`).join('')
  const svg = `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 ${SKELETON_TILE_LINES * SKELETON_LINE_PITCH_PX}' preserveAspectRatio='none'>${rects}</svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}

/**
 * The skeleton body a row shows while its real content isn't ready. Sized
 * inline to the row's reserved height (measured or estimated) so it can never
 * perturb the offset map.
 *
 * The block carries `role="status"`, exactly as Oat's docs show — and that
 * attribute is LOAD-BEARING for the styling, not just semantics: Oat's
 * component selector is `[role=status].skeleton`, so a `skeleton line`
 * element WITHOUT the role gets no skeleton styles at all (0 height,
 * transparent — the invisible-skeleton failure mode).
 */
export function ChatRowSkeleton(props: { height: number, seed: string }): JSX.Element {
  const maskUrl = createMemo(() => `url("${skeletonMaskSvg(props.seed)}")`)
  const maskSize = `100% ${SKELETON_TILE_LINES * SKELETON_LINE_PITCH_PX}px`
  return (
    <div
      class={rowSkeleton}
      data-testid="row-skeleton"
      style={{ height: `${props.height}px` }}
    >
      <div
        role="status"
        class={`skeleton line ${rowSkeletonFill}`}
        style={{
          'mask-image': maskUrl(),
          'mask-size': maskSize,
          'mask-repeat': 'repeat',
          '-webkit-mask-image': maskUrl(),
          '-webkit-mask-size': maskSize,
          '-webkit-mask-repeat': 'repeat',
        }}
      />
    </div>
  )
}
