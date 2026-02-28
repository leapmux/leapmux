import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import {
  createWorkspaceViaAPI,
  loginViaToken,
  waitForWorkspaceReady,
} from './helpers'

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
  await expect(sidebarBtn.or(createBtn)).toBeVisible()
  if (await sidebarBtn.isVisible())
    await sidebarBtn.click()
  else
    await createBtn.click()
  await expect(page.getByRole('heading', { name: 'New Workspace' })).toBeVisible()
}

/**
 * Create a workspace with a worktree enabled via the ConnectRPC API.
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
  const res = await fetch(`${hubUrl}/leapmux.v1.WorkspaceService/CreateWorkspace`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({
      workerId,
      title,
      orgId,
      workingDir,
      createWorktree: true,
      worktreeBranch,
    }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`createWorkspaceWithWorktreeViaAPI failed: ${res.status} ${body}`)
  }
  const data = await res.json() as { workspace: { id: string } }
  return data.workspace.id
}

/**
 * Open a terminal with a worktree via the ConnectRPC API.
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
  const res = await fetch(`${hubUrl}/leapmux.v1.TerminalService/OpenTerminal`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({
      workspaceId,
      workerId,
      orgId,
      cols: 80,
      rows: 24,
      workingDir,
      createWorktree: true,
      worktreeBranch,
    }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`openTerminalWithWorktreeViaAPI failed: ${res.status} ${body}`)
  }
  const data = await res.json() as { terminalId: string }
  return data.terminalId
}

/**
 * Close a terminal via the ConnectRPC API.
 * Returns the response including worktree cleanup info.
 */
async function closeTerminalViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
  orgId: string,
  terminalId: string,
): Promise<{ worktreeCleanupPending?: boolean, worktreePath?: string, worktreeId?: string }> {
  const res = await fetch(`${hubUrl}/leapmux.v1.TerminalService/CloseTerminal`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workspaceId, orgId, terminalId }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`closeTerminalViaAPI failed: ${res.status} ${body}`)
  }
  return await res.json() as any
}

/**
 * Close an agent via the ConnectRPC API.
 * Returns the response including worktree cleanup info.
 */
async function closeAgentViaAPI(
  hubUrl: string,
  token: string,
  agentId: string,
): Promise<{ worktreeCleanupPending?: boolean, worktreePath?: string, worktreeId?: string }> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AgentService/CloseAgent`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ agentId }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`closeAgentViaAPI failed: ${res.status} ${body}`)
  }
  return await res.json() as any
}

/**
 * List agents for a workspace via API.
 */
async function listAgentsViaAPI(
  hubUrl: string,
  token: string,
  workspaceId: string,
): Promise<Array<{ id: string, workingDir: string }>> {
  const res = await fetch(`${hubUrl}/leapmux.v1.AgentService/ListAgents`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ workspaceId }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`listAgentsViaAPI failed: ${res.status} ${body}`)
  }
  const data = await res.json() as { agents: Array<{ id: string, workingDir: string }> }
  return data.agents ?? []
}

/**
 * Force-remove a worktree via API.
 */
async function forceRemoveWorktreeViaAPI(
  hubUrl: string,
  token: string,
  worktreeId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.GitService/ForceRemoveWorktree`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ worktreeId }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`forceRemoveWorktreeViaAPI failed: ${res.status} ${body}`)
  }
}

/**
 * Keep a worktree (stop tracking but leave on disk) via API.
 */
async function keepWorktreeViaAPI(
  hubUrl: string,
  token: string,
  worktreeId: string,
): Promise<void> {
  const res = await fetch(`${hubUrl}/leapmux.v1.GitService/KeepWorktree`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'Authorization': `Bearer ${token}`,
    },
    body: JSON.stringify({ worktreeId }),
  })
  if (!res.ok) {
    const body = await res.text().catch(() => '')
    throw new Error(`keepWorktreeViaAPI failed: ${res.status} ${body}`)
  }
}

/** Wait for a worker to be available (retry with backoff). */
async function waitForWorker(page: Page) {
  const dialog = page.getByRole('dialog')
  const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })
  const refreshBtn = dialog.getByTitle('Refresh workers')
  for (let attempt = 0; attempt < 6; attempt++) {
    try {
      await expect(createBtn).toBeEnabled()
      break
    }
    catch {
      if (attempt === 5)
        throw new Error('No online worker found')
      await refreshBtn.click()
    }
  }
}

// ─── UI Detection Tests ─────────────────────────────────────────────

test.describe('Worktree Support', () => {
  test('non-git directory hides worktree option in new workspace dialog', async ({
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

    // Open new workspace dialog via sidebar button
    await openNewWorkspaceDialog(page)

    await waitForWorker(page)

    // Set working directory to a known non-git directory
    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(nonGitDir)
    await pathInput.press('Enter')

    // Wait for the git info check to complete.
    await page.waitForTimeout(2000)

    // Verify worktree checkbox is not visible
    await expect(page.getByText('Create new worktree')).not.toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('subdirectory of git repo hides worktree option in new workspace dialog', async ({
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
    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(subDir)
    await pathInput.press('Enter')

    // Wait for the git info check to complete.
    await page.waitForTimeout(2000)

    // Verify worktree checkbox is not visible (even though it's inside a git repo)
    await expect(page.getByText('Create new worktree')).not.toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('worktree checkbox appears for git repo directory in new workspace dialog', async ({
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

    // Set working directory to the git repo
    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    // Worktree checkbox should appear and be checked by default
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

    // Branch name input and path preview should appear (checkbox is checked by default)
    await expect(page.getByText('Branch Name')).toBeVisible()
    await expect(page.getByText('Worktree path:')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('worktree checkbox appears in new agent dialog for git repo', async ({
    page,
    leapmuxServer,
    workspace,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-agent')

    await loginViaToken(page, adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
    await addMenu.click()
    await page.getByRole('menuitem', { name: 'New agent...' }).click()

    await expect(page.getByRole('heading', { name: 'New Agent' })).toBeVisible()

    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('worktree checkbox appears in new terminal dialog for git repo', async ({
    page,
    leapmuxServer,
    workspace,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-terminal')

    await loginViaToken(page, adminToken)
    await page.goto(workspace.workspaceUrl)
    await waitForWorkspaceReady(page)

    const addMenu = page.locator('[data-testid="tab-more-menu"]').first()
    await addMenu.click()
    await page.getByRole('menuitem', { name: 'New terminal...' }).click()

    await expect(page.getByRole('heading', { name: 'New Terminal' })).toBeVisible()

    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

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
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    // Worktree checkbox is checked by default
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    await branchInput.clear()
    await branchInput.fill('e2e-test-branch')

    await expect(page.getByText('e2e-test-branch')).toBeVisible()

    await dialog.getByRole('button', { name: 'Create', exact: true }).click()

    // Wait for the dialog to close first — this signals the API call
    // (including the git worktree creation) has completed.
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 30000 })
    await expect(page).toHaveURL(/\/workspace\//, { timeout: 30000 })
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
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workspaceId)
    expect(agents.length).toBeGreaterThan(0)

    // Close the agent (last tab referencing the worktree)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, agents[0].id)

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
    const termResp = await closeTerminalViaAPI(hubUrl, adminToken, workspaceId, adminOrgId, terminalId)
    expect(termResp.worktreeCleanupPending).toBeFalsy()
    expect(existsSync(worktreeDir)).toBe(true)

    // Now close the agent (last tab)
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workspaceId)
    expect(agents.length).toBeGreaterThan(0)
    const agentResp = await closeAgentViaAPI(hubUrl, adminToken, agents[0].id)

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
      workerId,
      'Existing WT WS',
      adminOrgId,
      manualWorktreeDir,
    )

    // Get the auto-created agent
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workspaceId)
    expect(agents.length).toBeGreaterThan(0)

    // Close the agent
    const resp = await closeAgentViaAPI(hubUrl, adminToken, agents[0].id)

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
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workspaceId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, agents[0].id)

    // Should trigger confirmation (worktree NOT auto-deleted)
    expect(resp.worktreeCleanupPending).toBe(true)
    // The response path is the resolved (canonical) path from the worker
    expect(resp.worktreePath).toContain('test-repo-dirty-worktrees/dirty-branch')
    expect(resp.worktreeId).toBeTruthy()

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)

    // Now force-remove it
    await forceRemoveWorktreeViaAPI(hubUrl, adminToken, resp.worktreeId!)
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
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workspaceId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, agents[0].id)

    // Should trigger confirmation — commits exist only on this branch
    expect(resp.worktreeCleanupPending).toBe(true)
    expect(existsSync(worktreeDir)).toBe(true)

    // Force-remove it
    await forceRemoveWorktreeViaAPI(hubUrl, adminToken, resp.worktreeId!)
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
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workspaceId)
    expect(agents.length).toBeGreaterThan(0)
    const resp = await closeAgentViaAPI(hubUrl, adminToken, agents[0].id)

    // Should trigger confirmation
    expect(resp.worktreeCleanupPending).toBe(true)
    expect(existsSync(worktreeDir)).toBe(true)

    // This time, choose "keep" instead of force-remove
    await keepWorktreeViaAPI(hubUrl, adminToken, resp.worktreeId!)

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
    await expect(page.getByText('cancel-branch', { exact: true })).toBeVisible()

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
    await expect(page.getByText('test-repo-dialog-worktrees/dialog-branch')).toBeVisible()

    // Dialog should show the branch name
    await expect(page.getByText('dialog-branch', { exact: true })).toBeVisible()

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
    await expect(page.getByText('keep-branch', { exact: true })).toBeVisible()

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

  test('worktree checkbox appears for existing worktree root', async ({
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
    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(worktreeDir)
    await pathInput.press('Enter')

    // Worktree checkbox should appear for an existing worktree root
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

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

    const dialog = page.getByRole('dialog')
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    // Worktree checkbox is checked by default
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

    // Warning about uncommitted changes should be visible (checkbox is checked by default)
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
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    // Worktree checkbox is checked by default
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

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
    const pathInput = dialog.getByPlaceholder('Enter path...')
    await pathInput.fill(repoDir)
    await pathInput.press('Enter')

    // Worktree checkbox is checked by default
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })

    // Read initial branch name
    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    const initialBranch = await branchInput.inputValue()
    expect(initialBranch).toBeTruthy()

    // Click the randomize button for branch name (last one — first is for workspace title)
    await dialog.getByTitle('Generate random name').last().click()

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
})
