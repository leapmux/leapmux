import type { AuthState } from '~/context/AuthContext'
import type { PreferencesState } from '~/context/PreferencesContext'
import type { User } from '~/generated/leapmux/v1/auth_pb'
import { Code, ConnectError } from '@connectrpc/connect'
import { cleanup, render, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, useAuth } from '~/context/AuthContext'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { localStorageClearForTests } from '~/lib/browserStorage'
import { useReloadPreferencesOnIdentityChange } from './useReloadPreferencesOnIdentityChange'

const getCurrentUser = vi.hoisted(() => vi.fn())
const login = vi.hoisted(() => vi.fn())
const logout = vi.hoisted(() => vi.fn())
const listUserSettings = vi.hoisted(() => vi.fn())
const updateUserSetting = vi.hoisted(() => vi.fn())

vi.mock('~/api/clients', () => ({
  authClient: {
    getCurrentUser: (...args: unknown[]) => getCurrentUser(...args),
    login: (...args: unknown[]) => login(...args),
    logout: (...args: unknown[]) => logout(...args),
    getSystemInfo: vi.fn().mockResolvedValue({}),
  },
  userClient: {
    listUserSettings: (...args: unknown[]) => listUserSettings(...args),
    updateUserSetting: (...args: unknown[]) => updateUserSetting(...args),
    resetUserSetting: vi.fn(),
  },
}))

vi.mock('~/api/workerRpc', () => ({
  channelManager: { closeAll: vi.fn() },
}))

// `~/api/transport` reads these from the same module at load time, so the
// factory must supply them beside the bridge the auth provider uses.
vi.mock('~/api/platformBridge', () => ({
  desktopFetch: vi.fn(),
  getCapabilities: () => ({ hubTransport: 'direct' }),
  isTauriApp: () => false,
  platformBridge: { resetTunnels: vi.fn().mockResolvedValue(undefined) },
}))

vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  loadSystemInfo: vi.fn().mockResolvedValue(undefined),
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
  getAltchaAlgorithm: () => '',
  getCaptchaProvider: () => 1,
  getCaptchaSiteKey: () => '',
}))

/** The hub's answer for a page that carries no session cookie. */
function unauthenticated(): ConnectError {
  return new ConnectError('no session', Code.Unauthenticated)
}

function user(id: string): User {
  return { id, username: id, isAdmin: false } as unknown as User
}

/** A SettingValue-shaped object, as `listUserSettings` returns it. */
function settingValue(key: string, effectiveJson: string, customized = false) {
  return { key, effectiveJson, customized, valueJson: effectiveJson, secretSet: {} }
}

/** One `listUserSettings` reply carrying a single account theme. */
function themeReply(theme: string) {
  return { descriptors: [], values: [settingValue('theme', `"${theme}"`)] }
}

/** A promise plus its resolvers, for pinning completion order. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  // An unhandled rejection is reported before the code under test can
  // attach its own handler, so the promise is kept handled from the start.
  promise.catch(() => {})
  return { promise, resolve, reject }
}

/** Let every already-settled promise chain run to completion. */
function flushMicrotasks(): Promise<void> {
  return new Promise(resolve => setTimeout(resolve, 0))
}

/**
 * The production nesting: the auth provider, the preferences provider
 * inside it, and one component inside BOTH that installs the trigger —
 * which is exactly where `PreferencesApplier` sits in `app.tsx`.
 */
function renderApp() {
  let auth: AuthState | undefined
  let prefs: PreferencesState | undefined
  function Host() {
    auth = useAuth()
    prefs = usePreferences()
    useReloadPreferencesOnIdentityChange()
    return null
  }
  render(() => (
    <AuthProvider>
      <PreferencesProvider>
        <Host />
      </PreferencesProvider>
    </AuthProvider>
  ))
  return { auth: () => auth!, prefs: () => prefs! }
}

/** Render, and wait for the auth bootstrap to answer. */
async function renderBootstrapped() {
  const app = renderApp()
  await waitFor(() => expect(app.auth().loading()).toBe(false))
  return app
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorageClearForTests()
  getCurrentUser.mockRejectedValue(unauthenticated())
  login.mockResolvedValue({ user: user('u1') })
  logout.mockResolvedValue({})
  listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
  updateUserSetting.mockResolvedValue({})
})

afterEach(() => {
  cleanup()
  localStorageClearForTests()
})

describe('useReloadPreferencesOnIdentityChange', () => {
  // THE live bug. The provider loads once at its own mount, above the
  // router, so the one attempt a visitor gets is spent before any
  // credential exists. Without this trigger the stored theme, fonts and
  // keybindings never applied after a sign-in through the form.
  it('reloads once after a sign-in that follows a failed anonymous load', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await waitFor(() => expect(app.prefs().accountLoadError()).not.toBeNull())
    expect(listUserSettings).toHaveBeenCalledTimes(1)
    expect(app.prefs().theme()).toBe('system')

    await app.auth().login('u1', 'pw')

    await waitFor(() => expect(app.prefs().theme()).toBe('dark'))
    expect(listUserSettings).toHaveBeenCalledTimes(2)
    expect(app.prefs().accountLoadError()).toBeNull()
  })

  it('reloads when one user replaces another', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(app.prefs().theme()).toBe('dark'))

    listUserSettings.mockResolvedValue(themeReply('light'))
    login.mockResolvedValue({ user: user('u2') })
    await app.auth().login('u2', 'pw')

    await waitFor(() => expect(app.prefs().theme()).toBe('light'))
    expect(listUserSettings).toHaveBeenCalledTimes(3)
  })

  // Signing back in as the SAME account is still an identity change: the
  // sign-out recorded that nothing is loaded, so the account tier has to be
  // read again rather than kept from before the sign-out.
  it('reloads after a sign-out and a sign-in as the same user', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(2))

    await app.auth().logout()
    await flushMicrotasks()
    // A signed-out page has no account tier to read, so the sign-out itself
    // must not issue a load that can only answer Unauthenticated.
    expect(listUserSettings).toHaveBeenCalledTimes(2)

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(3))
  })

  // The provider's own mount load carries the SAME session cookie the
  // bootstrap authenticates with, so it already read this identity's
  // settings. A second load here would double every ordinary page view.
  it('does not reload for the identity the mount load already covered', async () => {
    getCurrentUser.mockResolvedValue({ user: user('u1') })
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await waitFor(() => expect(app.prefs().theme()).toBe('dark'))
    expect(app.auth().user()?.id).toBe('u1')
    await flushMicrotasks()
    expect(listUserSettings).toHaveBeenCalledTimes(1)
  })

  // The identity is what changes, not the User object: `refreshUser`
  // replaces it with an equal one, and a re-render replaces nothing at all.
  it('does not reload while the identity stays the same', async () => {
    getCurrentUser.mockResolvedValue({ user: user('u1') })
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(1))

    await app.auth().refreshUser()
    await app.auth().retryBootstrap()
    await flushMicrotasks()

    expect(app.auth().user()?.id).toBe('u1')
    expect(listUserSettings).toHaveBeenCalledTimes(1)
  })

  // A failed login leaves the visitor exactly where they were.
  it('does not reload when the login is refused', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    const app = await renderBootstrapped()
    expect(listUserSettings).toHaveBeenCalledTimes(1)

    login.mockRejectedValue(new Error('bad password'))
    await expect(app.auth().login('u1', 'wrong')).rejects.toThrow('bad password')
    await flushMicrotasks()

    expect(listUserSettings).toHaveBeenCalledTimes(1)
  })

  // The mount load is still IN FLIGHT when the sign-in issues its own, so
  // the two can finish in either order. `loadSeq` is what keeps the newer
  // answer on screen.
  it('drops a stale reply from the load the sign-in superseded', async () => {
    const stale = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockReturnValueOnce(stale.promise)
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(app.prefs().theme()).toBe('dark'))

    stale.resolve({ descriptors: [], values: [settingValue('theme', '"light"')] })
    await flushMicrotasks()
    expect(app.prefs().theme()).toBe('dark')
  })

  // The same rule for the FAILURE half: the anonymous load rejects with
  // Unauthenticated after the sign-in load succeeded, and recording it would
  // state a load error for the rest of the session over settings that are
  // on screen.
  it('drops a stale failure from the load the sign-in superseded', async () => {
    const stale = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockReturnValueOnce(stale.promise)
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(app.prefs().theme()).toBe('dark'))

    stale.reject(unauthenticated())
    await flushMicrotasks()
    expect(app.prefs().accountLoadError()).toBeNull()
    expect(app.prefs().theme()).toBe('dark')
  })

  // The per-key WRITE sequence is the other guard this path relies on: the
  // reload stamps every key when it is issued, so a key the user writes
  // while it is in flight keeps the value the write returned.
  it('leaves a key written during the reload at the value the write returned', async () => {
    const pending = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockReturnValueOnce(pending.promise)
    updateUserSetting.mockResolvedValue({ value: settingValue('theme', '"light"', true) })
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(2))

    // The user picks a theme while the identity reload is still in flight.
    await app.prefs().dual.theme.setAccount('light')
    expect(app.prefs().theme()).toBe('light')

    // The reload answers with the value the account held BEFORE that write.
    pending.resolve({ descriptors: [], values: [settingValue('theme', '"dark"')] })
    await flushMicrotasks()
    expect(app.prefs().theme()).toBe('light')
    expect(app.prefs().accountCustomized().theme).toBe(true)
  })
})
