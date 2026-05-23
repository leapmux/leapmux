import { existsSync, realpathSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import {
  branchExists,
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
} from './helpers/worktree'

test.describe('Branch context menu', () => {
  test('three-dot menu opens with Change and Delete items', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-menu-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Menu WS',
      adminOrgId,
      repoDir,
      'menu-branch',
    )

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const branchRow = page.locator('[data-testid="tab-tree-branch-group"]').first()
    await expect(branchRow).toBeVisible()
    await branchRow.hover()

    // The three-dot button only appears on hover; sidebarActions makes
    // it visible once the row has :hover. Click it.
    const menuTrigger = branchRow.locator('button').last()
    await menuTrigger.click()

    await expect(page.getByRole('menuitem', { name: 'Change branch...' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Delete branch...' })).toBeVisible()
  })

  test('delete branch (worktree variant) removes worktree and branch', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-delete-repo')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'branch-delete-repo-worktrees', 'delete-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Delete WS',
      adminOrgId,
      repoDir,
      'delete-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const branchRow = page.locator('[data-testid="tab-tree-branch-group"]').first()
    await branchRow.hover()
    await branchRow.locator('button').last().click()
    await page.getByRole('menuitem', { name: 'Delete branch...' }).click()

    await expect(page.getByRole('heading', { name: 'Delete branch' })).toBeVisible()

    // Worktree variant: dialog shows the worktree path.
    await expect(page.getByRole('dialog').getByText(/branch-delete-repo-worktrees\/delete-branch/)).toBeVisible()

    // ConfirmButton arms on first click, fires on second.
    await page.getByRole('button', { name: 'Delete branch' }).click()
    await page.getByRole('button', { name: 'Confirm?' }).click()

    await expect(page.getByRole('heading', { name: 'Delete branch' })).not.toBeVisible()

    // Worktree directory and branch are deleted in the background by the
    // worker after the close RPCs return.
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'delete-branch')).toBe(false)
    }).toPass()
  })

  test('change branch dialog opens with the current branch shown', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-change-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Change WS',
      adminOrgId,
      repoDir,
      'change-branch',
    )

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const branchRow = page.locator('[data-testid="tab-tree-branch-group"]').first()
    await branchRow.hover()
    await branchRow.locator('button').last().click()
    await page.getByRole('menuitem', { name: 'Change branch...' }).click()

    await expect(page.getByRole('heading', { name: 'Change branch' })).toBeVisible()
    await expect(page.getByRole('dialog').getByText(/change-branch/)).toBeVisible()

    // Switch-to mode is the default; the three radios are visible.
    await expect(page.getByRole('dialog').getByText('Switch to branch')).toBeVisible()
    await expect(page.getByRole('dialog').getByText('Create new branch')).toBeVisible()
    await expect(page.getByRole('dialog').getByText('Create new worktree')).toBeVisible()

    // Cancel closes the dialog without changes.
    await page.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('heading', { name: 'Change branch' })).not.toBeVisible()
  })
})
