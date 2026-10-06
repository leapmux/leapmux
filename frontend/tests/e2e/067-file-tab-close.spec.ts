import { existsSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { TabType } from '../../src/generated/proto/leapmux/v1/workspace_pb'
import { expect, test } from './fixtures'
import { retryUntilPass } from './helpers/retryUntilPass'
import { clearRecordedToasts, getRecordedToasts } from './helpers/toast'
import { agentTabs, expectAgentTabCount, loginViaToken, openWorkspace, treeRow } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { commitFile, createGitRepo, createWorkspaceWithWorktreeViaAPI, inspectLastTabCloseViaAPI, waitForAgentStartupViaAPI } from './helpers/worktree'

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
    const repoDir = createGitRepo(leapmuxServer.dataDir, 'file-tab-close-repo')
    commitFile(repoDir, 'notes.md', '# notes\n', 'add notes')
    // Uncommitted work on the branch, so a last-tab close would prompt.
    writeFileSync(join(repoDir, 'notes.md'), '# notes\nedited\n')

    // The agent tab is the sibling that keeps the branch alive, and the tab
    // whose working dir the file tab inherits.
    await showWorkspaceWithAgents(page, leapmuxServer, 'File Tab Close', { workingDir: repoDir })

    // Open the file from the tree, the way a user does.
    await treeRow(page, 'notes.md').click()
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
    await expectAgentTabCount(page, 1)
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

    const { workspaceId, worktreeDir } = await createWorkspaceWithWorktreeViaAPI(
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

    await loginViaToken(page, adminToken)
    await openWorkspace(page, workspaceId)

    // The tree is rooted at the agent's working dir, which IS the worktree.
    await treeRow(page, 'README.md').click()
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
    commitFile(repoDir, 'notes.md', '# notes\n', 'add notes')
    writeFileSync(join(repoDir, 'notes.md'), '# notes\nedited\n')

    await showWorkspaceWithAgents(page, leapmuxServer, 'File Tab Close Last', { workingDir: repoDir })

    await treeRow(page, 'notes.md').click()
    const fileTab = page.locator('[data-testid="tab"][data-tab-type="file"]')
    const agentTab = agentTabs(page)
    await expect(fileTab).toHaveCount(1)

    // The file tab is a live sibling on this branch, so closing the agent is
    // not a last-tab close.
    await agentTab.locator('[data-testid="tab-close"]').click()
    await expectAgentTabCount(page, 0)
    await expect(page.getByRole('dialog')).toHaveCount(0)

    // The tab count above is OPTIMISTIC local state: the CRDT tombstone
    // applies speculatively, so the tab leaves the bar while CloseAgent is
    // still running its teardown on the worker. The next close asks the
    // WORKER whether a sibling tab still holds this branch, and an agent
    // row that is not closed YET answers yes -- which takes the no-prompt
    // fast path in inspectLastTabClose's hasOtherNonWorktreeTabOnBranch.
    // The hub's tab list cannot answer this, because the tombstone clears
    // it the instant it applies; only the worker knows when its own row
    // closed. Poll the worker's own verdict for this file tab, so the
    // assertion below tests the UI rather than racing the previous close.
    const fileTabId = await fileTab.getAttribute('data-tab-id')
    expect(fileTabId, 'the file tab must carry its id for the worker probe').toBeTruthy()
    await retryUntilPass(async () => {
      const inspection = await inspectLastTabCloseViaAPI(hubUrl, adminToken, workerId, TabType.FILE, fileTabId!)
      expect(inspection.shouldPrompt, 'the Worker treats the file tab as the last tab of the branch').toBe(true)
    })

    // Now it IS the last tab, on a branch with uncommitted work.
    await fileTab.locator('[data-testid="tab-close"]').click()
    await expect(page.getByRole('dialog')).toBeVisible()
  })
})
