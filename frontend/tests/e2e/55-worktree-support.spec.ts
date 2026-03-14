import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import path, { join } from 'node:path'
import {
  CloseAgentRequestSchema,
  CloseAgentResponseSchema,
  ListAgentsRequestSchema,
  ListAgentsResponseSchema,
} from '../../src/generated/leapmux/v1/agent_pb'
import {
  ForceRemoveWorktreeRequestSchema,
  ForceRemoveWorktreeResponseSchema,
  KeepWorktreeRequestSchema,
  KeepWorktreeResponseSchema,
} from '../../src/generated/leapmux/v1/git_pb'
import {
  CloseTerminalRequestSchema,
  CloseTerminalResponseSchema,
  OpenTerminalRequestSchema,
  OpenTerminalResponseSchema,
} from '../../src/generated/leapmux/v1/terminal_pb'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

const frontendDir = path.resolve(import.meta.dirname, '../..')
const WORKSPACE_URL_RE = /\/workspace\//

/**
 * Create a git repo inside the server's data directory so the worker can access it.
 */
function createGitRepo(dataDir: string, name: string): string {
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
function branchExists(repoDir: string, branchName: string): boolean {
  const output = execSync(`git branch --list ${branchName}`, { cwd: repoDir }).toString().trim()
  return output.length > 0
}

/**
 * Poll until a path no longer exists on disk (worktree removal is async).
 */
async function waitForPathDeleted(path: string, timeoutMs = 10_000, intervalMs = 200): Promise<void> {
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
async function waitForOrgPageReady(page: Page) {
  await expect(page.locator('[data-testid="section-header-workspaces_in_progress"]')).toBeVisible({ timeout: 15_000 })
}

/**
 * Open the "New Workspace" dialog by clicking whichever button is available.
 * The left sidebar may be collapsed (rail mode), hiding `sidebar-new-workspace`.
 * Fall back to the empty-state `create-workspace-button` in the main area.
 */
async function openNewWorkspaceDialog(page: Page) {
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
async function openNewAgentDialog(page: Page) {
  const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
  await addMenu.click()
  await page.getByRole('menuitem', { name: 'New agent...' }).click()
  await expect(page.getByRole('heading', { name: 'New Agent' })).toBeVisible()
}

/**
 * Create a workspace on the hub, then open an agent with worktree enabled.
 * Returns the workspace ID.
 */
async function createWorkspaceWithWorktreeViaAPI(
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
 * Open a terminal with a worktree via E2EE channel.
 * Returns the terminal ID.
 */
async function openTerminalWithWorktreeViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
  workerId: string,
  orgId: string,
  workingDir: string,
  worktreeBranch: string,
): Promise<string> {
  const channel = await getTestChannel(hubUrl, token)
  const resp = await channel.callWorker(
    workerId,
    'OpenTerminal',
    OpenTerminalRequestSchema,
    OpenTerminalResponseSchema,
    { workspaceId, workerId, orgId, cols: 80, rows: 24, workingDir, createWorktree: true, worktreeBranch },
  )
  return resp.terminalId
}

/**
 * Close a terminal via E2EE channel.
 * Returns the response including worktree cleanup info.
 */
async function closeTerminalViaAPI(
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
async function closeAgentViaAPI(
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
 * List agents for a workspace via hub ListTabs + worker ListAgents.
 * The ListAgents RPC now accepts tab_ids instead of workspace_id,
 * so we first fetch the tab list from the hub and then request agents by ID.
 */
async function listAgentsViaAPI(
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
  const resp = await channel.callWorker(
    workerId,
    'ListAgents',
    ListAgentsRequestSchema,
    ListAgentsResponseSchema,
    { tabIds: agentTabIds },
  )
  return (resp.agents ?? []).map(a => ({ id: a.id, workingDir: a.workingDir }))
}

/**
 * Force-remove a worktree via E2EE channel.
 */
async function forceRemoveWorktreeViaAPI(
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
 * Keep a worktree (stop tracking but leave on disk) via E2EE channel.
 */
async function keepWorktreeViaAPI(
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
async function waitForWorker(page: Page) {
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
async function setWorkingDir(page: Page, dirPath: string) {
  const dialog = page.getByRole('dialog')
  const pathInput = dialog.getByPlaceholder('Enter path...')
  await pathInput.click()
  await pathInput.evaluate((el: HTMLInputElement, value: string) => {
    el.value = value
    el.dispatchEvent(new InputEvent('input', { bubbles: true }))
  }, dirPath)
  await pathInput.press('Enter')
}

// ─── UI Detection Tests ─────────────────────────────────────────────

test.describe('Worktree Support', () => {
  test('non-git directory hides git options in new workspace dialog', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer

    // Create a plain directory that is NOT a git repo.
    const nonGitDir = join(dataDir, 'not-a-repo')
    mkdirSync(nonGitDir, { recursive: true })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    // Set working directory to a known non-git directory
    await setWorkingDir(page, nonGitDir)

    // Wait for the git info check to complete.
    await page.waitForTimeout(2000)

    // Verify git mode radio options are not visible
    await expect(page.getByText('Use current state')).not.toBeVisible()
    await expect(page.getByText('Create new worktree')).not.toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('subdirectory of git repo hides git options in new workspace dialog', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-subdir')

    // Create a subdirectory inside the repo.
    const subDir = join(repoDir, 'src', 'components')
    mkdirSync(subDir, { recursive: true })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    // Set working directory to a subdirectory of the git repo
    await setWorkingDir(page, subDir)

    // Wait for the git info check to complete.
    await page.waitForTimeout(2000)

    // Verify git mode radio options are not visible (even though it's inside a git repo)
    await expect(page.getByText('Use current state')).not.toBeVisible()
    await expect(page.getByText('Create new worktree')).not.toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('four git mode radio options appear for git repo directory in new workspace dialog', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-ws')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)
    await setWorkingDir(page, repoDir)

    // All four radio options should appear
    await expect(page.getByText('Use current state')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Switch to branch')).toBeVisible()
    await expect(page.getByText('Create new worktree')).toBeVisible()
    await expect(page.getByText('Use existing worktree')).toBeVisible()

    // Default should be "Use current state" — branch name input should NOT be visible
    await expect(page.getByText('Branch Name')).not.toBeVisible()
    await expect(page.getByText('Worktree path:')).not.toBeVisible()

    // Select "Create new worktree" — sub-controls should appear
    await page.getByText('Create new worktree').click()
    await expect(page.getByText('Branch Name')).toBeVisible()
    await expect(page.getByText('Worktree path:')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('git mode radio options appear in new agent dialog for git repo', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-agent')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Agent WT Dialog Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    await openNewAgentDialog(page)
    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Use current state')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Create new worktree')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('git mode radio options appear in new terminal dialog for git repo', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-terminal')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Terminal WT Dialog Test', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
    await addMenu.click()
    await page.getByRole('menuitem', { name: 'New terminal...' }).click()

    await expect(page.getByRole('heading', { name: 'New Terminal' })).toBeVisible()

    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Use current state')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Create new worktree')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Workspace Creation with Worktree ────────────────────────────

  test('create workspace with worktree via UI', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-create')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Worktree Test WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    await branchInput.clear()
    await branchInput.fill('e2e-test-branch')

    await expect(page.getByText('e2e-test-branch')).toBeVisible()

    await dialog.getByRole('button', { name: 'Create', exact: true }).click()

    // Wait for the dialog to close first — this signals the API call
    // (including the git worktree creation) has completed.
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 30000 })
    await expect(page).toHaveURL(WORKSPACE_URL_RE, { timeout: 30000 })
    await waitForWorkspaceReady(page)

    // Verify the worktree directory was created on disk.
    // Use realpathSync to resolve macOS symlinks (e.g. /var -> /private/var).
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-create-worktrees', 'e2e-test-branch')
    expect(existsSync(worktreeDir)).toBe(true)
  })

  // ─── Worktree Lifecycle: Cleanup on Last Tab Close ────────────────

  test('clean worktree is auto-deleted when the last tab closes', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-autoclean')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-autoclean-worktrees', 'autoclean-branch')

    // Create workspace with worktree via API
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Autoclean WS',
      adminOrgId,
      repoDir,
      'autoclean-branch',
    )

    // Worktree should now exist on disk
    expect(existsSync(worktreeDir)).toBe(true)

    // Get the initial agent that was auto-created with the workspace
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)

    // Close the agent (last tab referencing the worktree)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Worktree should be clean, so it should have been auto-deleted
    expect(resp.worktreeCleanupPending).toBeFalsy()
    // Worktree removal happens asynchronously; wait for it to complete.
    await waitForPathDeleted(worktreeDir)

    // Branch should also be deleted (async, along with worktree removal)
    await waitForPathDeleted(join(repoDir, '.git', 'refs', 'heads', 'autoclean-branch'))
    expect(branchExists(repoDir, 'autoclean-branch')).toBe(false)
  })

  test('worktree persists while other tabs still reference it', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-shared')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-shared-worktrees', 'shared-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Shared WS',
      adminOrgId,
      repoDir,
      'shared-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Open a second terminal using the same worktree
    const terminalId = await openTerminalWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workspaceId,
      workerId,
      adminOrgId,
      repoDir,
      'shared-branch',
    )

    // Close the terminal — agent still holds reference, worktree should persist
    const termResp = await closeTerminalViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId, terminalId)
    expect(termResp.worktreeCleanupPending).toBeFalsy()
    expect(existsSync(worktreeDir)).toBe(true)

    // Now close the agent (last tab)
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const agentResp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Clean worktree should now be auto-deleted
    expect(agentResp.worktreeCleanupPending).toBeFalsy()
    // Worktree removal happens asynchronously; wait for it to complete.
    await waitForPathDeleted(worktreeDir)

    // Branch should also be deleted (async, along with worktree removal)
    await waitForPathDeleted(join(repoDir, '.git', 'refs', 'heads', 'shared-branch'))
    expect(branchExists(repoDir, 'shared-branch')).toBe(false)
  })

  // ─── Existing Worktree Not Tracked ─────────────────────────────────

  test('existing worktree (not created by us) is not deleted on close', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-existing')
    const realDataDir = realpathSync(dataDir)

    // Create a worktree MANUALLY outside of LeapMux
    const manualWorktreeDir = join(realDataDir, 'manual-worktree')
    execSync(`git worktree add ${join(dataDir, 'manual-worktree')} -b manual-branch`, { cwd: repoDir })
    expect(existsSync(manualWorktreeDir)).toBe(true)

    // Create a workspace pointing at the manual worktree WITHOUT createWorktree
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      'Existing WT WS',
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, frontendDir)

    // Get the auto-created agent
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)

    // Close the agent
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // No worktree cleanup should be triggered (not tracked by LeapMux)
    expect(resp.worktreeCleanupPending).toBeFalsy()

    // The manual worktree should STILL exist on disk
    expect(existsSync(manualWorktreeDir)).toBe(true)
  })

  // ─── Dirty Worktree: Uncommitted Changes ───────────────────────────

  test('dirty worktree with uncommitted changes triggers confirmation', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dirty')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dirty-worktrees', 'dirty-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Dirty WS',
      adminOrgId,
      repoDir,
      'dirty-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Make the worktree dirty: add an uncommitted file
    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted change\n')

    // Close the agent (last tab)
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Should trigger confirmation (worktree NOT auto-deleted)
    expect(resp.worktreeCleanupPending).toBe(true)
    // The response path is the resolved (canonical) path from the worker
    expect(resp.worktreePath).toContain('test-repo-dirty-worktrees/dirty-branch')
    expect(resp.worktreeId).toBeTruthy()

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)

    // Now force-remove it
    await forceRemoveWorktreeViaAPI(hubUrl, adminToken, workerId, resp.worktreeId!)
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'dirty-branch')).toBe(false)
    }).toPass({ timeout: 15_000 })
  })

  test('worktree with local-only commits and no upstream triggers confirmation', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    // Use a plain local repo (no remote configured) so branches have no upstream.
    const repoDir = createGitRepo(dataDir, 'test-repo-no-upstream')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-no-upstream-worktrees', 'no-upstream-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'No Upstream WS',
      adminOrgId,
      repoDir,
      'no-upstream-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Make a local commit in the worktree (no upstream to push to)
    execSync('git config user.email "test@test.com"', { cwd: worktreeDir })
    execSync('git config user.name "Test"', { cwd: worktreeDir })
    writeFileSync(join(worktreeDir, 'local-only.txt'), 'would be lost\n')
    execSync('git add .', { cwd: worktreeDir })
    execSync('git commit -m "local only"', { cwd: worktreeDir })

    // Close the agent (last tab)
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Should trigger confirmation — commits exist only on this branch
    expect(resp.worktreeCleanupPending).toBe(true)
    expect(existsSync(worktreeDir)).toBe(true)

    // Force-remove it
    await forceRemoveWorktreeViaAPI(hubUrl, adminToken, workerId, resp.worktreeId!)
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'no-upstream-branch')).toBe(false)
    }).toPass({ timeout: 15_000 })
  })

  test('dirty worktree with unpushed commits triggers confirmation', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const realDataDir = realpathSync(dataDir)

    // Create a bare "remote" and clone from it so there is an upstream
    const bareDir = join(dataDir, 'test-repo-unpushed-bare')
    mkdirSync(bareDir, { recursive: true })
    execSync('git init --bare', { cwd: bareDir })

    const repoDir = join(dataDir, 'test-repo-unpushed')
    execSync(`git clone ${bareDir} ${repoDir}`)
    execSync('git config user.email "test@test.com"', { cwd: repoDir })
    execSync('git config user.name "Test"', { cwd: repoDir })
    writeFileSync(join(repoDir, 'README.md'), '# Test\n')
    execSync('git add .', { cwd: repoDir })
    execSync('git commit -m "init"', { cwd: repoDir })
    execSync('git push -u origin HEAD', { cwd: repoDir })

    const worktreeDir = join(realDataDir, 'test-repo-unpushed-worktrees', 'unpushed-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Unpushed WS',
      adminOrgId,
      repoDir,
      'unpushed-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Make an unpushed commit in the worktree and set upstream tracking
    execSync('git config user.email "test@test.com"', { cwd: worktreeDir })
    execSync('git config user.name "Test"', { cwd: worktreeDir })
    writeFileSync(join(worktreeDir, 'extra.txt'), 'local only\n')
    execSync('git add .', { cwd: worktreeDir })
    execSync('git commit -m "unpushed"', { cwd: worktreeDir })
    // Push the branch to the remote first, then add another unpushed commit.
    // This ensures the branch has an upstream so `git log @{upstream}..HEAD` works.
    execSync('git push -u origin unpushed-branch', { cwd: worktreeDir })
    writeFileSync(join(worktreeDir, 'extra2.txt'), 'also local only\n')
    execSync('git add .', { cwd: worktreeDir })
    execSync('git commit -m "another unpushed"', { cwd: worktreeDir })

    // Close the agent (last tab)
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Should trigger confirmation
    expect(resp.worktreeCleanupPending).toBe(true)
    expect(existsSync(worktreeDir)).toBe(true)

    // This time, choose "keep" instead of force-remove
    await keepWorktreeViaAPI(hubUrl, adminToken, workerId, resp.worktreeId!)

    // Worktree should still exist on disk (kept by user choice)
    expect(existsSync(worktreeDir)).toBe(true)

    // Branch should also still exist
    expect(branchExists(repoDir, 'unpushed-branch')).toBe(true)
  })

  // ─── Dirty Worktree Confirmation Dialog (UI) ──────────────────────

  test('dirty worktree confirmation dialog: cancel keeps tab open', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog-cancel')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dialog-cancel-worktrees', 'cancel-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Cancel Test WS',
      adminOrgId,
      repoDir,
      'cancel-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    // Close the agent tab via UI
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTab).toBeVisible()
    const agentCloseBtn = agentTab.locator('[data-testid="tab-close"]')
    await agentCloseBtn.dispatchEvent('click')

    // Confirmation dialog should appear BEFORE the tab is closed
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).toBeVisible({ timeout: 10000 })

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByText('cancel-branch', { exact: true })).toBeVisible()

    // Click "Remove" once — should arm the button (show "Confirm?"), not remove
    await page.getByRole('button', { name: 'Remove' }).click()
    await expect(page.getByRole('button', { name: 'Confirm?' })).toBeVisible()

    // Dialog should still be open, tab should still be present
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).toBeVisible()
    await expect(agentTab).toBeVisible()

    // Click "Cancel" — resets the armed button and closes dialog
    await page.getByRole('button', { name: 'Cancel' }).click()

    // Dialog should close
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).not.toBeVisible()

    // Tab should still be present (not closed)
    await expect(agentTab).toBeVisible()

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('dirty worktree confirmation dialog: remove closes tab and deletes worktree', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dialog-worktrees', 'dialog-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Dialog Test WS',
      adminOrgId,
      repoDir,
      'dialog-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTab).toBeVisible()
    await agentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Dialog appears BEFORE tab closes
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).toBeVisible({ timeout: 10000 })
    await expect(page.getByRole('dialog').getByText('test-repo-dialog-worktrees/dialog-branch')).toBeVisible()

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByText('dialog-branch', { exact: true })).toBeVisible()

    // Click "Remove" once — should arm the button (show "Confirm?")
    await page.getByRole('button', { name: 'Remove' }).click()
    await expect(page.getByRole('button', { name: 'Confirm?' })).toBeVisible()

    // Click "Confirm?" to actually remove
    await page.getByRole('button', { name: 'Confirm?' }).click()

    // Dialog closes and tab is removed
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).not.toBeVisible()
    await expect(agentTab).not.toBeVisible({ timeout: 5000 })

    // Worktree directory and branch are deleted in the background by
    // the worker after ForceRemoveWorktree returns, so poll for completion.
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'dialog-branch')).toBe(false)
    }).toPass({ timeout: 10_000 })
  })

  test('dirty worktree confirmation dialog: keep closes tab but preserves worktree', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog-keep')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dialog-keep-worktrees', 'keep-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Keep Test WS',
      adminOrgId,
      repoDir,
      'keep-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTab).toBeVisible()
    await agentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Dialog appears
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).toBeVisible({ timeout: 10000 })

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByText('keep-branch', { exact: true })).toBeVisible()

    // Click "Keep"
    await page.getByRole('button', { name: 'Keep' }).click()

    // Dialog closes and tab is removed
    await expect(page.getByRole('heading', { name: 'Dirty Worktree' })).not.toBeVisible()
    await expect(agentTab).not.toBeVisible({ timeout: 5000 })

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)

    // Branch should also still exist
    expect(branchExists(repoDir, 'keep-branch')).toBe(true)
  })

  // ─── Worktree-from-Worktree ──────────────────────────────────────

  test('git mode options appear for existing worktree root', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-wt-root')
    const realDataDir = realpathSync(dataDir)

    // Create a worktree manually
    const worktreeDir = join(realDataDir, 'test-repo-wt-root-wt')
    execSync(`git worktree add ${join(dataDir, 'test-repo-wt-root-wt')} -b wt-root-branch`, { cwd: repoDir })
    expect(existsSync(worktreeDir)).toBe(true)

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    // Set working directory to the worktree root
    await setWorkingDir(page, worktreeDir)

    // Git mode radio options should appear for an existing worktree root
    await expect(page.getByText('Use current state')).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Create new worktree')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('dirty warning appears when source working copy has uncommitted changes', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dirty-warn')

    // Make the repo dirty
    writeFileSync(join(repoDir, 'dirty-file.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    // Warning about uncommitted changes should be visible
    await expect(page.getByText('uncommitted changes that will not be transferred')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('create worktree from existing worktree starts from correct branch', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-wt-from-wt')
    const realDataDir = realpathSync(dataDir)

    // Create a worktree with an extra commit that diverges from main
    const firstWorktreeDir = join(realDataDir, 'test-repo-wt-from-wt-worktrees', 'source-branch')
    execSync(`git worktree add ${join(dataDir, 'test-repo-wt-from-wt-worktrees/source-branch')} -b source-branch`, { cwd: repoDir })
    expect(existsSync(firstWorktreeDir)).toBe(true)

    // Add an extra commit on the source-branch worktree
    execSync('git config user.email "test@test.com"', { cwd: firstWorktreeDir })
    execSync('git config user.name "Test"', { cwd: firstWorktreeDir })
    writeFileSync(join(firstWorktreeDir, 'extra.txt'), 'diverged\n')
    execSync('git add .', { cwd: firstWorktreeDir })
    execSync('git commit -m "diverge from main"', { cwd: firstWorktreeDir })

    // Get the HEAD of source-branch (should differ from main)
    const sourceBranchHead = execSync('git rev-parse HEAD', { cwd: firstWorktreeDir }).toString().trim()
    const mainHead = execSync('git rev-parse HEAD', { cwd: repoDir }).toString().trim()
    expect(sourceBranchHead).not.toBe(mainHead)

    // Create workspace from the worktree root (source-branch) with createWorktree enabled
    await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'WT from WT WS',
      adminOrgId,
      firstWorktreeDir,
      'derived-branch',
    )

    // The new worktree should exist
    const derivedWorktreeDir = join(realDataDir, 'test-repo-wt-from-wt-worktrees', 'derived-branch')
    expect(existsSync(derivedWorktreeDir)).toBe(true)

    // The new worktree's HEAD should match the source-branch's HEAD (not main's HEAD)
    const derivedHead = execSync('git rev-parse HEAD', { cwd: derivedWorktreeDir }).toString().trim()
    expect(derivedHead).toBe(sourceBranchHead)
    expect(derivedHead).not.toBe(mainHead)
  })

  // ─── Edge Cases ────────────────────────────────────────────────────

  test('invalid branch name disables Create button and shows error', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-error')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Error Test WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })

    // The default random branch name should be valid — Create button enabled
    await expect(createBtn).toBeEnabled()

    // Enter a name with spaces — should show error and disable Create
    await branchInput.clear()
    await branchInput.fill('invalid branch name')
    await expect(dialog.getByText('Branch name contains invalid characters')).toBeVisible()
    await expect(createBtn).toBeDisabled()

    // Enter a name with '..' — should show different error
    await branchInput.clear()
    await branchInput.fill('foo..bar')
    await expect(dialog.getByText('Branch name must not contain ..')).toBeVisible()
    await expect(createBtn).toBeDisabled()

    // Enter a name starting with '-' — should show leading char error
    await branchInput.clear()
    await branchInput.fill('-bad-start')
    await expect(dialog.getByText('Branch name must not start with')).toBeVisible()
    await expect(createBtn).toBeDisabled()

    // Enter a valid branch name — error clears, Create re-enabled
    await branchInput.clear()
    await branchInput.fill('valid-branch-name')
    await expect(dialog.locator('text=/Branch name/')).not.toBeVisible()
    await expect(createBtn).toBeEnabled()
  })

  test('randomize button generates a new branch name', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-randomize')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    // Read initial branch name
    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    const initialBranch = await branchInput.inputValue()
    expect(initialBranch).toBeTruthy()

    // Click the randomize button for branch name (last one — first is for workspace title)
    await dialog.getByLabel('Generate random name').last().click()

    // Branch name should change
    const newBranch = await branchInput.inputValue()
    expect(newBranch).toBeTruthy()
    expect(newBranch).not.toBe(initialBranch)

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Dialog Default Working Directory Resolution ──────────────────

  test('new agent dialog defaults to repo root when opened from worktree tab', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-agent-resolve')
    const realRepoDir = realpathSync(repoDir)

    // Create workspace with worktree so the initial agent tab is in the worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Agent Resolve WS',
      adminOrgId,
      repoDir,
      'agent-resolve-branch',
    )

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    // Open "New agent..." dialog via the tab menu
    const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
    await addMenu.click()
    await page.getByRole('menuitem', { name: 'New agent...' }).click()

    await expect(page.getByRole('heading', { name: 'New Agent' })).toBeVisible()

    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')

    // The path should resolve to the original repo root, not the worktree path.
    await expect(pathInput).toHaveValue(realRepoDir, { timeout: 10000 })

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('new terminal dialog defaults to repo root when opened from worktree tab', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-terminal-resolve')
    const realRepoDir = realpathSync(repoDir)

    // Create workspace with worktree so the initial agent tab is in the worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Terminal Resolve WS',
      adminOrgId,
      repoDir,
      'terminal-resolve-branch',
    )

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    // Open "New terminal..." dialog via the tab menu
    const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
    await addMenu.click()
    await page.getByRole('menuitem', { name: 'New terminal...' }).click()

    await expect(page.getByRole('heading', { name: 'New Terminal' })).toBeVisible()

    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')

    // The path should resolve to the original repo root, not the worktree path.
    await expect(pathInput).toHaveValue(realRepoDir, { timeout: 10000 })

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Git Mode: Switch to Branch ─────────────────────────────────────

  test('switch-to-branch mode via UI: branch dropdown loads and submit checks out branch', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-switch-ui')

    // Create a second branch to switch to.
    execSync('git branch feature-switch', { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Switch Branch WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options and select "Switch to branch"
    await expect(page.getByText('Switch to branch')).toBeVisible({ timeout: 10000 })
    await page.getByText('Switch to branch').click()

    // Branch dropdown should load with local branches
    const branchSelect = dialog.locator('select').last()
    await expect(branchSelect).toBeEnabled({ timeout: 10000 })
    await branchSelect.selectOption('feature-switch')

    // Submit
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 30000 })
    await expect(page).toHaveURL(WORKSPACE_URL_RE, { timeout: 30000 })

    // Verify the repo is now on the feature-switch branch
    const currentBranch = execSync('git rev-parse --abbrev-ref HEAD', { cwd: repoDir }).toString().trim()
    expect(currentBranch).toBe('feature-switch')
  })

  test('switch-to-branch mode via API: verifies checkout on disk', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-switch-api')

    // Create a second branch.
    execSync('git branch api-switch-target', { cwd: repoDir })

    // Verify we start on main.
    const before = execSync('git rev-parse --abbrev-ref HEAD', { cwd: repoDir }).toString().trim()
    expect(before).toBe('main')

    // Create workspace with checkout_branch.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Switch API WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir, {
      checkoutBranch: 'api-switch-target',
    })

    // Verify the repo is now on the target branch.
    const after = execSync('git rev-parse --abbrev-ref HEAD', { cwd: repoDir }).toString().trim()
    expect(after).toBe('api-switch-target')
  })

  test('switch-to-branch with dirty workdir shows warning in UI', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-switch-dirty')

    execSync('git branch dirty-switch-target', { cwd: repoDir })
    writeFileSync(join(repoDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Switch to branch')).toBeVisible({ timeout: 10000 })
    await page.getByText('Switch to branch').click()

    // Warning about uncommitted changes should appear
    await expect(page.getByText('uncommitted changes')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Git Mode: Use Existing Worktree ────────────────────────────────

  test('use-existing-worktree mode via API: switches working dir to worktree', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-api')
    const realDataDir = realpathSync(dataDir)

    // Create an existing worktree manually.
    const worktreeDir = join(realDataDir, 'test-repo-use-wt-api-existing')
    execSync(`git worktree add ${join(dataDir, 'test-repo-use-wt-api-existing')} -b existing-wt-branch`, { cwd: repoDir })
    expect(existsSync(worktreeDir)).toBe(true)

    // Create workspace using the use_worktree_path field.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Use WT API WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir, {
      useWorktreePath: worktreeDir,
    })

    // Verify the agent's working dir is the worktree path.
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    expect(agents[0].workingDir).toBe(worktreeDir)
  })

  test('use-existing-worktree on managed worktree: tracks tab correctly', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-managed')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-use-wt-managed-worktrees', 'managed-branch')

    // Create workspace with a managed worktree (via create-worktree mode).
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Managed WT WS',
      adminOrgId,
      repoDir,
      'managed-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Now open a second agent using "use existing worktree" pointing to the same managed worktree.
    const secondAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir, {
      useWorktreePath: worktreeDir,
    })

    // Close the first agent — worktree should persist because second agent still references it.
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    const firstAgent = agents.find(a => a.id !== secondAgentId)!
    expect(firstAgent).toBeTruthy()
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, firstAgent.id)
    expect(resp.worktreeCleanupPending).toBeFalsy()
    expect(existsSync(worktreeDir)).toBe(true)

    // Close the second agent — last tab, clean worktree → auto-delete.
    const resp2 = await closeAgentViaAPI(hubUrl, adminToken, workerId, secondAgentId)
    expect(resp2.worktreeCleanupPending).toBeFalsy()
    await waitForPathDeleted(worktreeDir)
  })

  test('use-existing-worktree on unmanaged worktree: does NOT auto-delete on close', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-unmanaged')
    const realDataDir = realpathSync(dataDir)

    // Create a worktree manually (not via LeapMux).
    const worktreeDir = join(realDataDir, 'test-repo-use-wt-unmanaged-ext')
    execSync(`git worktree add ${join(dataDir, 'test-repo-use-wt-unmanaged-ext')} -b ext-branch`, { cwd: repoDir })
    expect(existsSync(worktreeDir)).toBe(true)

    // Create workspace using "use existing worktree" pointing to the unmanaged worktree.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Unmanaged WT WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir, {
      useWorktreePath: worktreeDir,
    })

    // Close the agent.
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Unmanaged worktree should NOT be cleaned up.
    expect(resp.worktreeCleanupPending).toBeFalsy()
    expect(existsSync(worktreeDir)).toBe(true)
    expect(branchExists(repoDir, 'ext-branch')).toBe(true)
  })

  test('use-existing-worktree via UI: dropdown loads and submit uses worktree dir', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-ui')

    // Create a worktree manually.
    execSync(`git worktree add ${join(dataDir, 'test-repo-use-wt-ui-wt')} -b ui-wt-branch`, { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Use WT UI WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options and select "Use existing worktree"
    await expect(page.getByText('Use existing worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Use existing worktree').click()

    // Worktree dropdown should load
    const wtSelect = dialog.locator('select').last()
    await expect(wtSelect).toBeEnabled({ timeout: 10000 })

    // Select the worktree entry (format: "branch — path")
    const options = await wtSelect.locator('option').allTextContents()
    const wtOption = options.find(o => o.includes('ui-wt-branch'))
    expect(wtOption).toBeTruthy()
    await wtSelect.selectOption({ label: wtOption! })

    // Submit
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 30000 })
    await expect(page).toHaveURL(WORKSPACE_URL_RE, { timeout: 30000 })
  })

  // ─── Git Mode: Use Current State ────────────────────────────────────

  test('use-current-state on managed worktree: registers tab so worktree is not prematurely deleted', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-current-managed')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-current-managed-worktrees', 'current-branch')

    // Create a workspace with a managed worktree.
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Current Managed WS',
      adminOrgId,
      repoDir,
      'current-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Open a second agent using "use current state" (no git mode fields) pointing directly
    // at the managed worktree path. The backend should detect it's a managed worktree and
    // register this tab.
    const secondAgentId = await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, worktreeDir)

    // Close the first agent — worktree should persist because second agent registered.
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    const firstAgent = agents.find(a => a.id !== secondAgentId)!
    expect(firstAgent).toBeTruthy()
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, firstAgent.id)
    expect(resp.worktreeCleanupPending).toBeFalsy()
    expect(existsSync(worktreeDir)).toBe(true)

    // Close the second agent — last tab, clean worktree → auto-delete.
    const resp2 = await closeAgentViaAPI(hubUrl, adminToken, workerId, secondAgentId)
    expect(resp2.worktreeCleanupPending).toBeFalsy()
    await waitForPathDeleted(worktreeDir)
  })

  test('use-current-state on unmanaged worktree: does NOT register or track', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-current-unmanaged')
    const realDataDir = realpathSync(dataDir)

    // Create a worktree manually (not via LeapMux).
    const worktreeDir = join(realDataDir, 'test-repo-current-unmanaged-ext')
    execSync(`git worktree add ${join(dataDir, 'test-repo-current-unmanaged-ext')} -b ext-branch`, { cwd: repoDir })
    expect(existsSync(worktreeDir)).toBe(true)

    // Create workspace using "use current state" (default) pointing at the manual worktree.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Current Unmanaged WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, worktreeDir)

    // Close the agent.
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // No cleanup — unmanaged worktree should still exist.
    expect(resp.worktreeCleanupPending).toBeFalsy()
    expect(existsSync(worktreeDir)).toBe(true)
    expect(branchExists(repoDir, 'ext-branch')).toBe(true)
  })

  // ─── Git Mode: Create Worktree with Base Branch ─────────────────────

  test('create-worktree with base branch: new worktree starts from specified base', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-base-branch')
    const realDataDir = realpathSync(dataDir)

    // Create a feature branch with an extra commit.
    execSync('git checkout -b feature-base', { cwd: repoDir })
    execSync('git config user.email "test@test.com"', { cwd: repoDir })
    execSync('git config user.name "Test"', { cwd: repoDir })
    writeFileSync(join(repoDir, 'feature.txt'), 'feature content\n')
    execSync('git add .', { cwd: repoDir })
    execSync('git commit -m "feature commit"', { cwd: repoDir })
    const featureHead = execSync('git rev-parse HEAD', { cwd: repoDir }).toString().trim()

    // Go back to main.
    execSync('git checkout main', { cwd: repoDir })
    const mainHead = execSync('git rev-parse HEAD', { cwd: repoDir }).toString().trim()
    expect(featureHead).not.toBe(mainHead)

    // Create workspace with worktree based on feature-base branch.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Base Branch WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir, {
      createWorktree: true,
      worktreeBranch: 'derived-from-feature',
      worktreeBaseBranch: 'feature-base',
    })

    // Verify worktree was created.
    const worktreeDir = join(realDataDir, 'test-repo-base-branch-worktrees', 'derived-from-feature')
    expect(existsSync(worktreeDir)).toBe(true)

    // Verify the new worktree's HEAD matches the feature branch HEAD (not main).
    const derivedHead = execSync('git rev-parse HEAD', { cwd: worktreeDir }).toString().trim()
    expect(derivedHead).toBe(featureHead)
    expect(derivedHead).not.toBe(mainHead)
  })

  test('create-worktree with base branch via UI: base branch selector works', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-base-branch-ui')
    const realDataDir = realpathSync(dataDir)

    // Create a feature branch.
    execSync('git checkout -b feature-ui-base', { cwd: repoDir })
    execSync('git config user.email "test@test.com"', { cwd: repoDir })
    execSync('git config user.name "Test"', { cwd: repoDir })
    writeFileSync(join(repoDir, 'feature.txt'), 'feature content\n')
    execSync('git add .', { cwd: repoDir })
    execSync('git commit -m "feature commit"', { cwd: repoDir })
    execSync('git checkout main', { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Base Branch UI WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options, select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    // Base Branch label and selector should be visible
    await expect(dialog.getByText('Base Branch')).toBeVisible({ timeout: 10000 })

    // The base branch selector should default to "main" (current)
    // and include "feature-ui-base"
    const baseBranchSelect = dialog.locator('select').last()
    await expect(baseBranchSelect).toBeEnabled({ timeout: 10000 })
    const options = await baseBranchSelect.locator('option').allTextContents()
    expect(options.some(o => o.includes('feature-ui-base'))).toBe(true)

    // Select feature-ui-base as base branch
    await baseBranchSelect.selectOption('feature-ui-base')

    // Set a branch name and submit
    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    await branchInput.clear()
    await branchInput.fill('from-feature-base')

    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 30000 })
    await expect(page).toHaveURL(WORKSPACE_URL_RE, { timeout: 30000 })

    // Verify the worktree was created from the feature branch
    const worktreeDir = join(realDataDir, 'test-repo-base-branch-ui-worktrees', 'from-feature-base')
    expect(existsSync(worktreeDir)).toBe(true)
    expect(existsSync(join(worktreeDir, 'feature.txt'))).toBe(true)
  })

  // ─── Git Mode: Validation ──────────────────────────────────────────

  test('switch-branch mode disables submit when no branch selected', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-switch-validate')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Switch Validate WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Switch to branch')).toBeVisible({ timeout: 10000 })
    await page.getByText('Switch to branch').click()

    // Create button should be disabled until a branch is selected
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })
    await expect(createBtn).toBeDisabled()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('use-worktree mode disables submit when no worktree selected', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-validate')

    // Create a worktree so the dropdown has entries
    execSync(`git worktree add ${join(dataDir, 'test-repo-use-wt-validate-wt')} -b validate-wt`, { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Use WT Validate WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Use existing worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Use existing worktree').click()

    // Create button should be disabled until a worktree is selected
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })
    await expect(createBtn).toBeDisabled()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('use-current-state mode enables submit immediately', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-current-validate')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Current Validate WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // "Use current state" is default — submit should be enabled immediately
    await expect(page.getByText('Use current state')).toBeVisible({ timeout: 10000 })
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })
    await expect(createBtn).toBeEnabled()

    // Current branch info should be displayed
    await expect(page.getByText('Currently on branch:')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })
})
