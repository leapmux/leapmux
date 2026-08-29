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

/**
 * One linked provider's card is a `credentialRow` (see
 * `./credentialList.css.ts`): a COLUMN like every credential row, with the
 * provider's name (and its disabled note) taking the full width and the
 * Unlink in a footer row beneath them. The card style itself is shared, so
 * the provider row and the credential row cannot drift apart.
 */

export const linkedAccountName = style({
  fontWeight: 'var(--font-medium)',
})

/** The note on a link whose provider an administrator turned off. */
export const linkedAccountDisabled = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  minWidth: 0,
})
