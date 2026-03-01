// ──────────────────────────────────────────────
// API helpers for setting up test prerequisites
// ──────────────────────────────────────────────

/**
 * Login via the Connect API. Returns the auth token.
 */
export async function loginViaAPI(hubUrl: string, username: string, password: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AuthService/Login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!res.ok) {
    throw new Error(`loginViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { token: string }
  return data.token
}

/**
 * Sign up a new user via the Connect API. Returns the auth token.
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
  })
  if (!res.ok) {
    throw new Error(`signUpViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { token: string }
  return data.token
}

/**
 * Enable signup via admin API.
 */
export async function enableSignupViaAPI(hubUrl: string, adminToken: string): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AdminService/UpdateSettings`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${adminToken}`,
    },
    body: JSON.stringify({ settings: { signupEnabled: true } }),
  })
  if (!res.ok) {
    throw new Error(`enableSignupViaAPI failed: ${res.status}`)
  }
}

/**
 * Get the admin user's personal org ID via the Connect API.
 */
export async function getAdminOrgId(hubUrl: string, token: string): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.OrgService/ListMyOrgs`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
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
export async function getWorkerId(hubUrl: string, token: string): Promise<string> {
  const deadline = Date.now() + 30_000
  while (true) {
    const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ListWorkers`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${token}`,
      },
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
  token: string,
  orgId: string,
  username: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.OrgService/InviteOrgMember`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ orgId, username, role: 'ORG_MEMBER_ROLE_MEMBER' }),
  })
  if (!res.ok) {
    throw new Error(`inviteToOrgViaAPI failed: ${res.status}`)
  }
}

/**
 * Create a workspace via the Connect API. Returns the workspace ID.
 */
export async function createWorkspaceViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  title: string,
  orgId: string,
  workingDir?: string,
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workerId, title, orgId, workingDir }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`createWorkspaceViaAPI failed: ${res.status} ${body}`)
  }
  const data = await res.json() as { workspace: { id: string } }
  return data.workspace.id
}

/**
 * Update workspace sharing via the Connect API.
 */
export async function shareWorkspaceViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
  shareMode: 'SHARE_MODE_PRIVATE' | 'SHARE_MODE_ORG' | 'SHARE_MODE_MEMBERS',
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/UpdateWorkspaceSharing`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workspaceId, shareMode }),
  })
  if (!res.ok) {
    throw new Error(`shareWorkspaceViaAPI failed: ${res.status}`)
  }
}

/**
 * Deregister a worker via the Connect API.
 */
export async function deregisterWorkerViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/DeregisterWorker`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
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
  token: string,
  registrationToken: string,
  name: string,
  orgId: string,
): Promise<string> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkerManagementService/ApproveRegistration`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ registrationToken, name, orgId }),
  })
  if (!res.ok) {
    throw new Error(`approveRegistrationViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workerId: string }
  return data.workerId
}

/**
 * Delete (soft-delete) a workspace via the Connect API.
 */
export async function deleteWorkspaceViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/DeleteWorkspace`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workspaceId }),
  })
  if (!res.ok) {
    throw new Error(`deleteWorkspaceViaAPI failed: ${res.status}`)
  }
}

/**
 * List all workspaces in an org via the Connect API.
 */
export async function listWorkspacesViaAPI(
  hubUrl: string,
  token: string,
  orgId: string,
): Promise<{ id: string }[]> {
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListWorkspaces`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ orgId }),
  })
  if (!res.ok) {
    throw new Error(`listWorkspacesViaAPI failed: ${res.status}`)
  }
  const data = await res.json() as { workspaces: { id: string }[] }
  return data.workspaces ?? []
}

/**
 * Delete all workspaces in an org via the Connect API (best effort).
 */
export async function deleteAllWorkspacesViaAPI(
  hubUrl: string,
  token: string,
  orgId: string,
): Promise<void> {
  const workspaces = await listWorkspacesViaAPI(hubUrl, token, orgId)
  for (const ws of workspaces) {
    await deleteWorkspaceViaAPI(hubUrl, token, ws.id).catch(() => {})
  }
}
