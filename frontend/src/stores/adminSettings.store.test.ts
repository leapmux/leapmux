import type { SettingValue } from '~/generated/leapmux/v1/settings_pb'
import { createSignal } from 'solid-js'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { createAdminSettingsStore } from './adminSettings.store'

const listSettings = vi.hoisted(() => vi.fn())
const updateSetting = vi.hoisted(() => vi.fn())
const updateSettingSecret = vi.hoisted(() => vi.fn())
const resetSetting = vi.hoisted(() => vi.fn())
const loadSystemInfo = vi.hoisted(() => vi.fn(() => Promise.resolve()))

vi.mock('~/api/clients', () => ({
  adminSettingsClient: { listSettings, updateSetting, updateSettingSecret, resetSetting },
}))

vi.mock('~/lib/systemInfo', () => ({ loadSystemInfo }))

function value(key: string, effectiveJson: string, customized = false): SettingValue {
  return { key, valueJson: effectiveJson, effectiveJson, customized, secretSet: {} } as unknown as SettingValue
}

describe('createAdminSettingsStore', () => {
  it('is gated on the admin accessor: load is a no-op and mutations refuse', async () => {
    const store = createAdminSettingsStore(() => false)
    await store.load()
    expect(listSettings).not.toHaveBeenCalled()
    await expect(store.update('signup_enabled', 'true')).rejects.toThrow(/administrator/)
    await expect(store.updateSecret('smtp', '{"password":"x"}')).rejects.toThrow(/administrator/)
    await expect(store.reset('signup_enabled')).rejects.toThrow(/administrator/)
  })

  it('loads descriptors and values when enabled', async () => {
    listSettings.mockResolvedValue({
      descriptors: [{ key: 'session_duration_seconds', category: 'general', title: 'Session duration', summary: '', order: 30, hiddenInSolo: false, restart: false, fields: [] }],
      values: [value('session_duration_seconds', '604800')],
    })
    const store = createAdminSettingsStore(() => true)
    await store.load()
    expect(store.state.descriptors).toHaveLength(1)
    expect(store.values().get('session_duration_seconds')?.effectiveJson).toBe('604800')
    expect(store.state.error).toBeNull()
  })

  it('records load errors on the store', async () => {
    listSettings.mockRejectedValue(new Error('forbidden'))
    const store = createAdminSettingsStore(() => true)
    await store.load()
    expect(store.state.error).toBe('forbidden')
  })

  it('a stale response never overwrites a newer one', async () => {
    let resolveFirst: (v: unknown) => void = () => {}
    listSettings.mockImplementationOnce(() => new Promise(resolve => (resolveFirst = resolve)))
    listSettings.mockResolvedValueOnce({ descriptors: [], values: [value('smtp', '{"host":"a"}')] })
    const store = createAdminSettingsStore(() => true)
    const first = store.load()
    const second = store.load()
    await second
    resolveFirst({ descriptors: [], values: [value('smtp', '{"host":"stale"}')] })
    await first
    expect(store.values().get('smtp')?.effectiveJson).toBe('{"host":"a"}')
  })

  it('merges write replies (public, secret, reset) and records rejections without applying', async () => {
    const store = createAdminSettingsStore(() => true)
    updateSetting.mockResolvedValue({ value: value('session_duration_seconds', '3600', true) })
    await store.update('session_duration_seconds', '3600')
    expect(store.values().get('session_duration_seconds')?.effectiveJson).toBe('3600')

    // The reply must be MERGED, exactly as update and reset are: the store
    // returns `.value` from the RPC so the row re-renders with the server's
    // truth (a secret's `secretSet` flips to true). Asserting only the call
    // argument would still pass if the reply were dropped.
    updateSettingSecret.mockResolvedValue({ value: value('smtp', '{"host":"mail"}', true) })
    await store.updateSecret('smtp', '{"password":"hunter2"}')
    expect(updateSettingSecret).toHaveBeenCalledWith({ key: 'smtp', partialJson: '{"password":"hunter2"}' })
    expect(store.values().get('smtp')?.effectiveJson).toBe('{"host":"mail"}')
    expect(store.state.writeError).toBeNull()

    // And a refused secret write records the error against its own key
    // without disturbing the stored value, like the other two mutators.
    updateSettingSecret.mockRejectedValue(new Error('keystore unavailable'))
    await expect(store.updateSecret('smtp', '{"password":"x"}')).rejects.toThrow('keystore unavailable')
    expect(store.values().get('smtp')?.effectiveJson).toBe('{"host":"mail"}')
    expect(store.state.writeError).toMatchObject({ key: 'smtp' })
    updateSettingSecret.mockReset()

    updateSetting.mockRejectedValue(new Error('validation failed'))
    await expect(store.update('session_duration_seconds', '1')).rejects.toThrow('validation failed')
    expect(store.values().get('session_duration_seconds')?.effectiveJson).toBe('3600')
    expect(store.state.writeError).toMatchObject({ key: 'session_duration_seconds' })

    resetSetting.mockResolvedValue({ value: value('session_duration_seconds', '604800', false) })
    await store.reset('session_duration_seconds')
    expect(store.values().get('session_duration_seconds')?.effectiveJson).toBe('604800')

    resetSetting.mockRejectedValue(new Error('reset refused'))
    await expect(store.reset('session_duration_seconds')).rejects.toThrow('reset refused')
    expect(store.values().get('session_duration_seconds')?.effectiveJson).toBe('604800')
    expect(store.state.writeError).toMatchObject({ key: 'session_duration_seconds', message: 'reset refused' })
  })

  it('clears its contents when the session loses admin', async () => {
    const [enabled, setEnabled] = createSignal(true)
    listSettings.mockResolvedValue({
      descriptors: [{ key: 'smtp', category: 'email', title: 'SMTP relay', summary: '', order: 10, hiddenInSolo: false, restart: false, fields: [] }],
      values: [value('smtp', '{}')],
    })
    const store = createAdminSettingsStore(enabled)
    await store.load()
    expect(store.state.descriptors).toHaveLength(1)
    setEnabled(false)
    await store.load()
    expect(store.state.descriptors).toHaveLength(0)
    expect(store.values().size).toBe(0)
  })
})

/**
 * Half of what GetSystemInfo answers IS hub settings, and the page fetches it
 * once at bootstrap. Without a refresh here every one of those flags stayed
 * at the value the page loaded with: an administrator who published the hub's
 * URL watched Add passkey stay disabled, on the screen that had just accepted
 * the change, with no way to know why.
 */
describe('createAdminSettingsStore system-info convergence', () => {
  beforeEach(() => {
    loadSystemInfo.mockReset().mockResolvedValue(undefined)
    updateSetting.mockReset()
    updateSettingSecret.mockReset()
    resetSetting.mockReset()
  })

  it('re-reads the system info after each accepted write', async () => {
    const store = createAdminSettingsStore(() => true)

    updateSetting.mockResolvedValue({ value: value('public_url', '"https://hub.example"', true) })
    await store.update('public_url', '"https://hub.example"')
    expect(loadSystemInfo).toHaveBeenCalledWith(true)

    updateSettingSecret.mockResolvedValue({ value: value('smtp', '{"host":"mail"}', true) })
    await store.updateSecret('smtp', '{"password":"hunter2"}')
    expect(loadSystemInfo).toHaveBeenCalledTimes(2)

    resetSetting.mockResolvedValue({ value: value('public_url', '""', false) })
    await store.reset('public_url')
    expect(loadSystemInfo).toHaveBeenCalledTimes(3)
  })

  // A refused write changed nothing on the hub, so there is nothing to
  // converge on and the round trip is waste. Both refusals count: one the hub
  // raised, and one the store's own admin guard raised before it asked.
  it('does not re-read after a refused write', async () => {
    const store = createAdminSettingsStore(() => true)
    updateSetting.mockRejectedValue(new Error('validation failed'))

    await expect(store.update('public_url', '"nonsense"')).rejects.toThrow('validation failed')
    expect(updateSetting).toHaveBeenCalledTimes(1)
    expect(loadSystemInfo).not.toHaveBeenCalled()
  })

  it('does not re-read for a write the admin guard refused', async () => {
    const store = createAdminSettingsStore(() => false)

    await expect(store.update('public_url', '"https://hub.example"')).rejects.toThrow(/administrator/)
    expect(updateSetting).not.toHaveBeenCalled()
    expect(loadSystemInfo).not.toHaveBeenCalled()
  })

  // The row must not wait on a second round trip to show the value the hub
  // already accepted. A refresh that never settles must not hold the reply.
  it('merges the reply without waiting for the refresh', async () => {
    const store = createAdminSettingsStore(() => true)
    loadSystemInfo.mockReturnValue(new Promise(() => {}))
    updateSetting.mockResolvedValue({ value: value('public_url', '"https://hub.example"', true) })

    await store.update('public_url', '"https://hub.example"')
    expect(store.values().get('public_url')?.effectiveJson).toBe('"https://hub.example"')
  })

  // A failed refresh keeps the previous snapshot. It must not turn an
  // accepted write into a reported failure, and it must not reach the console
  // as an unhandled rejection.
  it('keeps an accepted write accepted when the refresh fails', async () => {
    const store = createAdminSettingsStore(() => true)
    loadSystemInfo.mockRejectedValue(new Error('hub unreachable'))
    updateSetting.mockResolvedValue({ value: value('public_url', '"https://hub.example"', true) })

    await store.update('public_url', '"https://hub.example"')
    expect(store.state.writeError).toBeNull()
    expect(store.values().get('public_url')?.effectiveJson).toBe('"https://hub.example"')
  })
})
