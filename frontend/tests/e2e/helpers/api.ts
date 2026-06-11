// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

import type { ChannelManager } from '../../../src/lib/channel'
import { createTestChannelManager } from './e2e-channel'

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

// ---- Hub API helpers (Auth, Org, Admin, Worker management) ----

/**
 * Login via the Connect API. Returns the session cookie string
 * (e.g. "leapmux-session=abc123") for use in subsequent requests.
 */
export async function loginViaAPI(hubUrl: string, username: string, password: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/Login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
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
  orgId: string
  orgName: string
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
 * Get the current user's ID via the Connect API.
 */
export async function getUserId(hubUrl: string, cookie: string): Promise<string> {
  return (await getCurrentUser(hubUrl, cookie)).id
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
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/SignUp`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password, displayName, email }),
    redirect: 'manual',
  })
  if (!res.ok) {
    throw new Error(`signUpViaAPI failed: ${res.status}`)
  }
  return extractSessionCookie(res.headers.get('set-cookie'))
}

/**
 * Get the admin user's personal org ID via the Connect API.
 */
export async function getAdminOrgId(hubUrl: string, cookie: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.OrgService/ListMyOrgs`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`getAdminOrgId failed: ${res.status}`)
  }
  const data = await res.json() as { orgs: Array<{ id: string, name: string, isPersonal?: boolean }> }
  const org = data.orgs.find(o => o.isPersonal) ?? data.orgs[0]
  if (!org) {
    throw new Error('No org found for admin user')
  }
  return org.id
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
    await new Promise(r => setTimeout(r, 500))
  }
}

/**
 * Invite a user to an org via the Connect API.
 */
export async function inviteToOrgViaAPI(
  hubUrl: string,
  cookie: string,
  orgId: string,
  username: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.OrgService/InviteOrgMember`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ orgId, username, role: 'ORG_MEMBER_ROLE_MEMBER' }),
  })
  if (!res.ok) {
    throw new Error(`inviteToOrgViaAPI failed: ${res.status}`)
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
    await new Promise(r => setTimeout(r, 500))
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

  // Notify the worker that the test channel has access to this workspace
  // BEFORE issuing any workspace-scoped RPC. getTestChannel caches a
  // ChannelManager across tests, so on the 2nd+ test the channel's
  // AccessibleWorkspaceIds set was frozen at handshake time and does not
  // include the workspace created in this test. The worker's hardened
  // requireAccessibleWorkspace check would reject OpenAgent until the
  // ChannelAccessUpdate lands. Calling PrepareWorkspaceAccess first — which
  // now blocks on the worker's ack — guarantees the set is up-to-date.
  const prepResp = await fetch(`${hubUrl}/leapmux.v1.ChannelService/PrepareWorkspaceAccess`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workerId, workspaceId }),
  })
  if (!prepResp.ok) {
    throw new Error(`openAgentViaAPI: PrepareWorkspaceAccess failed: ${prepResp.status}`)
  }

  const resp = await channel.callWorker(
    workerId,
    'OpenAgent',
    OpenAgentRequestSchema,
    OpenAgentResponseSchema,
    {
      workspaceId,
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
  // The seed uses the SHARED `OrgEventsSubscription` opened by
  // `createWorkspaceViaAPI` BEFORE the workspace existed. That
  // subscription's state is populated by the hub's broadcast of the
  // seed `SetWorkspaceRootNode` op (or the `WorkspaceCreated` event),
  // exactly like the browser's long-lived `/ws/orgevents`. A
  // workspace where the hub failed to deliver those events surfaces
  // here as an `awaitRootNodeId` timeout — same diagnostic the user
  // would see in production (empty workspace, missing agent tab).
  const orgId = await getAdminOrgId(hubUrl, cookie)
  const { seedTabIntoWorkspace, getOrgEventsSubscription } = await import('./crdt')
  const { TabType } = await import('../../../src/generated/leapmux/v1/workspace_pb')
  const orgEvents = await getOrgEventsSubscription(hubUrl, cookie, orgId)
  await seedTabIntoWorkspace({
    hubUrl,
    cookie,
    orgId,
    workspaceId,
    tabType: TabType.AGENT,
    tabId: resp.agent.id,
    workerId,
    orgEvents,
  })
  return resp.agent.id
}

// ---- Hub API helpers (Workspace CRUD) ----
// These call the hub's WorkspaceService directly via HTTP.

/**
 * Create a workspace via the hub's WorkspaceService. Returns the workspace ID.
 *
 * Warms the per-(hub, org) `OrgEventsSubscription` BEFORE dispatching
 * the create RPC. This makes the test fixture mirror the production
 * browser flow: a long-lived `/ws/orgevents` subscription is already
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
  orgId: string,
): Promise<string> {
  // Establish the subscription FIRST so the hub's broadcast of the
  // lifecycle-create's seed batch lands on it. Awaiting the open
  // here guarantees the WebSocket is in the manager's subscriber set
  // by the time the CreateWorkspace RPC reaches the lifecycle
  // outbox.
  const { getOrgEventsSubscription } = await import('./crdt')
  await getOrgEventsSubscription(hubUrl, cookie, orgId)

  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ title, orgId }),
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
 * Update workspace sharing via the hub's WorkspaceService.
 */
export async function shareWorkspaceViaAPI(
  hubUrl: string,
  cookie: string,
  workspaceId: string,
  shareMode: 'SHARE_MODE_PRIVATE' | 'SHARE_MODE_ORG' | 'SHARE_MODE_MEMBERS',
  userIds?: string[],
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/UpdateWorkspaceSharing`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workspaceId, shareMode, userIds }),
  })
  if (!res.ok) {
    throw new Error(`shareWorkspaceViaAPI failed: ${res.status}`)
  }
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
  // Refresh the channel's accessible-workspace set before the workspace-scoped RPC
  // (the cached channel froze its set at handshake), exactly as openAgentViaAPI does.
  const prepResp = await fetch(`${hubUrl}/leapmux.v1.ChannelService/PrepareWorkspaceAccess`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workerId, workspaceId }),
  })
  if (!prepResp.ok) {
    throw new Error(`cleanupWorkspaceViaAPI: PrepareWorkspaceAccess failed: ${prepResp.status}`)
  }
  await channel.callWorker(
    workerId,
    'CleanupWorkspace',
    CleanupWorkspaceRequestSchema,
    CleanupWorkspaceResponseSchema,
    { workspaceId },
  )
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
 * List all workspaces in an org via the hub's WorkspaceService.
 */
export async function listWorkspacesViaAPI(
  hubUrl: string,
  cookie: string,
  orgId: string,
): Promise<{ id: string }[]> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListWorkspaces`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ orgId }),
  })
  if (!res.ok) {
    throw new Error(`listWorkspacesViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workspaces?: Array<{ id: string }> }
  return data.workspaces ?? []
}

/**
 * Delete all workspaces in an org via the hub (best effort).
 */
export async function deleteAllWorkspacesViaAPI(
  hubUrl: string,
  cookie: string,
  orgId: string,
): Promise<void> {
  const workspaces = await listWorkspacesViaAPI(hubUrl, cookie, orgId)
  for (const ws of workspaces) {
    await deleteWorkspaceViaAPI(hubUrl, cookie, ws.id).catch(() => {})
  }
}
