import { style } from '@vanilla-extract/css'

/*
 * The style vocabulary the account's CREDENTIAL LISTS share.
 *
 * Two surfaces render the same row shape -- a name, a line of metadata, and a
 * couple of actions on the right: the passkeys and the command-line
 * credentials. They used to read these names out of one component's private
 * stylesheet, which made a sibling import a file named after something it does
 * not render.
 *
 * `./accountFields.css.ts` keeps the FIELD vocabulary (labels, the email
 * value, the provider rows) that the other account editors share.
 */

export const credentialListLoading = style({
  display: 'flex',
  justifyContent: 'center',
  padding: 'var(--space-2) 0',
})

export const credentialListEmpty = style({
  fontSize: 'var(--text-7)',
  color: 'var(--muted-foreground)',
  margin: 0,
})

export const credentialRow = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
  padding: 'var(--space-2) var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-2)',
})

export const credentialInfo = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  flex: 1,
  minWidth: 0,
})

export const credentialName = style({
  fontWeight: 'var(--font-medium)',
})

export const credentialMeta = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

export const credentialActions = style({
  display: 'flex',
  gap: 'var(--space-2)',
  flexShrink: 0,
})

export const credentialDanger = style({
  color: 'var(--danger)',
})

/** The row of buttons that opens a passkey ceremony. */
export const passkeyButtons = style({
  display: 'flex',
  gap: 'var(--space-3)',
  flexWrap: 'wrap',
})
