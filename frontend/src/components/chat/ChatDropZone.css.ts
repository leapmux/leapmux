import { style } from '@vanilla-extract/css'

export const dropZone = style({
  display: 'flex',
  flexDirection: 'column',
  flex: 1,
  overflow: 'hidden',
  position: 'relative',
})

export const dropOverlay = style({
  position: 'absolute',
  inset: 0,
  zIndex: 50,
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  backgroundColor: 'color-mix(in srgb, var(--background) 60%, transparent)',
  border: '2px dashed var(--primary)',
  borderRadius: 'var(--radius-medium)',
  pointerEvents: 'none',
})

export const dropOverlayContent = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
  gap: 'var(--space-2)',
  padding: 'var(--space-5) var(--space-8)',
  backgroundColor: 'var(--background)',
  borderRadius: 'var(--radius-large)',
  boxShadow: 'var(--shadow-large)',
})

export const dropOverlayTitle = style({
  fontSize: 'var(--text-4)',
  fontWeight: 600,
  color: 'var(--foreground)',
})

export const dropOverlayHint = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})
