import { style } from '@vanilla-extract/css'
import { codeTypography, codeWrap } from '~/styles/codeBlock'
import { shikiDualThemeColors } from '../shikiTokenColors.css'

// Code view container (used by Read tool result with syntax highlighting)
export const codeViewContainer = style({
  ...codeTypography,
  overflow: 'auto',
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  marginTop: 'var(--space-1)',
})

export const codeViewLine = style({
  ...codeWrap,
  display: 'flex',
  padding: '0 var(--space-2)',
})

export const codeViewLineNumber = style({
  display: 'inline-block',
  flexShrink: 0,
  userSelect: 'none',
  textAlign: 'right',
  color: 'var(--faint-foreground)',
  whiteSpace: 'nowrap',
  marginRight: 'var(--space-2)',
})

export const codeViewContent = style({
  flex: 1,
  minWidth: 0,
})

// Shiki dual-theme support for code view token spans. Targets `span[data-shiki-token]`
// (not a bare `span[style]`): the line-number span carries an inline `style` (its width)
// too, and a `span[style]` rule -- higher specificity than the codeViewLineNumber class --
// would override its `--faint-foreground` with `var(--shiki-light)`, which resolves to
// nothing on a non-token span (so the numbers fall back to full foreground color).
shikiDualThemeColors(`${codeViewContainer} span[data-shiki-token]`, { bg: true })
