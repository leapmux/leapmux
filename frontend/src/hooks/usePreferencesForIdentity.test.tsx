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
 * Write `prefs` into `userId`'s namespace, then leave the page with no account.
 *
 * The seed has to be in place BEFORE the identity arrives: the provider reads
 * the device tier from the account-change notification, so a document written
 * afterwards is not what that notification carries.
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
  // These cases drive the identity themselves, through the real AuthProvider.
  // The suite's default sign-in would make the first resolved identity look
  // like a no-op change.
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

    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))
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
    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))

    stale.resolve(themeReply('light'))
    await flushMicrotasks()
    expect(app.prefs().theme().mode).toBe('dark')
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
    await waitFor(() => expect(app.prefs().theme().mode).toBe('dark'))

    stale.reject(unauthenticated())
    await flushMicrotasks()
    expect(app.prefs().accountLoadError()).toBeNull()
    expect(app.prefs().theme().mode).toBe('dark')
  })

  // The per-key WRITE sequence is the other guard this path relies on: the
  // reload stamps every key when it is issued, so a key the user writes
  // while it is in flight keeps the value the write returned.
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

    // The reload answers with the value the account held BEFORE that write.
    pending.resolve(themeReply('dark'))
    await flushMicrotasks()
    expect(app.prefs().theme().mode).toBe('light')
    expect(app.prefs().accountCustomized().theme).toBe(true)
  })

  // The DEVICE half does not live in this hook: the provider subscribes to
  // `onStorageAccountChange`, which fires from inside `setStorageAccount` --
  // synchronously with the identity write, which an effect cannot be. These two
  // cases hold that wiring from the outside, through `login`, so the seed is
  // proved by the transition rather than by a call.
  it('seeds the device tier as the first identity is written, with no reload', async () => {
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    // u1's document, written before anyone signs in. That is the ordinary
    // shape: the page loads with no identity and the account arrives after.
    seedDeviceTierFor('u1', { theme: { name: 'nord', mode: 'dark' } })

    const app = await renderBootstrapped()
    expect(app.prefs().theme()).toEqual({ name: 'default', mode: 'system' })

    await app.auth().login('u1', 'pw')

    // No `waitFor`: the seed runs inside `setStorageAccount`, which
    // `AuthContext` calls as it writes the identity, so the value is already in
    // place by the time anything can observe the new identity. An effect-driven
    // seed would need a flush here, and every consumer that reads once at mount
    // would have taken the default.
    expect(app.prefs().theme()).toEqual({ name: 'nord', mode: 'dark' })
  })

  // A user switch with no page reload. The device tier used to be read once and
  // never again, so every signal kept the PREVIOUS user's values and the dialog
  // reported them as "This device" over the new user's own account settings.
  it('re-seeds the device tier when one user replaces another', async () => {
    listUserSettings.mockResolvedValue({ descriptors: [], values: [] })
    seedDeviceTierFor('u1', { theme: { name: 'nord', mode: 'dark' } })
    const app = await renderBootstrapped()

    await app.auth().login('u1', 'pw')
    expect(app.prefs().theme()).toEqual({ name: 'nord', mode: 'dark' })

    login.mockResolvedValue({ user: user('u2') })
    await app.auth().login('u2', 'pw')

    // u2 has written nothing on this device, so they get the defaults rather
    // than u1's palette.
    await waitFor(() => expect(app.prefs().theme()).toEqual({ name: 'default', mode: 'system' }))
  })

  // A sign-out is client-side: this provider and everything under it stay
  // mounted, and the namespace deliberately keeps pointing at the account that
  // LEFT so its teardown writes still land. Both tiers therefore still hold that
  // account's values, and `PreferencesApplier` keeps painting them -- so the
  // sign-in page would render in the palette and fonts of whoever used the
  // browser last.
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

  // Nothing to READ for a signed-out page: the account request would answer
  // Unauthenticated and record an error over a screen that shows no account row.
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
