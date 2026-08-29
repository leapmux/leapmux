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

/**
 * A second line under the credential name, for a fact that qualifies it
 * rather than identifies it -- which app holds the credential, and which
 * installation of that app.
 */
export const credentialSubRow = style({
  display: 'flex',
  alignItems: 'center',
  gap: 'var(--space-2)',
  flexWrap: 'wrap',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

/**
 * A short chip beside a credential's name: "unverified", "hub administration".
 *
 * It carries its own colours through a variant rather than a caller-supplied
 * class, so a warning and a neutral note cannot be spelled two ways.
 */
export const credentialBadge = style({
  display: 'inline-flex',
  alignItems: 'center',
  padding: '1px var(--space-2)',
  borderRadius: 'var(--radius-1)',
  fontSize: 'var(--text-8)',
  fontWeight: 'var(--font-medium)',
  backgroundColor: 'var(--muted)',
  color: 'var(--muted-foreground)',
  border: '1px solid var(--border)',
})

/** credentialBadge for a fact the reader should weigh before continuing. */
export const credentialBadgeWarning = style([credentialBadge, {
  backgroundColor: 'var(--danger-subtle, var(--muted))',
  color: 'var(--danger)',
  borderColor: 'var(--danger)',
}])

/**
 * The permission list under a credential.
 *
 * It renders EVERY granted scope rather than a count, because the list is what
 * a person reads to decide whether to disconnect: "12 permissions" answers a
 * question nobody asked.
 */
export const credentialScopeLine = style({
  display: 'flex',
  flexWrap: 'wrap',
  gap: 'var(--space-1)',
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

/** One permission inside credentialScopeLine. */
export const credentialScope = style({
  padding: '0 var(--space-1)',
  borderRadius: 'var(--radius-1)',
  backgroundColor: 'var(--muted)',
  fontFamily: 'var(--font-mono)',
})

/**
 * One APP's block in the connected-apps list: its name and its Disconnect on
 * one line, its installations stacked under them.
 *
 * The grouping is the panel's whole shape. One app holds one credential per
 * machine, so a flat list of credentials made "stop this app reaching my
 * account" a repeated action whose completeness the reader had to verify by
 * eye.
 */
export const credentialGroup = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  padding: 'var(--space-3)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-2)',
})

/** The app's own line: its name and identity on the left, Disconnect right. */
export const credentialGroupHeader = style({
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: 'var(--space-3)',
})

/**
 * The installations inside a group.
 *
 * Indented and separated from the app line above, so the two levels read as
 * "this app" and "on these machines" rather than as one flat list whose rows
 * happen to share a name.
 */
export const credentialGroupBody = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
  paddingLeft: 'var(--space-3)',
  borderLeft: '1px solid var(--border)',
})
