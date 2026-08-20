import { execSync } from 'node:child_process'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { loginViaToken, menuOptionTexts } from './helpers/ui'
import {
  createGitRepo,
  openNewWorkspaceDialog,
  setWorkingDir,
  waitForAppPageReady,
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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Error Test WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible()
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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible()
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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Switch Validate WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Switch to branch')).toBeVisible()
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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Use WT Validate WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Use existing worktree')).toBeVisible()
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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Current Validate WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // "Use current state" is default — submit should be enabled immediately
    await expect(page.getByText('Use current state')).toBeVisible()
    const createBtn = dialog.getByRole('button', { name: 'Create', exact: true })
    await expect(createBtn).toBeEnabled()

    // Current branch info should be displayed
    await expect(page.getByText('Currently on branch:')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Git Mode Preservation & Dynamic Updates ──────────────────────

  test('git mode resets to the default when switching between git repos', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repo1 = createGitRepo(dataDir, 'test-repo-preserve-mode-1')
    const repo2 = createGitRepo(dataDir, 'test-repo-preserve-mode-2')

    await loginViaToken(page, adminToken)
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')

    // Navigate to first repo and select "Create new branch"
    await setWorkingDir(page, repo1)
    await expect(page.getByText('Use current state')).toBeVisible()
    await page.getByText('Create new branch').click()

    // Verify sub-controls are visible
    await expect(dialog.getByText('Branch Name')).toBeVisible()
    await expect(dialog.getByText('Base Branch')).toBeVisible()

    // Switch to a different git repo. Every selection the mode depends on --
    // base branch, checkout branch, worktree path, and the fetched lists
    // behind them -- belongs to the OLD repo, so GitOptions drops the mode
    // along with them and starts the new repo at its default (see the
    // worker/path reset effect). Asserting the mode survives, as this spec
    // used to, has been failing since that reset landed.
    await setWorkingDir(page, repo2)

    await expect(dialog.getByText('Branch Name')).not.toBeVisible()
    await expect(dialog.getByText('Base Branch')).not.toBeVisible()
    await expect(dialog.getByText('Use current state')).toBeVisible()

    // The reset is per-repo-switch, not a one-shot: picking a mode in the new
    // repo works, and switching back resets that one too.
    await page.getByText('Create new worktree').click()
    await expect(page.getByText('Worktree path:')).toBeVisible()

    await setWorkingDir(page, repo1)
    await expect(page.getByText('Worktree path:')).not.toBeVisible()

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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')

    // Navigate to repo1 and select "Switch to branch"
    await setWorkingDir(page, repo1)
    await expect(page.getByText('Switch to branch')).toBeVisible()
    await page.getByText('Switch to branch').click()

    // Wait for branches to load — should contain alpha-branch
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
    const repo1Options = await menuOptionTexts(dialog, 'branch-select-menu')
    expect(repo1Options.some(o => o.includes('alpha-branch'))).toBe(true)
    expect(repo1Options.some(o => o.includes('beta-branch'))).toBe(false)

    // Switch to repo2. The switch resets the mode to the repo default, so
    // re-select "Switch to branch" to bring the branch picker back -- the
    // point of this test is that the list it then shows is REFETCHED for
    // repo2 rather than served from repo1's cache.
    await setWorkingDir(page, repo2)
    await expect(page.getByText('Use current state')).toBeVisible()
    await page.getByText('Switch to branch').click()

    // Branch list should update — should contain beta-branch, not alpha-branch
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
    await expect(async () => {
      const repo2Options = await menuOptionTexts(dialog, 'branch-select-menu')
      expect(repo2Options.some(o => o.includes('beta-branch'))).toBe(true)
      expect(repo2Options.some(o => o.includes('alpha-branch'))).toBe(false)
    }).toPass()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('refresh button re-fetches branch list', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-refresh-branches')

    await loginViaToken(page, adminToken)
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Select "Switch to branch" to see the branch list
    await expect(page.getByText('Switch to branch')).toBeVisible()
    await page.getByText('Switch to branch').click()
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()

    // Initially, only "main" should be listed
    const options = await menuOptionTexts(dialog, 'branch-select-menu')
    expect(options.some(o => o.includes('new-after-open'))).toBe(false)

    // Create a new branch in the repo while the dialog is open
    execSync('git branch new-after-open', { cwd: repoDir })

    // Click the refresh button (the one next to "Working directory" label)
    await dialog.getByLabel('Refresh directory tree').click()

    // The branch list should now include the newly created branch
    await expect(async () => {
      const updatedOptions = await menuOptionTexts(dialog, 'branch-select-menu')
      expect(updatedOptions.some(o => o.includes('new-after-open'))).toBe(true)
    }).toPass()

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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Select "Create new branch"
    await expect(page.getByText('Create new branch')).toBeVisible()
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
    await page.goto('/')
    await waitForAppPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible()
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
