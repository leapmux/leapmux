import type { Timestamp } from '@bufbuild/protobuf/wkt'
import type { Location } from '@solidjs/router'
import type { ParentComponent } from 'solid-js'
import type { EmailVerificationStatus, GetCurrentUserResponse, SessionReport, User } from '~/generated/proto/leapmux/v1/auth_pb'
import type { StorageAccount } from '~/lib/browserStorage'
import type { CaptchaRequestFields } from '~/lib/captchaForm'
import { create } from '@bufbuild/protobuf'
import { timestampFromDate } from '@bufbuild/protobuf/wkt'
import { Code, ConnectError } from '@connectrpc/connect'
import { createMemo, createSignal, onCleanup, onMount, untrack, useContext } from 'solid-js'
import { authClient } from '~/api/clients'
import { platformBridge } from '~/api/platformBridge'
import { advanceAuthSessionEpoch, loadTimeouts, setElevationAdoptionOpener, setOnAuthError } from '~/api/transport'
import { channelManager } from '~/api/workerRpc'
import { LoginRequestSchema } from '~/generated/proto/leapmux/v1/auth_pb'
import { setBootPhase, setBootShell } from '~/lib/bootSplashTheme'
import { prepareStorageAccount, setStorageAccount, storageAccountFor } from '~/lib/browserStorage'
import { createStableContext } from '~/lib/createStableContext'
import { dropElevation as dropElevationRequest, elevateWithPasskey as elevateWithPasskeyRequest, elevateWithPassword as elevateWithPasswordRequest } from '~/lib/elevation'
import { formatErrorMessage } from '~/lib/errors'
import { hubOrigin } from '~/lib/hubSite'
import { createLogger } from '~/lib/logger'
import { isAutoAuthenticated, isPasswordSetupGate, loadSystemInfo } from '~/lib/systemInfo'
import { authenticateForChallenge, passkeyErrorMessage } from '~/lib/webauthn'

const log = createLogger('auth')

/**
 * What a successful sign-in RPC led to.
 *
 * - `adopted`: the reply carried the account, and this document adopted it.
 *   The caller navigates as it always did.
 * - `recovering`: the sign-in succeeded but the reply carried no account.
 *   Sign-in recovery reads it, and recovery owns the navigation that follows,
 *   even when its first read succeeds at once. The caller does nothing.
 * - `superseded`: a newer auth operation owns the state. The caller does
 *   nothing.
 */
export type SignInOutcome = 'adopted' | 'recovering' | 'superseded'

type SignInRecoveryRedirect = string | ((location: Location) => string | null | undefined)

/** Where a sign-in was asked to continue, for the navigation recovery owns. */
export interface SignInOptions {
  /**
   * The address to continue to, such as the CLI consent page. A resolver reads
   * the router's location after recovery adopts the account. It returns null
   * to stay on the current page or undefined to use the default destination.
   */
  redirect?: SignInRecoveryRedirect | undefined
}

/**
 * Where the account read of a sign-in recovery stands.
 *
 * - `reading`: the read runs.
 * - `failed`: the read failed, and `message` says why. The gate offers Retry.
 * - `completed`: the read succeeded and the account is adopted. The gate now
 *   navigates to the destination and calls `finishSignInRecovery`.
 */
export type SignInRecoveryRead
  = | { kind: 'reading' }
    | { kind: 'failed', message: string }
    | { kind: 'completed' }

/**
 * A sign-in that succeeded but could not report its account. `SignInRecoveryGate`
 * replaces every route while this is set.
 */
export interface SignInRecovery {
  /**
   * The account read. ONE field, so a read cannot be failed and completed at
   * once, and a failure whose message is empty still reads as a failure.
   */
  read: SignInRecoveryRead
  /** Whether email verification is required, once a response reports it. */
  verificationRequired?: boolean
  /** Where to continue after recovery adopts the account. */
  redirect?: SignInRecoveryRedirect
}

/**
 * The recovery error when the account read succeeded, but a local write moved
 * the account state while its rows loaded, so the read could not be adopted.
 */
const RECOVERY_ADOPTION_DISPLACED = 'Your account changed while it was loading. Retry to load it again.'

/**
 * What one account read led to.
 *
 * - `adopted`: this document adopted the account that the read returned.
 * - `superseded`: the caller's staleness rule discarded the response, or a
 *   newer write that the rule covers refused its adoption.
 * - `displaced`: `setUser` refused the account because a local write landed
 *   while its rows loaded, and the caller's rule still holds the read
 *   current. Nothing else adopts the account then.
 */
type AccountReadResult
  = | { outcome: 'adopted', verificationRequired: boolean | undefined }
    | { outcome: 'superseded' | 'displaced' }

export interface AuthLoginResult {
  outcome: SignInOutcome
  /**
   * The sign-in reported that the account must verify an email address. The
   * context already seeded the resend cooldown from the same report.
   */
  verificationRequired: boolean
}

/**
 * Adopts the user a mutation's response carried; see
 * `AuthState.openUserAdoption`.
 */
export type UserAdoption = (user: User | undefined, verification?: EmailVerificationStatus) => Promise<void>

/**
 * One adopted identity's LIFETIME, as the scope a worker trust decision
 * captures.
 *
 * Distinct from the `authGeneration` write fence, which moves on every local
 * write. This scope advances only when the document adopts a different
 * identity: a sign-out, a sign-in (including a sign-in as the same user, which
 * is a new lifetime), or an adopted user id that changed. A same-user refresh,
 * an elevation window and a verification cooldown leave it alone.
 *
 * A consumer captures one `WorkerTrustScope` before it acts and revalidates
 * `generation` (and `signal.aborted`) after every wait: a scope that retired
 * mid-operation cancelled its `signal` and must not have its decision
 * published into the successor's world.
 */
export interface WorkerTrustScope {
  /** The adopted user, or null while signed out. */
  userId: string | null
  /** Monotonic identity generation; one per lifetime. */
  generation: number
  /** Aborted when this scope retires. */
  signal: AbortSignal
}

export interface AuthState {
  user: () => User | null
  /** The hydrated browser-storage identity published with `user`. */
  storageAccount: () => StorageAccount | null
  /** Subscribe to an authenticated account ending before its shell unmounts. */
  onAccountEnd: (listener: (account: StorageAccount) => void) => () => void
  loading: () => boolean
  error: () => string | null
  /**
   * When the hub accepts another request for a verification email; undefined
   * when no cooldown is known.
   *
   * The context writes it from the reports and the replies it adopts: a
   * sign-in, an account read and a same-identity adoption. It has no public
   * setter, for the reason `elevateWithPassword` gives for the elevation
   * mirror.
   */
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
  login: (username: string, password: string, captcha?: CaptchaRequestFields, opts?: SignInOptions) => Promise<AuthLoginResult>
  loginWithPasskey: (username: string, captcha?: CaptchaRequestFields, opts?: SignInOptions) => Promise<AuthLoginResult>
  /**
   * Adopt the session the sidecar's jar already holds, after a successful
   * browser handoff. Reads the account through `GetCurrentUser`; a failed
   * read starts sign-in recovery.
   */
  adoptBrowserSignIn: (opts?: SignInOptions) => Promise<AuthLoginResult>
  logout: () => Promise<void>
  /**
   * Adopt the session a sign-up, the OAuth completion or the first solo
   * password established, from its `SessionReport`.
   *
   * It is the identity TRANSITION, so it clears the elevation the previous
   * identity held, and then adopts the elevation the report carries: only
   * `SetInitialSoloPassword` grants one, in the same transaction that creates
   * the session. A report with no account starts sign-in recovery; see
   * `SignInOutcome`.
   */
  setAuth: (report: SessionReport | undefined, opts?: SignInOptions) => Promise<SignInOutcome>
  /**
   * The sign-in recovery in progress, or null. See `SignInRecoveryGate`.
   */
  signInRecovery: () => SignInRecovery | null
  /** Read the account again after a failed recovery read; nothing else repeats. */
  retrySignInRecovery: () => Promise<void>
  /**
   * End a completed recovery. `SignInRecoveryGate` calls it once the router
   * reached the destination, so the routes mount on the destination directly.
   */
  finishSignInRecovery: () => void
  /**
   * Open the adoption of a mutation's reply, BEFORE the mutation leaves.
   *
   * The returned function adopts the reply's user only when it is the account
   * that was signed in when the mutation left, and the account signed in
   * when the reply lands. Otherwise it re-reads the account, so a reply that
   * lands after a sign-out or a sign-in as someone else cannot put the old
   * account back. It never writes the elevation: a mutation's reply carries
   * none, so it cannot restore a window the user ended.
   *
   * A local write that leaves the identity alone, such as a proven elevation
   * or a refresh that landed, does not discard the reply. The reply is the
   * hub's account after the mutation, and the read that would replace it can
   * fail.
   */
  openUserAdoption: () => UserAdoption
  /** Re-read the account and report whether this response was adopted. */
  reloadUser: () => Promise<boolean>
  refreshUser: () => Promise<void>
  isAuthenticated: () => boolean
  /**
   * The current identity-lifetime scope, from one snapshot: the adopted user
   * ID, the identity generation and the cancellation signal together. See
   * {@link WorkerTrustScope} for what advances it and what must not.
   */
  workerTrustScope: () => WorkerTrustScope
}

const AuthContext = createStableContext<AuthState>('context/AuthContext')

export const AuthProvider: ParentComponent = (props) => {
  const [identity, setIdentity] = createSignal<{ user: User, storageAccount: StorageAccount } | null>(null)
  const user = createMemo(() => identity()?.user ?? null)
  const storageAccount = createMemo(() => identity()?.storageAccount ?? null)
  const accountEndListeners = new Set<(account: StorageAccount) => void>()
  const onAccountEnd = (listener: (account: StorageAccount) => void): (() => void) => {
    accountEndListeners.add(listener)
    return () => accountEndListeners.delete(listener)
  }
  const [loading, setLoading] = createSignal(true)
  const [error, setError] = createSignal<string | null>(null)
  const [bootstrapError, setBootstrapError] = createSignal<string | null>(null)
  const [verificationResendAvailableAt, setVerificationResendAvailableAtSignal] = createSignal<Timestamp | undefined>()
  const [elevationExpiresAt, setElevationExpiresAtSignal] = createSignal<Timestamp | undefined>()
  const [signInRecovery, setSignInRecovery] = createSignal<SignInRecovery | null>(null)

  /**
   * Counts the auth OPERATIONS. A sign-in, a `setAuth`, a recovery retry, a
   * session restore and a sign-out each take the next value when they start.
   *
   * Every write an operation makes AFTER an await -- the identity, the
   * bootstrap error, the recovery state, the login error and the end of
   * `loading` -- first checks that its value is still the newest, for a
   * rejected read as well as a successful one. A superseded operation writes
   * nothing: an older sign-in's late answer cannot overwrite a newer one's,
   * its late failure cannot paint an error over a session that works, and its
   * `finally` cannot end the `loading` a newer operation still holds.
   *
   * A separate count from `authGeneration`, which marks a READ of the account
   * stale whenever any local write lands. That is a different question: a
   * slide report from an unrelated request moves it, and must not cancel an
   * operation.
   */
  let authOperation = 0
  const beginOperation = () => ++authOperation
  const isCurrent = (op: number) => op === authOperation

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
   * `supersededByAnyWrite` records the count before the request leaves, and
   * `readCurrentUser` discards the response when the count moved. Every local
   * write below moves it, so this covers a concurrent read AND a ceremony, for
   * every signal the response carries rather than for the deadline alone.
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
   * The identity-lifetime scope: a distinct counter from `authGeneration`,
   * which is a write fence over the ACCOUNT STATE. This one marks IDENTITY
   * LIFETIMES only -- see {@link WorkerTrustScope}. `setUser` is the single
   * writer: it retires the previous scope before the storage namespace moves
   * or the identity signal publishes, and installs the successor in the same
   * synchronous step.
   */
  let trustGeneration = 0
  let trustController = new AbortController()
  const initialTrustScope: WorkerTrustScope = {
    userId: null,
    generation: 0,
    signal: trustController.signal,
  }
  const [trustScope, setTrustScopeSignal] = createSignal<WorkerTrustScope>(initialTrustScope)
  onCleanup(platformBridge.setTunnelTrustScope(() => untrack(trustScope)))

  /** Abort the live scope's signal. Its decisions are now cancellations. */
  const retireWorkerTrustScope = () => {
    trustController.abort()
  }

  /** Install the next lifetime. The caller retires the previous scope first. */
  const installWorkerTrustScope = (userId: string | null) => {
    trustGeneration++
    trustController = new AbortController()
    setTrustScopeSignal({
      userId,
      generation: trustGeneration,
      signal: trustController.signal,
    })
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
   * The identity-lifetime scope moves in the same step: the previous scope
   * retires BEFORE the namespace switches or the identity publishes, and the
   * successor installs before the signal notifies. `opts.identityTransition`
   * marks the calls that are an identity CHANGE whatever the user IDs say --
   * a sign-out and a sign-in, including a sign-in as the same user -- because
   * each is a new lifetime. Everything else advances the scope only when the
   * adopted user ID actually differs.
   *
   * ASYNCHRONOUS, and only for a non-null user. The synchronous storage tier is
   * an in-memory mirror of an IndexedDB-backed store, so the rows have to be
   * READ before the namespace may point at them -- `setStorageAccount` refuses
   * an account it was not hydrated for. Every caller of this already awaits the
   * hub round trip that produced the User, so the gate costs no extra hop; and
   * `prepareStorageAccount` never rejects, so a browser with no IndexedDB signs
   * in on defaults rather than failing to sign in.
   *
   * The two writes after that await stay ADJACENT and synchronous, which is the
   * property everything downstream rests on.
   *
   * A null user does NOT clear the namespace -- see `setStorageAccount`. The
   * writes a sign-out triggers during the tear-down belong to the account that
   * leaves. It also takes no await, so a sign-out still moves the signal in the
   * same turn -- which `setOnAuthError` and `logout`'s `finally` depend on.
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
  const setUser = async (next: User | null, opts: { identityTransition?: boolean } = {}): Promise<boolean> => {
    const generation = ++authGeneration
    const outgoingAccount = identity()?.storageAccount ?? null
    if (next && !next.id) {
      log.error('the hub returned a user with no id; treating the session as signed out', { user: next })
      next = null
    }
    let account: StorageAccount | null = null
    // A flagged adoption of a USER is a new lifetime whatever the ids say -- a
    // sign-in as the same user (a bookmarked `/login` over a live session)
    // starts one. A flagged adoption of nobody is a sign-out, which is already
    // an id change when there was somebody to sign out, and a no-op when there
    // was not. Everything else advances only when the adopted user changed.
    const nextId = next?.id ?? null
    const advanceScope = nextId !== trustScope().userId
      || (opts.identityTransition === true && next !== null)
    if (next) {
      account = storageAccountFor(hubOrigin(), next.id)
      const prepared = await prepareStorageAccount(account)
      // A later identity superseded this one while its rows were loading. It
      // owns the mirror now, so publishing this one would point the namespace
      // at an account the mirror no longer holds -- which `setStorageAccount`
      // would refuse anyway, loudly, in the middle of a sign-in.
      if (generation !== authGeneration)
        return false
      if (!prepared.commit())
        return false
    }
    // The scope retires BEFORE the namespace moves or the identity publishes,
    // so a decision captured under it sees the cancellation rather than the
    // successor's namespace.
    if (advanceScope) {
      const outgoing = trustScope()
      if (outgoing.userId) {
        // Install the native reset barrier before any successor can list or
        // mutate tunnels. Auth can still finish if the sidecar refuses reset.
        void platformBridge.resetTunnels(outgoing).catch((err: unknown) => {
          log.warn('Failed to reset desktop tunnels on identity change', err)
        })
      }
      retireWorkerTrustScope()
      if (outgoing.userId)
        channelManager.closeAll()
    }
    if (next && account)
      setStorageAccount(account)
    if (!next && outgoingAccount) {
      for (const listener of [...accountEndListeners]) {
        try {
          listener(outgoingAccount)
        }
        catch (err) {
          log.error('authenticated account cleanup failed', { err })
        }
      }
    }
    if (advanceScope)
      installWorkerTrustScope(nextId)
    setIdentity(next && account
      ? { user: next, storageAccount: account }
      : null)
    return true
  }

  const clearAuthUser = () => {
    advanceAuthSessionEpoch()
    // No await: the null branch takes none, so the signal still moves in this
    // turn. See `setUser`. A sign-out is an identity transition: the scope the
    // previous identity held retires even when nobody adopts the successor.
    void setUser(null, { identityTransition: true })
    // A recovery reads the account of a session, and this session has ended.
    setSignInRecovery(null)
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
   * ONE site, because the response carries three signals and three paths read
   * it: the bootstrap restore, `refreshUser` and the sign-in recovery read.
   * The first two adopted different subsets by hand, so a refresh dropped the
   * verification cooldown and left `/verify-email` counting from zero against
   * a hub that just minted a code. A fourth signal added to the response now
   * lands in every path.
   *
   * `readCurrentUser` is the only caller. Before it gets here, it asks the
   * staleness rule its own caller passed whether the response may still land:
   * the restore and `refreshUser` discard a response once any local write
   * moved `authGeneration`, and the recovery read discards one once a newer
   * operation started.
   */
  const adoptCurrentUser = async (resp: GetCurrentUserResponse): Promise<boolean> => {
    const report = resp.sessionReport
    // EVERY write below belongs to `resp`, so none of them may run once a newer
    // identity has taken over. `setUser` is asynchronous now -- it reads the
    // account's stored rows before it publishes -- and a sign-in or a sign-out
    // landing inside that await supersedes this response. Publishing its
    // elevation window then mirrors a dead session's deadline onto the live one,
    // which is what makes `/elevate` redirect a visitor it should have prompted.
    // The guard in `readCurrentUser` runs BEFORE the await and cannot see this.
    if (!await setUser(report?.user ?? null))
      return false
    setElevationExpiresAt(report?.elevationExpiresAt)
    // The only response the /verify-email page's own bootstrap reads. Seeding
    // the cooldown from it is what lets a hard reload of that page resume the
    // countdown instead of restarting at zero and letting the button send
    // repeated requests that the hub refuses with ResourceExhausted.
    setVerificationResendAvailableAt(report?.emailVerification?.nextResendAvailableAt)
    return true
  }

  /**
   * The staleness rule of a read that may adopt only the NEWEST account
   * state: the read is stale once any local write moved `authGeneration`
   * after this call. Call it as the read leaves, as the argument of
   * `readCurrentUser`. See `authGeneration` for what a stale adopt costs.
   */
  const supersededByAnyWrite = (): (() => boolean) => {
    const generation = authGeneration
    return () => generation !== authGeneration
  }

  /**
   * Read the account and reject on a failed request. Ask `isSuperseded`
   * whether the answer still applies before adopting it.
   *
   * The caller states the staleness rule. Desktop sign-in reads
   * `GetCurrentUser` through `runSignIn`, which checks its operation before
   * adopting the session. The other readers use one of these rules.
   *
   * - `refreshUser` and the bootstrap restore pass `supersededByAnyWrite()`,
   *   because a response that an unrelated write outdated puts older account
   *   state back.
   * - The sign-in recovery read passes its operation, because a recovery read
   *   that an unrelated write discarded leaves the gate waiting with no read
   *   in flight. See `readForRecovery`.
   */
  const readCurrentUser = async (isSuperseded: () => boolean): Promise<AccountReadResult> => {
    const resp = await authClient.getCurrentUser({})
    if (isSuperseded())
      return { outcome: 'superseded' }
    if (await adoptCurrentUser(resp)) {
      return {
        outcome: 'adopted',
        verificationRequired: resp.sessionReport?.emailVerification?.verificationRequired,
      }
    }
    // `setUser` moved `authGeneration` itself, so `supersededByAnyWrite` reads
    // every refusal as superseded, which it is for that rule: a newer write
    // owns the state. The operation rule tells the two cases apart.
    return {
      outcome: isSuperseded()
        ? 'superseded'
        : 'displaced',
    }
  }

  const reloadUser = async (): Promise<boolean> => {
    return (await readCurrentUser(supersededByAnyWrite())).outcome === 'adopted'
  }

  const refreshUser = async () => {
    try {
      // Every outcome leaves the newest account state in place, which is all
      // a refresh asks for.
      await reloadUser()
    }
    catch {
      // Ignore — user state unchanged.
    }
  }

  /**
   * Reveal the shell rows of the boot splash's checklist, then jump the
   * checklist to `workspaces`. Every caller runs this before AppShell mounts,
   * so a sign-in does not flash the finished signed-out `ready` phase.
   */
  const enterShell = () => {
    setBootShell(true)
    setBootPhase('workspaces')
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
  const adoptSignedInUser = async (u: User): Promise<boolean> => {
    // See `adoptCurrentUser`: nothing below may run for an identity a newer one
    // superseded during the hydration await. A sign-in is an identity
    // transition even when the user id is the one already adopted -- a
    // bookmarked `/login` over a live session starts a NEW lifetime -- so the
    // scope advances and the previous one retires.
    if (!await setUser(u, { identityTransition: true }))
      return false
    setElevationExpiresAt(undefined)
    enterShell()
    return true
  }

  /**
   * Patch the sign-in recovery in progress, and write nothing when none is in
   * progress: a sign-out during a read ended recovery, and a late answer of
   * that read must not start it again.
   */
  const patchRecovery = (patch: Partial<SignInRecovery>) => {
    setSignInRecovery(prev => prev && {
      ...prev,
      ...patch,
    })
  }

  /**
   * Read the account for sign-in recovery, under the operation `op`.
   *
   * The OPERATION decides staleness here, not `authGeneration`: that count
   * moves on any local write, such as a concurrent `refreshUser` or the slide
   * report of a request sent after the sign-in, and a recovery read discarded
   * for one would leave the gate waiting with no read in flight. A newer
   * operation supersedes it.
   *
   * A success adopts the account and marks the read completed; the gate then
   * navigates and calls `finishSignInRecovery`. A failure records its message
   * for the gate's Retry. A superseded read writes neither.
   */
  const readForRecovery = async (op: number) => {
    try {
      const result = await readCurrentUser(() => !isCurrent(op))
      // The operation check also covers an adopted account: a newer
      // operation that started while the account's rows loaded owns the
      // state, and writes the rest of it.
      if (result.outcome === 'superseded' || !isCurrent(op))
        return
      if (result.outcome !== 'adopted') {
        // `setUser` refused the account because a local write landed while
        // its rows loaded, yet no newer operation owns the state. Nothing
        // else will finish this recovery, so the gate offers Retry.
        patchRecovery({
          read: { kind: 'failed', message: RECOVERY_ADOPTION_DISPLACED },
        })
        return
      }
      enterShell()
      loadTimeouts().catch(() => {})
      patchRecovery({
        read: { kind: 'completed' },
        verificationRequired: signInRecovery()?.verificationRequired ?? result.verificationRequired ?? false,
      })
    }
    catch (err) {
      if (!isCurrent(op))
        return
      patchRecovery({
        read: {
          kind: 'failed',
          message: formatErrorMessage(err, 'Failed to load your account'),
        },
      })
    }
  }

  /**
   * Adopt what a SUCCESSFUL sign-in reported, under the operation that ran it.
   * `runSignIn` handles password, passkey and desktop browser sign-in.
   * `setAuth` handles sign-up, OAuth completion and the first solo password.
   *
   * With an account, this is the identity transition `adoptSignedInUser`
   * makes, followed by the verification cooldown and any elevation the
   * reply reports.
   *
   * Without an account, the sign-in still SUCCEEDED: the session cookie now
   * belongs to an account this page cannot read yet. The identity transition
   * happens at once -- the previous identity, its elevation and, through the
   * `user` effect, its pooled channels go -- and sign-in recovery starts.
   * `SignInRecoveryGate` replaces every route while the account read runs, and
   * recovery owns the navigation that follows.
   */
  const adoptSignInReport = async (report: SessionReport | undefined, op: number, opts: SignInOptions): Promise<SignInOutcome> => {
    if (!isCurrent(op))
      return 'superseded'
    const verification = report?.emailVerification
    if (report?.user) {
      const adopted = await adoptSignedInUser(report.user)
      if (!isCurrent(op))
        return 'superseded'
      if (adopted) {
        setVerificationResendAvailableAt(verification?.nextResendAvailableAt)
        // AFTER the adopt, which cleared it: this restores a window this
        // document owns rather than one it inherited.
        if (report.elevationExpiresAt)
          setElevationExpiresAt(report.elevationExpiresAt)
        // A newer sign-in that adopts its account ends whatever an older one
        // left behind.
        setSignInRecovery(null)
        setBootstrapError(null)
        loadTimeouts().catch(() => {})
        return 'adopted'
      }
      // `setUser` refused the account because a local write landed while its
      // rows loaded, such as a slide report from a request the previous
      // identity sent, yet no newer operation owns the state. The sign-in
      // succeeded and nobody else will finish it, so it recovers the account
      // below, as a report without one does.
    }
    setElevationExpiresAt(undefined)
    // The identity transition happens at once -- the previous identity and its
    // lifetime scope go -- and sign-in recovery starts.
    void setUser(null, { identityTransition: true })
    setVerificationResendAvailableAt(verification?.nextResendAvailableAt)
    setBootstrapError(null)
    setSignInRecovery({
      read: { kind: 'reading' },
      ...(verification
        ? { verificationRequired: verification.verificationRequired }
        : {}),
      ...(opts.redirect !== undefined
        ? { redirect: opts.redirect }
        : {}),
    })
    void readForRecovery(op)
    return 'recovering'
  }

  const retrySignInRecovery = async () => {
    if (!signInRecovery())
      return
    const op = beginOperation()
    patchRecovery({
      read: { kind: 'reading' },
    })
    await readForRecovery(op)
  }

  const finishSignInRecovery = () => {
    setSignInRecovery(null)
  }

  const openUserAdoption = (): UserAdoption => {
    // A one-time snapshot on purpose: the identity as it stood when the
    // mutation left, which the reply must still match.
    const initialUserId = user()?.id
    return async (next, verification) => {
      // The LIVE identity too: a sign-out, or a sign-in as someone else, while
      // the mutation was out leaves the reply describing an account this
      // document no longer holds. Not `authGeneration`, which every local
      // write moves: a slide report or a refresh that landed meanwhile would
      // discard the one reply that states the mutation's result.
      if (!next || next.id !== initialUserId || user()?.id !== initialUserId) {
        await refreshUser()
        return
      }
      // `setUser` refuses the reply when any local write lands while the
      // account's rows load, because it cannot tell a newer identity from an
      // unrelated write. None of the reply may land then, the cooldown
      // included, so the account is read instead.
      if (!await setUser(next)) {
        await refreshUser()
        return
      }
      if (verification)
        setVerificationResendAvailableAt(verification.nextResendAvailableAt)
    }
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

  onMount(() => {
    // Register the process-global transport callback only for this provider's
    // lifetime, so a disposed provider cannot receive a later 401.
    setOnAuthError(() => {
      // isAutoAuthenticated, not isSoloMode: the question is whether THIS
      // connection holds a session to lose. A solo hub reached at a network
      // address does -- it signed in -- so a 401 there must clear the user like
      // anywhere else, or the app keeps rendering a session the hub refused.
      if (!untrack(isAutoAuthenticated))
        clearAuthUser()
    })
    onCleanup(() => setOnAuthError(null))
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
  const restoreSession = async (op: number) => {
    try {
      // On ANY adoption, whatever the operation. The read's staleness rule is
      // the account state, so it still adopts the account after a sign-in
      // superseded the restore. When that sign-in then fails, or its passkey
      // prompt is dismissed, the restored account is this document's account,
      // and nothing else loads the hub's timeouts for it.
      if ((await readCurrentUser(supersededByAnyWrite())).outcome === 'adopted')
        loadTimeouts().catch(() => {})
    }
    catch (err) {
      // A newer operation, such as a sign-in, owns the state now; this
      // restore's failure describes a session nobody is using.
      if (!isCurrent(op))
        return
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
   * The whole bootstrap sequence. It records `bootstrapError`, and a sign-in
   * that adopts an account or starts recovery clears it (`adoptSignInReport`).
   *
   * System info comes FIRST and is a hard prerequisite: every downstream
   * decision (solo vs. multi-user, setup-required) reads module getters that
   * are fabricated defaults until it succeeds. Continuing to the session
   * restore on a failed load is how the app treats a solo user as a multi-user
   * one -- the restore succeeds (a solo hub authenticates the desktop app's
   * local IPC connection with no credential), the code records nothing, and the
   * app offers a "Log out" that strands them at /login. So a failed load aborts
   * here with a `bootstrapError` for the guard's retry panel to render.
   *
   * That abort holds only while this bootstrap is the current operation. A
   * sign-in that starts during the system info load supersedes it, and a
   * failed load then records no `bootstrapError`: the sign-in owns the state,
   * and it ends `loading` on a snapshot that never loaded. `AuthGuard` holds
   * there anyway, because `loadSystemInfo` records its own failure
   * (`systemInfoLoadFailure`) and the guard decides nothing until the
   * snapshot loads. The credential forms also refuse to submit while the
   * snapshot is pending.
   *
   * Each stage also advances the boot splash's checklist (data-boot-phase on
   * <html>): system info, then the session restore, then either `ready`
   * (signed-out / password-setup) or `workspaces` with the shell rows revealed
   * (authenticated AppShell). AppShell advances `tabs` → `ready`. A failure
   * leaves the checklist on the stage that failed, which is the truth.
   */
  const bootstrap = async (): Promise<number> => {
    const op = beginOperation()
    setBootstrapError(null)
    setBootShell(false)
    setBootPhase('system-info')
    try {
      await loadSystemInfo()
    }
    catch (err) {
      if (isCurrent(op)) {
        const msg = formatErrorMessage(err)
        log.warn('system info load failed', { error: msg })
        setBootstrapError(msg)
      }
      return op
    }
    if (!isCurrent(op))
      return op
    setBootPhase('session')
    await restoreSession(op)
    if (!isCurrent(op) || bootstrapError())
      return op
    // The restricted TCP setup state is not a session and not the shell, so it
    // must not reveal the workspace rows.
    if (user() && !isPasswordSetupGate()) {
      enterShell()
      return op
    }
    setBootPhase('ready')
    return op
  }

  onMount(async () => {
    if (isCurrent(await bootstrap()))
      setLoading(false)
  })

  const retryBootstrap = async () => {
    setLoading(true)
    if (isCurrent(await bootstrap()))
      setLoading(false)
  }

  /**
   * The state machine both sign-in paths share: clear the banner, hold
   * `loading` for the whole attempt, adopt the reported session, and report
   * the outcome and the verification status.
   *
   * `login` and `loginWithPasskey` were byte-identical apart from the RPC
   * in the middle and the fallback message, so a fix to the identity
   * transition below needed two edits. `runSignIn` passes `fallback` through
   * `passkeyErrorMessage`, which returns null for a dismissed passkey
   * prompt -- that is not a failure to report, so the banner stays empty.
   *
   * It never rejects for an absent account or a failed account read: those
   * follow a SUCCESSFUL sign-in, and recovery handles them. A caller's
   * `catch` sees only a failed sign-in RPC.
   */
  const runSignIn = async (
    fallback: string,
    opts: SignInOptions,
    // Proto responses carry their optional fields as explicitly-undefined
    // keys, so the attempt result allows undefined alongside absence.
    attempt: () => Promise<{ sessionReport?: SessionReport | undefined }>,
  ): Promise<AuthLoginResult> => {
    const op = beginOperation()
    setError(null)
    setLoading(true)
    try {
      const resp = await attempt()
      advanceAuthSessionEpoch()
      // Through adoptSignInReport: setUser drives the eager release of the
      // previous user's pooled channels through the createEffect above,
      // rather than leaving them to the lazy per-request identity check
      // (which evicts one channel per request while the shared WebSocket
      // stays held for the old user) -- and it clears the elevation the
      // previous identity held.
      const outcome = await adoptSignInReport(resp.sessionReport, op, opts)
      return {
        outcome,
        verificationRequired: resp.sessionReport?.emailVerification?.verificationRequired ?? false,
      }
    }
    catch (e) {
      if (isCurrent(op))
        setError(passkeyErrorMessage(e, fallback) ?? '')
      // The login form's captcha.reset() (see createCaptchaForm) triggers
      // the system-info reload that converges a stale captcha snapshot
      // after a runtime enable/disable or provider switch.
      throw e
    }
    finally {
      if (isCurrent(op))
        setLoading(false)
    }
  }

  const login = (username: string, password: string, captcha?: CaptchaRequestFields, opts: SignInOptions = {}): Promise<AuthLoginResult> =>
    runSignIn('Login failed', opts, () => authClient.login(create(LoginRequestSchema, {
      username,
      password,
      // The form sends the honeypot on every attempt; the payload is empty until
      // the widget verifies (the hub ignores it while captcha is
      // disabled — see createCaptchaForm's requirement gate).
      captcha: captcha?.captcha ?? { payload: '', honeypot: '' },
    })))

  const loginWithPasskey = (username: string, captcha?: CaptchaRequestFields, opts: SignInOptions = {}): Promise<AuthLoginResult> =>
    runSignIn('Passkey login failed', opts, async () => {
      const begin = await authClient.beginPasskeyLogin({
        username,
        captcha: captcha?.captcha ?? { payload: '', honeypot: '' },
      })
      return await authClient.finishPasskeyLogin({
        credential: await authenticateForChallenge(begin.challenge),
      })
    })

  const adoptBrowserSignIn = (opts: SignInOptions = {}): Promise<AuthLoginResult> =>
    runSignIn('Sign-in failed', opts, async () => {
      try {
        return await authClient.getCurrentUser({})
      }
      catch {
        // The handoff succeeded; a failed account read starts sign-in
        // recovery through adoptSignInReport with no session report.
        return {}
      }
    })

  const logout = async () => {
    // A credential-free connection has no session to end; every other one,
    // solo or not, signs out normally.
    if (isAutoAuthenticated())
      return
    // A sign-out supersedes every operation in flight, so none of them can
    // adopt an account into the signed-out document.
    beginOperation()
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

  const setAuth = async (report: SessionReport | undefined, opts: SignInOptions = {}): Promise<SignInOutcome> => {
    // The same identity transition runSignIn makes: the sign-ups, the OAuth
    // completion and the first solo password all land a NEW identity in a
    // document that may still hold the previous one's elevation.
    const op = beginOperation()
    advanceAuthSessionEpoch()
    const outcome = await adoptSignInReport(report, op, opts)
    if (isCurrent(op))
      setLoading(false)
    return outcome
  }

  const state: AuthState = {
    user,
    storageAccount,
    onAccountEnd,
    loading,
    error,
    verificationResendAvailableAt,
    elevationExpiresAt,
    elevateWithPassword,
    elevateWithPasskey,
    dropElevation,
    login,
    loginWithPasskey,
    adoptBrowserSignIn,
    logout,
    setAuth,
    signInRecovery,
    retrySignInRecovery,
    finishSignInRecovery,
    openUserAdoption,
    reloadUser,
    refreshUser,
    bootstrapError,
    retryBootstrap,
    isAuthenticated: () => user() !== null,
    workerTrustScope: () => trustScope(),
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
