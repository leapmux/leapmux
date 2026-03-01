import { style } from '@vanilla-extract/css'

export const floatingToolbar = style({
  position: 'absolute',
  top: 'var(--space-1)',
  right: 'var(--space-1)',
  display: 'flex',
  gap: '2px',
  zIndex: 10,
})

export const metaFloatingToolbar = style({
  position: 'absolute',
  top: '50%',
  right: 'var(--space-1)',
  transform: 'translateY(-50%)',
  display: 'flex',
  gap: '2px',
  zIndex: 1,
})

export const threadChildren = style({
  marginLeft: '6px',
  paddingLeft: 'var(--space-3)',
  paddingRight: 'var(--space-3)',
  borderLeft: '2px solid var(--border)',
})

export const messageWithError = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-end',
  alignSelf: 'flex-end',
  maxWidth: '85%',
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
