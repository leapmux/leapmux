import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { ParentComponent } from 'solid-js'
import type { EmailVerificationStatus, GetCurrentUserResponse, User } from '~/generated/leapmux/v1/auth_pb'
import type { CaptchaRequestFields } from '~/lib/captchaForm'
import { create } from '@bufbuild/protobuf'
import { Code, ConnectError } from '@connectrpc/connect'
import { createEffect, createSignal, on, onMount, useContext } from 'solid-js'
import { authClient } from '~/api/clients'
import { platformBridge } from '~/api/platformBridge'
import { loadTimeouts, setOnAuthError } from '~/api/transport'
import { channelManager } from '~/api/workerRpc'
import { LoginRequestSchema } from '~/generated/leapmux/v1/auth_pb'
import { setStorageAccount } from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'
import { isSoloMode, loadSystemInfo } from '~/lib/systemInfo'
import { passkeyErrorMessage, startAuthentication } from '~/lib/webauthn'

const log = createLogger('auth')

export interface AuthLoginResult {
  verificationRequired: boolean
  verificationEmailSent: boolean
  nextResendAvailableAt?: Timestamp
}

export interface AuthState {
  user: () => User | null
  loading: () => boolean
  error: () => string | null
  /** Cooldown timestamp seeded from login when verification is required. */
  verificationResendAvailableAt: () => Timestamp | undefined
  /**
   * When the session's step-up elevation lapses; undefined when the session
   * is not elevated.
   *
   * A timestamp, never a boolean, and never consulted to decide whether an
   * action is ALLOWED: the hub decides that, and this value can be stale by
   * a page's lifetime. It exists so a surface can show the state and so
   * `/elevate` can leave immediately when it has nothing to ask for.
   */
  elevationExpiresAt: () => Timestamp | undefined
  /** Adopt the deadline a step-up ceremony returned. */
  setElevationExpiresAt: (until?: Timestamp) => void
  /**
   * Set when bootstrap failed for a reason OTHER than "no session" -- i.e.
   * either `loadSystemInfo` could not reach the hub, or the session restore
   * failed with something other than Unauthenticated. Distinct from `error`,
   * which LoginPage renders as a *login* failure.
   *
   * Without this, a transport failure is indistinguishable from an expired
   * cookie: in solo mode (where there is no login to fall back to) the guard
   * would show its loading fallback forever. And a failed `loadSystemInfo`
   * leaves `isSoloMode()` at its fabricated `false`, so a solo user whose
   * session restore then SUCCEEDS is shown a "Log out" button that strands
   * them on a login form no credentials can satisfy. Both are unrecoverable
   * and silent; recording the failure here makes the guard show a retry
   * panel instead.
   */
  bootstrapError: () => string | null
  /** Retry the bootstrap session-restore after a `bootstrapError`. */
  retryBootstrap: () => Promise<void>
  login: (username: string, password: string, captcha?: CaptchaRequestFields) => Promise<AuthLoginResult>
  loginWithPasskey: (username: string, captcha?: CaptchaRequestFields) => Promise<AuthLoginResult>
  setVerificationResendAvailableAt: (at?: Timestamp) => void
  logout: () => Promise<void>
  setAuth: (user: User) => void
  refreshUser: () => Promise<void>
  isAuthenticated: () => boolean
}

const AuthContext = createStableContext<AuthState>('context/AuthContext')

export const AuthProvider: ParentComponent = (props) => {
  const [user, setUserSignal] = createSignal<User | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)
  const [bootstrapError, setBootstrapError] = createSignal<string | null>(null)
  const [verificationResendAvailableAt, setVerificationResendAvailableAt] = createSignal<Timestamp | undefined>()
  const [elevationExpiresAt, setElevationExpiresAtSignal] = createSignal<Timestamp | undefined>()
  // A setter, not the raw signal: Solid's setter treats a function argument
  // as an updater, and a Timestamp is not one -- but keeping the wrapper
  // means the context never exposes that distinction to a caller.
  const setElevationExpiresAt = (until?: Timestamp) => setElevationExpiresAtSignal(until)

  /**
   * The one writer of the identity, and the one place the storage namespace
   * moves with it.
   *
   * `setStorageAccount` runs BEFORE the signal notifies, so nothing downstream
   * can observe the new identity while storage still points at the old account.
   * An effect would be too late: `AuthGuard`'s `Show` is a render effect, and a
   * render effect runs ahead of a user effect in the same flush, so `AppShell`
   * could mount and read the previous account's tab state.
   *
   * A null user does NOT clear the namespace -- see `setStorageAccount`. The
   * writes a sign-out triggers on the way down belong to the account that is
   * leaving.
   *
   * AN IDENTITY WITH NO ID IS NOT AN IDENTITY. `setStorageAccount` refuses an
   * empty id, because there is no namespace to key by, and letting that throw
   * escape would be the worst of the three outcomes: `restoreSession` catches
   * it inside its network `try` and reports it as `bootstrapError`, so the user
   * gets "Could not reach the hub." over a client-side data fault, with a Retry
   * that hits the identical throw every time. Signed-out is the honest reading
   * of a User the app cannot key anything by, and it routes to `/login` -- with
   * the fault recorded, because a hub that answers this way has a real bug.
   */
  const setUser = (next: User | null) => {
    if (next && !next.id) {
      log.error('the hub returned a user with no id; treating the session as signed out', { user: next })
      next = null
    }
    if (next)
      setStorageAccount(next.id)
    setUserSignal(next)
  }

  /**
   * Drop every pooled E2EE channel on an identity change.
   *
   * Channels are pooled per worker for up to an hour and carry the identity the Hub
   * authenticated them as at OPEN time, and `channelManager` is a module-level
   * singleton that outlives this provider. So without this, logging out and back in as
   * someone else keeps serving the previous user's channels: every worker RPC the new
   * user's page issues runs on the worker AS THE OLD USER, for the rest of the
   * channels' hour. getOrOpenChannel also re-checks the identity before reusing a
   * pooled channel; that is the backstop, this is the eager release -- it also frees
   * the shared WebSocket, which the logged-out user has no business holding open.
   *
   * On the desktop this also resets the SIDECAR's tunnels. In distributed mode the Go
   * sidecar pools its own E2EE channels and runs tunnel listeners bound to the
   * connection (not the browser session), and it authenticates to the Hub purely by
   * the proxy's cookie jar -- it has no user identity to key an eager close on. So
   * without resetTunnels, a user switch would leave the previous user's tunnels
   * relaying (and their cached channels reusable) under the old identity until the Hub
   * revokes the session. resetTunnels is a no-op off the desktop and best-effort: a
   * failure must not break the logout flow, but it must not pass silently either.
   */
  const closePooledChannels = () => {
    channelManager.closeAll()
    platformBridge.resetTunnels().catch((err) => {
      log.warn('failed to reset sidecar tunnels on identity change', { error: String(err) })
    })
  }

  // Drive the eager release off the `user` signal itself, so EVERY identity
  // transition -- logout, an auth error, a login over a live session, and any future
  // setUser path (setAuth, refreshUser, an impersonation seed) -- releases by
  // construction rather than each transition site remembering to call it. Fires only
  // when a PREVIOUS authenticated user gives way to a different id or to logout: the
  // initial null -> first-user restore (prev falsy) and a same-user refresh
  // (prev.id === next id) must not churn the pool or fire a spurious resetTunnels.
  createEffect(on(user, (u, prev) => {
    if (prev && prev.id !== (u?.id ?? ''))
      closePooledChannels()
  }, { defer: true }))

  const clearAuthUser = () => {
    setUser(null)
    setVerificationResendAvailableAt(undefined)
    // The elevation belongs to the session that just ended. Leaving it would
    // make /elevate bounce a signed-out visitor straight back out.
    setElevationExpiresAt(undefined)
  }

  /**
   * Adopt a `GetCurrentUser` response.
   *
   * ONE site, because the response carries three signals and two callers read
   * it: `restoreSession` and `refreshUser`. They adopted different subsets by
   * hand, so a refresh dropped the verification cooldown and left
   * `/verify-email` counting from zero against a hub that had just minted a
   * code. A fourth signal added to the response now lands in both paths.
   */
  const adoptCurrentUser = (resp: GetCurrentUserResponse) => {
    setUser(resp.user ?? null)
    setElevationExpiresAt(resp.elevationExpiresAt)
    // The only response the /verify-email page's own bootstrap reads. Seeding
    // the cooldown from it is what lets a hard reload of that page resume the
    // countdown instead of restarting at zero and letting the button hammer a
    // ResourceExhausted refusal.
    setVerificationResendAvailableAt(resp.emailVerification?.nextResendAvailableAt)
  }

  /**
   * Adopt an identity that a sign-in or a sign-up just established.
   *
   * The elevation is cleared, and that is the point: signing in over a
   * still-authenticated session (a bookmarked `/login`, a stale tab) is an
   * identity transition just like logout, so the previous user's deadline
   * must not survive it. Left behind, `/elevate` reads a live window that
   * belongs to nobody in this document, redirects without prompting, and the
   * hub's consent gate then answers with a page that has no way forward.
   */
  const adoptSignedInUser = (u: User | null) => {
    setUser(u)
    setElevationExpiresAt(undefined)
  }

  // Register auth error callback for auto-logout on 401.
  setOnAuthError(() => {
    if (!isSoloMode())
      clearAuthUser()
  })

  /**
   * Restore the session from the cookie (both solo and multi-user modes).
   *
   * Unauthenticated is the ordinary "no session yet" answer and stays silent —
   * `AuthGuard` routes those to `/login`, and on a fresh install `SetupGate`
   * answers first with `/setup`.
   * Any OTHER failure means the hub is unreachable or erroring, which no
   * amount of logging in fixes, so it is recorded and surfaced rather than
   * swallowed. The two were previously indistinguishable; see
   * `AuthState.bootstrapError`.
   */
  const restoreSession = async () => {
    try {
      adoptCurrentUser(await authClient.getCurrentUser({}))
      loadTimeouts().catch(() => {})
    }
    catch (err) {
      if (err instanceof ConnectError && err.code === Code.Unauthenticated) {
        clearAuthUser()
      }
      else {
        const msg = formatErrorMessage(err)
        log.warn('session restore failed', { error: msg })
        setBootstrapError(msg)
      }
    }
  }

  /**
   * The whole bootstrap sequence, and the sole owner of `bootstrapError`.
   *
   * System info comes FIRST and is a hard prerequisite: every downstream
   * decision (solo vs. multi-user, setup-required) reads module getters that
   * are fabricated defaults until it succeeds. Continuing to the session
   * restore on a failed load is how a solo user ends up looking like a
   * multi-user one -- the restore succeeds (a solo hub authenticates every
   * procedure), nothing is recorded, and the app offers a "Log out" that
   * strands them at /login. So a failed load aborts here with a
   * `bootstrapError` for the guard's retry panel to render.
   */
  const bootstrap = async () => {
    setBootstrapError(null)
    try {
      await loadSystemInfo()
    }
    catch (err) {
      const msg = formatErrorMessage(err)
      log.warn('system info load failed', { error: msg })
      setBootstrapError(msg)
      return
    }
    await restoreSession()
  }

  onMount(async () => {
    await bootstrap()
    setLoading(false)
  })

  const retryBootstrap = async () => {
    setLoading(true)
    await bootstrap()
    setLoading(false)
  }

  const loginResultFromResponse = (status: EmailVerificationStatus | undefined): AuthLoginResult => ({
    verificationRequired: status?.verificationRequired ?? false,
    verificationEmailSent: status?.verificationEmailSent ?? false,
    nextResendAvailableAt: status?.nextResendAvailableAt,
  })

  /**
   * The state machine both sign-in paths share: clear the banner, hold
   * `loading` for the whole attempt, adopt the returned user, and report
   * the verification status.
   *
   * `login` and `loginWithPasskey` were byte-identical apart from the RPC
   * in the middle and the fallback message, so a fix to the identity
   * transition below had to be made twice. `fallback` is passed through
   * `passkeyErrorMessage`, which returns null for a dismissed passkey
   * prompt -- that is not a failure to report, so the banner stays empty.
   */
  const runSignIn = async (
    fallback: string,
    attempt: () => Promise<{ user?: User, emailVerification?: EmailVerificationStatus }>,
  ): Promise<AuthLoginResult> => {
    setError(null)
    setLoading(true)
    try {
      const resp = await attempt()
      // Through adoptSignedInUser: setUser drives the eager release of the
      // previous user's pooled channels through the createEffect above,
      // rather than leaving them to the lazy per-request identity check
      // (which evicts one channel per request while the shared WebSocket
      // stays held for the old user) -- and it clears the elevation the
      // previous identity held.
      adoptSignedInUser(resp.user ?? null)
      loadTimeouts().catch(() => {})
      return loginResultFromResponse(resp.emailVerification)
    }
    catch (e) {
      setError(passkeyErrorMessage(e, fallback) ?? '')
      // The login form's captcha.reset() (see createCaptchaForm) triggers
      // the system-info reload that converges a stale captcha snapshot
      // after a runtime enable/disable or provider switch.
      throw e
    }
    finally {
      setLoading(false)
    }
  }

  const login = (username: string, password: string, captcha?: CaptchaRequestFields): Promise<AuthLoginResult> =>
    runSignIn('Login failed', () => authClient.login(create(LoginRequestSchema, {
      username,
      password,
      // The honeypot rides on every attempt; the payload is empty until
      // the widget verifies (the hub ignores it while captcha is
      // disabled — see createCaptchaForm's requirement gate).
      captchaPayload: captcha?.captchaPayload ?? '',
      honeypot: captcha?.honeypot ?? '',
    })))

  const loginWithPasskey = (username: string, captcha?: CaptchaRequestFields): Promise<AuthLoginResult> =>
    runSignIn('Passkey login failed', async () => {
      const begin = await authClient.beginPasskeyLogin({
        username,
        captchaPayload: captcha?.captchaPayload ?? '',
        honeypot: captcha?.honeypot ?? '',
      })
      return await authClient.finishPasskeyLogin({
        sessionId: begin.sessionId,
        credentialJson: await startAuthentication(begin.optionsJson),
      })
    })

  const logout = async () => {
    if (isSoloMode())
      return
    try {
      await authClient.logout({})
    }
    catch {
      // Ignore logout errors.
    }
    finally {
      clearAuthUser()
    }
  }

  const setAuth = (u: User) => {
    // The same identity transition runSignIn makes: /oauth/complete-signup
    // and the verify-email page both land a NEW user in a document that may
    // still hold the previous one's elevation.
    adoptSignedInUser(u)
    setLoading(false)
  }

  const refreshUser = async () => {
    try {
      adoptCurrentUser(await authClient.getCurrentUser({}))
    }
    catch {
      // Ignore — user state unchanged.
    }
  }

  const state: AuthState = {
    user,
    loading,
    error,
    verificationResendAvailableAt,
    elevationExpiresAt,
    setElevationExpiresAt,
    login,
    loginWithPasskey,
    setVerificationResendAvailableAt,
    logout,
    setAuth,
    refreshUser,
    bootstrapError,
    retryBootstrap,
    isAuthenticated: () => user() !== null,
  }

  return (
    <AuthContext.Provider value={state}>
      {props.children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider')
  }
  return ctx
}
