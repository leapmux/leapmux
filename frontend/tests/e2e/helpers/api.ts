// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

import type { ChannelManager } from '../../../src/lib/channel'
import { solveCaptchaViaAPI } from './altcha'
import { createTestChannelManager } from './e2e-channel'

/**
 * Poll interval for the API-backed waits below.
 *
 * Each tick is a real HTTP round trip to the hub (plus, for the agent-list
 * helpers, an E2EE callWorker on top), landing on the same instance whose
 * latency the wait is measuring. What these wait on -- `git worktree add`, a
 * CLI spawn, a second worker process handshaking -- settles on a second scale,
 * so a 25ms tick was ~40x oversampling: a single 30s wait could fire ~2400
 * requests into the process it was waiting for. 150ms keeps the latency win
 * (the flat sleeps this replaced cost 200-500ms each) at a fraction of the
 * request volume.
 */
export const API_POLL_INTERVAL_MS = 150

// ---- E2EE channel cache ----
// Keeps a ChannelManager per hubUrl+cookie pair to avoid re-handshaking
// on every test API call.

const channelManagerCache = new Map<string, ChannelManager>()
const channelManagerPending = new Map<string, Promise<ChannelManager>>()

async function getTestChannel(hubUrl: string, cookie: string): Promise<ChannelManager> {
  const key = `${hubUrl}|${cookie}`
  const cached = channelManagerCache.get(key)
  if (cached) {
    return cached
  }
  // Deduplicate concurrent initialization for the same key.
  let pending = channelManagerPending.get(key)
  if (!pending) {
    pending = createTestChannelManager(hubUrl, cookie).then((mgr) => {
      channelManagerCache.set(key, mgr)
      channelManagerPending.delete(key)
      return mgr
    })
    channelManagerPending.set(key, pending)
  }
  return pending
}

export { getTestChannel }

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
 * Get the current user's ID via the Connect API. Which user that is comes
 * entirely from `cookie` -- there is no separate "admin" lookup, since admin
 * is a property of the session the caller supplies, not of the endpoint.
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
 * Open self-serve sign-up on a hub, as an admin.
 *
 * A hub started with `leapmux hub` resolves `signup_enabled` from its STORED
 * row, and the code default is closed. Only `leapmux dev` reports it open, and
 * only while no operator row exists (`SignupEnabledEffective`). So a fixture
 * that spawns a plain hub and then signs a second account up has to store the
 * row first, or the hub answers `failed_precondition: sign-up is disabled`.
 *
 * The first account is exempt and needs no call here: a hub with no users at
 * all accepts one sign-up and makes it an administrator.
 */
export async function enableSignupViaAPI(hubUrl: string, cookie: string): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminSettingsService/UpdateSetting`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ key: 'signup_enabled', partialJson: 'true' }),
  })
  if (!res.ok) {
    throw new Error(`enableSignupViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

export interface SmtpCaptureTarget {
  host: string
  port: number
}

/**
 * Point the hub at a loopback capture SMTP relay. Verification gating follows
 * SMTP once host and from_address are both present.
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

/** Delete one passkey with the account password. */
export async function deletePasskeyViaAPI(
  hubUrl: string,
  cookie: string,
  passkeyId: string,
  currentPassword: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/DeletePasskey`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ id: passkeyId, currentPassword }),
  })
  if (!res.ok) {
    throw new Error(`deletePasskeyViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

/**
 * Backdate a user's pending-email row so ResendVerificationEmail cooldown
 * has elapsed (signup issues a code immediately; resend is blocked for 60s).
 */
export async function backdatePendingEmailIssuedAt(hubDataDir: string, username: string): Promise<void> {
  const { execFile } = await import('node:child_process')
  const { promisify } = await import('node:util')
  const { join } = await import('node:path')
  const execFileAsync = promisify(execFile)
  const dbPath = join(hubDataDir, 'hub.db')
  const escaped = username.replace(/'/g, `''`)
  // pending_email_expires_at = issued_at + 30m; set issued ~2m ago.
  // Retry on SQLITE_BUSY: the hub may hold a write lock briefly.
  const sql = `UPDATE users SET pending_email_expires_at = datetime('now', '+28 minutes') WHERE username = '${escaped}' AND deleted_at IS NULL;`
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
  const res = await fetch(`${hubUrl}/leapmux.v1.UserService/VerifyEmail`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ verificationToken }),
  })
  if (!res.ok) {
    throw new Error(`verifyEmailViaAPI failed: ${res.status} ${await res.text()}`)
  }
}

/** Request a password-reset email via the public AuthService RPC. */
export async function requestPasswordResetViaAPI(
  hubUrl: string,
  identifier: string,
): Promise<void> {
  const captcha = await solveCaptchaViaAPI(hubUrl)
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/RequestPasswordReset`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ identifier, captchaPayload: captcha.captchaPayload, honeypot: captcha.honeypot }),
  })
  if (!res.ok) {
    throw new Error(`requestPasswordResetViaAPI failed: ${res.status} ${await res.text()}`)
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
 * Poll `ListWorkers` until a worker that was NOT in `before` shows
 * up online and return its ID. Mirrors `multiWorker.waitForNewOnlineWorker`.
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
    createWorktree?: boolean
    worktreeBranch?: string
    worktreeBaseBranch?: string
    checkoutBranch?: string
    useWorktreePath?: string
    agentProvider?: number
    /**
     * Optional initial tab title. UI-driven opens pick a name via
     * `pickAgentTitle`; the API path leaves `title=""` by default so
     * tests that need a visible non-empty title (e.g. for
     * cross-workspace move regression coverage where the bug strips
     * exactly this field) must opt in explicitly.
     */
    title?: string
  },
): Promise<string> {
  const { OpenAgentRequestSchema, OpenAgentResponseSchema } = await import('../../../src/generated/leapmux/v1/agent_pb')
  const channel = await getTestChannel(hubUrl, cookie)

  // No workspace announcement. A channel carries no workspace set at all, so a
  // workspace created after the cached ChannelManager handshook needs nothing
  // done to it before a worker RPC on its tabs will be served.
  const resp = await channel.callWorker(
    workerId,
    'OpenAgent',
    OpenAgentRequestSchema,
    OpenAgentResponseSchema,
    {
      workerId,
      workingDir: workingDir ?? '',
      ...(options?.title ? { title: options.title } : {}),
      // The proto carries model under the `options` map, not a top-level `model`
      // field; spreading `{ model }` would be silently dropped by create(), opening
      // the agent at the provider default instead of the requested model.
      ...(options?.model ? { options: { model: options.model } } : {}),
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

  // Seed the tab into the CRDT so UI-driven tests find a rendered
  // tab on a freshly-API-seeded workspace. Mirrors what
  // `tabStore.addTab` emits during a browser-driven openAgent flow:
  // SetTabRegister(tile_id=root_node_id) + position + worker_id.
  //
  // The seed uses the SHARED `UserEventsSubscription` opened by
  // `createWorkspaceViaAPI` BEFORE the workspace existed. That
  // subscription's state is populated by the hub's broadcast of the
  // seed `SetWorkspaceRootNode` op (or the `WorkspaceCreated` event),
  // exactly like the browser's long-lived `/ws/userevents`. A
  // workspace where the hub failed to deliver those events surfaces
  // here as an `awaitRootNodeId` timeout — same diagnostic the user
  // would see in production (empty workspace, missing agent tab).
  const { seedTabIntoWorkspace, getUserEventsSubscription } = await import('./crdt')
  const { TabType } = await import('../../../src/generated/leapmux/v1/workspace_pb')
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

// ---- Hub API helpers (Workspace CRUD) ----
// These call the hub's WorkspaceService directly via HTTP.

/**
 * Create a workspace via the hub's WorkspaceService. Returns the workspace ID.
 *
 * Warms the per-(hub, session) `UserEventsSubscription` BEFORE dispatching
 * the create RPC. This makes the test fixture mirror the production
 * browser flow: a long-lived `/ws/userevents` subscription is already
 * attached at the moment the workspace is created, so the hub-side
 * seed-ops broadcast (and its filter-expansion contract) is on the
 * critical path of the test. Opening the subscription AFTER the
 * create would re-bootstrap from the materialized state and mask
 * regressions where the seed ops are dropped for existing
 * subscribers.
 */
export async function createWorkspaceViaAPI(
  hubUrl: string,
  cookie: string,
  title: string,
): Promise<string> {
  // Establish the subscription FIRST so the hub's broadcast of the
  // lifecycle-create's seed batch lands on it. Awaiting the open
  // here guarantees the WebSocket is in the manager's subscriber set
  // by the time the CreateWorkspace RPC reaches the lifecycle
  // outbox.
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

/**
 * Stop and close every agent/terminal a workspace owns on its worker — the
 * worker-side half of a workspace delete.
 *
 * The browser app's delete flow is two steps: the hub soft-deletes the workspace
 * (returning worker IDs), then the frontend fans out a `CleanupWorkspace` E2EE RPC
 * to each worker (useWorkspaceOperations.deleteWorkspace), which stops the agent
 * subprocesses. `deleteWorkspaceViaAPI` only does the hub half, so without this the
 * worker keeps every test's Claude CLI subprocess alive; across a suite they pile up
 * (observed peak ~17 concurrent) and starve local resources, which makes the live
 * frontend janky enough to flake settings-menu interactions. Run this at teardown to
 * mirror what the real client does. Best-effort; reuses the cached test channel,
 * which already has the workspace in its accessible set from openAgentViaAPI.
 */
export async function cleanupWorkspaceViaAPI(
  hubUrl: string,
  cookie: string,
  workerId: string,
  workspaceId: string,
): Promise<void> {
  const { CleanupWorkspaceRequestSchema, CleanupWorkspaceResponseSchema } = await import('../../../src/generated/leapmux/v1/workspace_pb')
  const channel = await getTestChannel(hubUrl, cookie)
  // The worker tracks no workspace id, so the CALLER supplies the tab list --
  // read from the hub here, exactly as the browser reads it from its projection.
  // Must run BEFORE the hub-side delete, while the tabs are still listable.
  const tabs = await listTabsViaAPI(hubUrl, cookie, workspaceId)
  await channel.callWorker(
    workerId,
    'CleanupWorkspace',
    CleanupWorkspaceRequestSchema,
    CleanupWorkspaceResponseSchema,
    { tabs: tabs.filter(t => t.workerId === workerId).map(t => ({ tabType: t.tabType, tabId: t.tabId })) },
  )
}

/**
 * The tabs the hub lists for a workspace, as (tab_type, tab_id, worker_id).
 * Used by cleanupWorkspaceViaAPI, which has to name them explicitly.
 *
 * `tab_type` arrives as the enum NAME, not its number: this is Connect's JSON
 * codec (protojson), which serializes enums as `"TAB_TYPE_AGENT"`. Reading it
 * as a number yielded a string that then failed to encode into
 * `CleanupWorkspaceRequest`, so every teardown asked the worker to close
 * TAB_TYPE_UNSPECIFIED tabs, the worker's switch fell to `default:`, and
 * nothing was closed — leaving every spec's agent subprocess alive on the
 * shared worker. Mapped back to the numeric enum here so the caller can build
 * a real request.
 *
 * A failed read THROWS rather than degrading to an empty list. An empty list is
 * a valid request meaning "close nothing", so swallowing the error would make
 * teardown report success while leaving every process running — the precise
 * failure this helper exists to prevent.
 */
async function listTabsViaAPI(
  hubUrl: string,
  cookie: string,
  workspaceId: string,
): Promise<{ tabType: number, tabId: string, workerId: string }[]> {
  const { TabType } = await import('../../../src/generated/leapmux/v1/workspace_pb')
  const tabTypeByName: Record<string, number> = {
    TAB_TYPE_UNSPECIFIED: TabType.UNSPECIFIED,
    TAB_TYPE_AGENT: TabType.AGENT,
    TAB_TYPE_TERMINAL: TabType.TERMINAL,
    TAB_TYPE_FILE: TabType.FILE,
  }
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListTabs`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workspaceIds: [workspaceId] }),
  })
  if (!res.ok)
    throw new Error(`listTabsViaAPI failed: ${res.status} ${await res.text()}`)
  const body = await res.json() as { tabs?: { tabType?: string, tabId?: string, workerId?: string }[] }
  return (body.tabs ?? [])
    .filter(t => t.tabId)
    .map((t) => {
      const tabType = tabTypeByName[t.tabType ?? '']
      if (tabType === undefined)
        throw new Error(`listTabsViaAPI: unrecognized tab_type ${JSON.stringify(t.tabType)} for tab ${t.tabId}`)
      return { tabType, tabId: t.tabId!, workerId: t.workerId ?? '' }
    })
}

/**
 * Delete (soft-delete) a workspace via the hub's WorkspaceService.
 */
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
  if (!res.ok) {
    throw new Error(`deleteWorkspaceViaAPI failed: ${res.status}`)
  }
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
