import type { SettingRowModel } from '../types'
import { CUSTOM_EDITOR_OWNS_ITS_VALUE } from './bindings'

/**
 * The ADMINISTRATION tier of the settings registry: rows that reach the
 * Administration side alone and have no hub SETTING behind them.
 *
 * The browser registry (settings.ts) cannot hold these: its categories feed
 * the USER sections, so an entry with category `apps` would also land in the
 * user-level Apps section and double-list the admin form. This tier is the
 * second source `groupRowsByNav`'s admin input draws from, beside the proto
 * descriptor rows, so an administration row lives in the registry like every
 * other row while the user groups never see it.
 */
export const ADMIN_ROWS: readonly SettingRowModel[] = [
  {
    descriptor: {
      id: 'apps.hubWideRegistrations',
      category: 'apps',
      label: 'Hub-wide app registrations',
      help: 'Apps every account on this hub may authorize. Registering one is an administrator\'s act, so the hub asks for a fresh proof first.',
      keywords: ['app', 'oauth', 'register', 'hub-wide', 'client', 'redirect', 'secret', 'developer'],
      scope: 'hub',
      control: { kind: 'custom', id: 'hubWideAppRegistrations' },
    },
    binding: CUSTOM_EDITOR_OWNS_ITS_VALUE,
  },
]
