import { style } from '@vanilla-extract/css'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  backgroundColor: 'var(--background)',
})

export const message = style({
  color: 'var(--muted-foreground)',
  marginBottom: 'var(--space-6)',
  lineHeight: 1.5,
})

export const link = style({
  'color': 'var(--primary)',
  'fontWeight': 400,
  'textDecoration': 'none',
  ':hover': {
    textDecoration: 'underline',
  },
})
