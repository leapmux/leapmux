// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

import type { ChannelManager } from '../../../src/lib/channel'
import { fromJson } from '@bufbuild/protobuf'
import { CleanupWorkspaceRequestSchema, CleanupWorkspaceResponseSchema, DeleteWorkspaceResponseSchema, TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
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

const SESSION_COOKIE_NAME = 'leapmux-session'

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

// ---- Hub API helpers (Auth, Admin, Worker management) ----

/**
 * Sign in through the Connect API. Return the session cookie for later requests, such as "leapmux-session=abc123".
 */
export async function loginViaAPI(hubUrl: string, username: string, password: string): Promise<string> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/Login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password, captchaPayload: captcha.captchaPayload, honeypot: captcha.honeypot }),
    redirect: 'manual',
  })
  if (!res.ok) {
    throw new Error(`loginViaAPI failed: ${res.status}`)
  }
  return extractSessionCookie(res.headers.get('set-cookie'))
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/GetCurrentUser`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`getCurrentUser failed: ${res.status}`)
  }
  const data = await res.json() as { user: ApiUser }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/SignUp`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password, displayName, email, captchaPayload: captcha.captchaPayload, honeypot: captcha.honeypot }),
    redirect: 'manual',
  })
  if (!res.ok) {
    throw new Error(`signUpViaAPI failed: ${res.status}`)
  }
  return extractSessionCookie(res.headers.get('set-cookie'))
}

/**
 * Get the first worker ID from the ListWorkers API.
 */
export async function getWorkerId(hubUrl: string, cookie: string): Promise<string> {
  const deadline = Date.now() + 30_000
  while (true) {
    const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
      method: 'POST',
      headers: authedHeaders(cookie),
      body: JSON.stringify({}),
    })
    if (!res.ok) {
      throw new Error(`getWorkerId failed: ${res.status}`)
    }
    const data = await res.json() as { workers: Array<{ id: string, online: boolean }> }
    // Wait until the database stores the Worker and its bidirectional stream connects.
    const firstWorker = data.workers?.[0]
    if (firstWorker?.online) {
      return firstWorker.id
    }
    if (Date.now() >= deadline) {
      throw new Error('Worker never came online within 30s')
    }
    await new Promise(r => setTimeout(r, API_POLL_INTERVAL_MS))
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
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/DeregisterWorker`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workerId }),
  })
  if (!res.ok) {
    throw new Error(`deregisterWorkerViaAPI failed: ${res.status}`)
  }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/UpdateSetting`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ key, partialJson }),
  })
  if (!res.ok) {
    throw new Error(`updateSettingViaAPI(${key}) failed: ${res.status} ${await res.text()}`)
  }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/UpdateSetting`, {
    method: 'POST',
    headers: authedHeaders(adminCookie),
    body: JSON.stringify({
      key: 'smtp',
      partialJson: JSON.stringify({
        host: relay.host,
        port: relay.port,
        from_address: fromAddress,
        tls_mode: 'none',
      }),
    }),
  })
  if (!res.ok) {
    throw new Error(`configureCaptureSmtpViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

/** Poll GetSystemInfo until emailEnabled reflects the staged SMTP block. */
export async function waitForEmailEnabled(hubUrl: string, timeoutMs = 30_000): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/GetSystemInfo`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({}),
    })
    if (res.ok) {
      const data = await res.json() as { emailEnabled?: boolean }
      if (data.emailEnabled)
        return
    }
    await new Promise(r => setTimeout(r, API_POLL_INTERVAL_MS))
  }
  throw new Error('waitForEmailEnabled: hub never reported emailEnabled=true')
}

export interface PasskeySummary {
  id: string
  friendlyName: string
}

/** List passkeys registered for the authenticated user. */
export async function listPasskeysViaAPI(hubUrl: string, cookie: string): Promise<PasskeySummary[]> {
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/ListPasskeys`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`listPasskeysViaAPI failed: ${res.status} ${await res.text()}`)
  }
  const data = await res.json() as { passkeys?: Array<{ id?: string, friendlyName?: string }> }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/ElevateSession`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ currentPassword }),
  })
  if (!res.ok) {
    throw new Error(`elevateSessionViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

/** Delete one passkey. The session must already be elevated. */
export async function deletePasskeyViaAPI(
  hubUrl: string,
  cookie: string,
  passkeyId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/DeletePasskey`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ id: passkeyId }),
  })
  if (!res.ok) {
    throw new Error(`deletePasskeyViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

/**
 * The marker identifies a refusal that permits elevation and another attempt.
 * A bearer credential that cannot elevate receives FailedPrecondition without this marker.
 * Status alone cannot distinguish that permanent refusal from a request to prove a factor.
 */
export const ELEVATION_REQUIRED_HEADER = 'leapmux-elevation-required'

/**
 * Attempt a passkey delete and return the raw response, so a test can read
 * the refusal's headers as well as its status.
 */
export async function deletePasskeyResponse(
  hubUrl: string,
  cookie: string,
  passkeyId: string,
): Promise<Response> {
  return await fetch(`${hubUrl}/leapmux.v1.UserService/DeletePasskey`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ id: passkeyId }),
  })
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
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/ListMyAPITokens`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`listMyAPITokensViaAPI failed: ${res.status} ${await res.text()}`)
  }
  const data = await res.json() as {
    tokens?: Array<{
      id?: string
      clientName?: string
      installationName?: string
      grantedScopes?: string[]
      current?: boolean
    }>
  }
  return (data.tokens ?? []).map(t => ({
    id: t.id ?? '',
    clientName: t.clientName ?? '',
    installationName: t.installationName ?? '',
    grantedScopes: t.grantedScopes ?? [],
    current: t.current === true,
  }))
}

/**
 * Backdate the pending-email row so the ResendVerificationEmail cooldown ends.
 * Signup issues a code immediately. The Hub blocks another code for 60 seconds.
 */
export async function expirePendingEmailCooldown(hubDataDir: string, username: string): Promise<void> {
  const { execFile } = await import('node:child_process')
  const { promisify } = await import('node:util')
  const { join } = await import('node:path')
  const execFileAsync = promisify(execFile)
  const dbPath = join(hubDataDir, 'hub.db')
  const escaped = username.replace(/'/g, `''`)
  // The cooldown compares pending_email_unblocked_at as text. Use the same strftime format as the hub.
  // SQLite datetime() puts a space where the stored format uses T. That format difference can pass the comparison regardless of elapsed time.
  // A test with mixed formats cannot prove that an expired cooldown permits resend.
  // Retry SQLITE_BUSY because the hub can briefly hold a write lock.
  const sql = `UPDATE users SET pending_email_unblocked_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now', '-2 minutes') WHERE username = '${escaped}' AND deleted_at IS NULL;`
  let lastErr: unknown
  for (let attempt = 0; attempt < 8; attempt++) {
    try {
      await execFileAsync('sqlite3', [dbPath, sql])
      return
    }
    catch (err) {
      lastErr = err
      const msg = err instanceof Error ? err.message : String(err)
      if (!/database is locked|SQLITE_BUSY/i.test(msg))
        throw err
      await new Promise(r => setTimeout(r, 50 * (attempt + 1)))
    }
  }
  throw lastErr
}

export async function readPendingEmailToken(hubDataDir: string, username: string): Promise<string> {
  const { execFile } = await import('node:child_process')
  const { promisify } = await import('node:util')
  const { join } = await import('node:path')
  const execFileAsync = promisify(execFile)
  const dbPath = join(hubDataDir, 'hub.db')
  const escaped = username.replace(/'/g, `''`)
  const { stdout } = await execFileAsync('sqlite3', [
    dbPath,
    `SELECT pending_email_token FROM users WHERE username = '${escaped}' AND deleted_at IS NULL;`,
  ])
  const token = stdout.trim()
  if (!token)
    throw new Error(`no pending_email_token for username ${username}`)
  return token
}

/** Verify the session user's pending email with a code from the DB or inbox. */
export async function verifyEmailViaAPI(hubUrl: string, cookie: string, verificationToken: string): Promise<void> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/VerifyEmail`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ verificationToken, captchaPayload: captcha.captchaPayload, honeypot: captcha.honeypot }),
  })
  if (!res.ok) {
    throw new Error(`verifyEmailViaAPI failed: ${res.status} ${await res.text()}`)
  }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/CreateRegistrationKey`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: '{}',
  })
  if (!res.ok) {
    throw new Error(`mintRegistrationKeyViaAPI failed: ${res.status} ${await res.text()}`)
  }
  const data = await res.json() as { registrationKey?: string }
  if (!data.registrationKey)
    throw new Error('mintRegistrationKeyViaAPI: empty key in response')
  return data.registrationKey
}

/**
 * Poll ListWorkers until an online worker ID appears outside the supplied before set.
 * Return that new worker ID.
 */
export async function waitForNewOnlineWorkerViaAPI(
  hubUrl: string,
  cookie: string,
  before: Set<string>,
  timeoutMs = 30_000,
  signal?: AbortSignal,
): Promise<string> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    signal?.throwIfAborted()
    const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
      method: 'POST',
      headers: authedHeaders(cookie),
      body: '{}',
      ...(signal ? { signal } : {}),
    })
    signal?.throwIfAborted()
    if (res.ok) {
      const data = await res.json() as { workers?: Array<{ id: string, online: boolean }> }
      signal?.throwIfAborted()
      const online = (data.workers ?? []).filter(w => w.online).map(w => w.id)
      const fresh = online.find(id => !before.has(id))
      if (fresh)
        return fresh
    }
    if (Date.now() >= deadline)
      throw new Error(`waitForNewOnlineWorkerViaAPI: no new worker came online within ${timeoutMs}ms`)
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
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: '{}',
  })
  if (!res.ok)
    throw new Error(`listOnlineWorkerIDsViaAPI: ListWorkers ${res.status}`)
  const data = await res.json() as { workers?: Array<{ id: string, online: boolean }> }
  return (data.workers ?? []).filter(w => w.online).map(w => w.id)
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
  const { TabType } = await import('../../../src/generated/proto/leapmux/v1/workspace_pb')
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

  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ title }),
  })
  if (!res.ok) {
    throw new Error(`createWorkspaceViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workspaceId?: string, workspace?: { id?: string } }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/DeleteWorkspace`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workspaceId }),
  })
  // Tests can delete a fixture workspace before fixture cleanup runs.
  if (res.status === 404)
    return
  // Include the body in a cleanup failure. The status alone gives neither the workspace ID nor the Hub's reason.
  // A failure that occurs only in the full suite must retain those diagnostics.
  if (!res.ok)
    throw new Error(`deleteWorkspaceViaAPI(${workspaceId}) failed: ${res.status} ${await res.text()}`)

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
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListWorkspaces`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`listWorkspacesViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workspaces?: Array<{ id: string }> }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/ListSettings`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: '{}',
  })
  if (!res.ok)
    throw new Error(`listCustomizedHubSettingsViaAPI failed: ${res.status} ${await res.text()}`)
  const data = await res.json() as { values?: Array<{ key?: string, valueJson?: string, customized?: boolean }> }
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/ResetSettings`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ keys: reset }),
  })
  if (!res.ok)
    throw new Error(`resetHubSettingsViaAPI could not reset ${reset.join(', ')}: ${res.status} ${await res.text()}`)
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
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/ListUserSettings`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: '{}',
  })
  if (!res.ok)
    throw new Error(`resetAllUserSettingsViaAPI could not list: ${res.status} ${await res.text()}`)
  const data = await res.json() as { values?: Array<{ key?: string, customized?: boolean }> }
  for (const value of data.values ?? []) {
    if (!value.customized || !value.key)
      continue
    const reset = await fetch(`${hubUrl}/leapmux.v1.UserService/ResetUserSetting`, {
      method: 'POST',
      headers: authedHeaders(cookie),
      body: JSON.stringify({ key: value.key }),
    })
    if (!reset.ok)
      throw new Error(`resetAllUserSettingsViaAPI could not reset ${value.key}: ${reset.status} ${await reset.text()}`)
  }
}

/** Clear SMTP so EmailVerificationEffective turns off. */
export async function clearSmtpViaAPI(hubUrl: string, adminCookie: string): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/ResetSetting`, {
    method: 'POST',
    headers: authedHeaders(adminCookie),
    body: JSON.stringify({ key: 'smtp' }),
  })
  if (!res.ok) {
    throw new Error(`clearSmtpViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

/** Point SMTP at an unreachable host so verification sends fail closed. */
export async function configureBrokenSmtpViaAPI(
  hubUrl: string,
  adminCookie: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/UpdateSetting`, {
    method: 'POST',
    headers: authedHeaders(adminCookie),
    body: JSON.stringify({
      key: 'smtp',
      partialJson: JSON.stringify({
        host: '127.0.0.1',
        port: 1,
        from_address: 'hub@test.local',
        tls_mode: 'none',
      }),
    }),
  })
  if (!res.ok) {
    throw new Error(`configureBrokenSmtpViaAPI failed: ${res.status} ${await res.text()}`)
  }
}
