import { style } from '@vanilla-extract/css'

/*
 * The style vocabulary the account's CREDENTIAL LISTS share.
 *
 * Two surfaces render the same row shape -- a name, a line of metadata, and a
 * couple of actions in a footer row: the passkeys and the command-line
 * credentials. They used to read these names out of one component's private
 * stylesheet, which made a sibling import a file named after something it does
 * not render.
 *
 * The chips a row carries (verified, hub-wide, retired) are Oat `badge`
 * variants rather than styles here, and a destructive action is an Oat danger
 * button -- a second spelling of either could drift from the one every other
 * panel wears.
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

/**
 * One credential's card.
 *
 * A COLUMN: the entry's text takes the full width, and the actions sit in a
 * footer row beneath it. Sharing one line squeezed the text into whatever the
 * buttons left -- three of them on an app registration left less than half
 * the card -- while the metadata they act on is the part worth reading.
 */
export const credentialRow = style({
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'stretch',
  gap: 'var(--space-2)',
  padding: 'var(--space-3)',
  fontSize: 'var(--text-7)',
  color: 'var(--foreground)',
  backgroundColor: 'var(--card)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-medium)',
})

export const credentialInfo = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
  minWidth: 0,
})

export const credentialName = style({
  fontWeight: 'var(--font-medium)',
})

export const credentialMeta = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
})

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
  borderRadius: 'var(--radius-small)',
  backgroundColor: 'var(--muted)',
  fontFamily: 'var(--font-mono)',
})

/**
 * One APP's block in the connected-apps list: its name, its installations
 * stacked under it, and the app-level ending in a footer row.
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
  borderRadius: 'var(--radius-medium)',
})

/**
 * The installations inside a group.
 *
 * A plain stack: each installation is itself a bordered row, so the nesting
 * reads from the cards alone -- an indent or a rule down the left edge drew
 * structure the inner cards had already stated.
 */
export const credentialGroupBody = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-2)',
})

/**
 * The category list the permission-ceiling fieldset renders.
 *
 * Plain UL markers, one indent per level, no bullets on the outer level: the
 * legend already states what the list is, and the outer entries carry their
 * own weight as family names. The structure is the grouping -- Account,
 * Workspace, Worker -- that scope.proto's own sections state.
 */
export const scopeChoiceList = style({
  margin: 0,
  padding: 0,
  listStyle: 'none',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-3)',
})

/** One family: its label and the scopes beneath it. */
export const scopeChoiceCategory = style({
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

export const scopeChoiceLabel = style({
  fontSize: 'var(--text-7)',
  fontWeight: 'var(--font-bold)',
  color: 'var(--foreground)',
})

export const scopeChoiceEntries = style({
  margin: 0,
  paddingInlineStart: 'var(--space-5)',
  display: 'flex',
  flexDirection: 'column',
  gap: 'var(--space-1)',
})

/**
 * One scope: a checkbox row and its description beside it.
 *
 * The description sits OUTSIDE the label, so the checkbox's accessible name
 * stays the bare token (`workspace:read`) -- the name a consent screen and a
 * stored grant both read -- rather than the sentence explaining it.
 *
 * CENTER-aligned, not baseline: the label is a flex row whose first item is
 * the checkbox, and a checkbox has no text baseline of its own -- CSS
 * synthesizes one from its bottom margin edge -- so the label's baseline sat
 * a few pixels under the token's text and a baseline-aligned description
 * landed visibly below it. Centering the two boxes puts the description's
 * line level with the token it explains.
 */
export const scopeChoiceEntry = style({
  display: 'flex',
  alignItems: 'center',
  flexWrap: 'wrap',
  gap: 'var(--space-2)',
})

export const scopeChoiceToken = style({
  fontFamily: 'var(--font-mono)',
})

export const scopeChoiceDescription = style({
  fontSize: 'var(--text-8)',
  color: 'var(--muted-foreground)',
  minWidth: 0,
})
