import { globalStyle, style } from '@vanilla-extract/css'

// Dialog container

export const standard = style({
  'position': 'relative',
  'minWidth': '360px',
  'maxWidth': '900px',
  'display': 'flex',
  'flexDirection': 'column',
  '@media': {
    '(max-width: 639px)': {
      minWidth: 'unset',
      maxWidth: '100vw',
      width: '100vw',
    },
  },
})

export const wide = style({
  width: 'min(900px, 90vw)',
})

export const tall = style({
  'height': '80vh',
  '@media': {
    '(max-width: 479px)': {
      height: '100vh',
    },
  },
})

export const header = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 0,
  padding: 'var(--space-4) var(--space-6) 0',
})

export const closeButton = style({
  position: 'absolute',
  top: 'var(--space-6)',
  right: 'var(--space-6)',
})

globalStyle(`${header} > h2`, {
  margin: 0,
})

// Dialog body wrapper provides consistent padding for all dialog content.
// The body has tabindex=-1 so it can absorb initial focus on dialog open
// without routing focus to the close button or a form control. Suppress
// its focus outline since it is only ever focused programmatically.
export const body = style({
  display: 'flex',
  flexDirection: 'column',
  flex: '1 1 auto',
  minHeight: 0,
  overflow: 'hidden',
  padding: 'var(--space-6)',
  paddingBlockStart: 'var(--space-4)',
  outline: 'none',
})

// Footer inside dialog body
globalStyle(`${standard} > .${body} > footer, ${standard} > .${body} > form > footer`, {
  display: 'flex',
  justifyContent: 'flex-end',
  gap: 'var(--space-2)',
  paddingBlockStart: 'var(--space-6)',
})

// Make dialog forms use flex layout so the tree container can fill remaining space.
globalStyle(`${standard} > .${body} > form`, {
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  flex: 1,
  minHeight: 0,
})

globalStyle(`${standard} > .${body} > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
  overflowY: 'auto',
})

globalStyle(`${standard} > .${body} > form > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

globalStyle(`${standard} > .${body} > form > section > .vstack`, {
  flex: 1,
  minHeight: 0,
})

// Layout: top section

export const topSection = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-4)',
})

export const topTwoColumn = style({
  'display': 'grid',
  'gridTemplateColumns': '1fr 1fr',
  'gap': 'var(--space-4)',
  '@media': {
    '(max-width: 639px)': {
      gridTemplateColumns: '1fr',
    },
  },
})

// Layout: column area

export const twoColumn = style({
  'display': 'grid',
  'gridTemplateColumns': '1fr 1fr',
  'gap': 'var(--space-4)',
  'flex': 1,
  'minHeight': 0,
  '@media': {
    '(max-width: 639px)': {
      gridTemplateColumns: '1fr',
    },
  },
})

export const singleColumn = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

export const leftPanel = style({
  display: 'flex',
  flexDirection: 'column',
  minHeight: 0,
  overflow: 'hidden',
  gap: 'var(--space-4)',
})

export const rightPanel = style({
  display: 'flex',
  flexDirection: 'column',
  minHeight: 0,
  overflowY: 'auto',
  gap: 'var(--space-4)',
})

// In two-column layout, the grid and its left panel must fill remaining height.
globalStyle(`${standard} > .${body} > form > section > .vstack > .${twoColumn}`, {
  flex: 1,
  minHeight: 0,
})

// Form utilities

export const labelRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
})

export const treeContainer = style({
  flex: 1,
  minHeight: 0,
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  overflow: 'hidden',
})

// The element wrapping the DirectoryTree needs to grow and use flex layout.
globalStyle(`${standard} > .${body} > form > section > .vstack :has(> .${treeContainer})`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

export const pathPreview = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  wordBreak: 'break-all',
})

export const radioGroup = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

export const radioRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  cursor: 'pointer',
})

export const radioSubContent = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  paddingLeft: 'var(--space-6)',
})
