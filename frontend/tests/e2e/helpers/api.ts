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

/**
 * Get the current user's ID via the Connect API.
 */
export async function getUserId(hubUrl: string, cookie: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/GetCurrentUser`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({}),
  })
  if (!res.ok) {
    throw new Error(`getUserId failed: ${res.status}`)
  }
  const data = await res.json() as { user: { id: string } }
  return data.user.id
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
 * Approve a worker registration via the Connect API. Returns the worker ID.
 */
export async function approveRegistrationViaAPI(
  hubUrl: string,
  cookie: string,
  registrationToken: string,
  orgId: string,
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ApproveRegistration`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ registrationToken, orgId }),
  })
  if (!res.ok) {
    throw new Error(`approveRegistrationViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workerId: string }
  return data.workerId
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
  },
): Promise<string> {
  const { OpenAgentRequestSchema, OpenAgentResponseSchema } = await import('../../../src/generated/leapmux/v1/agent_pb')
  const channel = await getTestChannel(hubUrl, cookie)
  const resp = await channel.callWorker(
    workerId,
    'OpenAgent',
    OpenAgentRequestSchema,
    OpenAgentResponseSchema,
    {
      workspaceId,
      workerId,
      workingDir: workingDir ?? '',
      ...(options?.model ? { model: options.model } : {}),
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

  // Register the tab on the hub so the frontend can discover it.
  await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/AddTab`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({
      workspaceId,
      tab: { tabType: 'TAB_TYPE_AGENT', tabId: resp.agent.id, workerId },
    }),
  })

  // Notify the worker that the test channel now has access to this workspace.
  // OpenChannel only includes workspaces that existed at handshake time; any
  // workspace created after that is invisible to ListAgents until the worker
  // receives a PrepareWorkspaceAccess (ChannelAccessUpdate) notification.
  await fetch(`${hubUrl}/leapmux.v1.ChannelService/PrepareWorkspaceAccess`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ workerId, workspaceId }),
  })

  return resp.agent.id
}

// ---- Hub API helpers (Workspace CRUD) ----
// These call the hub's WorkspaceService directly via HTTP.

/**
 * Create a workspace via the hub's WorkspaceService. Returns the workspace ID.
 */
export async function createWorkspaceViaAPI(
  hubUrl: string,
  cookie: string,
  title: string,
  orgId: string,
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`, {
    method: 'POST',
    headers: authedHeaders(cookie),
    body: JSON.stringify({ title, orgId }),
  })
  if (!res.ok) {
    throw new Error(`createWorkspaceViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workspace: { id: string } }
  if (!data.workspace) {
    throw new Error('createWorkspaceViaAPI: no workspace in response')
  }
  return data.workspace.id
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
