import type { Accessor } from 'solid-js'
import type { SettingsStore } from './settingsStore'
import type { SettingValue } from '~/generated/proto/leapmux/v1/settings_pb'
import { adminSettingsClient } from '~/api/clients'
import { loadSystemInfo } from '~/lib/systemInfo'
import { createSettingsStore } from './settingsStore'

/**
 * Re-read GetSystemInfo after a hub-settings write, and hand the reply back
 * unchanged.
 *
 * Half of what GetSystemInfo answers IS hub settings, and the page fetches it
 * once at bootstrap. So every one of those answers stayed at the value the
 * page loaded with until a reload: public_url decides which origin runs
 * passkey ceremonies and which URL the hub gives a worker to dial, the SMTP
 * key decides whether email affordances appear, and the sign-up and captcha
 * keys decide the sign-in form. An administrator who published the hub's URL
 * watched Add passkey stay disabled with no way to know why, on the very
 * screen that just accepted the change.
 *
 * Nothing here is a public_url special case, and that is the point: one
 * refresh after ANY hub-settings write covers every flag the snapshot
 * carries, including the ones nobody added yet.
 *
 * NOT awaited. The row must not wait on a second round trip to show the value
 * the hub already accepted, and no decision depends on the new snapshot
 * landing before the store merges the reply. A failed refresh keeps the previous
 * snapshot and the next write converges.
 *
 * `loadSystemInfo(true)` rather than `refreshSnapshot()`: this is a KNOWN
 * change, not a suspicion that the snapshot is stale, and it happens at human
 * speed. The
 * dedupe window that `refreshSnapshot` holds exists for failure sites that
 * fire in rapid succession, and it would discard the second of two writes made
 * inside three seconds -- leaving exactly the stale flag this exists to remove.
 */
async function withSystemInfoRefresh(
  call: () => Promise<SettingValue | undefined>,
): Promise<SettingValue | undefined> {
  const value = await call()
  loadSystemInfo(true).catch(() => {})
  return value
}

/**
 * The hub-scope (admin) settings store over `AdminSettingsService`.
 *
 * Restricted by the `enabled` accessor (wired to `useAuth().user()?.isAdmin` by
 * the dialog): `load` is a no-op while disabled and every mutation rejects,
 * so a non-admin session can neither list nor mutate hub settings.
 */
export function createAdminSettingsStore(enabled: Accessor<boolean>): SettingsStore {
  return createSettingsStore({
    list: () => adminSettingsClient.listSettings({}),
    update: (key, partialJson) => withSystemInfoRefresh(
      async () => (await adminSettingsClient.updateSetting({ key, partialJson })).value,
    ),
    updateSecret: (key, partialJson) => withSystemInfoRefresh(
      async () => (await adminSettingsClient.updateSettingSecret({ key, partialJson })).value,
    ),
    reset: key => withSystemInfoRefresh(
      async () => (await adminSettingsClient.resetSetting({ key })).value,
    ),
    enabled,
    guardMessage: 'admin settings require an administrator session',
    loadErrorFallback: 'Failed to load hub settings',
  })
}
