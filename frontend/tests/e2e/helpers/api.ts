// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

import type { ChannelManager } from '../../../src/lib/channel'
import { fromJson } from '@bufbuild/protobuf'
import { CleanupWorkspaceRequestSchema, CleanupWorkspaceResponseSchema, DeleteWorkspaceResponseSchema, TabType } from '../../../src/generated/proto/leapmux/v1/workspace_pb'
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

// ---- E2EE channel cache ----
// Keeps a ChannelManager per hubUrl+cookie pair to avoid re-handshaking
// on every test API call.

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
// The first-admin user seeded by e2e fixtures via /setup mode. Mirrors the
// backend's testutil.TestAdminUsername / TestAdminPassword.

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
 * Login via the Connect API. Returns the session cookie string
 * (e.g. "leapmux-session=abc123") for use in subsequent requests.
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
    // Wait until the worker is registered in the DB and its bidi-stream is connected.
    if (data.workers?.length && data.workers[0].online) {
      return data.workers[0].id
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
 * A standalone hub defaults to closed signup. Store signup_enabled before registering a second account, or signup returns failed_precondition.
 * Dev mode defaults to open signup only while no explicit setting exists.
 * The first account is exempt. A hub without users accepts its signup and makes it an administrator.
 */
export async function enableSignupViaAPI(hubUrl: string, cookie: string): Promise<void> {
  await updateSettingViaAPI(hubUrl, cookie, 'signup_enabled', 'true')
}

/**
 * Write one hub setting through an elevated administrator session. Elevate the session before this call.
 * partialJson follows UpdateSetting: a scalar uses a JSON value, and a structured setting uses an object with only changed fields.
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
 * Point the hub at a loopback capture SMTP relay. The hub requires email
 * verification as soon as host and from_address are both present.
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
 * Every sensitive UserService call needs this first: the step-up is a
 * property of the SESSION now, not a secret carried on each request.
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
 * Backdate a user's pending-email row so the ResendVerificationEmail
 * cooldown already ended (signup issues a code immediately; the hub blocks a
 * resend for 60s).
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
 * Mint a registration key as an authenticated user. Mirrors the
 * production UI flow: an admin (or any authorized user) calls
 * `WorkerManagementService.CreateRegistrationKey` and hands the
 * resulting key to the worker process via `--registration-key`.
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
): Promise<string> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
      method: 'POST',
      headers: authedHeaders(cookie),
      body: '{}',
    })
    if (res.ok) {
      const data = await res.json() as { workers?: Array<{ id: string, online: boolean }> }
      const online = (data.workers ?? []).filter(w => w.online).map(w => w.id)
      const fresh = online.find(id => !before.has(id))
      if (fresh)
        return fresh
    }
    if (Date.now() >= deadline)
      throw new Error(`waitForNewOnlineWorkerViaAPI: no new worker came online within ${timeoutMs}ms`)
    await new Promise(r => setTimeout(r, API_POLL_INTERVAL_MS))
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

// ---- Worker E2EE helpers (Agent) ----

/**
 * Open an agent via E2EE channel to the Worker and register the tab on the hub.
 * Returns the agent ID.
 */
export async function openAgentViaAPI(
  hubUrl: string,
  cookie: string,
  workerId: string,
  workspaceId: string,
  workingDir?: string,
  options?: {
    model?: string
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
     * Supply a title explicitly when a test requires visible text, such as a test that detects title loss during a workspace move.
     */
    title?: string
  },
): Promise<string> {
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
 * Without an explicit mode, Claude requests Auto Mode and the installed CLI decides whether it is available.
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
  if (!res.ok)
    throw new Error(`deleteWorkspaceViaAPI failed: ${res.status}`)

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
 * Delete all workspaces for the authenticated user via the hub (best effort).
 */
export async function deleteAllWorkspacesViaAPI(
  hubUrl: string,
  cookie: string,
): Promise<void> {
  const workspaces = await listWorkspacesViaAPI(hubUrl, cookie)
  for (const ws of workspaces) {
    await deleteWorkspaceViaAPI(hubUrl, cookie, ws.id).catch(() => {})
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
