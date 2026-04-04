import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  CloseAgentRequestSchema,
  CloseAgentResponseSchema,
  ListAgentsRequestSchema,
  ListAgentsResponseSchema,
} from '../../../src/generated/leapmux/v1/agent_pb'
import {
  ForceRemoveWorktreeRequestSchema,
  ForceRemoveWorktreeResponseSchema,
  InspectLastTabCloseRequestSchema,
  InspectLastTabCloseResponseSchema,
  KeepWorktreeRequestSchema,
  KeepWorktreeResponseSchema,
  PushBranchForCloseRequestSchema,
  PushBranchForCloseResponseSchema,
  ScheduleWorktreeDeletionRequestSchema,
  ScheduleWorktreeDeletionResponseSchema,
} from '../../../src/generated/leapmux/v1/git_pb'
import {
  CloseTerminalRequestSchema,
  CloseTerminalResponseSchema,
} from '../../../src/generated/leapmux/v1/terminal_pb'
import { expect } from '../fixtures'
import { createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './api'

export const WORKSPACE_URL_RE = /\/workspace\//

/**
 * Create a git repo inside the server's data directory so the worker can access it.
 */
export function createGitRepo(dataDir: string, name: string): string {
  const repoDir = join(dataDir, name)
  mkdirSync(repoDir, { recursive: true })
  execSync('git init', { cwd: repoDir })
  execSync('git config user.email "test@test.com"', { cwd: repoDir })
  execSync('git config user.name "Test"', { cwd: repoDir })
  writeFileSync(join(repoDir, 'README.md'), '# Test\n')
  execSync('git add .', { cwd: repoDir })
  execSync('git commit -m "init"', { cwd: repoDir })
  return repoDir
}

/**
 * Check if a git branch exists in a repository.
 */
export function branchExists(repoDir: string, branchName: string): boolean {
  const output = execSync(`git branch --list ${branchName}`, { cwd: repoDir }).toString().trim()
  return output.length > 0
}

/**
 * Poll until a path no longer exists on disk (worktree removal is async).
 */
export async function waitForPathDeleted(path: string, timeoutMs = 10_000, intervalMs = 200): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (existsSync(path)) {
    if (Date.now() >= deadline)
      throw new Error(`Path still exists after ${timeoutMs}ms: ${path}`)
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/**
 * Wait for the org landing page to be ready (sidebar sections loaded).
 * Unlike waitForWorkspaceReady, this works on non-workspace routes like /o/admin.
 */
export async function waitForOrgPageReady(page: Page) {
  await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible({ timeout: 15_000 })
}

/**
 * Open the "New Workspace" dialog by clicking whichever button is available.
 * The left sidebar may be collapsed (rail mode), hiding `sidebar-new-workspace`.
 * Fall back to the empty-state `create-workspace-button` in the main area.
 */
export async function openNewWorkspaceDialog(page: Page) {
  const sidebarBtn = page.locator('[data-testid="sidebar-new-workspace"]')
  const createBtn = page.locator('[data-testid="create-workspace-button"]')
  // Use .first() to avoid strict mode violation when both buttons are visible
  await expect(sidebarBtn.or(createBtn).first()).toBeVisible()
  if (await sidebarBtn.isVisible())
    await sidebarBtn.click()
  else
    await createBtn.click()
  await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()
}

/**
 * Open the "New Agent" dialog from within a workspace via the tab menu.
 */
export async function openNewAgentDialog(page: Page) {
  const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
  await addMenu.click()
  await page.getByRole('menuitem', { name: 'New agent...' }).click()
  await expect(page.getByRole('heading', { name: 'New Agent' })).toBeVisible()
}

/**
 * Create a workspace on the hub, then open an agent with worktree enabled.
 * Returns the workspace ID.
 */
export async function createWorkspaceWithWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  title: string,
  orgId: string,
  workingDir: string,
  worktreeBranch: string,
): Promise<string> {
  const workspaceId = await createWorkspaceViaAPI(hubUrl, token, title, orgId)
  await openAgentViaAPI(hubUrl, token, workerId, workspaceId, workingDir, {
    createWorktree: true,
    worktreeBranch,
  })
  return workspaceId
}

/**
 * Close a terminal via E2EE channel.
 * Returns the response including worktree cleanup info.
 */
export async function closeTerminalViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  orgId: string,
  terminalId: string,
): Promise<{ worktreeCleanupPending: boolean, worktreePath: string, worktreeId: string }> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'CloseTerminal',
    CloseTerminalRequestSchema,
    CloseTerminalResponseSchema,
    { workspaceId, orgId, terminalId },
  )
  return {
    worktreeCleanupPending: resp.worktreeCleanupPending,
    worktreePath: resp.worktreePath,
    worktreeId: resp.worktreeId,
  }
}

/**
 * Close an agent via E2EE channel.
 * Returns the response including worktree cleanup info.
 */
export async function closeAgentViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  agentId: string,
): Promise<{ worktreeCleanupPending: boolean, worktreePath: string, worktreeId: string }> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'CloseAgent',
    CloseAgentRequestSchema,
    CloseAgentResponseSchema,
    { agentId },
  )
  return {
    worktreeCleanupPending: resp.worktreeCleanupPending,
    worktreePath: resp.worktreePath,
    worktreeId: resp.worktreeId,
  }
}

/**
 * Poll `listAgentsViaAPI` until at least one agent is returned or the
 * timeout elapses.  Call this instead of `listAgentsViaAPI` directly when
 * the agent was just created via the UI or an API call that may not have
 * been persisted by the backend yet.
 */
export async function waitForAgentsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  orgId: string,
  timeoutMs = 15_000,
  intervalMs = 200,
): Promise<Array<{ id: string, workingDir: string }>> {
  const deadline = Date.now() + timeoutMs
  while (true) {
    const agents = await listAgentsViaAPI(hubUrl, token, workerId, workspaceId, orgId)
    if (agents.length > 0) {
      return agents
    }
    if (Date.now() >= deadline) {
      throw new Error(`No agents appeared for workspace ${workspaceId} within ${timeoutMs}ms`)
    }
    await new Promise(r => setTimeout(r, intervalMs))
  }
}

/**
 * List agents for a workspace via hub ListTabs + worker ListAgents.
 * The ListAgents RPC now accepts tab_ids instead of workspace_id,
 * so we first fetch the tab list from the hub and then request agents by ID.
 */
export async function listAgentsViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  workspaceId: string,
  orgId: string,
): Promise<Array<{ id: string, workingDir: string }>> {
  // Get tab IDs from the hub's ListTabs endpoint.
  const tabsRes = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/ListTabs`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ orgId, workspaceId }),
  })
  if (!tabsRes.ok) {
    throw new Error(`ListTabs failed: ${tabsRes.status}`)
  }
  const tabsData = await tabsRes.json() as { tabs?: Array<{ tabType: string, tabId: string }> }
  const agentTabIds = (tabsData.tabs ?? [])
    .filter(t => t.tabType === 'TAB_TYPE_AGENT')
    .map(t => t.tabId)

  if (agentTabIds.length === 0) {
    return []
  }

  const channel = await getTestChannel(hubUrl, token)
  let resp: Awaited<ReturnType<typeof channel.callWorker<typeof ListAgentsRequestSchema, typeof ListAgentsResponseSchema>>>
  try {
    resp = await channel.callWorker(
      workerId,
      'ListAgents',
      ListAgentsRequestSchema,
      ListAgentsResponseSchema,
      { tabIds: agentTabIds },
    )
  }
  catch {
    // Treat as transient; caller retries via waitForAgentsViaAPI.
    return []
  }
  return (resp.agents ?? []).map(a => ({ id: a.id, workingDir: a.workingDir }))
}

/**
 * Force-remove a worktree via E2EE channel.
 */
export async function forceRemoveWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  worktreeId: string,
): Promise<void> {
  const channel = await getTestChannel(hubUrl, token)
  await channel.callWorker(
    workerId,
    'ForceRemoveWorktree',
    ForceRemoveWorktreeRequestSchema,
    ForceRemoveWorktreeResponseSchema,
    { worktreeId },
  )
}

/**
 * Inspect the last-tab close state via E2EE channel.
 */
export async function inspectLastTabCloseViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  tabType: number,
  tabId: string,
): Promise<{
  target: number
  shouldPrompt: boolean
  worktreePath: string
  worktreeId: string
  branchName: string
  canPush: boolean
  hasUncommittedChanges: boolean
  unpushedCommitCount: number
  remoteBranchMissing: boolean
}> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'InspectLastTabClose',
    InspectLastTabCloseRequestSchema,
    InspectLastTabCloseResponseSchema,
    { tabType, tabId },
  )
  return {
    target: resp.target,
    shouldPrompt: resp.shouldPrompt,
    worktreePath: resp.worktreePath,
    worktreeId: resp.worktreeId,
    branchName: resp.branchName,
    canPush: resp.canPush,
    hasUncommittedChanges: resp.hasUncommittedChanges,
    unpushedCommitCount: resp.unpushedCommitCount,
    remoteBranchMissing: resp.remoteBranchMissing,
  }
}

/**
 * Schedule worktree deletion via E2EE channel.
 */
export async function scheduleWorktreeDeletionViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  worktreeId: string,
): Promise<void> {
  const channel = await getTestChannel(hubUrl, token)
  await channel.callWorker(
    workerId,
    'ScheduleWorktreeDeletion',
    ScheduleWorktreeDeletionRequestSchema,
    ScheduleWorktreeDeletionResponseSchema,
    { worktreeId },
  )
}

/**
 * Push or commit-and-push for close via E2EE channel.
 */
export async function pushBranchForCloseViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  tabType: number,
  tabId: string,
): Promise<void> {
  const channel = await getTestChannel(hubUrl, token)
  await channel.callWorker(
    workerId,
    'PushBranchForClose',
    PushBranchForCloseRequestSchema,
    PushBranchForCloseResponseSchema,
    { tabType, tabId },
  )
}

/**
 * Keep a worktree (stop tracking but leave on disk) via E2EE channel.
 */
export async function keepWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workerId: string,
  worktreeId: string,
): Promise<void> {
  const channel = await getTestChannel(hubUrl, token)
  await channel.callWorker(
    workerId,
    'KeepWorktree',
    KeepWorktreeRequestSchema,
    KeepWorktreeResponseSchema,
    { worktreeId },
  )
}

/** Wait for a worker to be available (retry with backoff). */
export async function waitForWorker(page: Page) {
  const dialog = page.getByRole('dialog')
  const workerSelect = dialog.locator('select').first()
  const refreshBtn = dialog.getByLabel('Refresh workers')
  for (let attempt = 0; attempt < 6; attempt++) {
    try {
      await expect(workerSelect).toContainText('Local', { timeout: 5000 })
      break
    }
    catch {
      if (attempt === 5)
        throw new Error('No online worker found')
      await refreshBtn.click()
    }
  }
}

/**
 * Set the working directory in a dialog by filling the path input and pressing Enter.
 * SolidJS uses event delegation (document-level listeners keyed by `$$eventType`).
 * Playwright's fill() sets el.value directly but may not trigger a bubbling InputEvent
 * that SolidJS's delegation picks up. We dispatch a real InputEvent manually to ensure
 * the SolidJS signal updates before pressing Enter.
 */
export async function setWorkingDir(page: Page, dirPath: string) {
  const dialog = page.getByRole('dialog')
  const pathInput = dialog.getByPlaceholder('Enter path...')
  await pathInput.click()
  await pathInput.evaluate((el: HTMLInputElement, value: string) => {
    el.value = value
    el.dispatchEvent(new InputEvent('input', { bubbles: true }))
  }, dirPath)
  await pathInput.press('Enter')
}
