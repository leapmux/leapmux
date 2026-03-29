import { execSync } from 'node:child_process'
import { existsSync, mkdirSync, realpathSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  OpenTerminalRequestSchema,
  OpenTerminalResponseSchema,
} from '../../src/generated/leapmux/v1/terminal_pb'
import { TabType } from '../../src/generated/leapmux/v1/workspace_pb'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, getTestChannel, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'
import {
  branchExists,
  closeAgentViaAPI,
  closeTerminalViaAPI,
  createGitRepo,
  createWorkspaceWithWorktreeViaAPI,
  inspectLastTabCloseViaAPI,
  listAgentsViaAPI,
  openNewWorkspaceDialog,
  pushBranchForCloseViaAPI,
  scheduleWorktreeDeletionViaAPI,
  setWorkingDir,
  waitForOrgPageReady,
  waitForPathDeleted,
  waitForWorker,
  WORKSPACE_URL_RE,
} from './helpers/worktree'

test.describe('Worktree Lifecycle', () => {
  test('create workspace with worktree via UI', async ({
    page,
    leapmuxServer,
  }) => {
    const { adminToken, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-create')

    await loginViaToken(page, adminToken)
    await page.goto('/o/admin')
    await waitForOrgPageReady(page)

    await openNewWorkspaceDialog(page)
    await waitForWorker(page)

    await page.getByPlaceholder('New Workspace').fill('Worktree Test WS')

    const dialog = page.getByRole('dialog')
    await setWorkingDir(page, repoDir)

    // Wait for git options to load, then select "Create new worktree"
    await expect(page.getByText('Create new worktree')).toBeVisible({ timeout: 10000 })
    await page.getByText('Create new worktree').click()

    const branchInput = dialog.locator('input[type="text"][placeholder="feature-branch"]')
    await branchInput.clear()
    await branchInput.fill('e2e-test-branch')

    await expect(page.getByText('e2e-test-branch')).toBeVisible()

    await dialog.getByRole('button', { name: 'Create', exact: true }).click()

    // Wait for the dialog to close first — this signals the API call
    // (including the git worktree creation) has completed.
    await expect(page.getByRole('dialog')).not.toBeVisible({ timeout: 30000 })
    await expect(page).toHaveURL(WORKSPACE_URL_RE, { timeout: 30000 })
    await waitForWorkspaceReady(page)

    // Verify the worktree directory was created on disk.
    // Use realpathSync to resolve macOS symlinks (e.g. /var -> /private/var).
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-create-worktrees', 'e2e-test-branch')
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('clean worktree last tab prompts and can be scheduled for deletion', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-autoclean')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-autoclean-worktrees', 'autoclean-branch')

    // Create workspace with worktree via API
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Autoclean WS',
      adminOrgId,
      repoDir,
      'autoclean-branch',
    )

    // Worktree should now exist on disk
    expect(existsSync(worktreeDir)).toBe(true)

    // Get the initial agent that was auto-created with the workspace
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)

    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.worktreePath).toContain('test-repo-autoclean-worktrees/autoclean-branch')
    await scheduleWorktreeDeletionViaAPI(hubUrl, adminToken, workerId, inspect.worktreeId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    await waitForPathDeleted(worktreeDir)
    await waitForPathDeleted(join(repoDir, '.git', 'refs', 'heads', 'autoclean-branch'))
    expect(branchExists(repoDir, 'autoclean-branch')).toBe(false)
  })

  test('worktree persists while other tabs still reference it', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-shared')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-shared-worktrees', 'shared-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Shared WS',
      adminOrgId,
      repoDir,
      'shared-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Open a second terminal using the existing worktree (use-worktree mode, not create)
    const channel = await getTestChannel(hubUrl, adminToken)
    const termResp2 = await channel.callWorker(
      workerId,
      'OpenTerminal',
      OpenTerminalRequestSchema,
      OpenTerminalResponseSchema,
      { workspaceId, workerId, orgId: adminOrgId, cols: 80, rows: 24, workingDir: repoDir, useWorktreePath: worktreeDir },
    )
    const terminalId = termResp2.terminalId

    // Close the terminal — agent still holds reference, worktree should persist
    await closeTerminalViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId, terminalId)
    expect(existsSync(worktreeDir)).toBe(true)

    // Now close the agent (last tab)
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    expect(inspect.shouldPrompt).toBe(true)
    await scheduleWorktreeDeletionViaAPI(hubUrl, adminToken, workerId, inspect.worktreeId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    await waitForPathDeleted(worktreeDir)
    await waitForPathDeleted(join(repoDir, '.git', 'refs', 'heads', 'shared-branch'))
    expect(branchExists(repoDir, 'shared-branch')).toBe(false)
  })

  test('existing worktree (not created by us) can be scheduled for deletion', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-existing')
    const realDataDir = realpathSync(dataDir)

    // Create a worktree MANUALLY outside of LeapMux
    const manualWorktreeDir = join(realDataDir, 'manual-worktree')
    execSync(`git worktree add ${join(dataDir, 'manual-worktree')} -b manual-branch`, { cwd: repoDir })
    expect(existsSync(manualWorktreeDir)).toBe(true)

    // Create a workspace and open an agent directly in the manual worktree.
    const workspaceId = await createWorkspaceViaAPI(
      hubUrl,
      adminToken,
      'Existing WT WS',
      adminOrgId,
    )
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, manualWorktreeDir)

    // Get the auto-created agent
    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)

    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.branchName).toBe('manual-branch')

    await scheduleWorktreeDeletionViaAPI(hubUrl, adminToken, workerId, inspect.worktreeId)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    await waitForPathDeleted(manualWorktreeDir)
    expect(branchExists(repoDir, 'manual-branch')).toBe(false)
  })

  test('last non-worktree tab prompts only when branch has pending git state', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-branch-prompt')

    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'Branch Prompt WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir)

    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)

    const cleanInspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    expect(cleanInspect.shouldPrompt).toBe(false)

    writeFileSync(join(repoDir, 'dirty-branch.txt'), 'dirty\n')
    const dirtyInspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    expect(dirtyInspect.shouldPrompt).toBe(true)
    expect(dirtyInspect.hasUncommittedChanges).toBe(true)

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)
    expect(existsSync(join(repoDir, 'dirty-branch.txt'))).toBe(true)
  })

  test('dirty worktree with uncommitted changes triggers last-tab prompt', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dirty')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dirty-worktrees', 'dirty-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Dirty WS',
      adminOrgId,
      repoDir,
      'dirty-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Make the worktree dirty: add an uncommitted file
    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted change\n')

    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)

    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.worktreePath).toContain('test-repo-dirty-worktrees/dirty-branch')
    expect(inspect.hasUncommittedChanges).toBe(true)

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('worktree with local-only commits and no upstream triggers last-tab prompt', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    // Use a plain local repo (no remote configured) so branches have no upstream.
    const repoDir = createGitRepo(dataDir, 'test-repo-no-upstream')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-no-upstream-worktrees', 'no-upstream-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'No Upstream WS',
      adminOrgId,
      repoDir,
      'no-upstream-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Make a local commit in the worktree (no upstream to push to)
    execSync('git config user.email "test@test.com"', { cwd: worktreeDir })
    execSync('git config user.name "Test"', { cwd: worktreeDir })
    writeFileSync(join(worktreeDir, 'local-only.txt'), 'would be lost\n')
    execSync('git add .', { cwd: worktreeDir })
    execSync('git commit -m "local only"', { cwd: worktreeDir })

    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)

    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.canPush).toBe(false)
    expect(inspect.unpushedCommitCount).toBe(0)
    expect(existsSync(worktreeDir)).toBe(true)

    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)
    expect(existsSync(worktreeDir)).toBe(true)
  })

  test('dirty worktree with uncommitted changes can commit and push before close', async ({
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const realDataDir = realpathSync(dataDir)

    // Create a bare "remote" and clone from it so there is an upstream
    const bareDir = join(dataDir, 'test-repo-unpushed-bare')
    mkdirSync(bareDir, { recursive: true })
    execSync('git init --bare', { cwd: bareDir })

    const repoDir = join(dataDir, 'test-repo-unpushed')
    execSync(`git clone ${bareDir} ${repoDir}`)
    execSync('git config user.email "test@test.com"', { cwd: repoDir })
    execSync('git config user.name "Test"', { cwd: repoDir })
    writeFileSync(join(repoDir, 'README.md'), '# Test\n')
    execSync('git add .', { cwd: repoDir })
    execSync('git commit -m "init"', { cwd: repoDir })
    execSync('git push -u origin HEAD', { cwd: repoDir })

    const worktreeDir = join(realDataDir, 'test-repo-unpushed-worktrees', 'unpushed-branch')

    // Create workspace with worktree
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Unpushed WS',
      adminOrgId,
      repoDir,
      'unpushed-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    // Make an uncommitted change in the worktree; the close action should
    // create a WIP commit and push it.
    execSync('git config user.email "test@test.com"', { cwd: worktreeDir })
    execSync('git config user.name "Test"', { cwd: worktreeDir })
    writeFileSync(join(worktreeDir, 'extra.txt'), 'local only\n')
    execSync('git push -u origin unpushed-branch', { cwd: worktreeDir })

    const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, workspaceId, adminOrgId)
    expect(agents.length).toBeGreaterThan(0)
    const inspect = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    expect(inspect.shouldPrompt).toBe(true)
    expect(inspect.hasUncommittedChanges).toBe(true)

    await pushBranchForCloseViaAPI(hubUrl, adminToken, workerId, TabType.AGENT, agents[0].id)
    await closeAgentViaAPI(hubUrl, adminToken, workerId, agents[0].id)

    const lastMessage = execSync('git log -1 --pretty=%s', { cwd: worktreeDir }).toString().trim()
    expect(lastMessage).toBe('WIP')
    const localHead = execSync('git rev-parse HEAD', { cwd: worktreeDir }).toString().trim()
    const remoteHead = execSync('git rev-parse refs/remotes/origin/unpushed-branch', { cwd: worktreeDir }).toString().trim()
    expect(localHead).toBe(remoteHead)
  })

  test('dirty worktree confirmation dialog: cancel keeps tab open', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog-cancel')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dialog-cancel-worktrees', 'cancel-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Cancel Test WS',
      adminOrgId,
      repoDir,
      'cancel-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)
    const closeDialog = page.getByRole('dialog').filter({ has: page.getByRole('heading', { name: 'Close Last Tab' }) })

    // Close the agent tab via UI
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTab).toBeVisible()
    const agentCloseBtn = agentTab.locator('[data-testid="tab-close"]')
    await agentCloseBtn.dispatchEvent('click')

    // Confirmation dialog should appear BEFORE the tab is closed
    await expect(closeDialog).toBeVisible({ timeout: 10000 })

    // Dialog should show the branch name
    await expect(closeDialog.getByText('cancel-branch', { exact: true })).toBeVisible()

    // Click "Delete" once — should arm the button (show "Confirm?"), not remove
    await closeDialog.getByRole('button', { name: 'Delete' }).click()
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
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dialog-worktrees', 'dialog-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Dialog Test WS',
      adminOrgId,
      repoDir,
      'dialog-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTab).toBeVisible()
    await agentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Dialog appears BEFORE tab closes
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible({ timeout: 10000 })
    await expect(page.getByRole('dialog').getByText('test-repo-dialog-worktrees/dialog-branch')).toBeVisible()

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByText('dialog-branch', { exact: true })).toBeVisible()

    // Click the dangerous action once — should arm the button (show "Confirm?")
    await page.getByRole('button', { name: 'Delete' }).click()
    await expect(page.getByRole('button', { name: 'Confirm?' })).toBeVisible()

    // Click "Confirm?" to actually remove
    await page.getByRole('button', { name: 'Confirm?' }).click()

    // Dialog closes and tab is removed
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).not.toBeVisible()
    await expect(agentTab).not.toBeVisible({ timeout: 5000 })

    // Worktree directory and branch are deleted in the background by
    // the worker after ForceRemoveWorktree returns, so poll for completion.
    await expect(async () => {
      expect(existsSync(worktreeDir)).toBe(false)
      expect(branchExists(repoDir, 'dialog-branch')).toBe(false)
    }).toPass({ timeout: 10_000 })
  })

  test('dirty worktree confirmation dialog: close anyway closes tab but preserves worktree', async ({
    page,
    leapmuxServer,
  }) => {
    const { hubUrl, adminToken, workerId, adminOrgId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'test-repo-dialog-keep')
    const realDataDir = realpathSync(dataDir)
    const worktreeDir = join(realDataDir, 'test-repo-dialog-keep-worktrees', 'keep-branch')

    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'Close Anyway Test WS',
      adminOrgId,
      repoDir,
      'keep-branch',
    )
    expect(existsSync(worktreeDir)).toBe(true)

    writeFileSync(join(worktreeDir, 'dirty.txt'), 'uncommitted\n')

    await loginViaToken(page, adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
    await expect(agentTab).toBeVisible()
    await agentTab.locator('[data-testid="tab-close"]').dispatchEvent('click')

    // Dialog appears
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).toBeVisible({ timeout: 10000 })

    // Dialog should show the branch name
    await expect(page.getByRole('dialog').getByText('keep-branch', { exact: true })).toBeVisible()

    // Click "Close anyway"
    await page.getByRole('button', { name: 'Close anyway' }).click()
    await expect(page.getByRole('button', { name: 'Confirm?' })).toBeVisible()
    await page.getByRole('button', { name: 'Confirm?' }).click()

    // Dialog closes and tab is removed
    await expect(page.getByRole('heading', { name: 'Close Last Tab' })).not.toBeVisible()
    await expect(agentTab).not.toBeVisible({ timeout: 5000 })

    // Worktree should still exist
    expect(existsSync(worktreeDir)).toBe(true)

    // Branch should also still exist
    expect(branchExists(repoDir, 'keep-branch')).toBe(true)
  })
})
