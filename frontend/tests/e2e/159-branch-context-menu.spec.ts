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

    // Target the three-dot trigger by its aria-expanded attribute, not
    // `.locator('button').last()`: DropdownMenu renders its items as
    // <button role="menuitem"> inside the row's popover (display:none
    // while closed), so `.last()` would resolve to the hidden "Delete
    // branch..." item and never become clickable. Only the trigger
    // carries aria-expanded (same approach as the worker context menu in
    // 027-tunnel-ui).
    const menuTrigger = branchRow.locator('[aria-expanded]').first()
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
    await branchRow.locator('[aria-expanded]').first().click()
    await page.getByRole('menuitem', { name: 'Delete branch...' }).click()

    await expect(page.getByRole('heading', { name: 'Delete branch' })).toBeVisible()

    // Worktree variant: dialog shows the worktree path.
    await expect(page.getByRole('dialog').getByText(/branch-delete-repo-worktrees\/delete-branch/)).toBeVisible()

    // ConfirmButton arms on first click, fires on second.
    await page.getByRole('button', { name: 'Delete branch' }).click()
    await page.getByRole('button', { name: 'Confirm?' }).click()

    // The dialog holds open under its busy overlay until the coupled tab
    // closes report back, then dismisses.
    await expect(page.getByRole('heading', { name: 'Delete branch' })).not.toBeVisible()

    // Removal is coupled to the tab closes (WorktreeAction.REMOVE), so the
    // worker tears down the worktree dir + branch in the background once
    // the last referencing tab closes; failures surface via toast.
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'delete-branch')).toBe(false)
    }).toPass()
  })

  test('change branch dialog opens in switch-to mode with all git options', async ({
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
    await branchRow.locator('[aria-expanded]').first().click()
    await page.getByRole('menuitem', { name: 'Change branch...' }).click()

    await expect(page.getByRole('heading', { name: 'Change branch' })).toBeVisible()
    // The dialog (via GitOptions) does not render the current branch name
    // as text -- the switch-to picker excludes the current branch and
    // there's no standalone "current branch" label -- so assert the
    // default mode state instead: SwitchBranch is preselected.
    await expect(page.getByRole('dialog').getByRole('radio', { name: 'Switch to branch' })).toBeChecked()

    // Switch-to mode is the default; the three radios are visible.
    await expect(page.getByRole('dialog').getByText('Switch to branch')).toBeVisible()
    await expect(page.getByRole('dialog').getByText('Create new branch')).toBeVisible()
    await expect(page.getByRole('dialog').getByText('Create new worktree')).toBeVisible()

    // Cancel closes the dialog without changes.
    await page.getByRole('button', { name: 'Cancel' }).click()
    await expect(page.getByRole('heading', { name: 'Change branch' })).not.toBeVisible()
  })
})
