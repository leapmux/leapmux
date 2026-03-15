import { execSync } from 'node:child_process'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { loginViaToken } from './helpers/ui'
import {
  createGitRepo,
  openNewWorkspaceDialog,
  setWorkingDir,
  waitForOrgPageReady,
  waitForWorker,
} from './helpers/worktree'

test.describe('Worktree Validation', () => {
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

  // ─── Git Mode Preservation & Dynamic Updates ──────────────────────

  test('git mode is preserved when switching between git repos', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repo1 = createGitRepo(dataDir, 'test-repo-preserve-mode-1')
    const repo2 = createGitRepo(dataDir, 'test-repo-preserve-mode-2')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')

    // Navigate to first repo and select "Create new branch"
    await setWorkingDir(page, repo1)
    await expect(page.getByText('Use current state')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new branch').click()

    // Verify sub-controls are visible
    await expect(dialog.getByText('Branch Name')).toBeVisible()
    await expect(dialog.getByText('Base Branch')).toBeVisible()

    // Switch to a different git repo
    await setWorkingDir(page, repo2)

    // Mode should be preserved as "Create new branch"
    await expect(dialog.getByText('Branch Name')).toBeVisible({ timeout: 10000 })
    await expect(dialog.getByText('Base Branch')).toBeVisible()

    // Now test with "Create new worktree"
    await page.getByText('Create new worktree').click()
    await expect(page.getByText('Worktree path:')).toBeVisible()

    // Switch back to first repo
    await setWorkingDir(page, repo1)

    // Mode should still be "Create new worktree"
    await expect(page.getByText('Worktree path:')).toBeVisible({ timeout: 10000 })

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('branch list updates when switching between git repos', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repo1 = createGitRepo(dataDir, 'test-repo-branch-refresh-1')
    const repo2 = createGitRepo(dataDir, 'test-repo-branch-refresh-2')

    // Create unique branches in each repo
    execSync('git branch alpha-branch', { cwd: repo1 })
    execSync('git branch beta-branch', { cwd: repo2 })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')

    // Navigate to repo1 and select "Switch to branch"
    await setWorkingDir(page, repo1)
    await expect(page.getByText('Switch to branch')).toBeVisible({ timeout: 10000 })
    await page.getByText('Switch to branch').click()

    // Wait for branches to load — should contain alpha-branch
    const branchSelect = dialog.locator('select').last()
    await expect(branchSelect).toBeEnabled({ timeout: 10000 })
    const repo1Options = await branchSelect.locator('option').allTextContents()
    expect(repo1Options.some(o => o.includes('alpha-branch'))).toBe(true)
    expect(repo1Options.some(o => o.includes('beta-branch'))).toBe(false)

    // Switch to repo2
    await setWorkingDir(page, repo2)

    // Branch list should update — should contain beta-branch, not alpha-branch
    await expect(async () => {
      const repo2Options = await branchSelect.locator('option').allTextContents()
      expect(repo2Options.some(o => o.includes('beta-branch'))).toBe(true)
      expect(repo2Options.some(o => o.includes('alpha-branch'))).toBe(false)
    }).toPass({ timeout: 10000 })

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('refresh button re-fetches branch list', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-refresh-branches')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Select "Switch to branch" to see the branch list
    await expect(page.getByText('Switch to branch')).toBeVisible({ timeout: 10000 })
    await page.getByText('Switch to branch').click()

    const branchSelect = dialog.locator('select').last()
    await expect(branchSelect).toBeEnabled({ timeout: 10000 })

    // Initially, only "main" should be listed
    const options = await branchSelect.locator('option').allTextContents()
    expect(options.some(o => o.includes('new-after-open'))).toBe(false)

    // Create a new branch in the repo while the dialog is open
    execSync('git branch new-after-open', { cwd: repoDir })

    // Click the refresh button (the one next to "Working directory" label)
    await dialog.getByLabel('Refresh directory tree').click()

    // The branch list should now include the newly created branch
    await expect(async () => {
      const updatedOptions = await branchSelect.locator('option').allTextContents()
      expect(updatedOptions.some(o => o.includes('new-after-open'))).toBe(true)
    }).toPass({ timeout: 10000 })

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Branch Existence Validation ──────────────────────────────────

  test('branch existence error shown for create-branch mode', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-branch-exists')

    // Create a branch that we'll try to duplicate
    execSync('git branch existing-branch', { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Select "Create new branch"
    await expect(page.getByText('Create new branch')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new branch').click()

    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })

    // Enter the name of the existing branch
    await branchInput.clear()
    await branchInput.fill('existing-branch')

    // Should show "already exists" error and disable Create
    await expect(dialog.getByText('A branch with this name already exists')).toBeVisible()
    await expect(createBtn).toBeDisabled()

    // Enter a unique name — error clears
    await branchInput.clear()
    await branchInput.fill('unique-new-branch')
    await expect(dialog.getByText('A branch with this name already exists')).not.toBeVisible()
    await expect(createBtn).toBeEnabled()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('branch existence error shown for create-worktree mode', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-wt-branch-exists')

    // Create a branch that we'll try to duplicate via worktree
    execSync('git branch wt-existing-branch', { cwd: repoDir })

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })

    // Enter the name of the existing branch
    await branchInput.clear()
    await branchInput.fill('wt-existing-branch')

    // Should show "already exists" error and disable Create
    await expect(dialog.getByText('A branch with this name already exists')).toBeVisible()
    await expect(createBtn).toBeDisabled()

    // Enter a unique name — error clears
    await branchInput.clear()
    await branchInput.fill('unique-wt-branch')
    await expect(dialog.getByText('A branch with this name already exists')).not.toBeVisible()
    await expect(createBtn).toBeEnabled()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })
})
