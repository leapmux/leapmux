import type { CategoryId } from './types'

/** One navigation entry: the dialog-visible id, its title, its category. */
export interface NavGroup {
  /** The id the deep-link signal carries. */
  id: string
  title: string
  category: CategoryId
  admin: boolean
}

/**
 * The dialog's category navigation, in render order: user categories under
 * PREFERENCES, then admin categories under ADMINISTRATION.
 */
export const NAV_GROUPS: readonly NavGroup[] = [
  // Account LEADS the list. It is the group a user comes to the dialog for
  // deliberately -- a password, a passkey, an address -- where the rest are
  // preferences they adjust while they are already here, so a place under
  // seven of them put the errand behind the browsing.
  { id: 'account', title: 'Account', category: 'account', admin: false },
  // Apps follows Account, because it is the same errand one step further out:
  // Account holds the apps you AUTHORIZED, this holds the apps you REGISTERED.
  // It is a user group, not an administration one -- an ordinary account may
  // register an app for itself, and ownership rather than a role decides what
  // each caller sees.
  { id: 'apps', title: 'Apps', category: 'apps', admin: false },
  { id: 'appearance', title: 'Appearance', category: 'appearance', admin: false },
  { id: 'notifications', title: 'Notifications', category: 'notifications', admin: false },
  { id: 'chat', title: 'Chat & Composer', category: 'chat', admin: false },
  { id: 'terminal', title: 'Terminal', category: 'terminal', admin: false },
  { id: 'files', title: 'Files & Editors', category: 'files', admin: false },
  { id: 'shortcuts', title: 'Keyboard Shortcuts', category: 'shortcuts', admin: false },
  { id: 'advanced', title: 'Advanced', category: 'advanced', admin: false },
  { id: 'admin-general', title: 'General', category: 'general', admin: true },
  { id: 'admin-signup', title: 'Sign-up & Access', category: 'signup', admin: true },
  { id: 'admin-email', title: 'Email (SMTP)', category: 'email', admin: true },
  { id: 'admin-captcha', title: 'Bot Protection', category: 'captcha', admin: true },
  { id: 'admin-rate-limits', title: 'Rate Limits', category: 'rate-limits', admin: true },
  { id: 'admin-limits', title: 'Limits & Timeouts', category: 'limits', admin: true },
  // The same category on the ADMINISTRATION side, which is where the hub's
  // own app settings live -- RFC 7591 open registration today. `advanced`
  // already appears on both sides for the same reason: one category can hold
  // a browser row and a hub row, and group.admin picks which source a group
  // draws from.
  { id: 'admin-apps', title: 'Apps', category: 'apps', admin: true },
  { id: 'admin-advanced', title: 'Advanced', category: 'advanced', admin: true },
]

/**
 * The section the dialog opens on when a caller asks for no particular one.
 *
 * DERIVED from the order rather than typed a second time: "Preferences opens
 * on its first section" is one statement, so moving a section to the top
 * moves the default with it. Every entry point used to spell 'appearance'
 * out, which is how the list and the landing section came to disagree.
 *
 * A deployment that HIDES that section still lands somewhere: solo mode hides
 * most Account rows, and the dialog resolves a requested id against the
 * visible groups (see `occupiedNavGroups`). Connected apps is the one Account
 * row solo KEEPS, because a solo hub authorizes apps like any other and its
 * owner must be able to disconnect one.
 */
export const DEFAULT_NAV_GROUP_ID: string = NAV_GROUPS[0]!.id
