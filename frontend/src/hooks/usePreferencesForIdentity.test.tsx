import type { AuthState } from '~/context/AuthContext'
import type { PreferencesState } from '~/context/PreferencesContext'
import type { User } from '~/generated/proto/leapmux/v1/auth_pb'
import type { BrowserPreferences } from '~/lib/browserStorage'
import { Code, ConnectError } from '@connectrpc/connect'
import { cleanup, render, waitFor } from '@solidjs/testing-library'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AuthProvider, useAuth } from '~/context/AuthContext'
import { PreferencesProvider, usePreferences } from '~/context/PreferencesContext'
import { KEY_BROWSER_PREFS, localStorageClearForTests, localStorageSet, resetStorageAccountForTests, setStorageAccount } from '~/lib/browserStorage'
import { TEST_USER_ID } from '~/test-support/crdtBridge'
import { usePreferencesForIdentity } from './usePreferencesForIdentity'

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
  // A multi-user hub: every caller signs in, and none of the solo facts hold.
  isAutoAuthenticated: () => false,
  isPasswordSetupGate: () => false,
  passwordSetupRequired: () => false,
  soloPasswordSet: () => false,
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
function themeReply(mode: string) {
  return { descriptors: [], values: [settingValue('theme', JSON.stringify({ name: 'default', mode }))] }
}

/** A promise plus its resolvers, for pinning completion order. */
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
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
 * Reproduce the production provider order.
 *
 * The preferences provider sits inside the auth provider. Its child installs
 * the identity trigger, as `PreferencesApplier` does in `app.tsx`.
 */
function renderApp() {
  let auth: AuthState | undefined
  let prefs: PreferencesState | undefined
  function Host() {
    auth = useAuth()
    prefs = usePreferences()
    usePreferencesForIdentity()
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

/**
 * Write `prefs` into the namespace for `userId`. Then remove the active account.
 *
 * The provider reads the device tier from the account-change notification.
 * Therefore, the seed must exist before the identity arrives.
 */
function seedDeviceTierFor(userId: string, prefs: BrowserPreferences) {
  setStorageAccount(userId)
  localStorageSet(KEY_BROWSER_PREFS, prefs)
  resetStorageAccountForTests()
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
  // These cases drive the identity through the real AuthProvider. The default
  // test identity would make the first resolved identity look unchanged.
  resetStorageAccountForTests()
  getCurrentUser.mockRejectedValue(unauthenticated())
  login.mockResolvedValue({ user: user('u1') })
  logout.mockResolvedValue({})
  listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
  updateUserSetting.mockResolvedValue({})
})

afterEach(() => {
  cleanup()
  localStorageClearForTests()
  setStorageAccount(TEST_USER_ID)
})

describe('usePreferencesForIdentity', () => {
  // The provider loads once when it mounts above the router. That request can
  // run before a visitor supplies credentials. This trigger applies stored
  // preferences after the visitor signs in through the form.
  it('reloads once after a sign-in that follows a failed anonymous load', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await waitFor(() => expect(app.prefs().accountLoadError()).not.toBeNull())
    expect(listUserSettings).toHaveBeenCalledTimes(1)
    expect(app.prefs().theme().mode).toBe('system')

    await app.auth().login('u1', 'pw')

    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))
    expect(listUserSettings).toHaveBeenCalledTimes(2)
    expect(app.prefs().accountLoadError()).toBeNull()
  })

  it('reloads when one user replaces another', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))

    listUserSettings.mockResolvedValue(themeReply('light'))
    login.mockResolvedValue({ user: user('u2') })
    await app.auth().login('u2', 'pw')

    await waitFor(() => expect(app.prefs().theme().mode).toBe('light'))
    expect(listUserSettings).toHaveBeenCalledTimes(3)
  })

  // Signing in again as the same account is still an identity change. The
  // sign-out records that no account tier is loaded, so the provider reads it
  // again.
  it('reloads after a sign-out and a sign-in as the same user', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(2))

    await app.auth().logout()
    await flushMicrotasks()
    // A signed-out page has no account tier. The sign-out must not issue a
    // request that can only return Unauthenticated.
    expect(listUserSettings).toHaveBeenCalledTimes(2)

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(3))
  })

  // The mount request carries the session cookie that bootstrap uses. It reads
  // this identity's settings, so another request would duplicate each normal
  // page load.
  it('does not reload for the identity the mount load already covered', async () => {
    getCurrentUser.mockResolvedValue({ user: user('u1') })
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))
    expect(app.auth().user()?.id).toBe('u1')
    await flushMicrotasks()
    expect(listUserSettings).toHaveBeenCalledTimes(1)
  })

  // The identity controls reloads, not the User object. `refreshUser` replaces
  // the object with an equal value. A render changes neither value.
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

  // A failed login does not change the visitor's identity.
  it('does not reload when the login is refused', async () => {
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    const app = await renderBootstrapped()
    expect(listUserSettings).toHaveBeenCalledTimes(1)

    login.mockRejectedValue(new Error('bad password'))
    await expect(app.auth().login('u1', 'wrong')).rejects.toThrow('bad password')
    await flushMicrotasks()

    expect(listUserSettings).toHaveBeenCalledTimes(1)
  })

  // The mount request remains active when the sign-in issues another request.
  // Either request can finish first. `loadSeq` keeps the newer reply on screen.
  it('drops a stale reply from the load the sign-in superseded', async () => {
    const stale = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockReturnValueOnce(stale.promise)
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))

    stale.resolve(themeReply('light'))
    await flushMicrotasks()
    expect(app.prefs().theme().mode).toBe('dark')
  })

  // The same rule applies to a stale failure. An anonymous request can reject
  // after the sign-in request succeeds. That rejection must not replace the
  // settings with an error.
  it('drops a stale failure from the load the sign-in superseded', async () => {
    const stale = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockReturnValueOnce(stale.promise)
    listUserSettings.mockResolvedValue(themeReply('dark'))
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))

    stale.reject(unauthenticated())
    await flushMicrotasks()
    expect(app.prefs().accountLoadError()).toBeNull()
    expect(app.prefs().theme().mode).toBe('dark')
  })

  // The sequence for each key protects writes during a reload. The reload
  // records each key when it starts. A later write keeps its returned value.
  it('leaves a key written during the reload at the value the write returned', async () => {
    const pending = deferred<{ descriptors: never[], values: unknown[] }>()
    listUserSettings.mockRejectedValueOnce(unauthenticated())
    listUserSettings.mockReturnValueOnce(pending.promise)
    updateUserSetting.mockResolvedValue({
      value: settingValue('theme', JSON.stringify({ name: 'default', mode: 'light' }), true),
    })
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await waitFor(() => expect(listUserSettings).toHaveBeenCalledTimes(2))

    // The user picks a theme while the identity reload is still in flight.
    await app.prefs().dual.theme.setAccount({ name: 'default', mode: 'light' })
    expect(app.prefs().theme().mode).toBe('light')

    // The reload returns the value from before that write.
    pending.resolve(themeReply('dark'))
    await flushMicrotasks()
    expect(app.prefs().theme().mode).toBe('light')
    expect(app.prefs().accountCustomized().theme).toBe(true)
  })

  // This hook does not control the device tier. The provider subscribes to
  // `onStorageAccountChange`, which runs inside `setStorageAccount`. Thus, the
  // device tier changes with the identity write. These cases verify that
  // behavior through `login`.
  it('seeds the device tier as the first identity is written, with no reload', async () => {
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    // Write the document for u1 before sign-in. A normal page starts without an
    // identity and receives the account later.
    seedDeviceTierFor('u1', { theme: { name: 'nord', mode: 'dark' } })

    const app = await renderBootstrapped()
    expect(app.prefs().theme()).toEqual({ name: 'default', mode: 'system' })

    await app.auth().login('u1', 'pw')

    // Do not use `waitFor` here. `AuthContext` calls `setStorageAccount` as it
    // writes the identity. The seed therefore exists before a consumer can
    // observe the new identity.
    expect(app.prefs().theme()).toEqual({ name: 'nord', mode: 'dark' })
  })

  // A user can change without a page reload. The provider must read the new
  // device tier. Otherwise, each signal keeps the previous user's values.
  it('re-seeds the device tier when one user replaces another', async () => {
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    seedDeviceTierFor('u1', { theme: { name: 'nord', mode: 'dark' } })
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    expect(app.prefs().theme()).toEqual({ name: 'nord', mode: 'dark' })

    login.mockResolvedValue({ user: user('u2') })
    await app.auth().login('u2', 'pw')

    // u2 wrote nothing on this device, so the provider uses the defaults.
    await waitFor(() => expect(app.prefs().theme()).toEqual({ name: 'default', mode: 'system' }))
  })

  // A client-side sign-out keeps this provider mounted. The namespace still
  // points to the old account for its teardown writes. Both tiers must return
  // to defaults, or the sign-in page uses the old account's appearance.
  it('returns both tiers to their defaults on a sign-out', async () => {
    listUserSettings.mockResolvedValue({
      descriptors: [],
      values: [{ key: 'diff_view', effectiveJson: JSON.stringify('split'), customized: true }],
    })
    seedDeviceTierFor('u1', { theme: { name: 'nord', mode: 'dark' }, enterKeyMode: 'enter-sends' })
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    expect(app.prefs().theme()).toEqual({ name: 'nord', mode: 'dark' })
    expect(app.prefs().enterKeyMode()).toBe('enter-sends')
    await waitFor(() => expect(app.prefs().diffView()).toBe('split'))

    await expect(app.auth().logout()).resolves.toBeUndefined()
    await flushMicrotasks()

    // The device tier, the account tier, and the flags the dialog reads.
    expect(app.prefs().theme()).toEqual({ name: 'default', mode: 'system' })
    expect(app.prefs().enterKeyMode()).toBe('cmd-enter-sends')
    expect(app.prefs().diffView()).toBe('unified')
    expect(app.prefs().accountCustomized()).toEqual({})
  })

  // A signed-out page has no account tier to read. A request would return
  // Unauthenticated and show an irrelevant error.
  it('loads nothing on a sign-out', async () => {
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    await flushMicrotasks()
    const before = listUserSettings.mock.calls.length

    await expect(app.auth().logout()).resolves.toBeUndefined()
    await flushMicrotasks()
    expect(listUserSettings).toHaveBeenCalledTimes(before)
  })
})
