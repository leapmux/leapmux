import { style } from '@vanilla-extract/css'
import { spacing } from '~/styles/tokens'

export const shell = style({
  height: '100%',
  width: '100%',
  overflow: 'hidden',
})

export const sidebar = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  backgroundColor: 'var(--card)',
  borderRight: '1px solid var(--border)',
  overflow: 'hidden',
})

export const resizeHandle = style({
  'all': 'unset',
  'boxSizing': 'border-box',
  'width': '4px',
  'background': 'transparent',
  'borderRadius': 0,
  'position': 'relative',
  'flexShrink': 0,
  ':hover': {
    background: 'var(--secondary)',
  },
  ':active': {
    background: 'var(--secondary)',
  },
})

export const center = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  overflow: 'hidden',
})

export const rightPanel = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  backgroundColor: 'var(--card)',
  borderLeft: '1px solid var(--border)',
  overflow: 'hidden',
})

export const centerContent = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
})

export const layoutHidden = style({
  display: 'none !important',
})

export const fullWindow = style({
  height: '100%',
  width: '100%',
  overflow: 'auto',
})

export const placeholder = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  flex: 1,
  color: 'var(--faint-foreground)',
  textAlign: 'center',
  padding: `0 ${spacing.xl}`,
})

// --- Mobile layout styles ---

export const mobileShell = style({
  height: '100%',
  width: '100%',
  overflow: 'hidden',
  position: 'relative',
})

export const mobileCenter = style({
  display: 'flex',
  flexDirection: 'column',
  height: '100%',
  width: '100%',
  overflow: 'hidden',
})

export const mobileSidebar = style({
  position: 'fixed',
  top: 0,
  bottom: 0,
  width: '80%',
  maxWidth: '320px',
  zIndex: 100,
  backgroundColor: 'var(--card)',
  transform: 'translateX(-100%)',
  transition: 'transform 0.2s ease',
  boxShadow: '2px 0 8px rgba(0, 0, 0, 0.3)',
  overflow: 'hidden',
  display: 'flex',
  flexDirection: 'column',
})

export const mobileSidebarRight = style({
  left: 'auto',
  right: 0,
  transform: 'translateX(100%)',
  boxShadow: '-2px 0 8px rgba(0, 0, 0, 0.3)',
})

export const mobileSidebarOpen = style({
  transform: 'translateX(0)',
})

export const mobileOverlay = style({
  position: 'fixed',
  top: 0,
  left: 0,
  right: 0,
  bottom: 0,
  backgroundColor: 'rgba(0, 0, 0, 0.4)',
  zIndex: 99,
})

// --- End mobile layout styles ---

export const dragPreviewTooltip = style({
  display: 'flex',
  alignItems: 'center',
  gap: '6px',
  padding: '4px 6px',
  fontSize: '13px',
  background: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: '4px 4px 0 0',
  boxShadow: '0 2px 8px rgba(0,0,0,0.15)',
  whiteSpace: 'nowrap',
  maxWidth: '180px',
  overflow: 'hidden',
  textOverflow: 'ellipsis',
})
