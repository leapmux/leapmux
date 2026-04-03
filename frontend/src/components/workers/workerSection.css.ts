import { style } from '@vanilla-extract/css'

export const statusDot = style({
  width: 8,
  height: 8,
  borderRadius: '50%',
  flexShrink: 0,
})

export const statusConnected = style({
  background: 'var(--success)',
})

export const statusDisconnected = style({
  background: 'var(--danger)',
})

export const tunnelItem = style({
  paddingLeft: 'var(--space-5)',
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
})
