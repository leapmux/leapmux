import type { JSX } from 'solid-js'
import { render } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { KEY_BROWSER_PREFS, KEY_PREFERRED_EDITOR, loadBrowserPrefs, localStorageClearForTests, localStorageGet } from '~/lib/browserStorage'

import { buildBrowserReset } from './registry/settings'

const listUserSettings = vi.hoisted(() => vi.fn().mockResolvedValue({ descriptors: [], values: [] }))
vi.mock('~/api/clients', () => ({
  userClient: { listUserSettings, updateUserSetting: vi.fn(), resetUserSetting: vi.fn() },
  authClient: {},
}))

type Prefs = ReturnType<typeof usePreferences>

function captureContext(): { get: () => Prefs } {
  let captured: Prefs | undefined
  function Capture(): JSX.Element {
    captured = usePreferences()
    return null
  }
  render(() => (
    <PreferencesProvider>
      <Capture />
    </PreferencesProvider>
  ))
  return {
    get: () => {
      if (!captured)
        throw new Error('Preferences context not yet captured')
      return captured
    },
  }
}

beforeEach(() => {
  localStorageClearForTests()
})

afterEach(() => {
  localStorageClearForTests()
})

describe('browserPrefReset', () => {
  it('returns every browser override to its sentinel default through the real context', () => {
    const ctx = captureContext()
    // One override per sentinel shape, plus a singleton-key override.
    ctx.get().dual.theme.setBrowser({ name: 'nord', mode: 'dark' }) // nullable
    ctx.get().setExpandAgentThoughts(false) // default-on opt-out
    ctx.get().setShowHiddenMessages(true) // default-off opt-in
    ctx.get().setPreferredEditorId('zed') // nullable, own key
    expect(loadBrowserPrefs().theme).toEqual({ name: 'nord', mode: 'dark' })

    for (const action of buildBrowserReset(ctx.get()))
      action.reset()

    // Nullable / default-off / default-on keys are all DELETED, never set to
    // their default value explicitly.
    expect('theme' in loadBrowserPrefs()).toBe(false)
    expect('expandAgentThoughts' in loadBrowserPrefs()).toBe(false)
    expect('showHiddenMessages' in loadBrowserPrefs()).toBe(false)
    expect(localStorageGet<string>(KEY_PREFERRED_EDITOR)).toBeUndefined()

    // The signals fall back to their defaults.
    expect(ctx.get().theme()).toEqual({ name: 'default', mode: 'system' })
    expect(ctx.get().expandAgentThoughts()).toBe(true)
    expect(ctx.get().showHiddenMessages()).toBe(false)
    expect(ctx.get().preferredEditorId()).toBeUndefined()

    // The consolidated blob itself survives (other fields would be kept).
    expect(localStorageGet(KEY_BROWSER_PREFS)).toBeDefined()
  })
})
