import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { ParentComponent } from 'solid-js'
import type { EmailVerificationStatus, GetCurrentUserResponse, User } from '~/generated/proto/leapmux/v1/auth_pb'
import type { CaptchaRequestFields } from '~/lib/captchaForm'
import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError } from '@connectrpc/connect'
import { createEffect, createSignal, on, onMount, useContext } from 'solid-js'
import { authClient } from '~/api/clients'
import { platformBridge } from '~/api/platformBridge'
import { loadTimeouts, setElevationAdoptionOpener, setOnAuthError } from '~/api/transport'
import { channelManager } from '~/api/workerRpc'
import { LoginRequestSchema } from '~/generated/proto/leapmux/v1/auth_pb'
import { setBootPhase, setBootShell } from '~/lib/bootSplashTheme'
import { setStorageAccount } from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { dropElevation as dropElevationRequest, elevateWithPasskey as elevateWithPasskeyRequest, elevateWithPassword as elevateWithPasswordRequest } from '~/lib/elevation'
import { formatErrorMessage } from '~/lib/errors'
import { createLogger } from '~/lib/logger'
import { isAutoAuthenticated, isPasswordSetupGate, loadSystemInfo } from '~/lib/systemInfo'
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
   * A timestamp, never a boolean, and no caller consults it to decide whether
   * an action is ALLOWED: the hub decides that, and this value can be stale by
   * a page's lifetime. It exists so a surface can show the state and so
   * `/elevate` can leave immediately when it has nothing to ask for.
   *
   * Three things write it, and the hub owns each one: a step-up ceremony
   * reports the window it granted, `GetCurrentUser` reports the window the
   * session holds, and the hub reports a SLID deadline on the response to the
   * request that slid it. The third reaches THIS document alone, so a tab that
   * performed no sensitive action still holds whatever it last read.
   */
  elevationExpiresAt: () => Timestamp | undefined
  /**
   * Prove a password, and adopt the window the hub grants.
   *
   * The ceremony sits HERE, and the mirror has no public setter, because the
   * hub owns the window and this signal only reflects it. A setter on the
   * context invited a surface to write the mirror from an answer it read
   * itself, which is a second source of truth for a security decision -- and
   * three surfaces already did.
   *
   * It REJECTS with the hub's own error, so the caller still renders "wrong
   * password" and the rate-limit refusal verbatim.
   */
  elevateWithPassword: (currentPassword: string) => Promise<void>
  /** Prove a passkey, and adopt the window the hub grants. */
  elevateWithPasskey: () => Promise<void>
  /** End the window now, and clear the mirror. */
  dropElevation: () => Promise<void>
  /**
   * Set when bootstrap failed for a reason OTHER than "no session" -- i.e.
   * either `loadSystemInfo` could not reach the hub, or the session restore
   * failed with something other than Unauthenticated. Distinct from `error`,
   * which LoginPage renders as a *login* failure.
   *
   * Without this, a transport failure is indistinguishable from an expired
   * cookie: in solo mode (where there is no login to fall back to) the guard
   * would show its loading fallback forever. And a failed `loadSystemInfo`
   * leaves `isAutoAuthenticated()` at its fabricated `false`, so the app shows a
   * "Log out" button to a solo user whose session restore then SUCCEEDS, and
   * that button strands them on a login form no credentials can satisfy. Both
   * are unrecoverable
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
  /**
   * Adopt a user the hub returned for the SESSION THAT IS ALREADY SIGNED IN.
   *
   * `setAuth` is the identity TRANSITION and clears the elevation with it.
   * This one keeps the elevation, because the hub changed a field of the same
   * account and touched no elevation column. `/verify-email` is the caller:
   * its response carries the updated user, and clearing the window there made
   * Preferences report a verified session as unverified.
   */
  adoptSameIdentityUser: (user: User) => void
  refreshUser: () => Promise<void>
  isAuthenticated: () => boolean
}

const AuthContext = createStableContext<AuthState>('context/AuthContext')

export const AuthProvider: ParentComponent = (props) => {
  const [user, setUserSignal] = createSignal<User | null>(null)
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)
  const [bootstrapError, setBootstrapError] = createSignal<string | null>(null)
  const [verificationResendAvailableAt, setVerificationResendAvailableAtSignal] = createSignal<Timestamp | undefined>()
  const [elevationExpiresAt, setElevationExpiresAtSignal] = createSignal<Timestamp | undefined>()

  /**
   * Counts the authoritative writes of the state a `GetCurrentUser` response
   * carries.
   *
   * A read of the account is not instant, and three surfaces start one without
   * awaiting it. So a response can land AFTER a newer answer already wrote the
   * same signals, and adopting it puts the older account state back. The
   * deadline is the damaging one: elevate, and a `GetCurrentUser` that left
   * before the ceremony reverts the window a moment later. Preferences then
   * reports an elevated session as unelevated, and the next sensitive action
   * prompts for a window the user already holds.
   *
   * `readCurrentUser` records the count before it sends the request and
   * discards the response when the count moved. Every local write below moves
   * it, so this covers a concurrent read AND a ceremony, for all four signals
   * the response carries rather than for the deadline alone.
   */
  let authGeneration = 0

  /**
   * Counts the writes that leave NO window mirrored: the "End now" drop, a
   * sign-out, an identity change, and an account read that reports the session
   * unelevated.
   *
   * A slide report travels on the response to the request that caused it, so it
   * describes the window as it stood when the hub served that request. Each
   * write above ENDS the window, and a report that left before one of them can
   * land after it. The drop is the damaging case: "End now" is the control the
   * documentation points at for a shared machine, and a report that outlived it
   * would put "verified until 15:55" back on that row for the rest of the
   * window, with nothing to correct it until the stale deadline passed.
   *
   * `openElevationAdoption` reads this count when a request leaves, and
   * discards the report when the count moved. A separate count rather than
   * `authGeneration`, because that one moves on EVERY write -- a refresh of
   * the account, or a second sliding request -- and discarding a live deadline
   * for those would put the early value back.
   */
  let elevationEndCount = 0

  /**
   * Writes the mirrored deadline, and marks every in-flight read stale.
   *
   * A setter, not the raw signal: Solid's setter treats a function argument as
   * an updater, and a Timestamp is not one -- but keeping the wrapper means the
   * context never exposes that distinction to a caller.
   */
  const setElevationExpiresAt = (until?: Timestamp) => {
    authGeneration++
    if (!until)
      elevationEndCount++
    setElevationExpiresAtSignal(until)
  }

  /**
   * Opens one adoption of a deadline the hub reports on a slide.
   *
   * The transport calls this when a request LEAVES and calls what it returns
   * when that request's response lands. Between the two the window may end --
   * see `elevationEndCount` -- and a report from before that ending must lose.
   *
   * Adopting through `setElevationExpiresAt` is what settles the OTHER race:
   * the write moves `authGeneration`, so a `GetCurrentUser` that left before
   * the slide cannot put its older answer back on top of it.
   *
   * The instant loses sub-millisecond precision, which costs nothing: every
   * reader compares it through `timestampDate(...).getTime()`, which is
   * milliseconds already.
   */
  const openElevationAdoption = () => {
    const openedAt = elevationEndCount
    return (until: Date) => {
      if (openedAt !== elevationEndCount)
        return
      setElevationExpiresAt(timestampFromDate(until))
    }
  }
  setElevationAdoptionOpener(openElevationAdoption)

  /** Writes the mirrored cooldown, and marks every in-flight read stale. */
  const setVerificationResendAvailableAt = (at?: Timestamp) => {
    authGeneration++
    setVerificationResendAvailableAtSignal(at)
  }

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
   * writes a sign-out triggers during the tear-down belong to the account that
   * leaves.
   *
   * AN IDENTITY WITH NO ID IS NOT AN IDENTITY. `setStorageAccount` refuses an
   * empty id, because there is no namespace to key by, and letting that throw
   * escape would be the worst of the three outcomes: `restoreSession` catches
   * it inside its network `try` and reports it as `bootstrapError`, so the user
   * reads "Could not reach the hub." for a client-side data fault, with a Retry
   * that hits the identical throw every time. Signed-out is the honest reading
   * of a User the app cannot key anything by, and it routes to `/login` -- with
   * the fault recorded, because a hub that answers this way has a real bug.
   */
  const setUser = (next: User | null) => {
    authGeneration++
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
   * pooled channel; that is the fallback check, this is the eager release -- it also
   * frees the shared WebSocket, which the logged-out user must not hold open.
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
  // when a PREVIOUS authenticated user changes to a different id or to logout: the
  // initial null -> first-user restore (prev falsy) and a same-user refresh
  // (prev.id === next id) must not close and reopen the pool, and must not fire a
  // spurious resetTunnels.
  createEffect(on(user, (u, prev) => {
    if (prev && prev.id !== (u?.id ?? ''))
      closePooledChannels()
  }, { defer: true }))

  const clearAuthUser = () => {
    setUser(null)
    setVerificationResendAvailableAt(undefined)
    // The elevation belongs to the session that just ended. Leaving it would
    // make /elevate redirect a signed-out visitor away immediately.
    setElevationExpiresAt(undefined)
    // Shell checklist rows must not linger on login after a sign-out.
    setBootShell(false)
    // Reset the phase so any BootSplash during the next bootstrap does not
    // show a finished checklist left over from the previous shell session.
    setBootPhase('initializing')
  }

  /**
   * Adopt a `GetCurrentUser` response.
   *
   * ONE site, because the response carries three signals and two paths read
   * it: the bootstrap restore and `refreshUser`. They adopted different
   * subsets by hand, so a refresh dropped the verification cooldown and left
   * `/verify-email` counting from zero against a hub that just minted a
   * code. A fourth signal added to the response now lands in both paths.
   *
   * `readCurrentUser` is the only caller, and it decides whether the response
   * is still the newest before it gets here.
   */
  const adoptCurrentUser = (resp: GetCurrentUserResponse) => {
    setUser(resp.user ?? null)
    setElevationExpiresAt(resp.elevationExpiresAt)
    // The only response the /verify-email page's own bootstrap reads. Seeding
    // the cooldown from it is what lets a hard reload of that page resume the
    // countdown instead of restarting at zero and letting the button send
    // repeated requests that the hub refuses with ResourceExhausted.
    setVerificationResendAvailableAt(resp.emailVerification?.nextResendAvailableAt)
  }

  /**
   * Read the account, and adopt the answer only while it is still the newest.
   *
   * The ONE caller of GetCurrentUser, so the staleness check cannot be
   * forgotten at a new call site. See `authGeneration` for what a stale adopt
   * costs.
   */
  const readCurrentUser = async () => {
    const generation = authGeneration
    const resp = await authClient.getCurrentUser({})
    if (generation !== authGeneration)
      return
    adoptCurrentUser(resp)
  }

  /**
   * Adopt an identity that a sign-in or a sign-up just established.
   *
   * This function clears the elevation, and that is the point: signing in over
   * a still-authenticated session (a bookmarked `/login`, a stale tab) is an
   * identity transition just like logout, so the previous user's deadline
   * must not survive it. When it survives, `/elevate` reads a live window that
   * belongs to nobody in this document, redirects without prompting, and the
   * hub's consent gate then answers with a page that has no way forward.
   */
  const adoptSignedInUser = (u: User | null) => {
    setUser(u)
    setElevationExpiresAt(undefined)
    // Enter the shell checklist before AppShell mounts so login does not flash
    // the finished signed-out `ready` phase, then jump to `workspaces`.
    if (u) {
      setBootShell(true)
      setBootPhase('workspaces')
    }
  }

  /**
   * Adopt a user the hub returned for the session that is already signed in.
   *
   * The elevation SURVIVES, which is the whole difference from
   * `adoptSignedInUser`. The hub changed a field of the same account and
   * touched no elevation column, so clearing the mirror here reports a live
   * window as closed.
   */
  const adoptSameIdentityUser = (u: User) => {
    setUser(u)
  }

  /**
   * Adopt the deadline a step-up ceremony returned.
   *
   * A success with NO timestamp leaves the mirror alone. The hub grants a
   * window on every success, so an absent field is a response the client
   * cannot read rather than an ended window -- and clearing on it turns a
   * successful elevation into a session that reports itself unelevated. The
   * ceremony's caller learns the outcome from the resolved promise, not from
   * this signal.
   */
  const adoptElevation = (until?: Timestamp) => {
    if (until)
      setElevationExpiresAt(until)
  }

  const elevateWithPassword = async (currentPassword: string) => {
    adoptElevation(await elevateWithPasswordRequest(currentPassword))
  }

  const elevateWithPasskey = async () => {
    adoptElevation(await elevateWithPasskeyRequest())
  }

  /**
   * End the window now.
   *
   * This function clears the mirror locally rather than reads it again: the hub
   * just reported the window gone, and a drop is the one change that must never
   * render as still live.
   */
  const dropElevation = async () => {
    await dropElevationRequest()
    setElevationExpiresAt(undefined)
  }

  // Register auth error callback for auto-logout on 401.
  setOnAuthError(() => {
    // isAutoAuthenticated, not isSoloMode: the question is whether THIS
    // connection holds a session to lose. A solo hub reached at a network
    // address does -- it signed in -- so a 401 there must clear the user like
    // anywhere else, or the app keeps rendering a session the hub refused.
    if (!isAutoAuthenticated())
      clearAuthUser()
  })

  /**
   * Restore the session from the cookie (both solo and multi-user modes).
   *
   * Unauthenticated is the ordinary "no session yet" answer and stays silent —
   * `AuthGuard` routes those to `/login`, and on a fresh install `SetupGate`
   * answers first with `/setup`.
   * Any OTHER failure means the hub is unreachable or returns an error, which
   * no login fixes, so this function records it for the guard to show, rather
   * than discards it. The two were previously indistinguishable; see
   * `AuthState.bootstrapError`.
   */
  const restoreSession = async () => {
    try {
      await readCurrentUser()
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
   * restore on a failed load is how the app treats a solo user as a multi-user
   * one -- the restore succeeds (a solo hub authenticates every procedure), the
   * code records nothing, and the app offers a "Log out" that strands them at
   * /login. So a failed load aborts here with a
   * `bootstrapError` for the guard's retry panel to render.
   *
   * Each stage also advances the boot splash's checklist (data-boot-phase on
   * <html>): system info, then the session restore, then either `ready`
   * (signed-out / password-setup) or `workspaces` with the shell rows revealed
   * (authenticated AppShell). AppShell advances `tabs` → `ready`. A failure
   * leaves the checklist on the stage that failed, which is the truth.
   */
  const bootstrap = async () => {
    setBootstrapError(null)
    setBootShell(false)
    setBootPhase('system-info')
    try {
      await loadSystemInfo()
    }
    catch (err) {
      const msg = formatErrorMessage(err)
      log.warn('system info load failed', { error: msg })
      setBootstrapError(msg)
      return
    }
    setBootPhase('session')
    await restoreSession()
    if (bootstrapError())
      return
    // The restricted TCP setup state is not a session and not the shell, so it
    // must not reveal the workspace rows.
    if (user() && !isPasswordSetupGate()) {
      setBootShell(true)
      setBootPhase('workspaces')
      return
    }
    setBootPhase('ready')
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
   * transition below needed two edits. `runSignIn` passes `fallback` through
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
      // The form sends the honeypot on every attempt; the payload is empty until
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
    // A credential-free connection has no session to end; every other one,
    // solo or not, signs out normally.
    if (isAutoAuthenticated())
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
    // The same identity transition runSignIn makes: /auth/idp/complete-signup
    // and the verify-email page both land a NEW user in a document that may
    // still hold the previous one's elevation.
    adoptSignedInUser(u)
    setLoading(false)
  }

  const refreshUser = async () => {
    try {
      await readCurrentUser()
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
    elevateWithPassword,
    elevateWithPasskey,
    dropElevation,
    login,
    loginWithPasskey,
    setVerificationResendAvailableAt,
    logout,
    setAuth,
    adoptSameIdentityUser,
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
