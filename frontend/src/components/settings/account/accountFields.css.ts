import { style } from '@vanilla-extract/css'

/*
 * The field vocabulary the account editors share.
 *
 * There is no outer-section rule here any more. Each account editor is now
 * the control of ONE `SettingRow`, and the row supplies the label, the help
 * text and the separator beneath it -- so an editor that drew its own border
 * and margin drew a second divider inside the row's.
 */

/** The field label style shared by the account editors. */
export const fieldLabel = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-normal)',
  color: 'var(--muted-foreground)',
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

/** The note on a link whose provider an administrator turned off. */
export const linkedAccountDisabled = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  flex: 1,
  minWidth: 0,
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
