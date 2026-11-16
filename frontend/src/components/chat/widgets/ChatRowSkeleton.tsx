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
 * A shimmer of ragged text bars at the CSS height `height`: the block of SkeletonLines and
 * of ChatRowSkeleton. `testId` states which content waits behind the shimmer, so a check
 * that counts one kind of skeleton never counts another.
 *
 * The block carries `role="status"`, exactly as Oat's docs show — and that
 * attribute is LOAD-BEARING for the styling, not just semantics: Oat's
 * component selector is `[role=status].skeleton`, so a `skeleton line`
 * element WITHOUT the role gets no skeleton styles at all (0 height,
 * transparent — the invisible-skeleton failure mode).
 */
function SkeletonBlock(props: { height: string, seed: string, testId: string }): JSX.Element {
  const maskUrl = createMemo(() => `url("${skeletonMaskSvg(props.seed)}")`)
  const maskSize = `100% ${SKELETON_TILE_LINES * SKELETON_LINE_PITCH_PX}px`
  return (
    <div
      class={rowSkeleton}
      data-testid={props.testId}
      style={{ height: props.height }}
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

/**
 * A shimmer of ragged text bars at a fixed height of `height` px, the placeholder of an
 * asynchronous body that waits for its worker, such as a Markdown body or an edit diff
 * (see SkeletonBlock for `testId`).
 */
export function SkeletonLines(props: { height: number, seed: string, testId: string }): JSX.Element {
  return <SkeletonBlock height={`${props.height}px`} seed={props.seed} testId={props.testId} />
}

/**
 * The skeleton body that a row shows while its real content is not ready. It fills the
 * height of its slot: the element around it carries the reserved height of the row,
 * which is its measured height, as its own height.
 *
 * The slot can draw the chrome of the row, as a band row draws a border above and below.
 * The reserved height counts that chrome, because a measure reads the whole row. A
 * skeleton with the reserved height inside such a slot would make the slot taller than
 * the reserved height by the chrome, and cover the top of the next row. The slot has
 * `box-sizing: border-box`, as every element has (Oat's base layer), so its own height
 * holds the chrome, and the skeleton takes what remains.
 */
export function ChatRowSkeleton(props: { seed: string }): JSX.Element {
  return <SkeletonBlock height="100%" seed={props.seed} testId="row-skeleton" />
}
