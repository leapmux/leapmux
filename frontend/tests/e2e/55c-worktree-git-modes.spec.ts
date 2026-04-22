import { execSync } from 'node:child_process'
import { existsSync, realpathSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { WorktreeAction } from '../../src/generated/leapmux/v1/common_pb'
import { TabType } from '../../src/generated/leapmux/v1/workspace_pb'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import {
  branchExists,
  closeAgentViaAPI,
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  inspectLastTabCloseViaAPI,
  openNewWorkspaceDialog,
  setWorkingDir,
  waitForAgentsViaAPI,
  waitForOrgPageReady,
  waitForPathDeleted,
  waitForWorker,
  WORKSPACE_URL_RE,
} from './helpers/worktree'

test.describe('Worktree Git Modes', () => {
  // ─── Worktree-from-Worktree ──────────────────────────────────────

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
    await expect(pathInput).toHaveValue(realRepoDir)

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
    await expect(pathInput).toHaveValue(realRepoDir)

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
    await expect(page.getByText('Switch to branch')).toBeVisible()
    await page.getByText('Switch to branch').click()

    // Branch dropdown should load with local branches
    const branchSelect = dialog.locator('select').last()
    await expect(branchSelect).toBeEnabled()
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

    await expect(page.getByText('Switch to branch')).toBeVisible()
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
    const agents = await waitForAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
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
    const agents = await waitForAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    const firstAgent = agents.find(a => a.id !== secondAgentId)!
    expect(firstAgent).toBeTruthy()
    await closeAgentViaAPI(hubUrl, adminToken, workerId, firstAgent.id)
    expect(existsSync(worktreeDir)).toBe(true)

    // Close the last tab with WORKTREE_ACTION_REMOVE — worktree should be deleted.
    const inspect2 = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, secondAgentId)
    expect(inspect2.shouldPrompt).toBe(true)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, secondAgentId, WorktreeAction.REMOVE)
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
    const agents = await waitForAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // Unmanaged worktree should NOT be cleaned up.
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
    await expect(page.getByText('Use existing worktree')).toBeVisible()
    await page.getByText('Use existing worktree').click()

    // Worktree dropdown should load
    const wtSelect = dialog.locator('select').last()
    await expect(wtSelect).toBeEnabled()

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
    const agents = await waitForAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    const firstAgent = agents.find(a => a.id !== secondAgentId)!
    expect(firstAgent).toBeTruthy()
    await closeAgentViaAPI(hubUrl, adminToken, workerId, firstAgent.id)
    expect(existsSync(worktreeDir)).toBe(true)

    // Close the last tab with WORKTREE_ACTION_REMOVE — worktree should be deleted.
    const inspect2 = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, secondAgentId)
    expect(inspect2.shouldPrompt).toBe(true)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, secondAgentId, WorktreeAction.REMOVE)
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
    const agents = await waitForAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    // No cleanup — unmanaged worktree should still exist.
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
    await expect(page.getByText('Create new worktree')).toBeVisible()
    await page.getByText('Create new worktree').click()

    // Base Branch label and selector should be visible
    await expect(dialog.getByText('Base Branch')).toBeVisible()

    // The base branch selector should default to "main" (current)
    // and include "feature-ui-base"
    const baseBranchSelect = dialog.locator('select').last()
    await expect(baseBranchSelect).toBeEnabled()
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
})
