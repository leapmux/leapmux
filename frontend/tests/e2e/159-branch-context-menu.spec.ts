import { existsSync, realpathSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { branchGroupRow, clickBranchMenuItem, loginViaToken, openBranchMenu, openWorkspace } from './helpers/ui'
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
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-menu-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Menu WS',
      repoDir,
      'menu-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const branchRow = branchGroupRow(page)
    await expect(branchRow).toBeVisible()
    await openBranchMenu(page, branchRow)

    await expect(page.getByRole('menuitem', { name: 'Change branch...' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Delete branch...' })).toBeVisible()
  })

  test('delete branch (worktree variant) removes worktree and branch', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-delete-repo')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'branch-delete-repo-worktrees', 'delete-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Delete WS',
      repoDir,
      'delete-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete branch...')

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
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-change-repo')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Branch Change WS',
      repoDir,
      'change-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Change branch...')

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
