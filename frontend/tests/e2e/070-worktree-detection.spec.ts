import { mkdirSync } from 'node:fs'
import path, { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import {
  createGitRepo,
  openNewAgentDialog,
  openNewWorkspaceDialog,
  setWorkingDir,
  waitForOrgPageReady,
  waitForWorker,
} from './helpers/worktree'

const frontendDir = path.resolve(import.meta.dirname, '../..')

test.describe('Worktree Detection', () => {
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

  test('five git mode radio options appear for git repo directory in new workspace dialog', async ({
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

    // All five radio options should appear
    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Switch to branch')).toBeVisible()
    await expect(page.getByText('Create new branch')).toBeVisible()
    await expect(page.getByText('Create new worktree')).toBeVisible()
    await expect(page.getByText('Use existing worktree')).toBeVisible()

    // Default should be "Use current state" — branch name input should NOT be visible
    await expect(page.getByText('Branch Name')).not.toBeVisible()
    await expect(page.getByText('Worktree path:')).not.toBeVisible()

    // Select "Create new branch" — sub-controls should appear (branch name + base, no worktree path)
    await page.getByText('Create new branch').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Branch Name')).toBeVisible()
    await expect(dialog.getByText('Base Branch')).toBeVisible()
    await expect(page.getByText('Worktree path:')).not.toBeVisible()

    // Select "Create new worktree" — sub-controls should appear
    await page.getByText('Create new worktree').click()
    await expect(dialog.getByText('Branch Name')).toBeVisible()
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

    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Create new branch')).toBeVisible()
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

    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Create new branch')).toBeVisible()
    await expect(page.getByText('Create new worktree')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('git mode options appear for existing worktree root', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-wt-root')
    const { realpathSync } = await import('node:fs')
    const { execSync } = await import('node:child_process')
    const { existsSync } = await import('node:fs')
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
    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Create new worktree')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('dirty warning appears when source working copy has uncommitted changes', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dirty-warn')
    const { writeFileSync } = await import('node:fs')

    // Make the repo dirty
    writeFileSync(join(repoDir, 'dirty-file.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible()
    await page.getByText('Create new worktree').click()

    // Warning about uncommitted changes should be visible
    await expect(page.getByText('uncommitted changes that will not be transferred')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })
})
