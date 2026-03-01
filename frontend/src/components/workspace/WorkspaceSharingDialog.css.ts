import { style } from '@vanilla-extract/css'

export { errorText } from '~/styles/shared.css'

export const memberList = style({
  maxHeight: '200px',
  overflowY: 'auto',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  marginBottom: 'var(--space-4)',
  padding: 'var(--space-2)',
  backgroundColor: 'var(--background)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

export const memberItem = style({
  'display': 'flex',
  'alignItems': 'center',
  'gap': 'var(--space-2)',
  'padding': 'var(--space-1) var(--space-2)',
  'borderRadius': 'var(--radius-small)',
  'color': 'var(--foreground)',
  'cursor': 'pointer',
  ':hover': {
    backgroundColor: 'var(--card)',
  },
})
