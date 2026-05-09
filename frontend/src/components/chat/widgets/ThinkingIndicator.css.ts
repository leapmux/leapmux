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

// verbStack hosts two overlapping verb spans so we can crossfade
// between them on each 60s in-turn rotation. Grid layout with
// `grid-template: 1fr / 1fr` puts both spans in the same cell;
// `gridArea: '1 / 1'` on each span (via the verb rule below) anchors
// them. The cell sizes to max(both contents) — short-lived during the
// 500ms fade, then the now-inactive slot is cleared and the cell
// shrinks to just the active verb.
export const verbStack = style({
  display: 'inline-grid',
  gridTemplate: '1fr / 1fr',
})

export const verb = style({
  gridArea: '1 / 1',
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-bold)',
  userSelect: 'none',
  opacity: 0,
  // ROTATION_FADE_MS in ThinkingIndicator.tsx tracks this duration —
  // keep them in lockstep so the inactive-slot clear lands exactly
  // when the fade visually completes.
  transition: 'opacity 500ms ease-in-out',
})

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
