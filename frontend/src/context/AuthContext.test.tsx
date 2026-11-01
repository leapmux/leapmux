import type { AuthState } from './AuthContext'
/// <reference types="vitest/globals" />
import type { EmailVerificationStatus, SessionReport, User } from '~/generated/proto/leapmux/v1/auth_pb'
import { timestampDate, timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError } from '@connectrpc/connect'
import { render, screen } from '@solidjs/testing-library'
import { IDBFactory } from 'fake-indexeddb'
import { createEffect, createRoot, Show } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { elevationDeadlineInterceptor } from '~/api/transport'
import { BOOT_SPLASH_PHASE_ATTRIBUTE } from '~/lib/bootSplashTheme'
import { loadBrowserPrefs } from '~/lib/browserPreferences'
import { flushStorageWrites, hasStorageAccount, KEY_BROWSER_PREFS, localStorageSet, resetBrowserStorageForTests, resetStorageAccountForTests, setStorageAccountForTests, storedKeyFor } from '~/lib/browserStorage'
import { deferred, settle, untilTrue } from '~/test-support/async'
import { TEST_USER_ID } from '~/test-support/crdtBridge'

import { AuthProvider, useAuth } from './AuthContext'

const mockGetCurrentUser = vi.fn()
const mockLogin = vi.fn()
const mockPasswordSetupGate = vi.fn<() => boolean>(() => false)
vi.mock('~/api/clients', () => ({
  authClient: {
    getCurrentUser: (...args: unknown[]) => mockGetCurrentUser(...args),
    login: (...args: unknown[]) => mockLogin(...args),
    logout: vi.fn().mockResolvedValue({}),
  },
  systemClient: {
    getSystemInfo: vi.fn().mockResolvedValue({ signupEnabled: false, soloMode: false }),
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

// A default resolution: closePooledChannels chains .catch onto the result, and
// a test that never states a tunnel outcome must not crash the auth effect.
const mockResetTunnels = vi.fn<() => Promise<void>>().mockResolvedValue(undefined)
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
  isPasswordSetupGate: () => mockPasswordSetupGate(),
  soloPasswordSet: () => false,
  loadSystemInfo: () => mockLoadSystemInfo(),
  isSystemInfoLoaded: () => true,
  isCaptchaEnabled: () => false,
  getAltchaAlgorithm: () => '',
  getCaptchaProvider: () => 1, // CaptchaProvider.ALTCHA
  getCaptchaSiteKey: () => '',
}))

/** A sign-in's SessionReport, for the adoption paths that take one. */
function reportFor(user: Record<string, unknown> | undefined, extra: Record<string, unknown> = {}): SessionReport {
  return {
    ...(user
      ? { user }
      : {}),
    ...extra,
  } as unknown as SessionReport
}

function TestConsumer() {
  const auth = useAuth()
  return (
    <div>
      <span data-testid="authenticated">
        {auth.isAuthenticated()
          ? 'yes'
          : 'no'}
      </span>
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
  return {
    auth: () => captured!,
  }
}

describe('AuthContext', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockPasswordSetupGate.mockReturnValue(false)
  })

  // Bootstrap writes the boot splash's phase onto <html>; later tests in this
  // file would otherwise read a phase a previous test left behind.
  afterEach(() => {
    document.documentElement.removeAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)
  })

  it('restores session from cookie on mount when getCurrentUser succeeds', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'testuser', isAdmin: false },
      },
    })

    renderWithAuth()

    await vi.waitFor(() => {
      expect(screen.getByTestId('authenticated')).toHaveTextContent('yes')
    })
    expect(screen.getByTestId('username')).toHaveTextContent('testuser')
  })

  // The boot splash's checklist advances on these phases; both RPCs are held
  // open with deferreds so each phase is observed, not raced past.
  it('advances the boot splash phase through bootstrap and lands on workspaces when authenticated', async () => {
    const systemInfo = deferred<void>()
    const session = deferred<{ sessionReport: { user: { id: string, username: string, isAdmin: boolean } } }>()
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

    session.resolve({
      sessionReport: {
        user: { id: 'u1', username: 'testuser', isAdmin: false },
      },
    })
    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('workspaces')
    })
  })

  it('lands on ready when bootstrap finishes signed out', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: { user: undefined },
    })
    renderWithAuth()
    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('ready')
    })
  })

  it('lands on ready without revealing the shell during TCP password setup', async () => {
    mockPasswordSetupGate.mockReturnValue(true)
    mockGetCurrentUser.mockRejectedValue(new ConnectError('unauthenticated', Code.Unauthenticated))
    renderWithAuth()

    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('ready')
    })
    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
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
  // failure leaves `isSoloMode()` at `false` while the session restore SUCCEEDS
  // on a solo hub reached over local IPC (which needs no credential), so the app
  // renders as multi-user and offers a "Log out" that strands the user at a
  // login form no credentials can satisfy. Bootstrap must stop and report
  // instead.
  it('records a bootstrap error when the system info load fails', async () => {
    mockLoadSystemInfo.mockRejectedValue(new Error('not connected'))
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'solo', isAdmin: false },
      },
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
      sessionReport: {
        user: { id: 'u1', username: 'recovered', isAdmin: false },
      },
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
      sessionReport: {
        user: { id: 'u1', username: 'recovered', isAdmin: false },
      },
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
      sessionReport: {
        user: {
          id: 'u2',
          username: 'oauth-user',
          isAdmin: false,
          oauthProviders: ['Google'],
        },
      },
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
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    mockLogin.mockResolvedValue({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })
    await auth().login('bob', 'pw')

    expect(mockCloseAll).toHaveBeenCalledOnce()
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('username')).toHaveTextContent('bob')
  })

  it('does not drop pooled channels when re-authenticating as the same user', async () => {
    // Re-login as the SAME identity (a session refresh) is not a transition, so
    // the pooled channels -- already correct for this user -- must be kept.
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()

    mockLogin.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    await auth().login('alice', 'pw')

    expect(mockCloseAll).not.toHaveBeenCalled()
    expect(mockResetTunnels).not.toHaveBeenCalled()
  })

  it('drops pooled channels and resets sidecar tunnels on logout', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    await auth().logout()

    expect(mockCloseAll).toHaveBeenCalledOnce()
    expect(mockResetTunnels).toHaveBeenCalledOnce()
    expect(screen.getByTestId('authenticated')).toHaveTextContent('no')
    // Finished shell checklist must not linger into the next bootstrap splash.
    expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('initializing')
  })

  it('enters the shell checklist on login before AppShell mounts', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: { user: undefined },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => {
      expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('ready')
    })

    mockLogin.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    await auth().login('alice', 'pw')

    expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('workspaces')
  })

  it('drops pooled channels when any setUser path swaps to a different identity', async () => {
    // The release is driven off the `user` signal, so the signal covers EVERY
    // identity transition by construction -- not just login/logout. setAuth is normally null->A
    // account creation, but a future impersonation seed or server-side session swap
    // that changes the id must release too; the old imperative wiring only released
    // at login/logout/auth-error and would have leaked the previous user's channels.
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    mockCloseAll.mockClear()
    mockResetTunnels.mockClear()
    mockResetTunnels.mockResolvedValue(undefined)

    void auth().setAuth(reportFor({ id: 'u2', username: 'bob', isAdmin: false }))

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
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: deadline,
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))
    expect(auth().elevationExpiresAt()).toBe(deadline)

    void auth().setAuth(reportFor({ id: 'u2', username: 'bob', isAdmin: false }))

    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('bob'))
    expect(auth().elevationExpiresAt()).toBeUndefined()
  })

  // The one reply that GRANTS an elevation in the same transaction that creates
  // the session: SetInitialSoloPassword. Its report carries the deadline, which
  // keeps the client state derived from the answer that created it, rather than
  // from a following refresh whose failure is swallowed -- which would prompt
  // the operator for the password they chose a moment earlier.
  it('keeps an elevation the same reply granted', async () => {
    const inherited = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    const granted = timestampFromDate(new Date(Date.now() + 30 * 60 * 1000))
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: inherited,
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    void auth().setAuth(reportFor({ id: 'solo', username: 'solo', isAdmin: true }, { elevationExpiresAt: granted }))

    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('solo'))
    expect(auth().elevationExpiresAt()).toBe(granted)
  })

  // One adoption site, so a refresh cannot leave one signal stale while
  // another moves. The verification cooldown was the one that drifted:
  // refreshUser dropped it, and /verify-email then counted from zero against
  // a hub that just minted a code and would refuse the next resend.
  it('adopts every signal on a refresh, not only the user', async () => {
    const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))
    const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: deadline,
        emailVerification: { nextResendAvailableAt: resend },
      },
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
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      // A refresh leaves, carrying no deadline, and does not land yet.
      const inFlight = deferred<{ sessionReport: { user: { id: string, username: string, isAdmin: boolean } } }>()
      mockGetCurrentUser.mockReturnValue(inFlight.promise)
      const refreshing = auth().refreshUser()

      // The user elevates while it is still out.
      mockElevateWithPassword.mockResolvedValue(deadline)
      await auth().elevateWithPassword('secret')
      expect(auth().elevationExpiresAt()).toBe(deadline)

      inFlight.resolve({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
      await refreshing

      // `readCurrentUser` discards the older answer. Adopting it would make Preferences
      // report an elevated session as unelevated, and the next sensitive
      // action would prompt for a window the user already holds.
      expect(auth().elevationExpiresAt()).toBe(deadline)
    })

    it('does not revert a newer response that already landed', async () => {
      const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const first = deferred<unknown>()
      mockGetCurrentUser.mockReturnValueOnce(first.promise)
      const firstRefresh = auth().refreshUser()

      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'renamed', isAdmin: false },
          emailVerification: { nextResendAvailableAt: resend },
        },
      })
      await auth().refreshUser()
      expect(auth().user()?.username).toBe('renamed')

      first.resolve({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
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
        sessionReport: {
          user: {
            id: 'u1',
            username: 'alice',
            emailVerified: false,
            isAdmin: false,
          },
        },
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const inFlight = deferred<unknown>()
      mockGetCurrentUser.mockReturnValue(inFlight.promise)
      const refreshing = auth().refreshUser()

      await auth().openUserAdoption()(
        { id: 'u1', username: 'alice', emailVerified: true } as unknown as User,
      )
      expect(auth().user()?.emailVerified).toBe(true)

      inFlight.resolve({
        sessionReport: {
          user: {
            id: 'u1',
            username: 'alice',
            emailVerified: false,
            isAdmin: false,
          },
        },
      })
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
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      const inFlight = deferred<unknown>()
      mockGetCurrentUser.mockReturnValue(inFlight.promise)
      const refreshing = auth().refreshUser()

      auth().setVerificationResendAvailableAt(resend)

      inFlight.resolve({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
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
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
      const { auth } = renderWithAuthCapture()
      await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

      mockElevateWithPassword.mockResolvedValue(deadline)
      await auth().elevateWithPassword('secret')

      expect(mockElevateWithPassword).toHaveBeenCalledWith('secret')
      expect(auth().elevationExpiresAt()).toBe(deadline)
    })

    it('adopts the window a passkey proved', async () => {
      const deadline = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
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
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
          elevationExpiresAt: deadline,
        },
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
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
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
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
          elevationExpiresAt: deadline,
        },
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
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
          elevationExpiresAt: deadline,
        },
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
      mockGetCurrentUser.mockResolvedValue({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
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

      await slidingRequest(async () => ({
        header: new Headers(),
      }) as never)

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

      const reading = deferred<{ sessionReport: { user: { id: string, username: string, isAdmin: boolean } } }>()
      mockGetCurrentUser.mockReturnValue(reading.promise)
      const refreshing = auth().refreshUser()

      await slidingRequest(async () => reporting(later))
      expect(auth().elevationExpiresAt()).toBeDefined()

      // The read answers with no window, because it left before the slide.
      reading.resolve({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
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
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
          elevationExpiresAt: timestampFromDate(new Date(Date.now() + 60 * 60 * 1000)),
        },
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
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: deadline,
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    await auth().openUserAdoption()({ id: 'u1', username: 'alice', emailVerified: true } as unknown as User)

    expect(auth().user()?.emailVerified).toBe(true)
    expect(auth().elevationExpiresAt()).toBe(deadline)
  })

  it('does not drop pooled channels on the initial session restore', async () => {
    // null -> first-user is not an identity SWAP: there is nothing pooled to release,
    // and a spurious resetTunnels must not fire on a fresh page load.
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    renderWithAuth()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('alice'))

    expect(mockCloseAll).not.toHaveBeenCalled()
    expect(mockResetTunnels).not.toHaveBeenCalled()
  })

  it('logout survives a sidecar tunnel-reset failure', async () => {
    // resetTunnels is best-effort: a rejected sidecar RPC (e.g. the sidecar
    // restarts) must be logged, not break the logout flow or surface as an
    // unhandled rejection.
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
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
/** Sign in as `alice` from a signed-out bootstrap; returns the live context. */
async function bootSignedOut(): Promise<() => AuthState> {
  mockGetCurrentUser.mockRejectedValue(new ConnectError('no session', Code.Unauthenticated))
  const { auth } = renderWithAuthCapture()
  await untilTrue(() => !auth().loading())
  mockGetCurrentUser.mockReset()
  return auth
}

describe('AuthContext sign-in recovery', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockPasswordSetupGate.mockReturnValue(false)
  })

  afterEach(() => {
    document.documentElement.removeAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)
  })

  // The sign-in SUCCEEDED; only its account projection could not be read. The
  // context reads the account, adopts it, and marks recovery completed for the
  // gate, which navigates and then finishes it.
  it('adopts the account a recovery read returns', async () => {
    const auth = await bootSignedOut()
    mockLogin.mockResolvedValue({
      sessionReport: {
        emailVerification: { verificationRequired: false },
      },
    })
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })

    const result = await auth().login('alice', 'pw')

    expect(result.outcome).toBe('recovering')
    await untilTrue(() => auth().signInRecovery()?.completed === true)
    expect(auth().user()?.username).toBe('alice')
    expect(auth().loading()).toBe(false)
    expect(auth().signInRecovery()?.error).toBeNull()

    auth().finishSignInRecovery()
    expect(auth().signInRecovery()).toBeNull()
  })

  it('records a failed recovery read, and Retry re-reads only the account', async () => {
    const auth = await bootSignedOut()
    mockLogin.mockResolvedValue({ sessionReport: {} })
    mockGetCurrentUser.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable))

    await auth().login('alice', 'pw')
    await untilTrue(() => auth().signInRecovery()?.error != null)
    expect(auth().user()).toBeNull()
    expect(auth().signInRecovery()?.error).toContain('down')

    mockGetCurrentUser.mockResolvedValueOnce({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    await auth().retrySignInRecovery()

    expect(auth().user()?.username).toBe('alice')
    expect(auth().signInRecovery()?.completed).toBe(true)
    expect(mockLogin).toHaveBeenCalledOnce()
  })

  // The identity transition happens at once, before the account read: the
  // previous identity, its elevation and its pooled channels all go.
  it('drops the previous identity at once when the sign-in reports no account', async () => {
    const deadline = timestampFromDate(new Date(Date.now() + 60 * 60 * 1000))
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u0', username: 'previous', isAdmin: true },
        elevationExpiresAt: deadline,
      },
    })
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(screen.getByTestId('username')).toHaveTextContent('previous'))
    expect(auth().elevationExpiresAt()).toBe(deadline)
    mockCloseAll.mockClear()

    mockLogin.mockResolvedValue({ sessionReport: {} })
    mockGetCurrentUser.mockRejectedValue(new ConnectError('down', Code.Unavailable))
    await auth().login('alice', 'pw')

    expect(auth().elevationExpiresAt()).toBeUndefined()
    expect(auth().user()).toBeNull()
    await vi.waitFor(() => expect(mockCloseAll).toHaveBeenCalled())
    await untilTrue(() => auth().signInRecovery()?.error != null)
    expect(auth().user()).toBeNull()
  })

  it('seeds the resend cooldown from a report with no account', async () => {
    const auth = await bootSignedOut()
    const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))
    mockLogin.mockResolvedValue({
      sessionReport: {
        emailVerification: { verificationRequired: true, nextResendAvailableAt: resend },
      },
    })
    mockGetCurrentUser.mockReturnValue(new Promise(() => {}))

    await auth().login('alice', 'pw')

    expect(auth().verificationResendAvailableAt()).toBe(resend)
    expect(auth().signInRecovery()?.verificationRequired).toBe(true)
  })

  // Recovery owns the navigation after the read, so it keeps the address the
  // sign-in asked to continue to, such as a consent page.
  it('keeps the redirect the sign-in asked to continue to', async () => {
    const auth = await bootSignedOut()
    mockLogin.mockResolvedValue({ sessionReport: {} })
    mockGetCurrentUser.mockReturnValue(new Promise(() => {}))

    await auth().login('alice', 'pw', undefined, { redirect: '/oauth/authorize?state=s' })

    expect(auth().signInRecovery()?.redirect).toBe('/oauth/authorize?state=s')
  })

  it('states no redirect when the sign-in asked for none', async () => {
    const auth = await bootSignedOut()
    mockLogin.mockResolvedValue({ sessionReport: {} })
    mockGetCurrentUser.mockReturnValue(new Promise(() => {}))

    await auth().login('alice', 'pw')

    const recovery = auth().signInRecovery()
    expect(recovery).not.toBeNull()
    expect(Object.keys(recovery ?? {})).not.toContain('redirect')
  })

  // setAuth is the other way into recovery: a sign-up or an OAuth completion
  // whose report carries no account.
  it('recovers from a setAuth report with no account', async () => {
    const auth = await bootSignedOut()
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })

    const outcome = await auth().setAuth(undefined, { redirect: '/settings' })

    expect(outcome).toBe('recovering')
    expect(auth().signInRecovery()?.redirect).toBe('/settings')
    await untilTrue(() => auth().signInRecovery()?.completed === true)
    expect(auth().user()?.username).toBe('alice')
  })

  it('reads nothing when Retry runs with no recovery in progress', async () => {
    const auth = await bootSignedOut()

    await auth().retrySignInRecovery()

    expect(mockGetCurrentUser).not.toHaveBeenCalled()
    expect(auth().signInRecovery()).toBeNull()
  })

  // A sign-out ends the session that the recovery reads. A late answer of that
  // read must neither adopt the account nor start the recovery again.
  it.each(['succeeds', 'fails'] as const)('stays signed out when the recovery read %s after a sign-out', async (answer) => {
    const auth = await bootSignedOut()
    const read = deferred<unknown>()
    mockLogin.mockResolvedValue({ sessionReport: {} })
    mockGetCurrentUser.mockReturnValueOnce(read.promise)
    await auth().login('alice', 'pw')
    expect(auth().signInRecovery()).not.toBeNull()

    await auth().logout()
    expect(auth().signInRecovery()).toBeNull()

    if (answer === 'succeeds') {
      read.resolve({
        sessionReport: {
          user: { id: 'u1', username: 'alice', isAdmin: false },
        },
      })
    }
    else {
      read.reject(new ConnectError('down', Code.Unavailable))
    }
    await settle()

    expect(auth().user()).toBeNull()
    expect(auth().signInRecovery()).toBeNull()
  })

  // The recovery read keys its staleness on the operation, not on the count
  // that every local write moves. A cooldown write during the read must not
  // discard the read and leave the gate waiting with no read in flight.
  it('adopts the account although an unrelated write landed during the read', async () => {
    const auth = await bootSignedOut()
    const read = deferred<unknown>()
    mockLogin.mockResolvedValue({ sessionReport: {} })
    mockGetCurrentUser.mockReturnValueOnce(read.promise)
    await auth().login('alice', 'pw')

    auth().setVerificationResendAvailableAt(timestampFromDate(new Date(Date.now() + 60 * 1000)))
    read.resolve({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })

    await untilTrue(() => auth().signInRecovery()?.completed === true)
    expect(auth().user()?.username).toBe('alice')
  })
})

describe('AuthContext operation ownership', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockPasswordSetupGate.mockReturnValue(false)
  })

  afterEach(() => {
    document.documentElement.removeAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)
  })

  // Sign-in A recovers, and its read fails only after a newer sign-in B
  // adopted its own account. A no longer owns the state, so it writes nothing.
  it('ignores an older sign-in\'s recovery failure after a newer sign-in adopted', async () => {
    const auth = await bootSignedOut()
    const aRead = deferred<never>()
    mockLogin.mockResolvedValueOnce({ sessionReport: {} })
    mockGetCurrentUser.mockReturnValueOnce(aRead.promise)
    await auth().login('alice', 'pw')

    mockLogin.mockResolvedValueOnce({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })
    const b = await auth().login('bob', 'pw')
    expect(b.outcome).toBe('adopted')

    aRead.reject(new ConnectError('down', Code.Unavailable))
    await Promise.resolve()
    await Promise.resolve()

    expect(auth().user()?.username).toBe('bob')
    expect(auth().signInRecovery()).toBeNull()
    expect(auth().bootstrapError()).toBeNull()
    expect(auth().loading()).toBe(false)
  })

  // A's sign-in RPC answers while B's is still pending. A is superseded, so its
  // `finally` must not end the `loading` B still holds.
  it('keeps loading until the newest sign-in finishes', async () => {
    const auth = await bootSignedOut()
    const aLogin = deferred<unknown>()
    const bLogin = deferred<unknown>()
    mockLogin.mockReturnValueOnce(aLogin.promise).mockReturnValueOnce(bLogin.promise)

    const a = auth().login('alice', 'pw')
    const b = auth().login('bob', 'pw')
    aLogin.resolve({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })
    expect((await a).outcome).toBe('superseded')
    expect(auth().loading()).toBe(true)
    expect(auth().user()).toBeNull()

    bLogin.resolve({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })
    expect((await b).outcome).toBe('adopted')
    expect(auth().loading()).toBe(false)
    expect(auth().user()?.username).toBe('bob')
  })

  // The bootstrap restore's read fails only after a sign-in superseded it. The
  // failure describes a session nobody uses, so no bootstrap error appears.
  it('ignores a restore failure after a sign-in superseded it', async () => {
    const restore = deferred<never>()
    mockGetCurrentUser.mockReturnValueOnce(restore.promise)
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(mockGetCurrentUser).toHaveBeenCalledOnce())

    mockLogin.mockResolvedValueOnce({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })
    await auth().login('bob', 'pw')
    restore.reject(new ConnectError('boom', Code.Internal))
    await Promise.resolve()
    await Promise.resolve()

    expect(auth().bootstrapError()).toBeNull()
    expect(auth().user()?.username).toBe('bob')
  })

  // A's sign-in RPC fails only after B adopted its account. The failure
  // belongs to an attempt nobody waits on, so the banner stays empty. A's
  // caller still sees the rejection.
  it('keeps the error banner empty when an older sign-in fails after a newer one adopted', async () => {
    const auth = await bootSignedOut()
    const aLogin = deferred<unknown>()
    mockLogin.mockReturnValueOnce(aLogin.promise).mockResolvedValueOnce({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })

    const a = auth().login('alice', 'wrong')
    expect((await auth().login('bob', 'pw')).outcome).toBe('adopted')

    aLogin.reject(new Error('invalid credentials'))
    await expect(a).rejects.toThrow('invalid credentials')

    expect(auth().error()).toBeNull()
    expect(auth().loading()).toBe(false)
    expect(auth().user()?.username).toBe('bob')
  })

  // A sign-in during the system info load owns the state. The bootstrap then
  // stops: it neither restores a session over the sign-in nor reports a
  // failure for a load nobody waits on.
  it.each(['succeeds', 'fails'] as const)('leaves a sign-in alone when the superseded system info load %s', async (answer) => {
    const systemInfo = deferred<void>()
    mockLoadSystemInfo.mockReturnValueOnce(systemInfo.promise)
    const { auth } = renderWithAuthCapture()
    await vi.waitFor(() => expect(mockLoadSystemInfo).toHaveBeenCalledOnce())
    mockLogin.mockResolvedValueOnce({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })
    await auth().login('bob', 'pw')

    if (answer === 'succeeds')
      systemInfo.resolve()
    else
      systemInfo.reject(new Error('not connected'))
    await settle()

    expect(mockGetCurrentUser).not.toHaveBeenCalled()
    expect(auth().bootstrapError()).toBeNull()
    expect(auth().user()?.username).toBe('bob')
    expect(auth().loading()).toBe(false)
    expect(document.documentElement.getAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)).toBe('workspaces')
  })
})

describe('AuthContext openUserAdoption', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockLoadSystemInfo.mockResolvedValue(undefined)
    mockPasswordSetupGate.mockReturnValue(false)
  })

  afterEach(() => {
    document.documentElement.removeAttribute(BOOT_SPLASH_PHASE_ATTRIBUTE)
  })

  async function bootAs(fields: Record<string, unknown>): Promise<() => AuthState> {
    mockGetCurrentUser.mockResolvedValue({ sessionReport: fields })
    const { auth } = renderWithAuthCapture()
    await untilTrue(() => !auth().loading())
    mockGetCurrentUser.mockClear()
    return auth
  }

  // A provider-only account's profile reply is the complete projection, and
  // adopting it keeps the provider list and the step-up option.
  it('adopts a same-identity reply with its providers and step-up rule', async () => {
    const auth = await bootAs({
      user: { id: 'u1', username: 'alice', isAdmin: false },
    })
    const adopt = auth().openUserAdoption()

    await adopt({
      id: 'u1',
      username: 'alice',
      displayName: 'Renamed',
      oauthProviders: [
        { id: 'gh', name: 'GitHub', enabled: true },
      ],
      mayElevateThroughAProvider: true,
    } as unknown as User)

    expect(auth().user()?.displayName).toBe('Renamed')
    expect(auth().user()?.oauthProviders).toHaveLength(1)
    expect(auth().user()?.mayElevateThroughAProvider).toBe(true)
    expect(mockGetCurrentUser).not.toHaveBeenCalled()
  })

  // The user ended the elevation while the profile write was out. The reply
  // carries no elevation, and adopting it must not put the window back.
  it('leaves an elevation the user ended in the meantime ended', async () => {
    const deadline = timestampFromDate(new Date(Date.now() + 60 * 60 * 1000))
    const auth = await bootAs({
      user: { id: 'u1', username: 'alice', isAdmin: false },
      elevationExpiresAt: deadline,
    })
    const adopt = auth().openUserAdoption()
    mockDropElevation.mockResolvedValue(undefined)
    await auth().dropElevation()
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
      },
    })

    await adopt({ id: 'u1', username: 'alice' } as unknown as User)

    expect(auth().elevationExpiresAt()).toBeUndefined()
  })

  // A newer elevation landed while the write was out. The adoption re-reads
  // rather than adopting, and the newer deadline stands.
  it('keeps a newer elevation that landed in the meantime', async () => {
    const auth = await bootAs({
      user: { id: 'u1', username: 'alice', isAdmin: false },
    })
    const adopt = auth().openUserAdoption()
    const newer = timestampFromDate(new Date(Date.now() + 2 * 60 * 60 * 1000))
    mockElevateWithPassword.mockResolvedValue(newer)
    await auth().elevateWithPassword('pw')
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u1', username: 'alice', isAdmin: false },
        elevationExpiresAt: newer,
      },
    })

    await adopt({ id: 'u1', username: 'alice', displayName: 'stale' } as unknown as User)

    expect(auth().elevationExpiresAt()).toBe(newer)
    expect(auth().user()?.displayName).not.toBe('stale')
    expect(mockGetCurrentUser).toHaveBeenCalledOnce()
  })

  // The identity changed while the write was out. The reply belongs to the old
  // account, so the adoption re-reads the NEW identity instead.
  it('re-reads instead of adopting after an identity change', async () => {
    const auth = await bootAs({
      user: { id: 'u1', username: 'alice', isAdmin: false },
    })
    const adopt = auth().openUserAdoption()
    await auth().setAuth(reportFor({ id: 'u2', username: 'bob', isAdmin: false }))
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'u2', username: 'bob', isAdmin: false },
      },
    })

    await adopt({ id: 'u1', username: 'alice' } as unknown as User)

    expect(auth().user()?.username).toBe('bob')
    expect(mockGetCurrentUser).toHaveBeenCalledOnce()
  })

  it('re-reads when the reply carries no user', async () => {
    const auth = await bootAs({
      user: { id: 'u1', username: 'alice', isAdmin: false },
    })
    const adopt = auth().openUserAdoption()
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: {
          id: 'u1',
          username: 'alice',
          displayName: 'Read',
          isAdmin: false,
        },
      },
    })

    await adopt(undefined)

    expect(mockGetCurrentUser).toHaveBeenCalledOnce()
    expect(auth().user()?.displayName).toBe('Read')
  })

  // The /verify-email reply carries the verification status beside the user,
  // and the adoption seeds the resend cooldown from it.
  it('adopts the cooldown a same-identity reply carries', async () => {
    const auth = await bootAs({
      user: { id: 'u1', username: 'alice', isAdmin: false },
    })
    const adopt = auth().openUserAdoption()
    const resend = timestampFromDate(new Date(Date.now() + 60 * 1000))

    await adopt(
      { id: 'u1', username: 'alice', emailVerified: true } as unknown as User,
      { nextResendAvailableAt: resend } as unknown as EmailVerificationStatus,
    )

    expect(auth().user()?.emailVerified).toBe(true)
    expect(auth().verificationResendAvailableAt()).toBe(resend)
    expect(mockGetCurrentUser).not.toHaveBeenCalled()
  })

  // Nobody was signed in when the mutation left, so no identity can match the
  // reply. The adoption reads the account instead, and the reply's cooldown
  // goes with the rest of the reply.
  it('re-reads when it opened with nobody signed in', async () => {
    const auth = await bootAs({ user: undefined })
    const adopt = auth().openUserAdoption()
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: {
          id: 'u1',
          username: 'alice',
          displayName: 'Read',
          isAdmin: false,
        },
      },
    })

    await adopt(
      { id: 'u1', username: 'alice', displayName: 'Reply' } as unknown as User,
      {
        nextResendAvailableAt: timestampFromDate(new Date(Date.now() + 60 * 1000)),
      } as unknown as EmailVerificationStatus,
    )

    expect(mockGetCurrentUser).toHaveBeenCalledOnce()
    expect(auth().user()?.displayName).toBe('Read')
    expect(auth().verificationResendAvailableAt()).toBeUndefined()
  })
})

describe('AuthContext storage namespace', () => {
  beforeEach(() => {
    resetStorageAccountForTests()
  })

  afterEach(() => {
    // The connection is dropped BEFORE the factory is unstubbed, so a cached
    // handle into a discarded universe never outlives the test that made it.
    resetBrowserStorageForTests()
    vi.unstubAllGlobals()
    resetStorageAccountForTests()
    setStorageAccountForTests(TEST_USER_ID)
  })

  /**
   * Put `prefs` on disk under `userId`, then forget everything in memory.
   *
   * What is left is the state a fresh page load starts from: rows in the
   * database, an empty mirror, and no account. Only `hydrateStorageAccount` can
   * bridge the two, which is the point of the tests that use this.
   */
  async function seedStoredPrefs(userId: string, prefs: Record<string, unknown>): Promise<void> {
    vi.stubGlobal('indexedDB', new IDBFactory())
    resetBrowserStorageForTests()
    setStorageAccountForTests(userId)
    localStorageSet(KEY_BROWSER_PREFS, prefs)
    await flushStorageWrites()
    resetBrowserStorageForTests()
    resetStorageAccountForTests()
  }

  it('points storage at the account before anything can render for it', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
      },
    })

    // Read the namespace from a component that REQUIRES the identity -- the shape
    // `AuthGuard` has. `Show` is a render effect, so this body runs ahead of
    // every user effect in the flush that publishes the identity: it is the
    // earliest moment any consumer exists, and the one an effect-based
    // `setStorageAccount` would lose to.
    let seenWhileRendering: string | null | undefined
    let captured!: AuthState
    function Guarded() {
      seenWhileRendering = storedKeyFor(KEY_BROWSER_PREFS)
      return null
    }
    function Gate() {
      const auth = useAuth()
      captured = auth
      return <Show when={auth.isAuthenticated()}><Guarded /></Show>
    }
    render(() => <AuthProvider><Gate /></AuthProvider>)

    // `loading` goes false after the flush that publishes the identity, and
    // that flush renders `Guarded`.
    await untilTrue(() => !captured.loading())
    expect(seenWhileRendering).toBe('leapmux:u:alice:browser-prefs')
  })

  // The namespace alone is not the point of moving it early: the VALUES have to
  // be there too. `setStorageAccount` refuses an account the mirror was not
  // hydrated for, so a stored preference is readable at the same first render
  // the key for it is.
  it('serves a stored preference from the first render that can ask for one', async () => {
    await seedStoredPrefs('alice', { diffView: 'split' })
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
      },
    })

    let seenWhileRendering: unknown
    let captured!: AuthState
    function Guarded() {
      seenWhileRendering = loadBrowserPrefs().diffView
      return null
    }
    function Gate() {
      const auth = useAuth()
      captured = auth
      return <Show when={auth.isAuthenticated()}><Guarded /></Show>
    }
    render(() => <AuthProvider><Gate /></AuthProvider>)

    // The hydration reads fake-indexeddb, which completes its requests on
    // macrotasks. So wait for the end of the bootstrap, not for a poll window.
    await untilTrue(() => !captured.loading())
    expect(seenWhileRendering).toBe('split')
  })

  // THE MOST IMPORTANT ONE. Hydration sits on the sign-in path, so a browser
  // whose storage cannot be opened at all -- a corrupt profile, a private
  // window that refuses IndexedDB, a quota wall -- must lose its stored
  // preferences and NOTHING ELSE. Reporting it as a failed bootstrap would lock
  // such a user out of an app whose session is a cookie.
  it('signs the user in when the storage database cannot be opened', async () => {
    vi.stubGlobal('indexedDB', {
      open: () => {
        throw new Error('the profile is corrupt')
      },
    })
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
      },
    })

    const { auth } = renderWithAuthCapture()

    await untilTrue(() => !auth().loading())
    expect(auth().isAuthenticated()).toBe(true)
    expect(auth().bootstrapError()).toBeNull()
    // Signed in, on defaults: the mirror is empty, which reads as "nothing
    // stored" everywhere.
    expect(storedKeyFor(KEY_BROWSER_PREFS)).toBe('leapmux:u:alice:browser-prefs')
    expect(loadBrowserPrefs()).toEqual({})
  })

  // Two identities landing in one turn. The first one's rows are still loading
  // when the second starts, and publishing it afterwards would point the
  // namespace at an account the mirror no longer holds -- which
  // `setStorageAccount` refuses, loudly, in the middle of a sign-in.
  it('does not publish an identity a newer one superseded mid-hydration', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await untilTrue(() => !auth().loading())
    expect(screen.getByTestId('username')).toHaveTextContent('alice')

    const published: Array<string | undefined> = []
    const stop = createRoot((dispose) => {
      createEffect(() => published.push(auth().user()?.id))
      return dispose
    })

    // Both calls run their synchronous half before either hydration resolves,
    // so the second one owns the identity from that moment on.
    const first = auth().setAuth(reportFor({ id: 'bob', username: 'bob' }))
    const second = auth().setAuth(reportFor({ id: 'carol', username: 'carol' }))
    await Promise.all([first, second])
    await vi.waitFor(() => expect(auth().user()?.id).toBe('carol'))

    expect(published).not.toContain('bob')
    expect(storedKeyFor(KEY_BROWSER_PREFS)).toBe('leapmux:u:carol:browser-prefs')
    stop()
  })

  // The identity is not the only thing a stale adopt would publish. `setUser`
  // returns early when a newer one superseded it mid-hydration, but its CALLERS
  // continue -- and `adoptCurrentUser` then mirrors the dead response's elevation
  // window onto whatever session is live now. That window is what `/elevate`
  // reads to decide whether to prompt, so a signed-out visitor is redirected
  // away and the hub's consent gate answers with a page that has no way forward.
  // The guard in `readCurrentUser` runs BEFORE the await and cannot see this.
  it('does not apply a superseded response\'s elevation window', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await untilTrue(() => !auth().loading())
    expect(screen.getByTestId('username')).toHaveTextContent('alice')
    expect(auth().elevationExpiresAt()).toBeUndefined()

    // A refresh carrying an elevation, superseded mid-flight by a sign-out.
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
        elevationExpiresAt: { seconds: 4_102_444_800n, nanos: 0 },
      },
    })
    // `logout` runs `clearAuthUser` in its `finally`, which is the ordinary
    // production path into the null branch of `setUser`.
    const stale = auth().refreshUser()
    await auth().logout()
    await stale

    expect(auth().isAuthenticated()).toBe(false)
    expect(auth().elevationExpiresAt()).toBeUndefined()
  })

  // Signing out tears down the authenticated tree, and the writes that teardown
  // makes -- a draft flush, a layout snapshot -- belong to the account that
  // LEAVES. Clearing the namespace would make every one of them throw.
  it('keeps the namespace after a sign-out', async () => {
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: 'alice', username: 'alice', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()
    await untilTrue(() => !auth().loading())
    expect(screen.getByTestId('username')).toHaveTextContent('alice')

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
    mockGetCurrentUser.mockResolvedValue({
      sessionReport: {
        user: { id: '', username: 'nobody', isAdmin: false },
      },
    })
    const { auth } = renderWithAuthCapture()

    await untilTrue(() => !auth().loading())
    expect(auth().isAuthenticated()).toBe(false)
    expect(auth().bootstrapError()).toBeNull()
    expect(error).toHaveBeenCalled()
    error.mockRestore()
  })
})
