import type { Page } from '@playwright/test'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { frontendRoot } from '~/test-support/sourceTree'
import { expect, test } from './fixtures'
import { branchGroupRow, chooseGitMode, openNewAgentDialog, openNewTerminalDialog, openNewWorkspaceDialogAt, openWorkspace, setWorkingDir, waitForWorker, workspaceChildren } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { addWorktree, createGitRepo, createWorkspaceWithWorktreeViaAPI } from './helpers/worktree'

/**
 * Move the open dialog from a repository root to `dir`, and require that the git mode options hide.
 *
 * The dialog shows the options of the repository root first. Between two directories it keeps the options of the
 * last answer while the probe of the next directory runs (`useGitPathInfo` skips its loading state when the options
 * show), so the options hide only when the probe of `dir` answers. An assertion on a dialog that opened at `dir`
 * directly can pass before that probe answers, because the options do not show yet either way.
 */
async function expectGitOptionsHideOnlyAfterTheProbe(page: Page, dir: string): Promise<void> {
  await expect(page.getByText('Use current state'), 'the repository root shows its options first').toBeVisible()
  await setWorkingDir(page, dir)
  await expect(page.getByText('Use current state')).not.toBeVisible()
  await expect(page.getByText('Create new worktree', { exact: true })).not.toBeVisible()
}

test.describe('Worktree Detection', () => {
  // The dialogs below open from a workspace whose agent works in the frontend
  // directory. The dialog's own working directory is set per case.
  test.use({ agentWorkingDir: frontendRoot })

  test('non-git directory hides git options in new workspace dialog', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer

    // Create a plain directory that is NOT a git repo.
    const nonGitDir = join(dataDir, 'not-a-repo')
    mkdirSync(nonGitDir, { recursive: true })

    // Set working directory to a known non-git directory, after a repository root.
    await openNewWorkspaceDialogAt(page, adminToken, createGitRepo(dataDir, 'test-repo-before-plain'))
    await expectGitOptionsHideOnlyAfterTheProbe(page, nonGitDir)

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

    // Set working directory to a subdirectory of the git repo, after the repository root.
    // The options stay hidden even though the directory is inside a git repo.
    await openNewWorkspaceDialogAt(page, adminToken, repoDir)
    await expectGitOptionsHideOnlyAfterTheProbe(page, subDir)

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('five git mode radio options appear for git repo directory in new workspace dialog', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-ws')

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)

    // All five radio options should appear
    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Switch to branch', { exact: true })).toBeVisible()
    await expect(page.getByText('Create new branch', { exact: true })).toBeVisible()
    await expect(page.getByText('Create new worktree', { exact: true })).toBeVisible()
    await expect(page.getByText('Use existing worktree')).toBeVisible()

    // Default should be "Use current state" — branch name input should NOT be visible
    await expect(page.getByText('Branch Name')).not.toBeVisible()
    await expect(page.getByText('Worktree path:')).not.toBeVisible()

    // Select "Create new branch" — sub-controls should appear (branch name + base, no worktree path)
    await chooseGitMode(page, 'Create new branch')
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Branch Name')).toBeVisible()
    await expect(dialog.getByText('Base Branch')).toBeVisible()
    await expect(page.getByText('Worktree path:')).not.toBeVisible()

    // Select "Create new worktree" — sub-controls should appear
    await chooseGitMode(page, 'Create new worktree')
    await expect(dialog.getByText('Branch Name')).toBeVisible()
    await expect(page.getByText('Worktree path:')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('git mode radio options appear in new agent dialog for git repo', async ({
    page,
    authenticatedWorkspace,
    leapmuxServer,
  }) => {
    void authenticatedWorkspace
    const repoDir = createGitRepo(leapmuxServer.dataDir, 'test-repo-agent')

    await openNewAgentDialog(page)
    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Create new branch', { exact: true })).toBeVisible()
    await expect(page.getByText('Create new worktree', { exact: true })).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('git mode radio options appear in new terminal dialog for git repo', async ({
    page,
    authenticatedWorkspace,
    leapmuxServer,
  }) => {
    void authenticatedWorkspace
    const repoDir = createGitRepo(leapmuxServer.dataDir, 'test-repo-terminal')

    await openNewTerminalDialog(page)

    await waitForWorker(page)

    await setWorkingDir(page, repoDir)

    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Create new branch', { exact: true })).toBeVisible()
    await expect(page.getByText('Create new worktree', { exact: true })).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('git mode options appear for existing worktree root', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-wt-root')

    // Create a worktree manually
    const worktreeDir = addWorktree(repoDir, dataDir, 'test-repo-wt-root-wt', 'wt-root-branch')
    expect(existsSync(worktreeDir)).toBe(true)

    // Set working directory to the worktree root
    await openNewWorkspaceDialogAt(page, adminToken, worktreeDir)

    // Git mode radio options should appear for an existing worktree root
    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByText('Create new worktree', { exact: true })).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // The two kinds used to render the same glyph and the same tooltip, so the
  // sidebar could not say which rows delete as a directory. Against the real
  // worker, because `isWorktree` travels the whole way from `git rev-parse` to
  // the row -- a fixture proves only the last hop.
  test('sidebar tells a worktree row from a main-repo branch row', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-row-icons')

    const { workspaceId: branchWs } = await showWorkspaceWithAgents(page, leapmuxServer, 'Main Repo WS', { workingDir: repoDir })

    // Each branch row is read inside its own workspace's subtree, because both
    // workspaces stay expanded after the switch below.
    const branchRow = branchGroupRow(workspaceChildren(page, branchWs))
    await expect(branchRow.getByTestId('branch-icon')).toBeVisible()
    await expect(branchRow.getByTestId('worktree-icon')).toHaveCount(0)

    // The tooltip states the kind and the directory on every hover, because
    // neither fact is anywhere else on the row. Hover the LABEL, which is the
    // element the tooltip listens on -- its wrapper is `display: contents` and
    // therefore has no box to hover.
    await branchRow.getByText('main', { exact: true }).hover()
    const branchTip = page.getByRole('tooltip')
    await expect(branchTip).toContainText('Branch')
    await expect(branchTip.getByTestId('working-tree-directory')).toContainText('test-repo-row-icons')

    // Now the same repo through a linked worktree.
    const { workspaceId: worktreeWs } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Worktree WS',
      repoDir,
      'row-icon-branch',
    )
    await openWorkspace(page, worktreeWs)

    const worktreeRow = branchGroupRow(workspaceChildren(page, worktreeWs))
    await expect(worktreeRow.getByTestId('worktree-icon')).toBeVisible()
    await expect(worktreeRow.getByTestId('branch-icon')).toHaveCount(0)

    await worktreeRow.getByText('row-icon-branch', { exact: true }).hover()
    const worktreeTip = page.getByRole('tooltip')
    await expect(worktreeTip).toContainText('Worktree')
    await expect(worktreeTip.getByTestId('working-tree-directory'))
      .toContainText('test-repo-row-icons-worktrees/row-icon-branch')

    // The composer chip names the same checkout as the row above it, through
    // the same component. It cannot assert the TILDE here: an E2E repo lives
    // under the temp data dir, never under the worker's home, so the correct
    // answer is the absolute path on both surfaces. The tilde wiring is pinned
    // in `AgentEditorPanel.test.tsx` instead, where the home dir is injectable.
    const rowDirectory = await worktreeTip.getByTestId('working-tree-directory').textContent() ?? ''
    await page.getByTestId('composer-branch-trigger').hover()
    const chipTip = page.getByRole('tooltip')
    await expect(chipTip).toContainText('Worktree')
    await expect(chipTip.getByTestId('working-tree-directory')).toHaveText(rowDirectory)
  })

  test('dirty warning appears when source working copy has uncommitted changes', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dirty-warn')

    // Make the repo dirty
    writeFileSync(join(repoDir, 'dirty-file.txt'), 'uncommitted\n')

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await chooseGitMode(page, 'Create new worktree')

    // Warning about uncommitted changes should be visible
    await expect(page.getByText('uncommitted changes that will not be transferred')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })
})
