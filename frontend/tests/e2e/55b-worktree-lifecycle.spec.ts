import { execSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import path, { join } from 'node:path'
import {
  OpenTerminalRequestSchema,
  OpenTerminalResponseSchema,
} from '../../src/generated/leapmux/v1/terminal_pb'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import {
  branchExists,
  closeAgentViaAPI,
  closeTerminalViaAPI,
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  forceRemoveWorktreeViaAPI,
  keepWorktreeViaAPI,
  listAgentsViaAPI,
  openNewWorkspaceDialog,
  setWorkingDir,
  waitForOrgPageReady,
  waitForPathDeleted,
  waitForWorker,
  WORKSPACE_URL_RE,
} from './helpers/worktree'

const frontendDir = path.resolve(import.meta.dirname, '../..')

test.describe('Worktree Lifecycle', () => {
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

    // Open a second terminal using the existing worktree (use-worktree mode, not create)
    const channel = await getTestChannel(hubUrl, adminToken)
    const termResp2 = await channel.callWorker(
      workerId,
      'OpenTerminal',
      OpenTerminalRequestSchema,
      OpenTerminalResponseSchema,
      { workspaceId, workerId, orgId: adminOrgId, cols: 80, rows: 24, workingDir: repoDir, useWorktreePath: worktreeDir },
    )
    const terminalId = termResp2.terminalId

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
})
