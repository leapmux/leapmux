import { createVar, style } from '@vanilla-extract/css'

export const wrapper = style({
  display: 'grid',
  gridTemplateRows: '0fr',
  opacity: 0,
  marginLeft: 'calc(-1 * var(--space-1))',
})

export const wrapperInner = style({
  overflow: 'hidden',
})

export const container = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  padding: 'var(--space-2) 0',
  color: 'var(--primary)',
})

export const compass = style({
  width: '24px',
  height: '24px',
  flexShrink: 0,
})

// verbRow pairs the verb with the optional thinking-token count on a shared
// text baseline. The outer container centers the 24px compass against this
// row, but its `align-items: center` would otherwise center the smaller count
// against the larger verb — aligning their visual centers, not their
// baselines, so the count rides high. Grouping them in a baseline row pins the
// count's baseline to the verb's regardless of the font-size difference. The
// per-char wave transform on the verb doesn't shift the baseline (transforms
// are applied after layout), so the count stays put while the verb bobs.
export const verbRow = style({
  display: 'flex',
  alignItems: 'baseline',
  gap: 'var(--space-1)',
})

// verbStack hosts two overlapping verb spans so we can crossfade
// between them on each 60s in-turn rotation. Grid layout with
// `grid-template: 1fr / 1fr` puts both spans in the same cell;
// `gridArea: '1 / 1'` on each span (via the verb rule below) anchors
// them. The cell sizes to max(both contents) — short-lived during the
// --transition fade, then the now-inactive slot is cleared and the cell
// shrinks to just the active verb.
export const verbStack = style({
  display: 'inline-grid',
  gridTemplate: '1fr / 1fr',
})

// Shared metrics for everything stacked in the single verb grid cell: the same
// cell anchor (gridArea), baseline alignment, and font so each slot — and the
// strut — synthesizes an identical text baseline. The strut depends on matching
// the verb's font EXACTLY (see baselineStrut), so sharing this base makes the
// two provably agree instead of relying on two hand-kept-in-sync copies.
const verbSlotBase = {
  gridArea: '1 / 1',
  // Align to the shared cell baseline (the strut's) so a slot's text always
  // sits on the same baseline; default `stretch` would let an empty slot
  // shift the grid baseline. See baselineStrut.
  alignSelf: 'baseline',
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-bold)',
  userSelect: 'none',
} as const

// baselineStrut is an always-present, zero-width, never-painted anchor that
// fixes the grid's baseline. Without it, `verbRow`'s baseline alignment reads
// the grid's baseline from whichever slot currently holds text — and after a
// 60s rotation the active verb moves to the other slot while the first is
// cleared to '', leaving an empty slot whose synthesized baseline yanks the
// whole verb vertically. The strut carries the verb's exact font metrics, so
// it always supplies the same text baseline regardless of which slot is empty.
export const baselineStrut = style([verbSlotBase, {
  width: 0,
  overflow: 'hidden',
  visibility: 'hidden',
}])

export const verb = style([verbSlotBase, {
  opacity: 0,
  // ROTATION_FADE_MS in ThinkingIndicator.tsx tracks --transition via
  // motion.medium from tokens.ts —
  // keep them in lockstep so the inactive-slot clear lands exactly
  // when the fade visually completes.
  transition: 'opacity var(--transition)',
}])

// Applied to whichever of the two stacked slots is currently the
// "live" one. The other slot stays opacity 0; CSS transitions on
// opacity handle the crossfade when activeIsA flips.
export const verbActive = style({
  opacity: 1,
})

// Per-char wave is computed entirely in CSS from two custom properties set
// by the component: --highlight-pos (current wave position, in chars) and
// --char-total (verb length). Each char also carries its own --char-i
// (index). Keeping the math in CSS lets the simulation update one var on
// the parent per tick instead of writing inline transform on every char.
const distVar = createVar()
const wrappedDistVar = createVar()
const falloffVar = createVar()

export const char = style({
  display: 'inline-block',
  whiteSpace: 'pre',
  vars: {
    [distVar]: 'max(calc(var(--char-i) - var(--highlight-pos)), calc(var(--highlight-pos) - var(--char-i)))',
    [wrappedDistVar]: `min(${distVar}, calc(var(--char-total) - ${distVar}))`,
    [falloffVar]: `max(0, calc(1 - ${wrappedDistVar} / 1.5))`,
  },
  transform: `translateY(calc(-4px * ${falloffVar}))`,
  transition: 'transform 0.5s ease-out',
})
