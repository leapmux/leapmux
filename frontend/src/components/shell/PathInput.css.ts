import { globalStyle, style } from '@vanilla-extract/css'

export const pathInput = style({
  display: 'flex',
  alignItems: 'center',
  padding: 'var(--space-1)',
  borderBottom: '1px solid var(--border)',
  flexShrink: 0,
})

// Oat's default `input` style sets `margin-block-start: var(--space-1)` for
// form-field spacing under a label. That's not wanted here — the input sits
// alone in a flex row and we want a uniform --space-1 gap on all four sides
// of the input (provided by pathInput's padding).
globalStyle(`${pathInput} input`, {
  marginBlockStart: 0,
})

export const pathHint = style({
  fontSize: 'var(--text-8)',
  color: 'var(--warning-foreground, var(--faint-foreground))',
  padding: '2px var(--space-2) 0',
  lineHeight: 1.2,
  flexShrink: 0,
})
