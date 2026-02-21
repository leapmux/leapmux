import { globalStyle, style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export const terminalInner = style({
  flex: 1,
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
})

export const container = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
})

export const terminalWrapper = style({
  flex: 1,
  overflow: 'hidden',
  backgroundColor: 'var(--background)',
  fontVariantLigatures: 'none',
})

// Apply padding to the xterm element rather than the wrapper so that
// FitAddon correctly accounts for it when calculating rows/cols.
globalStyle(`${terminalWrapper} .xterm`, {
  padding: spacing.xs,
})
