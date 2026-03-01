import { globalStyle, style } from '@vanilla-extract/css'

// Diff container
export const diffContainer = style({
  position: 'relative',
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
  position: 'relative',
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

// Gap separator row between hunks (shows hidden line count + expand buttons)
export const diffGapSeparator = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  gap: 'var(--space-4)',
  padding: 'var(--space-1) var(--space-2)',
  fontFamily: 'var(--font-sans)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  backgroundColor: 'color-mix(in srgb, var(--info) 6%, transparent)',
  userSelect: 'none',
  borderTop: '1px dashed var(--border)',
  borderBottom: '1px dashed var(--border)',
})

// Clickable expand button inside the gap separator
export const diffGapExpandButton = style({
  display: 'inline-flex',
  alignItems: 'center',
  gap: 'var(--space-0-5)',
  cursor: 'pointer',
  color: 'var(--muted-foreground)',
  selectors: {
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

// Clickable variant of gap separator (small gaps that expand all at once)
export const diffGapSeparatorClickable = style({
  cursor: 'pointer',
  selectors: {
    '&:hover': {
      color: 'var(--foreground)',
    },
  },
})

// Remove top border when gap separator is the first element in the container
export const diffGapSeparatorFirst = style({
  borderTop: 'none',
})

// Remove bottom border when gap separator is the last element in the container
export const diffGapSeparatorLast = style({
  borderBottom: 'none',
})

// Gap separator spanning full width in split view grid
export const diffGapSeparatorSplit = style({
  gridColumn: '1 / -1',
})

export const diffSplitColumn = style({
  overflow: 'hidden',
  selectors: {
    '&:first-child': {
      borderRight: '1px solid var(--border)',
    },
  },
})
