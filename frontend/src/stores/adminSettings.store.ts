import type { Accessor } from 'solid-js'
import type { SettingsStore } from './settingsStore'
import { adminSettingsClient } from '~/api/clients'
import { createSettingsStore } from './settingsStore'

/**
 * The hub-scope (admin) settings store over `AdminSettingsService`.
 *
 * Gated on the `enabled` accessor (wired to `useAuth().user()?.isAdmin` by
 * the dialog): `load` is a no-op while disabled and every mutation rejects,
 * so a non-admin session can neither list nor mutate hub settings.
 */
export function createAdminSettingsStore(enabled: Accessor<boolean>): SettingsStore {
  return createSettingsStore({
    list: () => adminSettingsClient.listSettings({}),
    update: async (key, partialJson) =>
      (await adminSettingsClient.updateSetting({ key, partialJson })).value,
    updateSecret: async (key, partialJson) =>
      (await adminSettingsClient.updateSettingSecret({ key, partialJson })).value,
    reset: async key => (await adminSettingsClient.resetSetting({ key })).value,
    enabled,
    guardMessage: 'admin settings require an administrator session',
    loadErrorFallback: 'Failed to load hub settings',
  })
}
