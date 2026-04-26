import { style } from '@vanilla-extract/css'

export const messageWithError = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  width: '100%',
})

export const messageError = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-1)',
  paddingTop: '2px',
  fontSize: 'var(--text-8)',
  color: 'var(--danger)',
})

export const messageErrorText = style({})

export const messageErrorDot = style({
  color: 'var(--danger)',
})

const messageActionButton = {
  all: 'unset' as const,
  cursor: 'pointer',
  fontWeight: 600,
  fontSize: 'var(--text-8)',
  textDecoration: 'underline',
  textUnderlineOffset: '2px',
}

export const messageRetryButton = style({
  ...messageActionButton,
  'color': 'var(--danger)',
  ':hover': { color: 'var(--danger)' },
})

export const messageDeleteButton = style({
  ...messageActionButton,
  'color': 'var(--muted-foreground)',
  ':hover': { color: 'var(--foreground)' },
})
