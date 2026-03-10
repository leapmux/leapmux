import { execSync } from 'node:child_process'
import { mkdirSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { createWorkspaceViaAPI, deleteWorkspaceViaAPI, openAgentViaAPI } from './helpers/api'
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

/**
 * Create a git repo inside the server's data directory.
 */
function createGitRepo(dataDir: string, name: string): string {
  const repoDir = join(dataDir, name)
  mkdirSync(repoDir, { recursive: true })
  execSync('git init', { cwd: repoDir })
  execSync('git config user.email "test@test.com"', { cwd: repoDir })
  execSync('git config user.name "Test"', { cwd: repoDir })
  writeFileSync(join(repoDir, 'README.md'), '# Test\n')
  execSync('git add .', { cwd: repoDir })
  execSync('git commit -m "init"', { cwd: repoDir })
  return repoDir
}

test.describe('Diff Stat Isolation', () => {
  test('diff stats do not leak from one workspace to another', async ({ page, leapmuxServer }) => {
    const { hubUrl, adminToken, adminOrgId, workerId, dataDir } = leapmuxServer

    // Create two separate git repos with different content.
    const repoA = createGitRepo(dataDir, 'repo-a')
    const repoB = createGitRepo(dataDir, 'repo-b')

    // Make file changes only in repo-b so it has diff stats.
    // Modify the tracked README.md so git detects unstaged changes
    // (not just untracked files which may not have line counts).
    writeFileSync(join(repoB, 'README.md'), '# Test\n\nModified line 1\nModified line 2\nModified line 3\n')
    writeFileSync(join(repoB, 'new-file.txt'), 'hello\nworld\n')

    // Create two workspaces, each pointing to a different repo.
    const wsA = await createWorkspaceViaAPI(hubUrl, adminToken, 'Clean WS', adminOrgId)
    const wsB = await createWorkspaceViaAPI(hubUrl, adminToken, 'Dirty WS', adminOrgId)
    await openAgentViaAPI(hubUrl, adminToken, workerId, wsA, repoA)
    await openAgentViaAPI(hubUrl, adminToken, workerId, wsB, repoB)

    try {
      await loginViaToken(page, adminToken)

      // Navigate to workspace B first to load its diff stats.
      await page.goto(`/o/admin/workspace/${wsB}`)
      await waitForWorkspaceReady(page)

      // The DiffStatsBadge is rendered inside the workspace-item div.
      const wsBItem = page.locator(`[data-testid="workspace-item-${wsB}"]`)
      const wsAItem = page.locator(`[data-testid="workspace-item-${wsA}"]`)

      // Wait for workspace B's diff stats badge to appear on the workspace item.
      // The git status refresh is triggered when the active tab context is set.
      // First wait for the agent tab to be visible (confirms restore is done).
      await page.locator('[data-testid="tab"][data-tab-type="agent"]').first().waitFor({ timeout: 10_000 })

      // Wait for any git-diff-stats badge on the page (workspace item or tab tree).
      await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible({ timeout: 15_000 })

      // Now switch to workspace A.
      await wsAItem.click()
      await waitForWorkspaceReady(page)

      // After switching, workspace A should still have no diff stats.
      // Before the fix, workspace B's diff stats would leak into workspace A
      // because the reactive effect applied stale git data during the switch.
      await page.waitForTimeout(3000) // Allow time for any stale effect to fire
      await expect(wsAItem.locator('[data-testid="git-diff-stats"]')).not.toBeVisible()

      // Switch back to workspace B — diff stats should reappear.
      await wsBItem.click()
      await waitForWorkspaceReady(page)
      await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible({ timeout: 15_000 })

      // Switch to workspace A one more time — still no diff stats.
      await wsAItem.click()
      await waitForWorkspaceReady(page)
      await page.waitForTimeout(3000)
      await expect(wsAItem.locator('[data-testid="git-diff-stats"]')).not.toBeVisible()
    }
    finally {
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsA).catch(() => {})
      await deleteWorkspaceViaAPI(hubUrl, adminToken, wsB).catch(() => {})
    }
  })
})
