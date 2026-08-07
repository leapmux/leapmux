import type { Page } from '@playwright/test'
import { execSync } from 'node:child_process'
import { existsSync, realpathSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { getRecordedToasts } from './helpers/toast'
import { branchGroupRow, clickBranchMenuItem, loginViaToken, openBranchMenu, openWorkspace } from './helpers/ui'
import {
  branchExists,
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  expectRepoBranch,
  waitForAgentStartupViaAPI,
} from './helpers/worktree'

/** ConfirmButton arms on the first click and fires on the second. */
async function confirmDeleteBranch(page: Page) {
  await page.getByRole('button', { name: 'Delete branch' }).click()
  await page.getByRole('button', { name: 'Confirm?' }).click()
}

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
    // `createWorkspaceWithWorktreeViaAPI` waits only for the worktree
    // DIRECTORY. The worker writes the `worktree_tabs` row later, on the same
    // async startup goroutine, and the REMOVE below reclaims the worktree only
    // when the last row that references it goes. `waitForAgentStartupViaAPI`
    // is what that helper's own comment prescribes for a test that closes a
    // tab and expects a worktree effect.
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete branch...')

    await expect(page.getByRole('heading', { name: 'Delete branch' })).toBeVisible()

    // Worktree variant: dialog shows the worktree path.
    await expect(page.getByRole('dialog').getByText(/branch-delete-repo-worktrees\/delete-branch/)).toBeVisible()

    await confirmDeleteBranch(page)

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

  test('delete branch (in-place variant) relabels the sidebar without a reload', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'branch-delete-inplace-repo')
    // A plain repo checked out on the doomed branch -- no worktree. That is
    // what puts the dialog on its non-worktree path. The user picks a
    // switch-to branch, and the worker then runs checkout and `git branch
    // -D` in place. Every tab stays on the new branch.
    // `createGitRepo` already persists `core.fsmonitor false` into this
    // repo's config, so this command needs no `-c` override.
    execSync('git checkout -b doomed-branch', { cwd: repoDir })

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Branch Delete In Place WS')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir)
    // Wait for the agent to leave STARTING before the browser subscribes.
    // `activeTabReady` keys AppShell's git-status effect, so a STARTING to
    // ACTIVE flip that lands AFTER the delete re-runs that effect and
    // relabels the sidebar on its own -- which would let this test pass
    // against the broken code. The ACTIVE broadcast also carries the git
    // snapshot taken at phase 1, so a late one reverts the label to
    // `doomed-branch` and would fail the test against the FIXED code. One
    // wait closes both directions.
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    await expect(branchGroupRow(page)).toContainText('doomed-branch')

    await clickBranchMenuItem(page, branchGroupRow(page), 'Delete branch...')
    await expect(page.getByRole('heading', { name: 'Delete branch' })).toBeVisible()

    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Switch this working directory to:')).toBeVisible()
    await dialog.getByRole('combobox').first().selectOption('main')

    await confirmDeleteBranch(page)
    await expect(page.getByRole('heading', { name: 'Delete branch' })).not.toBeVisible()

    // The worker did the work.
    await expectRepoBranch(repoDir, 'main')
    expect(branchExists(repoDir, 'doomed-branch')).toBe(false)

    // The regression this pins: the sidebar kept the DELETED branch's label
    // (plus a warn toast) until the user reloaded the page. The dialog closed
    // before it notified the shell. The callback then read the <Show> state
    // that the close disposed, so the branch stamp and the git-status
    // refresh never ran.
    await expect(branchGroupRow(page)).toContainText('main')
    await expect(branchGroupRow(page)).not.toContainText('doomed-branch')

    // The broken path caught the throw and warned instead. The absence of
    // that warning is the race-free half of this assertion, because the
    // dialog raises it before `onClose`, which the wait above already
    // covers.
    const toasts = await getRecordedToasts(page)
    expect(toasts.map(t => t.message)).not.toContain(
      'Branch deleted, but failed to update the sidebar label',
    )
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
