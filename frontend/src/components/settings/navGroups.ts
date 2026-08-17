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
  { id: 'appearance', title: 'Appearance', category: 'appearance', admin: false },
  { id: 'notifications', title: 'Notifications', category: 'notifications', admin: false },
  { id: 'chat', title: 'Chat & Composer', category: 'chat', admin: false },
  { id: 'terminal', title: 'Terminal', category: 'terminal', admin: false },
  { id: 'files', title: 'Files & Editors', category: 'files', admin: false },
  { id: 'shortcuts', title: 'Keyboard Shortcuts', category: 'shortcuts', admin: false },
  { id: 'advanced', title: 'Advanced', category: 'advanced', admin: false },
  { id: 'account', title: 'Account', category: 'account', admin: false },
  { id: 'admin-general', title: 'General', category: 'general', admin: true },
  { id: 'admin-signup', title: 'Sign-up & Access', category: 'signup', admin: true },
  { id: 'admin-email', title: 'Email (SMTP)', category: 'email', admin: true },
  { id: 'admin-captcha', title: 'Bot Protection', category: 'captcha', admin: true },
  { id: 'admin-rate-limits', title: 'Rate Limits', category: 'rate-limits', admin: true },
  { id: 'admin-limits', title: 'Limits & Timeouts', category: 'limits', admin: true },
  { id: 'admin-advanced', title: 'Advanced', category: 'advanced', admin: true },
]
