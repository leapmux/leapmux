import { style } from '@vanilla-extract/css'
import { headerHeight } from '~/styles/tokens'

export const titlebar = style({
  display: 'flex',
  alignItems: 'center',
  height: headerHeight,
  minHeight: headerHeight,
  borderBottom: '1px solid var(--border)',
  backgroundColor: 'var(--card)',
  paddingInline: 'var(--space-1)',
  gap: '2px',
  flexShrink: 0,
})

export const dragRegion = style({
  flex: 1,
  height: '100%',
  WebkitAppRegion: 'drag',
} as any)

export const windowControls = style({
  display: 'flex',
  alignItems: 'center',
  gap: '2px',
})

export const titlebarLayout = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  width: '100%',
})

export const titlebarContent = style({
  flex: 1,
  overflow: 'hidden',
})
