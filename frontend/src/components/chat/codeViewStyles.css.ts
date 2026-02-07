import { globalStyle, style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

// Code view container (used by Read tool result with syntax highlighting)
export const codeViewContainer = style({
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
  overflow: 'auto',
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  marginTop: spacing.xs,
})

export const codeViewLine = style({
  display: 'flex',
  padding: `0 ${spacing.sm}`,
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
})

export const codeViewLineNumber = style({
  display: 'inline-block',
  flexShrink: 0,
  userSelect: 'none',
  textAlign: 'right',
  color: 'var(--faint-foreground)',
  whiteSpace: 'nowrap',
  marginRight: spacing.sm,
})

export const codeViewContent = style({
  flex: 1,
  minWidth: 0,
})

// Shiki dual-theme support for code view token spans
globalStyle(`${codeViewContainer} span[style]`, {
  color: 'var(--shiki-light)',
  backgroundColor: 'var(--shiki-light-bg, transparent)',
})

globalStyle(`html[data-theme="dark"] ${codeViewContainer} span[style]`, {
  color: 'var(--shiki-dark)',
  backgroundColor: 'var(--shiki-dark-bg, transparent)',
})
