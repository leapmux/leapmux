import { style } from '@vanilla-extract/css'

export const container = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  height: '100%',
  backgroundColor: 'var(--background)',
})

export const authFooter = style({
  marginTop: 'var(--space-4)',
  fontSize: 'var(--text-8)',
})

export const verificationMessage = style({
  padding: 'var(--space-4) 0',
})

export const inlineLink = style({
  marginTop: 'var(--space-2)',
  display: 'inline-block',
})

export const oauthButton = style({
  'display': 'flex',
  'alignItems': 'center',
  'justifyContent': 'center',
  'padding': 'var(--space-3) var(--space-4)',
  'borderRadius': 'var(--radius-2)',
  'border': '1px solid var(--border)',
  'backgroundColor': 'var(--surface)',
  'color': 'var(--foreground)',
  'textDecoration': 'none',
  'fontSize': 'var(--text-7)',
  'fontWeight': 500,
  'cursor': 'pointer',
  'transition': 'background-color 0.15s',
  ':hover': {
    backgroundColor: 'var(--surface-hover)',
  },
})

export const divider = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-3)',
  'margin': 'var(--space-4) 0',
  'color': 'var(--muted)',
  'fontSize': 'var(--text-8)',
  '::before': {
    content: '""',
    flex: 1,
    borderTop: '1px solid var(--border)',
  },
  '::after': {
    content: '""',
    flex: 1,
    borderTop: '1px solid var(--border)',
  },
})
