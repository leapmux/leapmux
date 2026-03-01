import { globalStyle, style } from '@vanilla-extract/css'
import { spin } from '~/styles/animations.css'

export const errorText = style({
  color: 'var(--danger)',
  fontSize: 'var(--text-7)',
})

export const successText = style({
  color: 'var(--success)',
  fontSize: 'var(--text-7)',
})

export const warningText = style({
  color: 'var(--warning)',
  fontSize: 'var(--text-7)',
})

export const emptyState = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: 'var(--space-6)',
  color: 'var(--faint-foreground)',
})

export const backLink = style({
  'display': 'inline-block',
  'marginBottom': 'var(--space-4)',
  'color': 'var(--primary)',
  'textDecoration': 'none',
  ':hover': {
    color: 'var(--primary)',
  },
})

export const monoFont = style({
  fontFamily: 'var(--font-mono)',
  fontVariantLigatures: 'none',
})

// Menu utilities

export const dangerMenuItem = style({
  color: 'var(--danger)',
})

export const menuSectionHeader = style({
  fontSize: 'var(--text-8)',
  fontWeight: 600,
  color: 'var(--muted-foreground)',
  textTransform: 'uppercase',
  padding: 'var(--space-1) var(--space-3)',
})

// Layout utilities

export const inlineFlex = style({
  display: 'inline-flex',
})

export const centeredFull = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
})

export const heightFull = style({
  height: '100%',
})

// Auth card sizes

export const authCard = style({
  width: '360px',
})

export const authCardWide = style({
  width: '400px',
})

export const authCardXWide = style({
  width: '440px',
})

// Dialog sizes

export const dialogStandard = style({
  'position': 'relative',
  'minWidth': '360px',
  'maxWidth': '640px',
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

export const dialogTall = style({
  'height': '80vh',
  '@media': {
    '(max-width: 479px)': {
      height: '100vh',
    },
  },
})

export const dialogHeader = style({
  display: 'flex',
  flexDirection: 'row',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 0,
  padding: 'var(--space-6) var(--space-6) 0',
})

export const dialogCloseButton = style({
  position: 'absolute',
  top: 'var(--space-6)',
  right: 'var(--space-6)',
})

globalStyle(`${dialogHeader} > h2`, {
  margin: 0,
})

// Make dialog forms use flex layout so the tree container can fill remaining space.
globalStyle(`${dialogStandard} > form`, {
  display: 'flex',
  flexDirection: 'column',
  overflow: 'hidden',
  flex: 1,
  minHeight: 0,
})

globalStyle(`${dialogStandard} > form > section`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

globalStyle(`${dialogStandard} > form > section > .vstack`, {
  flex: 1,
  minHeight: 0,
})

// Dialog form utilities

export const labelRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
})

export const refreshButton = style({
  'all': 'unset',
  'display': 'inline-flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'padding': '2px',
  'cursor': 'pointer',
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--muted-foreground)',
  ':hover': {
    color: 'var(--foreground)',
    backgroundColor: 'var(--card)',
  },
  ':disabled': {
    cursor: 'not-allowed',
    opacity: 0.6,
  },
})

export const spinning = style({
  animation: `${spin} 1s linear infinite`,
})

export const treeContainer = style({
  flex: 1,
  minHeight: 0,
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
  overflow: 'hidden',
})

// The label wrapping the DirectoryTree needs to grow and use flex layout.
globalStyle(`${dialogStandard} > form > section > .vstack > label:has(.${treeContainer})`, {
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  minHeight: 0,
})

// Worktree options

export const checkboxRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  cursor: 'pointer',
})

export const pathPreview = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  wordBreak: 'break-all',
})
