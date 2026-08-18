import { style } from '@vanilla-extract/css'

/** The outer section: separated from the blocks below it. */
export const section = style({
  marginBottom: 'var(--space-6)',
  borderBottom: '1px solid var(--border)',
  paddingBottom: 'var(--space-6)',
  selectors: {
    '&:last-child': {
      borderBottom: 'none',
      marginBottom: 0,
    },
  },
})

/** The field label style shared by the profile blocks. */
export const fieldLabel = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-normal)',
  color: 'var(--muted-foreground)',
})

export const sectionHeading = style({
  marginTop: 'var(--space-6)',
})

export const emailValue = style({
  fontSize: 'var(--text-6)',
  color: 'var(--foreground)',
})

export const verifiedBadge = style({
  marginLeft: 'var(--space-2)',
  fontSize: 'var(--text-8)',
  color: 'var(--success)',
})

export const unverifiedBadge = style({
  marginLeft: 'var(--space-2)',
  fontSize: 'var(--text-8)',
  color: 'var(--warning)',
})

export const linkedAccount = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-2)',
})

export const linkedAccountName = style({
  flex: 1,
})

export const linkedAccountUnlink = style({
  'fontSize': 'var(--text-8)',
  'color': 'var(--muted-foreground)',
  'background': 'none',
  'border': 'none',
  'cursor': 'pointer',
  'padding': 'var(--space-1) var(--space-2)',
  ':hover': {
    color: 'var(--danger)',
  },
})
