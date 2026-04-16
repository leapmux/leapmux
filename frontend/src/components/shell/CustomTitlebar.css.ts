import { style } from '@vanilla-extract/css'
import { headerHeight } from '~/styles/tokens'

export const titlebar = style({
  position: 'relative',
  boxSizing: 'border-box',
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

export const titleText = style({
  position: 'absolute',
  left: '50%',
  transform: 'translateX(-50%)',
  maxWidth: '50%',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
  whiteSpace: 'nowrap',
  pointerEvents: 'none',
  userSelect: 'none',
  WebkitAppRegion: 'drag',
  fontSize: 'var(--text-7)',
  fontWeight: 'bold',
  letterSpacing: '0.02em',
  color: 'var(--text-secondary)',
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

// Optical-balance nudge: Lucide's Menu glyph (three horizontal lines) renders
// above the visual center that PanelLeft/PanelRight resolve to, so the
// hamburger looks misaligned next to the sidebar toggles without this shift.
export const menuTrigger = style({
  transform: 'translateY(1.5px)',
})
