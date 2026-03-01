import { globalStyle, style } from '@vanilla-extract/css'

// Diff container
export const diffContainer = style({
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
  overflow: 'auto',
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  marginTop: 'var(--space-1)',
})

export const diffRemoved = style({
  backgroundColor: 'color-mix(in srgb, var(--danger) 18%, transparent)',
})

export const diffAdded = style({
  backgroundColor: 'color-mix(in srgb, var(--success) 18%, transparent)',
})

// Inline character-level highlight within a removed line
export const diffRemovedInline = style({
  backgroundColor: 'color-mix(in srgb, var(--danger) 22%, transparent)',
  borderRadius: '2px',
})

// Inline character-level highlight within an added line
export const diffAddedInline = style({
  backgroundColor: 'color-mix(in srgb, var(--success) 22%, transparent)',
  borderRadius: '2px',
})

export const diffLine = style({
  display: 'flex',
  padding: '0 var(--space-2)',
  whiteSpace: 'pre-wrap',
  wordBreak: 'break-all',
})

export const diffLineNumber = style({
  display: 'inline-block',
  flexShrink: 0,
  userSelect: 'none',
  width: '4ch',
  textAlign: 'right',
  color: 'var(--faint-foreground)',
  whiteSpace: 'nowrap',
})

// Second line number column in unified diff — separated from the first
export const diffLineNumberNew = style({
  display: 'inline-block',
  flexShrink: 0,
  userSelect: 'none',
  width: '4ch',
  textAlign: 'right',
  color: 'var(--faint-foreground)',
  whiteSpace: 'nowrap',
  marginLeft: 'var(--space-1)',
  marginRight: 'var(--space-1)',
})

export const diffPrefix = style({
  flexShrink: 0,
  userSelect: 'none',
  width: '1.5ch',
  color: 'var(--muted-foreground)',
})

export const diffContent = style({
  flex: 1,
  minWidth: 0,
})

// Shiki dual-theme support for diff token spans
globalStyle(`${diffContainer} span[style]`, {
  color: 'var(--shiki-light)',
  backgroundColor: 'var(--shiki-light-bg, transparent)',
})

globalStyle(`html[data-theme="dark"] ${diffContainer} span[style]`, {
  color: 'var(--shiki-dark)',
  backgroundColor: 'var(--shiki-dark-bg, transparent)',
})

// Split diff container (two columns side by side)
export const diffSplitContainer = style({
  display: 'grid',
  gridTemplateColumns: '1fr 1fr',
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
  fontSize: 'var(--text-8)',
  lineHeight: 1.5,
  overflow: 'auto',
  borderRadius: 'var(--radius-small)',
  border: '1px solid var(--border)',
  marginTop: 'var(--space-1)',
})

globalStyle(`${diffSplitContainer} span[style]`, {
  color: 'var(--shiki-light)',
  backgroundColor: 'var(--shiki-light-bg, transparent)',
})

globalStyle(`html[data-theme="dark"] ${diffSplitContainer} span[style]`, {
  color: 'var(--shiki-dark)',
  backgroundColor: 'var(--shiki-dark-bg, transparent)',
})

export const diffSplitColumn = style({
  overflow: 'hidden',
  selectors: {
    '&:first-child': {
      borderRight: '1px solid var(--border)',
    },
  },
})
