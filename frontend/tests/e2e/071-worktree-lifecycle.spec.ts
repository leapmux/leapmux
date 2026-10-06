import { execFileSync } from 'node:child_process'
import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { WorktreeAction } from '../../src/generated/proto/leapmux/v1/common_pb'
import {
  OpenTerminalRequestSchema,
  OpenTerminalResponseSchema,
} from '../../src/generated/proto/leapmux/v1/terminal_pb'
import { TabType } from '../../src/generated/proto/leapmux/v1/workspace_pb'
import { expect, test } from './fixtures'
import { getTestChannel } from './helpers/api'
import { agentTabs, loginViaToken, openWorkspace, waitForWorkspaceReady } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'
import {
  addWorktree,
  branchExists,
  chooseGitMode,
  closeAgentViaAPI,
  closeTerminalViaAPI,
  commitFile,
  createGitRepo,
  createGitRepoWithRemote,
  createWorkspaceWithWorktreeViaAPI,
  fillWorkspaceTitle,
  inspectLastTabCloseViaAPI,
  managedWorktreePath,
  openNewWorkspaceDialogAt,
  pushBranchViaAPI,
  submitNewWorkspaceDialog,
  waitForPathDeleted,
  waitForPathExists,
  waitForSoleAgentViaAPI,
} from './helpers/worktree'

test.describe('Worktree Lifecycle', () => {
  test('create workspace with worktree via UI', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-create')

    await openNewWorkspaceDialogAt(page, adminToken, repoDir)
    await fillWorkspaceTitle(page, 'Worktree Test WS')

    // Wait for git options to load, then select "Create new worktree"
    await chooseGitMode(page, 'Create new worktree')

    const dialog = page.getByRole('dialog')
    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    await branchInput.clear()
    await branchInput.fill('e2e-test-branch')

    await expect(page.getByText('e2e-test-branch')).toBeVisible()

    // The dialog closes when the API call (including the git worktree
    // creation) has completed, and the new workspace is then the active one.
    await submitNewWorkspaceDialog(page, 'Worktree Test WS')
    await waitForWorkspaceReady(page)

    // Verify the worktree directory was created on disk. Polled, not read
    // once: the dialog closing means the RPC returned, and `git worktree add`
    // lands shortly after that. The path is where the Worker places a managed
    // worktree, with macOS symlinks (e.g. /var -> /private/var) resolved.
    await waitForPathExists(managedWorktreePath(repoDir, 'e2e-test-branch'))
  })

  test('clean worktree last tab prompts and can be scheduled for deletion', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-autoclean')

    // Create workspace with worktree via API. The helper returns once the
    // worktree exists on disk.
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Autoclean WS',
      repoDir,
      'autoclean-branch',
    )
    expect(worktreeDir).toContain('test-repo-autoclean-worktrees/autoclean-branch')

    // Get the initial agent that was auto-created with the workspace
    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)

    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)
    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.worktreePath).toContain('test-repo-autoclean-worktrees/autoclean-branch')
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id, WorktreeAction.REMOVE)

    await waitForPathDeleted(worktreeDir)
    await waitForPathDeleted(join(repoDir, '.git', 'refs', 'heads', 'autoclean-branch'))
    expect(branchExists(repoDir, 'autoclean-branch')).toBe(false)
  })

  test('worktree persists while other tabs still reference it', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-shared')

    // Create workspace with worktree
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Shared WS',
      repoDir,
      'shared-branch',
    )

    // Open a second terminal using the existing worktree (use-worktree mode, not create)
    const channel = await getTestChannel(hubUrl, adminToken)
    const termResp2 = await channel.callWorker(
      workerId,
      'OpenTerminal',
      OpenTerminalRequestSchema,
      OpenTerminalResponseSchema,
      { workerId, cols: 80, rows: 24, workingDir: repoDir, useWorktreePath: worktreeDir },
    )
    const terminalId = termResp2.terminalId

    // Close the terminal — agent still holds reference, worktree should persist
    await closeTerminalViaAPI(hubUrl, adminToken, workerId, terminalId)
    expect(existsSync(worktreeDir)).toBe(true)

    // Now close the agent (last tab)
    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)
    expect(inspect.shouldPrompt).toBe(true)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id, WorktreeAction.REMOVE)

    await waitForPathDeleted(worktreeDir)
    await waitForPathDeleted(join(repoDir, '.git', 'refs', 'heads', 'shared-branch'))
    expect(branchExists(repoDir, 'shared-branch')).toBe(false)
  })

  test('existing worktree (not created by us) can be scheduled for deletion', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-existing')

    // Create a worktree MANUALLY outside of LeapMux
    const manualWorktreeDir = addWorktree(repoDir, dataDir, 'manual-worktree', 'manual-branch')
    expect(existsSync(manualWorktreeDir)).toBe(true)

    // Create a workspace and open an agent directly in the manual worktree.
    const { workspaceId } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Existing WT WS', { workingDir: manualWorktreeDir })

    // Get the auto-created agent
    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)

    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)
    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.branchName).toBe('manual-branch')

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id, WorktreeAction.REMOVE)

    await waitForPathDeleted(manualWorktreeDir)
    expect(branchExists(repoDir, 'manual-branch')).toBe(false)
  })

  test('last non-worktree tab prompts only when branch has pending git state', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-branch-prompt')

    const { workspaceId } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Branch Prompt WS', { workingDir: repoDir })

    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)

    const cleanInspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)
    expect(cleanInspect.shouldPrompt).toBe(false)

    writeFileSync(join(repoDir, 'dirty-branch.txt'), 'dirty\n')
    const dirtyInspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)
    expect(dirtyInspect.shouldPrompt).toBe(true)
    expect(dirtyInspect.hasUncommittedChanges).toBe(true)

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id)
    expect(existsSync(join(repoDir, 'dirty-branch.txt'))).toBe(true)
  })

  test('dirty worktree with uncommitted changes triggers last-tab prompt', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dirty')

    // Create workspace with worktree
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Dirty WS',
      repoDir,
      'dirty-branch',
    )

    // Make the worktree dirty: add an uncommitted file
    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted change\n')

    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)

    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.worktreePath).toContain('test-repo-dirty-worktrees/dirty-branch')
    expect(inspect.hasUncommittedChanges).toBe(true)

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id)
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('worktree with local-only commits and no upstream triggers last-tab prompt', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    // Use a plain local repo (no remote configured) so branches have no upstream.
    const repoDir = createGitRepo(dataDir, 'test-repo-no-upstream')

    // Create workspace with worktree
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'No Upstream WS',
      repoDir,
      'no-upstream-branch',
    )

    // Make a local commit in the worktree (no upstream to push to). The
    // worktree reads the commit identity from its main repository's config.
    commitFile(worktreeDir, 'local-only.txt', 'would be lost\n', 'local only')

    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)

    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.canPush).toBe(false)
    expect(inspect.unpushedCommitCount).toBe(0)
    expect(existsSync(worktreeDir)).toBe(true)

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id)
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('dirty worktree with uncommitted changes can commit and push before close', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer

    // Create a bare "remote" and clone from it so there is an upstream. Both
    // repositories carry the pinned settings.
    const { repoDir, bareDir } = createGitRepoWithRemote(dataDir, 'test-repo-unpushed')

    // Create workspace with worktree
    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Unpushed WS',
      repoDir,
      'unpushed-branch',
    )

    // Make an uncommitted change in the worktree; the close action should
    // create a WIP commit and push it.
    writeFileSync(join(worktreeDir, 'extra.txt'), 'local only\n')
    execFileSync('git', ['push', '-u', 'origin', 'unpushed-branch'], { cwd: worktreeDir })

    const agent = await waitForSoleAgentViaAPI(leapmuxServer, workspaceId)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agent.id)
    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.hasUncommittedChanges).toBe(true)

    await pushBranchViaAPI(hubUrl, adminToken, workerId, agent.workingDir)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agent.id)

    // Read the result from the BARE REMOTE, not from the worktree. Closing the
    // last tab of a worktree workspace removes the worktree directory, so these
    // reads raced the removal -- the first `git log` succeeded and the next
    // command died with "fatal: Unable to read current working directory".
    // The remote is also the stronger assertion: it proves the WIP commit was
    // both made AND pushed, which is what this test is named for.
    const remoteMessage = execFileSync('git', ['log', '-1', '--pretty=%s', 'unpushed-branch'], { cwd: bareDir, encoding: 'utf8' }).trim()
    expect(remoteMessage).toBe('WIP')
  })

  test('dirty worktree confirmation dialog: cancel keeps tab open', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog-cancel')

    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Cancel Test WS',
      repoDir,
      'cancel-branch',
    )

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)
    const closeDialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Close Last Tab' }) })

    // Close the agent tab via UI
    const agentTab = agentTabs(page)
    await expect(agentTab).toBeVisible()
    const agentCloseBtn = agentTab.locator('[data-testid="tab-close"]')
    await agentCloseBtn.dispatchEvent('click')

    // Confirmation dialog should appear BEFORE the tab is closed
    await expect(closeDialog).toBeVisible()

    // Dialog should show the branch name
    await expect(closeDialog.getByText('cancel-branch', { exact: true })).toBeVisible()

    // Click "Delete worktree" once — should arm the button (show "Confirm?"),
    // not remove. The button names what it destroys, so a worktree prompt
    // spells it out rather than offering a bare "Delete".
    await closeDialog.getByRole('button', { name: 'Delete worktree' }).click()
    await expect(closeDialog.getByRole('button', { name: 'Confirm?' })).toBeVisible()

    // Dialog should still be open, tab should still be present
    await expect(closeDialog).toBeVisible()
    await expect(agentTab).toBeVisible()

    // Click "Cancel" — resets the armed button and closes dialog
    await closeDialog.getByRole('button', { name: 'Cancel' }).click()

    // Dialog should close
    await expect(closeDialog).not.toBeVisible()

    // Tab should still be present (not closed)
    await expect(agentTab).toBeVisible()

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('dirty worktree confirmation dialog: schedule deletion closes tab and deletes worktree', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog')

    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Dialog Test WS',
      repoDir,
      'dialog-branch',
    )

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const agentTab = agentTabs(page)
    await expect(agentTab).toBeVisible()
    await agentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Dialog appears BEFORE tab closes
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible()
    // The path renders ONCE now, in the status block's Directory row. The lead
    // sentence used to print it too, raw and unabbreviated, so this locator
    // needed a `.first()` to survive strict mode.
    await expect(
      page.getByRole('dialog').getByTestId('working-tree-directory'),
    ).toHaveText(/test-repo-dialog-worktrees\/dialog-branch$/)
    // ...and the dialog says WHICH KIND of checkout it is about, which is the
    // difference between closing a tab and destroying a directory.
    await expect(page.getByRole('dialog').getByText('Worktree branch', { exact: true })).toBeVisible()

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByTestId('working-tree-name')).toHaveText('dialog-branch')

    // Click the dangerous action once — should arm the button (show "Confirm?")
    await page.getByRole('button', { name: 'Delete worktree' }).click()
    await expect(page.getByRole('button', { name: 'Confirm?' })).toBeVisible()

    // Click "Confirm?" to actually remove
    await page.getByRole('button', { name: 'Confirm?' }).click()

    // Dialog closes and tab is removed
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).not.toBeVisible()
    await expect(agentTab).not.toBeVisible()

    // Worktree directory and branch are deleted in the background by the
    // worker once the last tab's REMOVE close ref-counts the worktree to
    // zero, so poll for completion.
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'dialog-branch')).toBe(false)
    }).toPass()
  })

  test('dirty worktree confirmation dialog: close anyway closes tab but preserves worktree', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog-keep')

    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Close Anyway Test WS',
      repoDir,
      'keep-branch',
    )

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    const agentTab = agentTabs(page)
    await expect(agentTab).toBeVisible()
    await agentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Dialog appears
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible()

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByText('keep-branch', { exact: true })).toBeVisible()

    // Click "Close anyway"
    await page.getByRole('button', { name: 'Close anyway' }).click()
    await expect(page.getByRole('button', { name: 'Confirm?' })).toBeVisible()
    await page.getByRole('button', { name: 'Confirm?' }).click()

    // Dialog closes and tab is removed
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).not.toBeVisible()
    await expect(agentTab).not.toBeVisible()

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)

    // Branch should also still exist
    expect(branchExists(repoDir, 'keep-branch')).toBe(true)
  })
})
