import type { SettingValue } from '~/generated/leapmux/v1/settings_pb'
import { createSignal } from 'solid-js'
import { describe, expect, it, vi } from 'vitest'

import { createAdminSettingsStore } from './adminSettings.store'

const listSettings = vi.hoisted(() => vi.fn())
const updateSetting = vi.hoisted(() => vi.fn())
const updateSettingSecret = vi.hoisted(() => vi.fn())
const resetSetting = vi.hoisted(() => vi.fn())

vi.mock('~/api/clients', () => ({
  adminSettingsClient: { listSettings, updateSetting, updateSettingSecret, resetSetting },
}))

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
