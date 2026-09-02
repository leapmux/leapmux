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
  // Apps follows Account, and holds the whole app errand: what the account
  // AUTHORIZED (Connected apps) and what it REGISTERED for others to
  // authorize. It is a user group, not an administration one -- an ordinary
  // account may register an app for itself, and ownership rather than a role
  // decides what each caller sees.
  { id: 'apps', title: 'Apps', category: 'apps', admin: false },
  { id: 'appearance', title: 'Appearance', category: 'appearance', admin: false },
  { id: 'notifications', title: 'Notifications', category: 'notifications', admin: false },
  { id: 'chat', title: 'Chat & Composer', category: 'chat', admin: false },
  { id: 'terminal', title: 'Terminal', category: 'terminal', admin: false },
  // The tray / menu-bar icon, what a close and a minimize do, and the login
  // launch. Every row carries `hidden: () => !isDesktopApp()`, so
  // `occupiedNavGroups` drops the whole section in a browser -- the group
  // needs no visibility rule of its own.
  { id: 'desktop', title: 'Desktop', category: 'desktop', admin: false },
  { id: 'files', title: 'Files & Editors', category: 'files', admin: false },
  { id: 'shortcuts', title: 'Keyboard Shortcuts', category: 'shortcuts', admin: false },
  { id: 'advanced', title: 'Advanced', category: 'advanced', admin: false },
  { id: 'admin-general', title: 'General', category: 'general', admin: true },
  { id: 'admin-signup', title: 'Sign-up & Access', category: 'signup', admin: true },
  // Straight after Sign-up & Access, because it answers the same question one
  // step earlier: that section decides who may hold an account, this one
  // decides which addresses they can reach the hub at. Solo only -- every key
  // in the category is hidden_in_hub, so `occupiedNavGroups` drops the group
  // on a multi-user hub.
  { id: 'admin-network', title: 'Network access', category: 'network', admin: true },
  { id: 'admin-email', title: 'Email (SMTP)', category: 'email', admin: true },
  // The same category on the ADMINISTRATION side, which is where the hub's
  // own app settings live -- RFC 7591 open registration today. `advanced`
  // already appears on both sides for the same reason: one category can hold
  // a browser row and a hub row, and group.admin picks which source a group
  // draws from.
  //
  // TITLED "Hub-wide Apps" rather than "Apps": the user-level Apps section
  // beside it holds what this ACCOUNT registered and authorized, and the two
  // one-word titles read as the same list twice. The word the administration
  // side adds is WHO the registration reaches -- every account on the hub.
  { id: 'admin-apps', title: 'Hub-wide Apps', category: 'apps', admin: true },
  { id: 'admin-captcha', title: 'Bot Protection', category: 'captcha', admin: true },
  { id: 'admin-rate-limits', title: 'Rate Limits', category: 'rate-limits', admin: true },
  { id: 'admin-limits', title: 'Limits & Timeouts', category: 'limits', admin: true },
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
 * every Account row, and the dialog resolves a requested id against the
 * visible groups (see `occupiedNavGroups`). Connected apps -- now an Apps row,
 * and the one solo keeps beside App registrations -- is why a solo hub still
 * shows the section at all: it authorizes apps like any other and its owner
 * must be able to disconnect one.
 */
export const DEFAULT_NAV_GROUP_ID: string = NAV_GROUPS[0]!.id
