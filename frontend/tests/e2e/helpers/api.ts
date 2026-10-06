// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

import type { ChannelManager } from '../../../src/lib/channel'
import { execFile } from 'node:child_process'
import { join } from 'node:path'
import { promisify } from 'node:util'
import { fromJson } from '@bufbuild/protobuf'
import { CleanupWorkspaceRequestSchema, CleanupWorkspaceResponseSchema, DeleteWorkspaceResponseSchema, TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
import { isObject } from '../../../src/lib/jsonPick'
import { sleep } from '../../../src/lib/sleep'
import { solveCaptchaViaAPI } from './altcha'
import { finishCleanup } from './cleanup'
import { createTestChannelManager } from './e2e-channel'

/**
 * Poll interval for API readiness checks.
 * Each check makes an HTTP request. Agent checks also make an encrypted worker request.
 * These operations can take seconds. Polling every 25ms could add about 2,400 requests during a 30-second wait.
 * A 150ms interval reduces request volume while retaining less delay than the previous fixed waits of 200–500ms.
 */
export const API_POLL_INTERVAL_MS = 150

// ---- Encrypted channel cache ----
// Keep one ChannelManager for each hubUrl and cookie pair. Reuse it instead of repeating the handshake for every API call.

const channelManagers = new Map<string, Promise<ChannelManager>>()

export async function getTestChannel(hubUrl: string, cookie: string): Promise<ChannelManager> {
  const key = JSON.stringify([hubUrl, cookie])
  let pending = channelManagers.get(key)
  if (!pending) {
    pending = createTestChannelManager(hubUrl, cookie).catch((error) => {
      if (channelManagers.get(key) === pending)
        channelManagers.delete(key)
      throw error
    })
    channelManagers.set(key, pending)
  }
  return pending
}

/** Close this hub's cached channels, including an initialization that still runs. */
export async function closeTestChannels(hubUrl: string): Promise<void> {
  const pending = [...channelManagers.entries()].filter(([key]) => JSON.parse(key)[0] === hubUrl)
  for (const [key] of pending)
    channelManagers.delete(key)
  await finishCleanup(pending.map(([, channel]) => channel.then(manager => manager.closeAll(), () => {})))
}

// ---- Test admin fixture credentials ----
// The E2E setup creates the first administrator. These credentials match testutil.TestAdminUsername and TestAdminPassword.

export const TEST_ADMIN_USERNAME = 'admin'
export const TEST_ADMIN_PASSWORD = 'admin123'
export const TEST_ADMIN_DISPLAY_NAME = 'Admin'

// ---- Cookie helpers ----

/** The name of the hub's session cookie. `readSessionCookie` in `ui.ts` reads it from here. */
export const SESSION_COOKIE_NAME = 'leapmux-session'

/**
 * Extract the session cookie value from a Set-Cookie header.
 */
function extractSessionCookie(setCookieHeader: string | null): string {
  if (!setCookieHeader) {
    throw new Error('No Set-Cookie header in response')
  }
  // Set-Cookie: leapmux-session=<value>; Path=/; HttpOnly; ...
  for (const part of setCookieHeader.split(';')) {
    const trimmed = part.trim()
    if (trimmed.startsWith(`${SESSION_COOKIE_NAME}=`)) {
      return trimmed
    }
  }
  throw new Error(`Session cookie ${SESSION_COOKIE_NAME} not found in Set-Cookie: ${setCookieHeader}`)
}

/**
 * Build authed fetch headers with the session cookie.
 */
export function authedHeaders(cookie: string): Record<string, string> {
  return {
    'Content-Type': 'application/json',
    'Cookie': cookie,
  }
}

// ---- Hub requests ----

/** One RPC of the hub, as `<Service>/<Method>` in the `leapmux.v1` package. */
export type HubMethod = `${string}Service/${string}`

export interface HubRequestOptions {
  /** The session cookie. An RPC that needs no session, such as Login, omits it. */
  cookie?: string
  redirect?: RequestRedirect
  /** Abort the request. A caller with an optional signal passes it through as it is. */
  signal?: AbortSignal | undefined
}

/**
 * Send one Connect JSON request to a hub RPC and return the response, whatever its status.
 * Use it where the test reads a refusal or a response header. Use `callHub` where the test needs a successful answer.
 */
export async function hubRequest(hubUrl: string, method: HubMethod, body: unknown, options: HubRequestOptions = {}): Promise<Response> {
  return fetch(`${hubUrl}/leapmux.v1.${method}`, {
    method: 'POST',
    headers: options.cookie === undefined ? { 'Content-Type': 'application/json' } : authedHeaders(options.cookie),
    body: JSON.stringify(body),
    ...(options.redirect ? { redirect: options.redirect } : {}),
    ...(options.signal ? { signal: options.signal } : {}),
  })
}

/**
 * The error of a hub call that reached the hub and that the hub refused.
 * A caller that polls tells it apart from a request that never reached the hub.
 */
class HubCallRefusedError extends Error {}

/**
 * Throw for a refused hub response. The message holds the operation, the RPC, the status, and the body, because the
 * body holds the hub's reason. A status alone does not tell which setup step failed.
 */
async function requireHubOk(res: Response, method: HubMethod, operation: string): Promise<void> {
  if (!res.ok)
    throw new HubCallRefusedError(`${operation} failed: ${method} returned HTTP ${res.status}: ${await res.text()}`)
}

/**
 * Call a hub RPC and return its JSON answer. A refused call throws, and the message states the hub's reason.
 * `operation` gives the helper and its arguments, so a failure tells which call of a test failed.
 */
export async function callHub<T>(
  hubUrl: string,
  method: HubMethod,
  body: unknown,
  options: HubRequestOptions & { operation: string },
): Promise<T> {
  const res = await hubRequest(hubUrl, method, body, options)
  await requireHubOk(res, method, options.operation)
  return await res.json() as T
}

/** The Connect error of a refused hub response: its code, such as `unauthenticated`, and its message. */
export interface HubRefusal {
  code: string
  message: string
}

/**
 * Read the Connect error of a refused hub response.
 * A successful response, or a refusal whose body is not a Connect error, throws: a test that expects a refusal must
 * not pass on another failure, such as a proxy error page.
 */
export async function hubRefusal(res: Response): Promise<HubRefusal> {
  const text = await res.text()
  if (res.ok)
    throw new Error(`The hub accepted the request (HTTP ${res.status}), but the test expects a refusal: ${text}`)
  let body: unknown
  try {
    body = JSON.parse(text)
  }
  catch {
    body = null
  }
  if (!isObject(body) || typeof body.code !== 'string')
    throw new Error(`The refused hub response (HTTP ${res.status}) holds no Connect error: ${text}`)
  return { code: body.code, message: typeof body.message === 'string' ? body.message : '' }
}

// ---- Hub API helpers (Auth, Admin, Worker management) ----

/**
 * Send one Login request with a solved captcha, and return the response, whatever its status.
 * A test that expects a refused sign-in reads the refusal with `hubRefusal`. The captcha fields make the refusal come
 * from the credentials: a request with no captcha fields can fail on the captcha alone, whatever the password.
 */
export async function attemptLoginViaAPI(hubUrl: string, username: string, password: string): Promise<Response> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  return hubRequest(hubUrl, 'AuthService/Login', {
    username,
    password,
    captchaPayload: captcha.captchaPayload,
    honeypot: captcha.honeypot,
  }, { redirect: 'manual' })
}

/**
 * Sign in through the Connect API. Return the session cookie for later requests, such as "leapmux-session=abc123".
 */
export async function loginViaAPI(hubUrl: string, username: string, password: string): Promise<string> {
  const res = await attemptLoginViaAPI(hubUrl, username, password)
  await requireHubOk(res, 'AuthService/Login', 'loginViaAPI')
  return extractSessionCookie(res.headers.get('set-cookie'))
}

/** End the session of `cookie` on the hub. A refused logout throws, so a later step cannot run on a live session. */
export async function logoutViaAPI(hubUrl: string, cookie: string): Promise<void> {
  await callHub(hubUrl, 'AuthService/Logout', {}, { cookie, operation: 'logoutViaAPI' })
}

export interface ApiUser {
  id: string
  username: string
  displayName: string
  isAdmin: boolean
  email: string
}

/**
 * Get the full current-user payload via the Connect API.
 */
export async function getCurrentUser(hubUrl: string, cookie: string): Promise<ApiUser> {
  const data = await callHub<{ user: ApiUser }>(hubUrl, 'AuthService/GetCurrentUser', {}, { cookie, operation: 'getCurrentUser' })
  return data.user
}

/**
 * Get the user ID for the supplied session cookie through the Connect API.
 * Administrator status belongs to that session. The endpoint has no separate administrator lookup.
 */
export async function getUserId(hubUrl: string, cookie: string): Promise<string> {
  const id = (await getCurrentUser(hubUrl, cookie)).id
  if (!id) {
    throw new Error('getUserId: no user id in GetCurrentUser response')
  }
  return id
}

/**
 * Sign up a new user via the Connect API. Returns the session cookie string.
 */
export async function signUpViaAPI(
  hubUrl: string,
  username: string,
  password: string,
  displayName = '',
  email = '',
): Promise<string> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  const res = await hubRequest(hubUrl, 'AuthService/SignUp', {
    username,
    password,
    displayName,
    email,
    captchaPayload: captcha.captchaPayload,
    honeypot: captcha.honeypot,
  }, { redirect: 'manual' })
  await requireHubOk(res, 'AuthService/SignUp', 'signUpViaAPI')
  return extractSessionCookie(res.headers.get('set-cookie'))
}

/** One registered worker, as ListWorkers reports it. */
export interface WorkerSummary {
  id: string
  online: boolean
}

/**
 * List the workers that `cookie` can see, in the hub's order. The one reader of ListWorkers: a worker with no ID
 * throws, and an absent `workers` field reads as no worker.
 */
export async function listWorkersViaAPI(hubUrl: string, cookie: string, signal?: AbortSignal): Promise<WorkerSummary[]> {
  const data = await callHub<{ workers?: Array<{ id?: string, online?: boolean }> }>(
    hubUrl,
    'WorkerManagementService/ListWorkers',
    {},
    { cookie, signal, operation: 'listWorkersViaAPI' },
  )
  return (data.workers ?? []).map((worker) => {
    if (!worker.id)
      throw new Error('listWorkersViaAPI received a worker with no ID.')
    return { id: worker.id, online: worker.online === true }
  })
}

/**
 * Wait until the first listed worker is online, and return its ID.
 * The database stores the Worker before its bidirectional stream connects, so the first read can show it offline.
 */
export async function getWorkerId(hubUrl: string, cookie: string): Promise<string> {
  const deadline = Date.now() + 30_000
  while (true) {
    const first = (await listWorkersViaAPI(hubUrl, cookie))[0]
    if (first?.online)
      return first.id
    if (Date.now() >= deadline)
      throw new Error('Worker never came online within 30s')
    await sleep(API_POLL_INTERVAL_MS)
  }
}

/**
 * Deregister a worker via the Connect API.
 */
export async function deregisterWorkerViaAPI(
  hubUrl: string,
  cookie: string,
  workerId: string,
): Promise<void> {
  await callHub(hubUrl, 'WorkerManagementService/DeregisterWorker', { workerId }, { cookie, operation: `deregisterWorkerViaAPI(${workerId})` })
}

/**
 * Enable signup through an administrator session.
 * A standalone Hub disables signup by default. Store signup_enabled before a second account registers, or signup returns failed_precondition.
 * Dev mode defaults to open signup only while no explicit setting exists.
 * The first account is exempt. A hub without users accepts its signup and makes it an administrator.
 */
export async function enableSignupViaAPI(hubUrl: string, cookie: string): Promise<void> {
  await updateSettingViaAPI(hubUrl, cookie, 'signup_enabled', 'true')
}

/**
 * Write one hub setting through an elevated administrator session. Elevate the session before this call.
 * partialJson follows UpdateSetting. A scalar uses a JSON value. A structured setting uses an object with only the changed fields.
 */
export async function updateSettingViaAPI(
  hubUrl: string,
  cookie: string,
  key: string,
  partialJson: string,
): Promise<void> {
  await callHub(hubUrl, 'AdminSettingsService/UpdateSetting', { key, partialJson }, { cookie, operation: `updateSettingViaAPI(${key})` })
}

export interface SmtpCaptureTarget {
  host: string
  port: number
}

/**
 * Point the Hub at a loopback relay for Simple Mail Transfer Protocol (SMTP).
 * The Hub requires email verification when both host and from_address exist.
 */
export async function configureCaptureSmtpViaAPI(
  hubUrl: string,
  adminCookie: string,
  relay: SmtpCaptureTarget,
  fromAddress = 'hub@test.local',
): Promise<void> {
  await updateSettingViaAPI(hubUrl, adminCookie, 'smtp', JSON.stringify({
    host: relay.host,
    port: relay.port,
    from_address: fromAddress,
    tls_mode: 'none',
  }))
}

/** Poll GetSystemInfo until emailEnabled reflects the staged SMTP block. */
export async function waitForEmailEnabled(hubUrl: string, timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const res = await hubRequest(hubUrl, 'AuthService/GetSystemInfo', {})
    if (res.ok) {
      const data = await res.json() as { emailEnabled?: boolean }
      if (data.emailEnabled)
        return
    }
    await sleep(API_POLL_INTERVAL_MS)
  }
  throw new Error('waitForEmailEnabled: hub never reported emailEnabled=true')
}

export interface PasskeySummary {
  id: string
  friendlyName: string
}

/** List passkeys registered for the authenticated user. */
export async function listPasskeysViaAPI(hubUrl: string, cookie: string): Promise<PasskeySummary[]> {
  const data = await callHub<{ passkeys?: Array<{ id?: string, friendlyName?: string }> }>(
    hubUrl,
    'UserService/ListPasskeys',
    {},
    { cookie, operation: 'listPasskeysViaAPI' },
  )
  return (data.passkeys ?? []).map(pk => ({ id: pk.id ?? '', friendlyName: pk.friendlyName ?? '' }))
}

/**
 * Elevate a session with its account password.
 *
 * Elevate the session before each sensitive UserService call. The session retains elevation. A separate secret does not travel with each request.
 */
export async function elevateSessionViaAPI(
  hubUrl: string,
  cookie: string,
  currentPassword: string,
): Promise<void> {
  await callHub(hubUrl, 'UserService/ElevateSession', { currentPassword }, { cookie, operation: 'elevateSessionViaAPI' })
}

/** Delete one passkey. The session must already be elevated. */
export async function deletePasskeyViaAPI(
  hubUrl: string,
  cookie: string,
  passkeyId: string,
): Promise<void> {
  await requireHubOk(await deletePasskeyResponse(hubUrl, cookie, passkeyId), 'UserService/DeletePasskey', `deletePasskeyViaAPI(${passkeyId})`)
}

/**
 * Attempt a passkey delete and return the raw response, so a test can read
 * the refusal's headers as well as its status.
 */
export async function deletePasskeyResponse(
  hubUrl: string,
  cookie: string,
  passkeyId: string,
): Promise<Response> {
  return hubRequest(hubUrl, 'UserService/DeletePasskey', { id: passkeyId }, { cookie })
}

/**
 * An app credential for this account.
 * clientName identifies the registered app. installationName identifies the device or checkout that holds this credential.
 * One app can have multiple installations.
 */
export interface MyAPITokenSummary {
  id: string
  clientName: string
  installationName: string
  grantedScopes: string[]
  current: boolean
}

export async function listMyAPITokensViaAPI(hubUrl: string, cookie: string): Promise<MyAPITokenSummary[]> {
  const data = await callHub<{
    tokens?: Array<{
      id?: string
      clientName?: string
      installationName?: string
      grantedScopes?: string[]
      current?: boolean
    }>
  }>(hubUrl, 'UserService/ListMyAPITokens', {}, { cookie, operation: 'listMyAPITokensViaAPI' })
  return (data.tokens ?? []).map(t => ({
    id: t.id ?? '',
    clientName: t.clientName ?? '',
    installationName: t.installationName ?? '',
    grantedScopes: t.grantedScopes ?? [],
    current: t.current === true,
  }))
}

const execFileAsync = promisify(execFile)

/** The number of attempts of `runHubSql` while the hub holds the write lock. */
const HUB_SQL_ATTEMPTS = 8

/**
 * Quote `value` as a SQLite text literal.
 * The sqlite3 shell takes no bound parameter on its command line, so this quote is the one defense against a value
 * that holds a quote character.
 */
export function sqliteTextLiteral(value: string): string {
  if (value.includes('\0'))
    throw new Error('A SQLite text literal cannot hold a NUL character.')
  return `'${value.replaceAll('\'', '\'\'')}'`
}

/**
 * Run one SQL statement on the hub database with the sqlite3 shell, and return the shell's output.
 * The hub can hold its write lock for a moment, so the function tries again after SQLITE_BUSY, with a longer pause
 * each time. Any other failure throws at once.
 */
async function runHubSql(hubDataDir: string, sql: string): Promise<string> {
  const dbPath = join(hubDataDir, 'hub.db')
  let lastError: unknown
  for (let attempt = 0; attempt < HUB_SQL_ATTEMPTS; attempt++) {
    try {
      return (await execFileAsync('sqlite3', [dbPath, sql])).stdout
    }
    catch (error) {
      lastError = error
      const message = error instanceof Error ? error.message : String(error)
      if (!/database is locked|SQLITE_BUSY/i.test(message))
        throw error
      await sleep(50 * (attempt + 1))
    }
  }
  throw lastError
}

/**
 * Backdate the pending-email row so the ResendVerificationEmail cooldown ends.
 * Signup issues a code immediately. The Hub blocks another code for 60 seconds.
 */
export async function expirePendingEmailCooldown(hubDataDir: string, username: string): Promise<void> {
  // The cooldown compares pending_email_unblocked_at as text. Use the same strftime format as the hub.
  // SQLite datetime() puts a space where the stored format uses T. That format difference can pass the comparison regardless of elapsed time.
  // A test with mixed formats cannot prove that an expired cooldown permits resend.
  await runHubSql(hubDataDir, `UPDATE users SET pending_email_unblocked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-2 minutes') WHERE username = ${sqliteTextLiteral(username)} AND deleted_at IS NULL;`)
}

/** Read the pending email verification code of `username`. An absent code throws. */
export async function readPendingEmailToken(hubDataDir: string, username: string): Promise<string> {
  const token = (await runHubSql(hubDataDir, `SELECT pending_email_token FROM users WHERE username = ${sqliteTextLiteral(username)} AND deleted_at IS NULL;`)).trim()
  if (!token)
    throw new Error(`no pending_email_token for username ${username}`)
  return token
}

/** Verify the session user's pending email with a code from the DB or inbox. */
export async function verifyEmailViaAPI(hubUrl: string, cookie: string, verificationToken: string): Promise<void> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  await callHub(hubUrl, 'UserService/VerifyEmail', {
    verificationToken,
    captchaPayload: captcha.captchaPayload,
    honeypot: captcha.honeypot,
  }, { cookie, operation: 'verifyEmailViaAPI' })
}

/**
 * Mint a registration key through an authenticated user session.
 * The production UI calls WorkerManagementService.CreateRegistrationKey through an administrator or another authorized user.
 * The Worker receives that key through --registration-key.
 */
export async function mintRegistrationKeyViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<string> {
  const data = await callHub<{ registrationKey?: string }>(
    hubUrl,
    'WorkerManagementService/CreateRegistrationKey',
    {},
    { cookie, operation: 'mintRegistrationKeyViaAPI' },
  )
  if (!data.registrationKey)
    throw new Error('mintRegistrationKeyViaAPI: empty key in response')
  return data.registrationKey
}

/**
 * Poll ListWorkers until an online worker ID appears outside the supplied before set.
 * Return that new worker ID.
 *
 * A refused read does not end the wait, because a worker can register while the hub refuses one read.
 * The last refusal becomes the cause of the timeout error, so a hub that refuses every read states its reason.
 * A read that cannot reach the hub ends the wait at once, because a hub that does not answer registers no worker.
 * An abort of `signal` ends the wait at once, also during a read.
 */
export async function waitForNewOnlineWorkerViaAPI(
  hubUrl: string,
  cookie: string,
  before: Set<string>,
  timeoutMs = 30_000,
  signal?: AbortSignal,
): Promise<string> {
  const deadline = Date.now() + timeoutMs
  let lastError: unknown
  while (true) {
    signal?.throwIfAborted()
    try {
      const workers = await listWorkersViaAPI(hubUrl, cookie, signal)
      signal?.throwIfAborted()
      const fresh = workers.find(worker => worker.online && !before.has(worker.id))
      if (fresh)
        return fresh.id
    }
    catch (error) {
      signal?.throwIfAborted()
      if (!(error instanceof HubCallRefusedError))
        throw error
      lastError = error
    }
    if (Date.now() >= deadline)
      throw new Error(`waitForNewOnlineWorkerViaAPI: no new worker came online within ${timeoutMs}ms`, { cause: lastError })
    await sleep(API_POLL_INTERVAL_MS, signal)
  }
}

/**
 * List IDs of every currently-online worker visible to `cookie`.
 */
export async function listOnlineWorkerIDsViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<string[]> {
  return (await listWorkersViaAPI(hubUrl, cookie)).filter(worker => worker.online).map(worker => worker.id)
}

// ---- Encrypted Worker helpers (Agent) ----

/**
 * Open an agent through an encrypted channel to the Worker. Register its tab on the Hub and return the agent ID.
 */
export async function openAgentViaAPI(
  hubUrl: string,
  cookie: string,
  workerId: string,
  workspaceId: string,
  workingDir?: string,
  options?: {
    model?: string
    /** Reopen this exact native session rather than create a new session. */
    agentSessionId?: string
    /** Initial values for any provider option group. */
    optionValues?: Record<string, string>
    createWorktree?: boolean
    worktreeBranch?: string
    worktreeBaseBranch?: string
    checkoutBranch?: string
    useWorktreePath?: string
    agentProvider?: number
    /**
     * Optional initial tab title. Browser opens use pickAgentTitle. This API helper defaults to an empty title.
     * Supply a title when the test requires visible text. A workspace move test can then detect title loss.
     */
    title?: string
  },
): Promise<string> {
  if (options?.agentSessionId !== undefined && options.agentSessionId.trim() === '')
    throw new Error('The native session ID must be nonempty when a resume is requested.')
  const { OpenAgentRequestSchema, OpenAgentResponseSchema } = await import('../../../src/generated/proto/leapmux/v1/agent_pb')
  const channel = await getTestChannel(hubUrl, cookie)
  // The options map holds the model and every option-group value.
  // A top-level model property would disappear during protobuf creation, and the agent would use the provider default.
  const initialOptions = {
    ...options?.optionValues,
    ...(options?.model ? { model: options.model } : {}),
  }

  // Channels hold no workspace set. A workspace created after the handshake needs no announcement before a worker serves its tabs.
  const resp = await channel.callWorker(
    workerId,
    'OpenAgent',
    OpenAgentRequestSchema,
    OpenAgentResponseSchema,
    {
      workerId,
      workingDir: workingDir ?? '',
      ...(options?.agentSessionId !== undefined ? { agentSessionId: options.agentSessionId } : {}),
      ...(options?.title ? { title: options.title } : {}),
      ...(Object.keys(initialOptions).length > 0 ? { options: initialOptions } : {}),
      ...(options?.agentProvider ? { agentProvider: options.agentProvider } : {}),
      ...(options?.createWorktree ? { createWorktree: true, worktreeBranch: options.worktreeBranch ?? '' } : {}),
      ...(options?.worktreeBaseBranch ? { worktreeBaseBranch: options.worktreeBaseBranch } : {}),
      ...(options?.checkoutBranch ? { checkoutBranch: options.checkoutBranch } : {}),
      ...(options?.useWorktreePath ? { useWorktreePath: options.useWorktreePath } : {}),
    },
  )
  if (!resp.agent) {
    throw new Error('openAgentViaAPI: no agent in response')
  }

  // Register the tab in the conflict-free replicated data type (CRDT), as tabStore.addTab does in the browser.
  // The register contains the root tile ID, position, and worker ID.
  // Use the subscription that createWorkspaceViaAPI opened before workspace creation.
  // The hub must deliver its root-node operation or creation event through that existing subscription.
  // A lost event then causes awaitRootNodeId to time out, as the browser would otherwise show an empty workspace without the agent tab.
  const { seedTabIntoWorkspace, getUserEventsSubscription } = await import('./crdt')
  const userEvents = await getUserEventsSubscription(hubUrl, cookie)
  await seedTabIntoWorkspace({
    hubUrl,
    cookie,
    workspaceId,
    tabType: TabType.AGENT,
    tabId: resp.agent.id,
    workerId,
    userEvents,
  })
  return resp.agent.id
}

/**
 * Open an agent with permission mode default.
 * Without an explicit mode, Claude requests Auto Mode. The installed command line interface decides whether Auto Mode is available.
 * Use this helper when a test requires an exact mode or a mode-change notification.
 * A test of the provider default must open the agent without this helper.
 * Read the offered modes to select expectations, as 044-agent-settings.spec.ts does.
 */
export async function openPinnedModeAgentViaAPI(
  hubUrl: string,
  cookie: string,
  workerId: string,
  workspaceId: string,
): Promise<string> {
  return openAgentViaAPI(hubUrl, cookie, workerId, workspaceId, undefined, {
    optionValues: { permissionMode: 'default' },
  })
}

// ---- Hub API helpers (Workspace CRUD) ----
// These call the hub's WorkspaceService directly via HTTP.

/**
 * Create a workspace through WorkspaceService and return its ID.
 * Open the session subscription before creating the workspace, as the browser does.
 * The test then requires the hub to expand its subscription filter and broadcast the initial operations.
 * A subscription opened afterward would reload materialized state and hide lost operations for existing subscribers.
 */
export async function createWorkspaceViaAPI(
  hubUrl: string,
  cookie: string,
  title: string,
): Promise<string> {
  // Wait for the subscription before CreateWorkspace.
  // Its WebSocket must belong to the subscriber set before the lifecycle outbox publishes the initial operations.
  const { getUserEventsSubscription } = await import('./crdt')
  await getUserEventsSubscription(hubUrl, cookie)

  const data = await callHub<{ workspaceId?: string, workspace?: { id?: string } }>(
    hubUrl,
    'WorkspaceService/CreateWorkspace',
    { title },
    { cookie, operation: `createWorkspaceViaAPI(${JSON.stringify(title)})` },
  )
  const workspaceId = data.workspaceId ?? data.workspace?.id
  if (!workspaceId) {
    throw new Error('createWorkspaceViaAPI: no workspace ID in response')
  }
  return workspaceId
}

/** Delete a workspace and close the tabs that its online workers own. */
export async function deleteWorkspaceViaAPI(
  hubUrl: string,
  cookie: string,
  workspaceId: string,
): Promise<void> {
  const res = await hubRequest(hubUrl, 'WorkspaceService/DeleteWorkspace', { workspaceId }, { cookie })
  // Tests can delete a fixture workspace before fixture cleanup runs.
  if (res.status === 404)
    return
  // The failure names the workspace and holds the Hub's reason.
  // A failure that occurs only in the full suite must retain those diagnostics.
  await requireHubOk(res, 'WorkspaceService/DeleteWorkspace', `deleteWorkspaceViaAPI(${workspaceId})`)

  // The hub returns an atomic snapshot of owned tabs with the deletion.
  // A separate ListTabs request can miss tabs or arrive after the hub removes them.
  const { workerTabs } = fromJson(DeleteWorkspaceResponseSchema, await res.json())
  const groups = workerTabs.filter(worker => worker.tabs.length > 0)
  if (groups.length === 0)
    return
  for (const worker of groups) {
    if (!worker.workerId)
      throw new Error('Missing worker ID in workspace deletion response')
    for (const tab of worker.tabs) {
      if (!tab.tabId)
        throw new Error('Missing tab ID in workspace deletion response')
      if (tab.tabType === TabType.UNSPECIFIED || !Object.values(TabType).includes(tab.tabType))
        throw new Error(`Invalid tab type for ${tab.tabId} in workspace deletion response`)
    }
  }

  // Offline workers reconcile deleted workspaces when they reconnect.
  // Do not wait for a channel to a worker that the test deliberately stopped.
  const online = new Set(await listOnlineWorkerIDsViaAPI(hubUrl, cookie))
  const reachable = groups.filter(worker => online.has(worker.workerId))
  if (reachable.length === 0)
    return
  const channel = await getTestChannel(hubUrl, cookie)
  await finishCleanup(reachable.map(worker => channel.callWorker(
    worker.workerId,
    'CleanupWorkspace',
    CleanupWorkspaceRequestSchema,
    CleanupWorkspaceResponseSchema,
    { tabs: worker.tabs },
  )))
}

/**
 * List all workspaces for the authenticated user via the hub's WorkspaceService.
 */
export async function listWorkspacesViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<{ id: string }[]> {
  const data = await callHub<{ workspaces?: Array<{ id: string }> }>(
    hubUrl,
    'WorkspaceService/ListWorkspaces',
    {},
    { cookie, operation: 'listWorkspacesViaAPI' },
  )
  return data.workspaces ?? []
}

/**
 * Delete all workspaces of the authenticated user through the hub.
 * The function starts every delete, waits for all of them, and then throws one `AggregateError` that holds each
 * failure. `deleteWorkspaceViaAPI` accepts a workspace that is already gone and skips an offline Worker, so a failure
 * that remains is a real fault. A workspace that the reset leaves behind changes the sidebar of a later test.
 */
export async function deleteAllWorkspacesViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<void> {
  const workspaces = await listWorkspacesViaAPI(hubUrl, cookie)
  await finishCleanup(workspaces.map(workspace => deleteWorkspaceViaAPI(hubUrl, cookie, workspace.id)))
}

/** One hub setting that holds a stored value, as `ListSettings` reports it. The hub redacts each secret in `valueJson`. */
export interface CustomizedHubSetting {
  key: string
  valueJson: string
}

/** List the hub settings that hold a stored value. A setting at its code default is absent from the result. */
export async function listCustomizedHubSettingsViaAPI(hubUrl: string, cookie: string): Promise<CustomizedHubSetting[]> {
  const data = await callHub<{ values?: Array<{ key?: string, valueJson?: string, customized?: boolean }> }>(
    hubUrl,
    'AdminSettingsService/ListSettings',
    {},
    { cookie, operation: 'listCustomizedHubSettingsViaAPI' },
  )
  return (data.values ?? []).flatMap((value) => {
    if (!value.customized)
      return []
    if (!value.key)
      throw new Error('listCustomizedHubSettingsViaAPI received a customized setting with no key.')
    return [{ key: value.key, valueJson: value.valueJson ?? '' }]
  })
}

/**
 * Compare the customized hub settings with the baseline that the hub had when it started.
 * - `reset`: the keys that the baseline does not hold. A reset returns each one to its code default.
 * - `changedBaseline`: the baseline keys whose stored value differs, or that lost their stored value.
 */
export function hubSettingsDrift(
  current: readonly CustomizedHubSetting[],
  baseline: readonly CustomizedHubSetting[],
): { reset: string[], changedBaseline: string[] } {
  const baselineValues = new Map(baseline.map(setting => [setting.key, setting.valueJson]))
  const currentValues = new Map(current.map(setting => [setting.key, setting.valueJson]))
  return {
    reset: current.filter(setting => !baselineValues.has(setting.key)).map(setting => setting.key),
    changedBaseline: baseline.filter(setting => currentValues.get(setting.key) !== setting.valueJson).map(setting => setting.key),
  }
}

/**
 * Return each hub setting to the state that it had when the hub started.
 *
 * The suite shares one hub across all tests, so a setting survives its test and can change the result of the next one.
 * The hub stores some settings itself when it starts, such as the generated captcha key, and `baseline` lists them.
 * The function leaves a baseline setting alone, because a reset makes the hub generate a new value. It fails for a
 * baseline setting whose stored value differs, because it cannot restore a value that the hub generated.
 *
 * A write to a hub setting needs an elevated session, and a test can drop the elevation of the shared session. So the
 * function elevates the session before the reset. It resets all keys in one transaction, and it sends no write when no
 * key needs a reset.
 */
export async function resetHubSettingsViaAPI(
  hubUrl: string,
  cookie: string,
  options: { baseline: readonly CustomizedHubSetting[], password: string },
): Promise<void> {
  const { reset, changedBaseline } = hubSettingsDrift(await listCustomizedHubSettingsViaAPI(hubUrl, cookie), options.baseline)
  if (changedBaseline.length > 0) {
    throw new Error(`The hub settings ${changedBaseline.join(', ')} differ from their values at the start of the run. `
      + 'The reset cannot restore a value that the hub generated, so restore it in the test that changed it.')
  }
  if (reset.length === 0)
    return
  await elevateSessionViaAPI(hubUrl, cookie, options.password)
  await callHub(hubUrl, 'AdminSettingsService/ResetSettings', { keys: reset }, { cookie, operation: `resetHubSettingsViaAPI(${reset.join(', ')})` })
}

/**
 * Reset each customized account setting to its default.
 * The suite shares one account across all tests. A setting survives its test and can change the next test's result.
 * For example, a separate terminal palette enables the theme mode control that 196-pill-group expects to remain disabled.
 * Reset only the keys that customized identifies. A clean account then needs one request instead of one request for each setting.
 */
export async function resetAllUserSettingsViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<void> {
  const data = await callHub<{ values?: Array<{ key?: string, customized?: boolean }> }>(
    hubUrl,
    'UserService/ListUserSettings',
    {},
    { cookie, operation: 'resetAllUserSettingsViaAPI' },
  )
  for (const value of data.values ?? []) {
    if (!value.customized || !value.key)
      continue
    await callHub(hubUrl, 'UserService/ResetUserSetting', { key: value.key }, { cookie, operation: `resetAllUserSettingsViaAPI(${value.key})` })
  }
}

/** Clear SMTP so EmailVerificationEffective turns off. */
export async function clearSmtpViaAPI(hubUrl: string, adminCookie: string): Promise<void> {
  await callHub(hubUrl, 'AdminSettingsService/ResetSetting', { key: 'smtp' }, { cookie: adminCookie, operation: 'clearSmtpViaAPI' })
}

/**
 * The SMTP relay that `configureBrokenSmtpViaAPI` sets: port 1 on loopback, where nothing listens, so every send fails.
 */
const UNREACHABLE_SMTP_RELAY: SmtpCaptureTarget = { host: '127.0.0.1', port: 1 }

/** Point SMTP at an unreachable host so verification sends fail closed. */
export async function configureBrokenSmtpViaAPI(
  hubUrl: string,
  adminCookie: string,
): Promise<void> {
  await configureCaptureSmtpViaAPI(hubUrl, adminCookie, UNREACHABLE_SMTP_RELAY)
}
