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

export const verb = style({
  fontSize: 'var(--text-7)',
  fontWeight: 600,
  userSelect: 'none',
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
