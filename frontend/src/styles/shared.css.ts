import { globalStyle, style } from '@vanilla-extract/css'
import { spin } from '~/styles/animations.css'
import { spacing } from '~/styles/tokens'

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
  padding: spacing.xl,
  color: 'var(--faint-foreground)',
})

export const backLink = style({
  'display': 'inline-block',
  'marginBottom': spacing.lg,
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
  padding: `${spacing.xs} ${spacing.md}`,
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
  minWidth: '360px',
  maxWidth: '480px',
  display: 'flex',
  flexDirection: 'column',
})

export const dialogWithTree = style({
  height: '80vh',
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
  gap: spacing.sm,
  cursor: 'pointer',
})

export const pathPreview = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  wordBreak: 'break-all',
})
