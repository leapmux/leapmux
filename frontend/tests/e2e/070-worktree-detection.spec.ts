import type { Page } from '@playwright/test'
import type { AgentServer } from './helpers/api'
import { execFileSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, realpathSync, utimesSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import process from 'node:process'
import { expect } from '@playwright/test'
import { frontendRoot } from '~/test-support/sourceTree'
import { TabInsertion } from '../../src/generated/proto/leapmux/v1/tab_creation_pb'
import { TerminalStatus, WorkerTerminalService } from '../../src/generated/proto/leapmux/v1/terminal_pb'
import { test } from './fixtures'
import { getTestChannel } from './helpers/api'
import { retryUntilPass } from './helpers/retryUntilPass'
import { tabIdSelector } from './helpers/tabSelectors'
import { branchGroupRow, chooseGitMode, openNewAgentDialog, openNewTerminalDialog, openNewWorkspaceDialogAt, openWorkspace, setWorkingDir, waitForActiveTabContext, waitForWorker, workspaceChildren } from './helpers/ui'
import { listTerminalsViaAPI, waitForAgentStartupViaAPI } from './helpers/workerTabs'
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

  test('runs none of a repository\'s filters while browsing to it', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace
    const repo = createGitRepo(leapmuxServer.dataDir, 'browse-filter')
    const file = join(repo, 'a.txt')
    const marker = join(repo, '.git', 'filter-ran')
    writeFileSync(file, 'content\n')
    writeFileSync(join(repo, '.gitattributes'), '* filter=evil\n')
    execFileSync('git', ['add', 'a.txt', '.gitattributes'], { cwd: repo })
    execFileSync('git', ['commit', '-m', 'add attributes'], { cwd: repo })
    writeFileSync(join(repo, '.git', 'leapmux-filter'), '#!/bin/sh\nprintf ran > .git/filter-ran\ncat\n')
    execFileSync('git', ['config', 'filter.evil.clean', 'sh .git/leapmux-filter'], { cwd: repo })
    utimesSync(file, new Date(1000), new Date(1000))

    await openNewAgentDialog(page)
    await waitForWorker(page)
    await setWorkingDir(page, repo)
    await expect(page.getByText('Use current state')).toBeVisible()
    await expect(page.getByRole('radio', { name: 'Use current state' })).toBeChecked()
    expect(existsSync(marker)).toBe(false)
    await page.getByRole('button', { name: 'Cancel' }).click()
  })

  test('shows dirty warnings for branch actions and preserves the working tree on cancel', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace
    const repo = createGitRepo(leapmuxServer.dataDir, 'mode-dirty-warnings')
    writeFileSync(join(repo, 'dirty.txt'), 'original\n')
    execFileSync('git', ['add', 'dirty.txt'], { cwd: repo })
    execFileSync('git', ['commit', '-m', 'add tracked file'], { cwd: repo })
    writeFileSync(join(repo, 'dirty.txt'), 'changed\n')

    await openNewAgentDialog(page)
    await waitForWorker(page)
    await setWorkingDir(page, repo)
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('radio', { name: 'Use current state' })).toBeChecked()
    await expect(dialog.getByText(/uncommitted changes/i)).not.toBeVisible()

    await chooseGitMode(page, 'Switch to branch')
    await expect(dialog.getByText(/Switching branches may fail or discard changes/)).toBeVisible()
    await chooseGitMode(page, 'Create new branch')
    await expect(dialog.getByText(/Creating a new branch will include them/)).toBeVisible()
    await chooseGitMode(page, 'Create new worktree')
    await expect(dialog.getByText(/will not be transferred to the new worktree/)).toBeVisible()
    await chooseGitMode(page, 'Use current state')
    await expect(dialog.getByText(/uncommitted changes/i)).not.toBeVisible()
    await chooseGitMode(page, 'Switch to branch')
    await expect(dialog.getByText(/Switching branches may fail or discard changes/)).toBeVisible()

    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).not.toBeVisible()
    expect(execFileSync('git', ['diff', '--', 'dirty.txt'], { cwd: repo, encoding: 'utf8' })).toContain('+changed')
  })

  test('shows unavailable status and retries without blocking creation', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace
    const repo = createGitRepo(leapmuxServer.dataDir, 'unavailable-status')
    const indexPath = join(repo, '.git', 'index')
    writeFileSync(join(repo, 'tracked.txt'), 'original\n')
    execFileSync('git', ['add', 'tracked.txt'], { cwd: repo })
    execFileSync('git', ['commit', '-m', 'add tracked file'], { cwd: repo })
    const index = readFileSync(indexPath)
    writeFileSync(join(repo, 'tracked.txt'), 'changed\n')
    await openNewAgentDialog(page)
    await waitForWorker(page)
    await setWorkingDir(page, repo)
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('radio', { name: 'Use current state' })).toBeChecked()
    writeFileSync(indexPath, 'invalid index')
    await chooseGitMode(page, 'Create new branch')
    await expect(dialog.getByText('Working-tree status is unavailable.')).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Create', exact: true })).toBeEnabled()
    await expect(dialog.getByText(/Creating a new branch will include them/)).not.toBeVisible()
    writeFileSync(indexPath, index)
    await dialog.getByRole('button', { name: 'Retry', exact: true }).click()
    await expect(dialog.getByText('Working-tree status is unavailable.')).not.toBeVisible()
    await expect(dialog.getByText(/Creating a new branch will include them/)).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Create', exact: true })).toBeEnabled()
    await dialog.getByRole('button', { name: 'Cancel' }).click()
    await expect(dialog).not.toBeVisible()
  })

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

/** Wait for the Worker's startup result, then assert success outside the transient wait. */
async function waitForGitTestTerminalStartup(server: AgentServer, workspaceId: string, title: string) {
  const channel = await getTestChannel(server.hubUrl, server.adminToken)
  const terminal = await retryUntilPass(async () => {
    const terminals = await listTerminalsViaAPI(server.hubUrl, server.adminToken, server.workerId, workspaceId)
    const response = await channel.callWorker(server.workerId, WorkerTerminalService.method.listTerminals, {
      tabIds: terminals.map(terminal => terminal.id),
    })
    const matches = response.terminals.filter(entry => entry.title === title)
    expect(matches, `the Worker publishes exactly one terminal titled ${title}`).toHaveLength(1)
    const current = matches[0]
    if (!current)
      throw new Error(`The Worker has not published terminal ${title}.`)
    expect(current.status, `terminal ${title} is still starting`).not.toBe(TerminalStatus.STARTING)
    return current
  })
  // A failed startup is a completed result. Assert it here so it fails at once.
  expect(terminal.startupError, `Worker startup error for ${title}`).toBe('')
  expect(terminal.status, `Worker startup status for ${title}`).toBe(TerminalStatus.READY)
  return terminal
}

test.describe('git checkout metadata', () => {
  for (const kind of ['agent', 'terminal'] as const) {
    test(`retains a bare-backed checkout for the initial ${kind} dialog and submission`, async ({ page, authenticatedEmptyWorkspace, leapmuxServer }) => {
      const source = createGitRepo(leapmuxServer.dataDir, `approved-bare-${kind}-source`)
      const bare = join(leapmuxServer.dataDir, `approved-${kind}-project.git`)
      execFileSync('git', [
        '-c',
        'core.fsmonitor=false',
        'clone',
        '--bare',
        source,
        bare,
      ])
      for (const [key, value] of [
        ['core.fsmonitor', 'false'],
        ['gc.auto', '0'],
        ['maintenance.auto', 'false'],
      ] as const) {
        execFileSync('git', [
          `--git-dir=${bare}`,
          'config',
          key,
          value,
        ])
      }
      const requestedCheckout = join(leapmuxServer.dataDir, `approved-bare-${kind}-checkout`)
      execFileSync('git', [
        `--git-dir=${bare}`,
        'worktree',
        'add',
        requestedCheckout,
        'main',
      ])
      const checkout = realpathSync(requestedCheckout)
      const workspaceId = authenticatedEmptyWorkspace.workspaceId
      const channel = await getTestChannel(leapmuxServer.hubUrl, leapmuxServer.adminToken)
      const contextId = randomUUID()
      const contextTitle = `approved-${kind}-context`
      // Use the live Worker creation API. This terminal supplies the active
      // tab's directory before the new dialog's hook is constructed.
      const opened = await channel.callWorker(leapmuxServer.workerId, WorkerTerminalService.method.openTerminal, {
        target: {
          case: 'creation',
          value: {
            tabId: contextId,
            placement: {
              workspaceId,
              destination: { case: 'firstLeaf', value: {} },
              insertion: TabInsertion.LAST,
            },
          },
        },
        cols: 80,
        rows: 24,
        workingDir: checkout,
        title: contextTitle,
      })
      expect(opened.terminalId).toBe(contextId)
      const context = await waitForGitTestTerminalStartup(leapmuxServer, workspaceId, contextTitle)
      expect(context.terminalId).toBe(contextId)
      expect(context.workingDir).toBe(checkout)
      await openWorkspace(page, workspaceId)
      const active = page.locator(`[data-testid="tab"][data-tab-type="terminal"]${tabIdSelector(contextId)}:visible`).first()
      await active.click()
      await expect(active).toHaveAttribute('aria-selected', 'true')
      await waitForActiveTabContext(page)
      await expect(page.locator('[data-working-dir]:visible').first()).toHaveAttribute('data-working-dir', checkout)

      if (kind === 'agent')
        await openNewAgentDialog(page)
      else
        await openNewTerminalDialog(page)
      await waitForWorker(page)
      const dialog = page.getByRole('dialog')
      // No directory edit occurs after opening. These controls can appear
      // only after the first probe publishes the checkout's metadata.
      await expect(dialog.getByText('Use current state', { exact: true })).toBeVisible()
      await expect(dialog.getByPlaceholder('Enter path...')).toHaveValue(checkout)
      const title = `approved-bare-${kind}`
      await dialog.getByRole('textbox', { name: 'Title', exact: true }).fill(title)
      await dialog.getByRole('button', { name: 'Create', exact: true }).click()
      await expect(dialog).not.toBeVisible()
      if (kind === 'agent') {
        const agents = await waitForAgentStartupViaAPI(leapmuxServer.hubUrl, leapmuxServer.adminToken, leapmuxServer.workerId, workspaceId)
        const created = agents.find(agent => agent.title === title)
        expect(created?.startupError).toBe('')
        expect(created?.workingDir).toBe(checkout)
      }
      else {
        const created = await waitForGitTestTerminalStartup(leapmuxServer, workspaceId, title)
        expect(created.workingDir).toBe(checkout)
      }
    })
  }

  test('attaches a same-label branch and creates from detached HEAD by default', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    void authenticatedWorkspace
    const repo = createGitRepo(leapmuxServer.dataDir, 'approved-detached-origin')
    const a = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim()
    const label = a.slice(0, 7)
    execFileSync('git', ['commit', '--allow-empty', '-m', 'B'], { cwd: repo })
    const b = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim()
    expect(b).not.toBe(a)
    execFileSync('git', ['branch', label], { cwd: repo })
    execFileSync('git', ['checkout', '--detach', a], { cwd: repo })
    await openNewTerminalDialog(page)
    await waitForWorker(page)
    await setWorkingDir(page, repo)
    let dialog = page.getByRole('dialog')
    await expect(dialog.getByText(`Detached HEAD: ${label}`)).toBeVisible()
    await chooseGitMode(page, 'Switch to branch')
    await dialog.getByRole('button', { name: 'Branch', exact: true }).click()
    await expect(page.getByRole('menuitemradio', { name: `${label} (current)`, exact: true })).toHaveCount(0)
    await page.getByRole('menuitemradio', { name: label, exact: true }).click()
    await expect(dialog.getByText('Working directory is already on this branch.')).toHaveCount(0)
    const attachedTitle = 'approved-attach-collision-terminal'
    await dialog.getByRole('textbox', { name: 'Title', exact: true }).fill(attachedTitle)
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    const attached = await waitForGitTestTerminalStartup(leapmuxServer, authenticatedWorkspace.workspaceId, attachedTitle)
    expect(attached.workingDir).toBe(repo)
    expect(execFileSync('git', ['symbolic-ref', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim()).toBe(`refs/heads/${label}`)
    expect(execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repo, encoding: 'utf8' }).trim()).toBe(b)

    // READY means phase 0 and its saved-state validation finished. Mutating
    // HEAD earlier could make this test invalidate the first tab's startup.
    execFileSync('git', ['checkout', '--detach', a], { cwd: repo })
    await openNewTerminalDialog(page)
    await waitForWorker(page)
    await setWorkingDir(page, repo)
    dialog = page.getByRole('dialog')
    await expect(dialog.getByText(`Detached HEAD: ${label}`)).toBeVisible()
    await chooseGitMode(page, 'Create new branch')
    await dialog.getByPlaceholder('feature-branch').fill('approved-from-head')
    await expect(dialog.getByRole('button', { name: 'Branch', exact: true })).toHaveText('Select a branch...')
    const createdTitle = 'approved-create-from-head-terminal'
    await dialog.getByRole('textbox', { name: 'Title', exact: true }).fill(createdTitle)
    await dialog.getByRole('button', { name: 'Create', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    const created = await waitForGitTestTerminalStartup(leapmuxServer, authenticatedWorkspace.workspaceId, createdTitle)
    expect(created.workingDir).toBe(repo)
    expect(execFileSync('git', ['rev-parse', 'refs/heads/approved-from-head'], { cwd: repo, encoding: 'utf8' }).trim()).toBe(a)
  })

  test('shows correct worktree options with a newline-containing administrative path', async ({ page, leapmuxServer }) => {
    test.skip(process.platform === 'win32', 'Windows does not permit the newline filename used by this fixture')
    const repo = createGitRepo(leapmuxServer.dataDir, 'approved-main\nrepository')
    const checkout = join(leapmuxServer.dataDir, 'approved-plain-checkout')
    execFileSync('git', [
      'worktree',
      'add',
      '-b',
      'approved-linked',
      checkout,
    ], { cwd: repo })
    // Finish an ordinary repository probe before browsing to the linked path.
    // This case tests raw metadata, independently of initial-path remapping.
    const control = createGitRepo(leapmuxServer.dataDir, 'metadata-probe-control')
    await openNewWorkspaceDialogAt(page, leapmuxServer.adminToken, control)
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Use current state', { exact: true })).toBeVisible()
    await setWorkingDir(page, checkout)
    await expect(dialog.getByText('Currently on branch: approved-linked')).toBeVisible()
    await expect(dialog.getByPlaceholder('Enter path...')).toHaveValue(checkout)
    await dialog.getByRole('button', { name: 'Cancel', exact: true }).click()
  })
})
