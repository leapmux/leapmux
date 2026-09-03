import type { AuthState } from './AuthContext'
/// <reference types="vitest/globals" />
import type { User } from '~/generated/proto/leapmux/v1/auth_pb'
import { timestampDate, timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError } from '@connectrpc/connect'
import { render, screen } from '@solidjs/testing-library'
import { Show } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { elevationDeadlineInterceptor } from '~/api/transport'
import { BOOT_SPLASH_PHASE_ATTRIBUTE } from '~/lib/bootSplashTheme'
import { hasStorageAccount, KEY_BROWSER_PREFS, resetStorageAccountForTests, setStorageAccount, storedKeyFor } from '~/lib/browserStorage'
import { deferred } from '~/test-support/async'
import { TEST_USER_ID } from '~/test-support/crdtBridge'

import { AuthProvider, useAuth } from './AuthContext'

const mockGetCurrentUser = vi.fn()
const mockLogin = vi.fn()
vi.mock('~/api/clients', () => ({
  authClient: {
    getCurrentUser: (...args: unknown[]) => mockGetCurrentUser(...args),
    getSystemInfo: vi.fn().mockResolvedValue({ signupEnabled: false, soloMode: false }),
    login: (...args: unknown[]) => mockLogin(...args),
    logout: vi.fn().mockResolvedValue({}),
  },
  userClient: {},
}))

// The step-up ceremonies the context now owns. The mirror has NO public
// setter, so this is the seam a surface reaches instead.
const mockElevateWithPassword = vi.fn<(p: string) => Promise<unknown>>()
const mockElevateWithPasskey = vi.fn<() => Promise<unknown>>()
const mockDropElevation = vi.fn<() => Promise<void>>()
vi.mock('~/lib/elevation', async () => {
  const actual = await vi.importActual<typeof import('~/lib/elevation')>('~/lib/elevation')
  return {
    ...actual,
    elevateWithPassword: (...a: [string]) => mockElevateWithPassword(...a),
    elevateWithPasskey: () => mockElevateWithPasskey(),
    dropElevation: () => mockDropElevation(),
  }
})

const mockCloseAll = vi.fn()
vi.mock('~/api/workerRpc', () => ({
  channelManager: {
    closeAll: () => mockCloseAll(),
  },
}))

const mockResetTunnels = vi.fn<() => Promise<void>>()
vi.mock('~/api/platformBridge', () => ({
  // ~/api/transport imports these from the same module at load time, so the
  // factory must provide them alongside the bridge under test.
  desktopFetch: vi.fn(),
  getCapabilities: () => ({ hubTransport: 'direct' }),
  isTauriApp: () => false,
  platformBridge: {
    resetTunnels: () => mockResetTunnels(),
  },
}))

const mockLoadSystemInfo = vi.fn<() => Promise<void>>(() => Promise.resolve())
vi.mock('~/lib/systemInfo', () => ({
  isSoloMode: () => false,
  // A multi-user hub: every caller signs in, and none of the solo facts hold.
  isAutoAuthenticated: () => false,
  passwordSetupRequired: () => false,
  soloPasswordSet: () => false,
  loadSystemInfo: () => mockLoadSystemInfo(),
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
  getAltchaAlgorithm: () => '',
  getCaptchaProvider: () => 1, // CaptchaProvider.ALTCHA
  getCaptchaSiteKey: () => '',
}))

function TestConsumer() {
  const auth = useAuth()
  return (
    <div>
      <span data-testid="authenticated">{auth.isAuthenticated() ? 'yes' : 'no'}</span>
      <span data-testid="username">{auth.user()?.username ?? 'none'}</span>
      <span data-testid="bootstrap-error">{auth.bootstrapError() ?? 'none'}</span>
    </div>
  )
}

function renderWithAuth() {
  return render(() => (
    <AuthProvider>
      <TestConsumer />
    </AuthProvider>
  ))
}

// Capture the live auth context so tests can drive login()/logout() directly.
function renderWithAuthCapture(): { auth: () => AuthState } {
  let captured: AuthState | undefined
  function Capture() {
    captured = useAuth()
    return null
  }
  render(() => (
    <AuthProvider>
      <Capture />
      <TestConsumer />
    </AuthProvider>
  ))
  return { auth: () => captured! }
}

describe('authContext', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockLoadSystemInfo.mockResolvedValue(undefined)
  })

  // Bootstrap writes the boot splash's phase onto <html>; later tests in this
  // file would otherwise read a phase a previous test left behind.
  afterEach(() => {
    document.documentElement.removeAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)
  })

  it('restores session from cookie on mount when getCurrentUser succeeds', async () => {
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'testuser', isAdmin: false },
    })

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('testuser')
  })

  // The boot splash's checklist advances on these phases; both RPCs are held
  // open with deferreds so each phase is observed, not raced past.
  it('advances the boot splash phase through bootstrap and lands on ready', async () => {
    const systemInfo = deferred<void>()
    const session = deferred<{ user: { id: string, username: string, isAdmin: boolean } }>()
    mockLoadSystemInfo.mockReturnValue(systemInfo.promise)
    mockGetCurrentUser.mockReturnValue(session.promise)

    renderWithAuth()

    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('system-info')
    })

    systemInfo.resolve()
    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('session')
    })

    session.resolve({ user: { id: 'u1', username: 'testuser', isAdmin: false } })
    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('ready')
    })
  })

  // A failed system-info load aborts bootstrap; the checklist stays on the
  // step that failed instead of claiming the session restore ran.
  it('leaves the boot splash phase on the step that failed', async () => {
    mockLoadSystemInfo.mockRejectedValue(new ConnectError('hub unreachable', Code.Unavailable))

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('bootstrap-error')).not.toHaveTextContent('none')
    })
    expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('system-info')
  })

  // The two ways bootstrap can fail must stay distinguishable. An expired or
  // absent cookie is the ordinary answer and stays silent; anything else means
  // the hub is unreachable, which no login fixes.
  //
  // This case previously rejected with a bare `new Error('unauthenticated')`,
  // which is NOT a ConnectError -- so it exercised the transport-failure path
  // while claiming to cover the expired-cookie one. Both leave `authenticated`
  // at 'no', so the assertion could not tell them apart.
  it('stays silently unauthenticated when the session cookie is expired or absent', async () => {
    mockGetCurrentUser.mockRejectedValue(
      new ConnectError('unauthenticated', Code.Unauthenticated),
    )

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('none')
    expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('none')
  })

  it('records a bootstrap error when the hub is unreachable', async () => {
    // Not an Unauthenticated code: the session may well be valid and the client
    // simply could not ask. Silently treating this as "logged out" is what left solo
    // mode on a loading spinner forever, with nothing to click.
    mockGetCurrentUser.mockRejectedValue(
      new ConnectError('connection refused', Code.Unavailable),
    )

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('bootstrap-error')).not.toHaveTextContent('none')
    })
    expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('connection refused')
    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
  })

  // System info is a hard prerequisite, not an optional best-effort step: its
  // module getters are fabricated defaults until it succeeds. Discarding the
  // failure leaves `isSoloMode()` at `false` while the session restore SUCCEEDS on a
  // solo hub (which authenticates every procedure), so the app renders as
  // multi-user and offers a "Log out" that strands the user at a login form
  // no credentials can satisfy. Bootstrap must stop and report instead.
  it('records a bootstrap error when the system info load fails', async () => {
    mockLoadSystemInfo.mockRejectedValue(new Error('not connected'))
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'solo', isAdmin: false },
    })

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('not connected')
    })
    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
    expect(mockGetCurrentUser).not.toHaveBeenCalled()
  })

  it('retryBootstrap recovers from a system-info failure', async () => {
    mockLoadSystemInfo.mockRejectedValue(new Error('not connected'))
    const { auth } = renderWithAuthCapture()

    await vi.waitFor(() => {
      expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('not connected')
    })

    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'recovered', isAdmin: false },
    })
    await auth().retryBootstrap()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('recovered')
    expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('none')
  })

  it('retryBootstrap re-attempts and clears the error on recovery', async () => {
    mockGetCurrentUser.mockRejectedValue(
      new ConnectError('connection refused', Code.Unavailable),
    )
    const { auth } = renderWithAuthCapture()

    await vi.waitFor(() => {
      expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('connection refused')
    })

    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'recovered', isAdmin: false },
    })
    await auth().retryBootstrap()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('recovered')
    expect(screen.getByTestId('bootstrap-error')).toHaveTextContent('none')
  })

  it('works after oauth callback redirect (page loads with cookie)', async () => {
    // Simulate: user just returned from OAuth callback with a valid session cookie.
    // AuthContext.onMount calls getCurrentUser, which succeeds because the cookie is set.
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u2', username: 'oauth-user', isAdmin: false, oauthProviders: ['Google'] },
    })

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('oauth-user')
  })

  it('drops pooled channels and resets sidecar tunnels when logging in over a different user', async () => {
    // A still-authenticated user (a bookmarked /login, a stale tab) submits
    // another account's credentials. That is an identity transition just like
    // logout, so the previous user's pooled E2EE channels must be released
    // eagerly rather than left to the lazy per-request identity check -- and the
    // desktop sidecar's tunnels (which carry no identity of their own) must be
    // reset so they cannot keep relaying under the old user.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    mockLogin.mockResolvedValue({ user: { id: 'u2', username: 'bob', isAdmin: false } })
    await auth().login('bob', 'pw')

    expect(mockCloseAll).toHaveBeenCalledOnce()
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('username')).toHaveTextContent('bob')
  })

  it('does not drop pooled channels when re-authenticating as the same user', async () => {
    // Re-login as the SAME identity (a session refresh) is not a transition, so
    // the pooled channels -- already correct for this user -- must be kept.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()

    mockLogin.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    await auth().login('alice', 'pw')

    expect(mockCloseAll).not.toHaveBeenCalled()
    expect(mockResetTunnels).not.toHaveBeenCalled()
  })

  it('drops pooled channels and resets sidecar tunnels on logout', async () => {
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    await auth().logout()

    expect(mockCloseAll).toHaveBeenCalledOnce()
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
  })

  it('drops pooled channels when any setUser path swaps to a different identity', async () => {
    // The release is driven off the `user` signal, so the signal covers EVERY
    // identity transition by construction -- not just login/logout. setAuth is normally null->A
    // account creation, but a future impersonation seed or server-side session swap
    // that changes the id must release too; the old imperative wiring only released
    // at login/logout/auth-error and would have leaked the previous user's channels.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    auth().setAuth({ id: 'u2', username: 'bob', isAdmin: false } as unknown as User)

    await vi.waitFor(() => expect(mockCloseAll).toHaveBeenCalledOnce())
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('username')).toHaveTextContent('bob')
  })

  // The elevation belongs to the identity that proved a factor, so an
  // identity SWAP must drop it -- the same rule logout already follows.
  // When it survives, /elevate reads a live deadline that belongs to nobody in
  // this document, redirects without prompting, and the hub's consent gate
  // then answers with a page that has no way forward while the CLI waits.
  it('drops the elevation when a setUser path swaps to a different identity', async () => {
    const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'alice', isAdmin: false },
      elevationExpiresAt: deadline,
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    expect(auth().elevationExpiresAt()).toBe(deadline)

    auth().setAuth({ id: 'u2', username: 'bob', isAdmin: false } as unknown as User)

    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('bob'))
    expect(auth().elevationExpiresAt()).toBeUndefined()
  })

  // One adoption site, so a refresh cannot leave one signal stale while
  // another moves. The verification cooldown was the one that drifted:
  // refreshUser dropped it, and /verify-email then counted from zero against
  // a hub that just minted a code and would refuse the next resend.
  it('adopts every signal on a refresh, not only the user', async () => {
    const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))
    const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'alice', isAdmin: false },
      elevationExpiresAt: deadline,
      emailVerification: { nextResendAvailableAt: resend },
    })
    await auth().refreshUser()

    expect(auth().elevationExpiresAt()).toBe(deadline)
    expect(auth().verificationResendAvailableAt()).toBe(resend)
  })

  /**
   * A read of the account is not instant, and three surfaces start one without
   * awaiting it. So a response can land AFTER a newer answer already wrote the
   * same signals, and adopting it puts the older account state back.
   */
  describe('a stale GetCurrentUser response', () => {
    it('does not revert a freshly proven elevation', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      // A refresh leaves, carrying no deadline, and does not land yet.
      const inFlight = deferred<{ user: { id: string, username: string, isAdmin: boolean } }>()
      mockGetCurrentUser.mockReturnValue(inFlight.promise)
      const refreshing = auth().refreshUser()

      // The user elevates while it is still out.
      mockElevateWithPassword.mockResolvedValue(deadline)
      await auth().elevateWithPassword('secret')
      expect(auth().elevationExpiresAt()).toBe(deadline)

      inFlight.resolve({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      await refreshing

      // `readCurrentUser` discards the older answer. Adopting it would make Preferences
      // report an elevated session as unelevated, and the next sensitive
      // action would prompt for a window the user already holds.
      expect(auth().elevationExpiresAt()).toBe(deadline)
    })

    it('does not revert a newer response that already landed', async () => {
      const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const first = deferred<unknown>()
      mockGetCurrentUser.mockReturnValueOnce(first.promise)
      const firstRefresh = auth().refreshUser()

      mockGetCurrentUser.mockResolvedValue({
        user: { id: 'u1', username: 'renamed', isAdmin: false },
        emailVerification: { nextResendAvailableAt: resend },
      })
      await auth().refreshUser()
      expect(auth().user()?.username).toBe('renamed')

      first.resolve({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      await firstRefresh

      expect(auth().user()?.username).toBe('renamed')
      expect(auth().verificationResendAvailableAt()).toBe(resend)
    })

    /**
     * The other write of the user, and the only one that touches that signal
     * alone. `/verify-email` adopts the account its own response carried while
     * the page's refresh is still out, so the read answers the account as it
     * stood BEFORE the verification. Adopting it puts `emailVerified: false`
     * back, and Preferences then renders "unverified / Verify" for an address
     * the hub verified a moment ago.
     */
    it('does not revert a same-identity user that landed first', async () => {
      mockGetCurrentUser.mockResolvedValue({
        user: { id: 'u1', username: 'alice', emailVerified: false, isAdmin: false },
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const inFlight = deferred<unknown>()
      mockGetCurrentUser.mockReturnValue(inFlight.promise)
      const refreshing = auth().refreshUser()

      auth().adoptSameIdentityUser(
        { id: 'u1', username: 'alice', emailVerified: true } as unknown as User,
      )
      expect(auth().user()?.emailVerified).toBe(true)

      inFlight.resolve({ user: { id: 'u1', username: 'alice', emailVerified: false, isAdmin: false } })
      await refreshing

      expect(auth().user()?.emailVerified).toBe(true)
    })

    /**
     * The resend cooldown moves without any read of the account: the
     * `/verify-email` page records the deadline the hub minted with the code.
     * A read that left before that mint answers the previous cooldown -- often
     * none at all -- and adopting it re-enables Resend at once. The button then
     * sends a second request that the hub refuses with ResourceExhausted.
     */
    it('does not revert a cooldown a resend recorded while it was out', async () => {
      const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const inFlight = deferred<unknown>()
      mockGetCurrentUser.mockReturnValue(inFlight.promise)
      const refreshing = auth().refreshUser()

      auth().setVerificationResendAvailableAt(resend)

      inFlight.resolve({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      await refreshing

      expect(auth().verificationResendAvailableAt()).toBe(resend)
    })
  })

  /**
   * The ceremonies the context owns, because the hub owns the window and this
   * signal only reflects it. Three surfaces used to write the mirror through a
   * public setter, which is what made the revert above possible.
   */
  describe('the step-up ceremonies', () => {
    it('adopts the window a password proved', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      mockElevateWithPassword.mockResolvedValue(deadline)
      await auth().elevateWithPassword('secret')

      expect(mockElevateWithPassword).toHaveBeenCalledWith('secret')
      expect(auth().elevationExpiresAt()).toBe(deadline)
    })

    it('adopts the window a passkey proved', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      mockElevateWithPasskey.mockResolvedValue(deadline)
      await auth().elevateWithPasskey()

      expect(auth().elevationExpiresAt()).toBe(deadline)
    })

    // The hub grants a window on EVERY success, so a response with no
    // timestamp is one this client cannot read rather than an ended window.
    // Clearing on it turned a successful elevation into a session that
    // reported itself unelevated.
    it('keeps the window when a success carries no timestamp', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: deadline,
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
      expect(auth().elevationExpiresAt()).toBe(deadline)

      mockElevateWithPassword.mockResolvedValue(undefined)
      await auth().elevateWithPassword('secret')

      expect(auth().elevationExpiresAt()).toBe(deadline)
    })

    // The hub's own message reaches the form: "current password is incorrect"
    // and the rate-limit refusal are both sentences the user can act on.
    it('rethrows the hub refusal, and adopts nothing', async () => {
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      mockElevateWithPassword.mockRejectedValue(new Error('current password is incorrect'))
      await expect(auth().elevateWithPassword('wrong')).rejects.toThrow('current password is incorrect')
      expect(auth().elevationExpiresAt()).toBeUndefined()
    })

    // The drop is the one change that must never render as still live, so the
    // context clears the mirror locally rather than reads it again.
    it('clears the window once the hub accepts a drop', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: deadline,
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      mockDropElevation.mockResolvedValue(undefined)
      await auth().dropElevation()
      expect(auth().elevationExpiresAt()).toBeUndefined()
    })

    it('keeps the window when the drop fails', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: deadline,
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      mockDropElevation.mockRejectedValue(new Error('the hub is unreachable'))
      await expect(auth().dropElevation()).rejects.toThrow('the hub is unreachable')
      expect(auth().elevationExpiresAt()).toBe(deadline)
    })
  })

  /**
   * The deadline the hub reports on a SLIDE.
   *
   * The hub extends the window on every sensitive action and emits no event
   * for it, because a client still holding the shorter deadline fails closed.
   * It reports the new deadline on the response to the request that slid it
   * instead. These drive the real interceptor, so the two halves of that
   * adoption are tested joined rather than each against a stub.
   */
  describe('a deadline the hub reports on a slide', () => {
    const later = new Date(Date.now() + 115 * 60 * 1000)

    /** A response carrying the hub's report. */
    function reporting(until: Date) {
      const header = new Headers()
      header.set('leapmux-elevation-expires-at', until.toISOString())
      return { header } as never
    }

    /** One sliding request, from the moment it leaves to the moment it lands. */
    function slidingRequest(land: () => Promise<never>) {
      return elevationDeadlineInterceptor(land)({ stream: false } as never)
    }

    async function signedIn() {
      mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      const captured = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
      return captured
    }

    it('adopts it into the mirror', async () => {
      const { auth } = await signedIn()

      await slidingRequest(async () => reporting(later))

      expect(timestampDate(auth().elevationExpiresAt()!).toISOString()).toBe(later.toISOString())
    })

    it('leaves the mirror alone when the response reports nothing', async () => {
      const { auth } = await signedIn()

      await slidingRequest(async () => ({ header: new Headers() }) as never)

      expect(auth().elevationExpiresAt()).toBeUndefined()
    })

    /**
     * A report is NEWER than any account read that left before it, so that read
     * must not put its own older answer back. `readCurrentUser` discards a
     * response whose generation moved, and adopting through the context's own
     * setter is what moves it.
     */
    it('is not reverted by a GetCurrentUser that left earlier', async () => {
      const { auth } = await signedIn()

      const reading = deferred<{ user: { id: string, username: string, isAdmin: boolean } }>()
      mockGetCurrentUser.mockReturnValue(reading.promise)
      const refreshing = auth().refreshUser()

      await slidingRequest(async () => reporting(later))
      expect(auth().elevationExpiresAt()).toBeDefined()

      // The read answers with no window, because it left before the slide.
      reading.resolve({ user: { id: 'u1', username: 'alice', isAdmin: false } })
      await refreshing

      expect(timestampDate(auth().elevationExpiresAt()!).toISOString()).toBe(later.toISOString())
    })

    /**
     * An End now must WIN over a report that was already in flight.
     *
     * Without this the row says "verified until 15:55" again for the rest of
     * the window, on the one control the documentation points at for a shared
     * machine, and nothing corrects it until that stale deadline passes.
     */
    it('loses to an End now that commits while it is in flight', async () => {
      mockGetCurrentUser.mockResolvedValue({
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: timestampFromDate(new Date(Date.now() + 60 * 60 * 1000)),
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const landing = deferred<never>()
      const inFlight = slidingRequest(() => landing.promise)

      mockDropElevation.mockResolvedValue(undefined)
      await auth().dropElevation()
      expect(auth().elevationExpiresAt()).toBeUndefined()

      landing.resolve(reporting(later))
      await inFlight

      expect(auth().elevationExpiresAt()).toBeUndefined()
    })

    // A second sliding request must still be adopted. The staleness rule keys
    // on the window ENDING, not on every write: keying it on the general
    // generation counter would discard this one and leave the early value.
    it('is not discarded by another slide that landed first', async () => {
      const { auth } = await signedIn()
      const earlier = new Date(Date.now() + 100 * 60 * 1000)

      const landing = deferred<never>()
      const inFlight = slidingRequest(() => landing.promise)

      await slidingRequest(async () => reporting(earlier))

      landing.resolve(reporting(later))
      await inFlight

      expect(timestampDate(auth().elevationExpiresAt()!).toISOString()).toBe(later.toISOString())
    })
  })

  // The hub changed a field of the SAME account and touched no elevation
  // column, so the window survives. /verify-email is the caller.
  it('keeps the elevation when it adopts a same-identity user', async () => {
    const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    mockGetCurrentUser.mockResolvedValue({
      user: { id: 'u1', username: 'alice', isAdmin: false },
      elevationExpiresAt: deadline,
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    auth().adoptSameIdentityUser({ id: 'u1', username: 'alice', emailVerified: true } as unknown as User)

    expect(auth().user()?.emailVerified).toBe(true)
    expect(auth().elevationExpiresAt()).toBe(deadline)
  })

  it('does not drop pooled channels on the initial session restore', async () => {
    // null -> first-user is not an identity SWAP: there is nothing pooled to release,
    // and a spurious resetTunnels must not fire on a fresh page load.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    renderWithAuth()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    expect(mockCloseAll).not.toHaveBeenCalled()
    expect(mockResetTunnels).not.toHaveBeenCalled()
  })

  it('logout survives a sidecar tunnel-reset failure', async () => {
    // resetTunnels is best-effort: a rejected sidecar RPC (e.g. the sidecar
    // restarts) must be logged, not break the logout flow or surface as an
    // unhandled rejection.
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'u1', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockResetTunnels.mockRejectedValueOnce(new Error('sidecar gone'))

    await auth().logout()
    // Let the rejected resetTunnels promise settle through its .catch.
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
  })
})

// The storage namespace moves with the identity, and this provider is the one
// writer of both. What matters is the ORDER: `AuthGuard`'s `Show` is a render
// effect and runs ahead of a user effect in the same flush, so an effect-based
// move would let `AppShell` mount and read the PREVIOUS account's keys.
describe('authContext storage namespace', () => {
  beforeEach(() => {
    resetStorageAccountForTests()
  })

  afterEach(() => {
    resetStorageAccountForTests()
    setStorageAccount(TEST_USER_ID)
  })

  it('points storage at the account before anything can render for it', async () => {
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'alice', username: 'alice', isAdmin: false } })

    // Read the namespace from a component that REQUIRES the identity -- the shape
    // `AuthGuard` has. `Show` is a render effect, so this body runs ahead of
    // every user effect in the flush that publishes the identity: it is the
    // earliest moment any consumer exists, and the one an effect-based
    // `setStorageAccount` would lose to.
    let seenWhileRendering: string | null | undefined
    function Guarded() {
      seenWhileRendering = storedKeyFor(KEY_BROWSER_PREFS)
      return null
    }
    function Gate() {
      const auth = useAuth()
      return <Show when={auth.isAuthenticated()}><Guarded /></Show>
    }
    render(() => <AuthProvider><Gate /></AuthProvider>)

    await vi.waitFor(() => expect(seenWhileRendering).toBeDefined())
    expect(seenWhileRendering).toBe('leapmux:u:alice:browser-prefs')
  })

  // Signing out tears down the authenticated tree, and the writes that teardown
  // makes -- a draft flush, a layout snapshot -- belong to the account that
  // LEAVES. Clearing the namespace would make every one of them throw.
  it('keeps the namespace after a sign-out', async () => {
    mockGetCurrentUser.mockResolvedValue({ user: { id: 'alice', username: 'alice', isAdmin: false } })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    await auth().logout()

    expect(hasStorageAccount()).toBe(true)
    expect(storedKeyFor(KEY_BROWSER_PREFS)).toBe('leapmux:u:alice:browser-prefs')
  })

  // A User the app cannot key storage by is not an identity. Letting
  // `setStorageAccount`'s refusal escape would land in `restoreSession`'s
  // network catch and report a client-side data fault as "Could not reach the
  // hub.", with a Retry that hits the identical throw every time.
  it('treats a user with no id as signed out, not as a failed bootstrap', async () => {
    const error = vi.spyOn(console, 'error').mockImplementation(() => {})
    mockGetCurrentUser.mockResolvedValue({ user: { id: '', username: 'nobody', isAdmin: false } })
    const { auth } = renderWithAuthCapture()

    await vi.waitFor(() => expect(auth().loading()).toBe(false))
    expect(auth().isAuthenticated()).toBe(false)
    expect(auth().bootstrapError()).toBeNull()
    expect(error).toHaveBeenCalled()
    error.mockRestore()
  })
})
