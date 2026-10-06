import { execFileSync } from 'node:child_process'
import { existsSync, realpathSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { WorktreeAction } from '../../src/generated/proto/leapmux/v1/common_pb'
import { TabType } from '../../src/generated/proto/leapmux/v1/workspace_pb'
import { test } from './fixtures'
import { createWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { chooseGitMode, fillWorkspaceTitle, loginViaToken, menuOptionTexts, openNewAgentDialog, openNewTerminalDialog, openNewWorkspaceDialogAt, openWorkspace, pickMenuOption, submitNewWorkspaceDialog, waitForActiveTabContext } from './helpers/ui'
import { closeAgentViaAPI, inspectLastTabCloseViaAPI, waitForAgentStartupViaAPI, waitForSoleAgentViaAPI } from './helpers/workerTabs'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'
import { addWorktree, branchExists, commitFile, createGitRepo, createWorkspaceWithWorktreeViaAPI, expectRepoBranch, managedWorktreePath, waitForPathDeleted, waitForPathExists } from './helpers/worktree'

/** The commit that HEAD of `dir` points at. */
function headCommit(dir: string): string {
  return execFileSync('git', ['rev-parse', 'HEAD'], { cwd: dir, encoding: 'utf8' }).trim()
}

test.describe('Worktree Git Modes', () => {
  // ─── Worktree-from-Worktree ──────────────────────────────────────

  test('create worktree from existing worktree starts from correct branch', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-wt-from-wt')

    // Create a worktree with an extra commit that diverges from main
    const firstWorktreeDir = addWorktree(repoDir, dataDir, 'test-repo-wt-from-wt-worktrees/source-branch', 'source-branch')
    expect(existsSync(firstWorktreeDir)).toBe(true)

    // Add an extra commit on the source-branch worktree. The worktree reads the
    // commit identity from its main repository's config.
    commitFile(firstWorktreeDir, 'extra.txt', 'diverged\n', 'diverge from main')

    // Get the HEAD of source-branch (should differ from main)
    const sourceBranchHead = headCommit(firstWorktreeDir)
    const mainHead = headCommit(repoDir)
    expect(sourceBranchHead).not.toBe(mainHead)

    // Create workspace from the worktree root (source-branch) with createWorktree enabled.
    // The Worker places the new worktree beside the MAIN repository, not beside
    // the source worktree, and the helper returns that directory once it exists.
    const { worktreeDir: derivedWorktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'WT from WT WS',
      firstWorktreeDir,
      'derived-branch',
    )
    expect(derivedWorktreeDir).toBe(join(realpathSync(dataDir), 'test-repo-wt-from-wt-worktrees', 'derived-branch'))

    // The new worktree's HEAD should match the source-branch's HEAD (not main's HEAD)
    const derivedHead = headCommit(derivedWorktreeDir)
    expect(derivedHead).toBe(sourceBranchHead)
    expect(derivedHead).not.toBe(mainHead)
  })

  // ─── Dialog Default Working Directory Resolution ──────────────────

  test('new agent dialog defaults to repo root when opened from worktree tab', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-agent-resolve')
    const realRepoDir = realpathSync(repoDir)

    // Create workspace with worktree so the initial agent tab is in the worktree
    const { workspaceId } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Agent Resolve WS',
      repoDir,
      'agent-resolve-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    // Both dialogs below read the tab context SYNCHRONOUSLY when they open, and
    // `createWorkerDialogContext` seeds its working-dir signal once from that
    // read. A tab whose directory has not hydrated yet gives an empty string,
    // and the input then stays empty for the life of the dialog -- the probe
    // that remaps a worktree root to its repo has no path to probe. The failure
    // reads as "the default resolved to the worktree" when nothing resolved at
    // all.
    await waitForActiveTabContext(page)

    // Open "New agent..." dialog via the tab menu
    await openNewAgentDialog(page)

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
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-terminal-resolve')
    const realRepoDir = realpathSync(repoDir)

    // Create workspace with worktree so the initial agent tab is in the worktree
    const { workspaceId } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Terminal Resolve WS',
      repoDir,
      'terminal-resolve-branch',
    )

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    // See the note in the agent-dialog test above: the dialog snapshots the tab
    // context when it opens.
    await waitForActiveTabContext(page)

    // Open "New terminal..." dialog via the tab menu
    await openNewTerminalDialog(page)

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
    execFileSync('git', ['branch', 'feature-switch'], { cwd: repoDir })

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)
    await fillWorkspaceTitle(page, 'Switch Branch WS')

    // Wait for git options and select "Switch to branch"
    await chooseGitMode(page, 'Switch to branch')

    // Branch dropdown should load with local branches
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
    await pickMenuOption(dialog, 'branch-select-menu', 'feature-switch')

    await submitNewWorkspaceDialog(page, 'Switch Branch WS')

    // Verify the repo is now on the feature-switch branch. Polled: the dialog
    // closing means the RPC returned, and the checkout runs after that.
    await expectRepoBranch(repoDir, 'feature-switch')
  })

  test('switch-to-branch mode via API: verifies checkout on disk', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-switch-api')

    // Create a second branch.
    execFileSync('git', ['branch', 'api-switch-target'], { cwd: repoDir })

    // Verify we start on main.
    await expectRepoBranch(repoDir, 'main')

    // Create workspace with checkout_branch.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Switch API WS')
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, repoDir, {
      checkoutBranch: 'api-switch-target',
    })

    await expectRepoBranch(repoDir, 'api-switch-target')
  })

  test('switch-to-branch with dirty workdir shows warning in UI', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-switch-dirty')

    execFileSync('git', ['branch', 'dirty-switch-target'], { cwd: repoDir })
    writeFileSync(join(repoDir, 'dirty.txt'), 'uncommitted\n')

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)

    await chooseGitMode(page, 'Switch to branch')

    // Warning about uncommitted changes should appear
    await expect(page.getByText('uncommitted changes')).toBeVisible()

    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  // ─── Git Mode: Use Existing Worktree ────────────────────────────────

  test('use-existing-worktree mode via API: switches working dir to worktree', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-api')

    // Create an existing worktree manually.
    const worktreeDir = addWorktree(repoDir, dataDir, 'test-repo-use-wt-api-existing', 'existing-wt-branch')
    expect(existsSync(worktreeDir)).toBe(true)

    // Create workspace using the use_worktree_path field.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Use WT API WS')
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, repoDir, {
      useWorktreePath: worktreeDir,
    })

    // Verify the agent's working dir is the worktree path.
    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    expect(agent.workingDir).toBe(worktreeDir)
  })

  test('use-existing-worktree on managed worktree: tracks tab correctly', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-managed')

    // Create workspace with a managed worktree (via create-worktree mode).
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Managed WT WS',
      repoDir,
      'managed-branch',
    )

    // Now open a second agent using "use existing worktree" pointing to the same managed worktree.
    const secondAgentId = await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, repoDir, {
      useWorktreePath: worktreeDir,
    })

    // Both agents must have FINISHED starting before the first is closed: the
    // second agent's worktree_tabs link is registered on its async startup
    // goroutine, and closing the first before that lands leaves the worktree
    // looking unreferenced -- so the last-tab inspect below reports no prompt.
    const agents = await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId, 2)
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
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-use-wt-unmanaged')

    // Create a worktree manually (not via LeapMux).
    const worktreeDir = addWorktree(repoDir, dataDir, 'test-repo-use-wt-unmanaged-ext', 'ext-branch')
    expect(existsSync(worktreeDir)).toBe(true)

    // Create workspace using "use existing worktree" pointing to the unmanaged worktree.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Unmanaged WT WS')
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, repoDir, {
      useWorktreePath: worktreeDir,
    })

    // Close the agent.
    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id)

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
    addWorktree(repoDir, dataDir, 'test-repo-use-wt-ui-wt', 'ui-wt-branch')

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)
    await fillWorkspaceTitle(page, 'Use WT UI WS')

    // Wait for git options and select "Use existing worktree"
    await chooseGitMode(page, 'Use existing worktree')

    // Worktree dropdown should load
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByTestId('worktree-select-menu-trigger')).toBeEnabled()

    // Select the worktree entry (label format: "branch — path")
    await dialog.getByTestId('worktree-select-menu-trigger').click()
    const wtMenu = dialog.getByTestId('worktree-select-menu')
    await wtMenu.getByRole('menuitemradio', { name: /ui-wt-branch/ }).first().click()

    await submitNewWorkspaceDialog(page, 'Use WT UI WS')
  })

  // ─── Git Mode: Use Current State ────────────────────────────────────

  test('use-current-state on managed worktree: registers tab so worktree is not prematurely deleted', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-current-managed')

    // Create a workspace with a managed worktree.
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Current Managed WS',
      repoDir,
      'current-branch',
    )

    // Open a second agent using "use current state" (no git mode fields) pointing directly
    // at the managed worktree path. The backend should detect it's a managed worktree and
    // register this tab.
    const secondAgentId = await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, worktreeDir)

    // Both agents must have FINISHED starting before the first is closed: the
    // second agent's worktree_tabs link is registered on its async startup
    // goroutine, so closing the first before that lands leaves the worktree
    // looking unreferenced and the last-tab inspect reports no prompt.
    const agents = await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId, 2)
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
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-current-unmanaged')

    // Create a worktree manually (not via LeapMux).
    const worktreeDir = addWorktree(repoDir, dataDir, 'test-repo-current-unmanaged-ext', 'ext-branch')
    expect(existsSync(worktreeDir)).toBe(true)

    // Create workspace using "use current state" (default) pointing at the manual worktree.
    const { workspaceId } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Current Unmanaged WS', { workingDir: worktreeDir })

    // Close the agent.
    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id)

    // No cleanup — unmanaged worktree should still exist.
    expect(existsSync(worktreeDir)).toBe(true)
    expect(branchExists(repoDir, 'ext-branch')).toBe(true)
  })

  // ─── Git Mode: Create Worktree with Base Branch ─────────────────────

  test('create-worktree with base branch: new worktree starts from specified base', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-base-branch')

    // Create a feature branch with an extra commit.
    execFileSync('git', ['checkout', '-b', 'feature-base'], { cwd: repoDir })
    commitFile(repoDir, 'feature.txt', 'feature content\n', 'feature commit')
    const featureHead = headCommit(repoDir)

    // Go back to main.
    execFileSync('git', ['checkout', 'main'], { cwd: repoDir })
    const mainHead = headCommit(repoDir)
    expect(featureHead).not.toBe(mainHead)

    // Create workspace with worktree based on feature-base branch.
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Base Branch WS')
    await openAgentViaAPI({ hubUrl, adminToken, workerId }, workspaceId, repoDir, {
      createWorktree: true,
      worktreeBranch: 'derived-from-feature',
      worktreeBaseBranch: 'feature-base',
    })
    // The worktree is materialized during async startup, after OpenAgent answers.
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)

    // Verify worktree was created.
    const worktreeDir = managedWorktreePath(repoDir, 'derived-from-feature')
    expect(existsSync(worktreeDir)).toBe(true)

    // Verify the new worktree's HEAD matches the feature branch HEAD (not main).
    const derivedHead = headCommit(worktreeDir)
    expect(derivedHead).toBe(featureHead)
    expect(derivedHead).not.toBe(mainHead)
  })

  test('create-worktree with base branch via UI: base branch selector works', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-base-branch-ui')

    // Create a feature branch.
    execFileSync('git', ['checkout', '-b', 'feature-ui-base'], { cwd: repoDir })
    commitFile(repoDir, 'feature.txt', 'feature content\n', 'feature commit')
    execFileSync('git', ['checkout', 'main'], { cwd: repoDir })

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)
    await fillWorkspaceTitle(page, 'Base Branch UI WS')

    // Wait for git options, select "Create new worktree"
    await chooseGitMode(page, 'Create new worktree')

    // Base Branch label and selector should be visible
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Base Branch')).toBeVisible()

    // The base branch selector should default to "main" (current)
    // and include "feature-ui-base"
    await expect(dialog.getByTestId('branch-select-menu-trigger')).toBeEnabled()
    const options = await menuOptionTexts(dialog, 'branch-select-menu')
    expect(options.some(o => o.includes('feature-ui-base'))).toBe(true)

    // Select feature-ui-base as base branch, and confirm it stuck before
    // submitting: a Create that races the selection silently branches from the
    // default base, which surfaces only as a missing file much later.
    await pickMenuOption(dialog, 'branch-select-menu', 'feature-ui-base')
    // The trigger reports the selection now; a menu has no `value`.
    await expect(dialog.getByTestId('branch-select-menu-trigger'))
      .toHaveAttribute('data-value', 'feature-ui-base')

    // Set a branch name and submit
    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    await branchInput.clear()
    await branchInput.fill('from-feature-base')

    await submitNewWorkspaceDialog(page, 'Base Branch UI WS')

    // Verify the worktree was created from the feature branch. Polled: the
    // dialog closing means the RPC returned, and the checkout populates the
    // tree just after that.
    const worktreeDir = managedWorktreePath(repoDir, 'from-feature-base')
    await waitForPathExists(worktreeDir)
    await waitForPathExists(join(worktreeDir, 'feature.txt'))
  })
})
