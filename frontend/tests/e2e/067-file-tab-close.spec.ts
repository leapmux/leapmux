import { execSync } from 'node:child_process'
import { existsSync, realpathSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { clearRecordedToasts, getRecordedToasts, installToastRecorder } from './helpers/toast'
import { loginViaToken, openWorkspace } from './helpers/ui'
import { createGitRepo, createWorkspaceWithWorktreeViaAPI, waitForAgentStartupViaAPI } from './helpers/worktree'

/**
 * How long to let a toast show up before concluding none is coming. The close
 * RPC has already resolved by then -- this covers only the render hop between
 * `showWarnToast` and the element landing in the DOM.
 */
const TOAST_SETTLE_MS = 1500

/**
 * Closing a file tab in an ordinary git checkout.
 *
 * This used to warn "working directory is not readable as a git repository;
 * closed without checking for uncommitted changes" on a perfectly readable
 * repo, with an agent tab still open on it. The worker could not resolve a
 * working directory for a FILE tab at all, so the close inspection failed
 * before it ever reached the sibling-tab count that ends it with no prompt.
 * File tabs now carry the working dir of the tab they were opened from.
 *
 * The repo is left dirty deliberately: uncommitted work is what the degraded
 * hint claims to be unable to check for, and what a last-tab close would
 * legitimately prompt about — so a silent close here means the check actually
 * ran and found a sibling, not that the prompt was skipped.
 */
test.describe('file tab close', () => {
  test('closes without a git warning when the repo has other tabs open', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'file-tab-close-repo')
    writeFileSync(join(repoDir, 'notes.md'), '# notes\n')
    execSync('git add notes.md', { cwd: repoDir })
    execSync('git commit -m "add notes"', { cwd: repoDir })
    // Uncommitted work on the branch, so a last-tab close would prompt.
    writeFileSync(join(repoDir, 'notes.md'), '# notes\nedited\n')

    await installToastRecorder(page)
    await loginViaToken(page, adminToken)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Tab Close')
    // The agent tab is the sibling that keeps the branch alive, and the tab
    // whose working dir the file tab inherits.
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir)

    try {
      await openWorkspace(page, workspaceId)

      // Open the file from the tree, the way a user does.
      await page.getByText('notes.md').click()
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toHaveCount(1)

      await clearRecordedToasts(page)
      await fileTab.locator('[data-testid="tab-close"]').click()

      // The tab goes and no dialog stands in the way: the worker resolved the
      // repo and found the agent still on the branch.
      await expect(fileTab).toHaveCount(0)
      await expect(page.getByRole('dialog')).toHaveCount(0)

      // Nothing was warned about. The wait is load-bearing: the toast is
      // rendered a beat after the close resolves, so asserting its absence the
      // instant the tab disappears passes whether or not one is coming.
      await page.waitForTimeout(TOAST_SETTLE_MS)
      const toasts = await getRecordedToasts(page)
      expect(toasts.map(t => `${t.variant}: ${t.message}`).join('\n')).toBe('')

      // The agent tab is untouched — closing a file viewer is not a close of
      // anything else.
      await expect(page.locator('[data-testid="tab"][data-tab-type="agent"]')).toHaveCount(1)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  /**
   * Closing a file tab that lives INSIDE a linked worktree.
   *
   * The worktree's ref-count is what decides whether a later close runs
   * `git worktree remove` -- an rm-rf of a directory that may still have an
   * editor mounted on it. File tabs join that ref-count now, and the FILE leg of
   * it is exercised nowhere else end to end: `071-worktree-lifecycle` drives
   * `inspectLastTabCloseViaAPI` with `TabType.AGENT` only.
   *
   * Closing the viewer must remove nothing: the agent that created the worktree
   * is still open on it.
   */
  test('closes without a git warning inside a worktree', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'file-tab-close-wt-repo')
    const worktreeDir = join(
      dirname(realpathSync(repoDir)),
      'file-tab-close-wt-repo-worktrees',
      'ftc-branch',
    )

    await installToastRecorder(page)
    await loginViaToken(page, adminToken)
    const workspaceId = await createWorkspaceWithWorktreeViaAPI(
      hubUrl,
      adminToken,
      workerId,
      'File Tab Close WT',
      repoDir,
      'ftc-branch',
    )
    await waitForAgentStartupViaAPI(hubUrl, adminToken, workerId, workspaceId)
    // Dirty, for the reason the ordinary-checkout test gives: uncommitted work
    // is what a degraded close claims it could not check for.
    writeFileSync(join(worktreeDir, 'README.md'), '# Test\nedited\n')

    try {
      await openWorkspace(page, workspaceId)

      // The tree is rooted at the agent's working dir, which IS the worktree.
      await page.getByText('README.md').click()
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      await expect(fileTab).toHaveCount(1)

      await clearRecordedToasts(page)
      await fileTab.locator('[data-testid="tab-close"]').click()

      await expect(fileTab).toHaveCount(0)
      await expect(page.getByRole('dialog')).toHaveCount(0)

      await page.waitForTimeout(TOAST_SETTLE_MS)
      const toasts = await getRecordedToasts(page)
      expect(toasts.map(t => `${t.variant}: ${t.message}`).join('\n')).toBe('')

      // The sibling agent still holds the worktree, so nothing was removed.
      expect(existsSync(worktreeDir)).toBe(true)
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })

  /**
   * A file tab as the LAST tab on a dirty branch must prompt.
   *
   * Both halves are the point. Closing the agent first must NOT prompt, because
   * the file tab is a live sibling on that branch -- which is the FILE leg of
   * the sibling scan, and the direction that used to be invisible because a file
   * tab had no working dir to be found by. Closing the file tab then must
   * prompt, because it is genuinely the last one and the branch has uncommitted
   * work -- where the old behavior was the degraded "not readable as a git
   * repository" close.
   */
  test('prompts when the file tab is the last tab on its branch', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, workerId, dataDir } = leapmuxServer
    const repoDir = createGitRepo(dataDir, 'file-tab-close-last-repo')
    writeFileSync(join(repoDir, 'notes.md'), '# notes\n')
    execSync('git add notes.md', { cwd: repoDir })
    execSync('git commit -m "add notes"', { cwd: repoDir })
    writeFileSync(join(repoDir, 'notes.md'), '# notes\nedited\n')

    await installToastRecorder(page)
    await loginViaToken(page, adminToken)
    const workspaceId = await createWorkspaceViaAPI(hubUrl, adminToken, 'File Tab Close Last')
    await openAgentViaAPI(hubUrl, adminToken, workerId, workspaceId, repoDir)

    try {
      await openWorkspace(page, workspaceId)

      await page.getByText('notes.md').click()
      const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
      const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]')
      await expect(fileTab).toHaveCount(1)

      // The file tab is a live sibling on this branch, so closing the agent is
      // not a last-tab close.
      await agentTab.locator('[data-testid="tab-close"]').click()
      await expect(agentTab).toHaveCount(0)
      await expect(page.getByRole('dialog')).toHaveCount(0)

      // Now it IS the last tab, on a branch with uncommitted work.
      await fileTab.locator('[data-testid="tab-close"]').click()
      await expect(page.getByRole('dialog')).toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, workspaceId).catch(() => {})
    }
  })
})
