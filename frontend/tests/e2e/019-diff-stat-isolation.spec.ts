import { writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect, test } from './fixtures'
import { agentTabs, loginViaToken, openWorkspace, waitForWorkspaceReady, workspaceRow, workspaceRowTitle } from './helpers/ui'
import { createWorkspaceWithAgentsViaAPI } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'

test.describe('Diff Stat Isolation', () => {
  test('diff stats do not leak from one workspace to another', async ({ page, leapmuxServer }) => {
    const { adminToken, dataDir } = leapmuxServer

    // Create two separate git repos with different content. The shared helper
    // pins the settings that keep a second writer out of `.git`.
    const repoA = createGitRepo(dataDir, 'repo-a')
    const repoB = createGitRepo(dataDir, 'repo-b')

    // Make file changes only in repo-b so it has diff stats.
    // Modify the tracked README.md so git detects unstaged changes
    // (not just untracked files which may not have line counts).
    writeFileSync(join(repoB, 'README.md'), '# Test\n\nModified line 1\nModified line 2\nModified line 3\n')
    writeFileSync(join(repoB, 'new-file.txt'), 'hello\nworld\n')

    // Create two workspaces, each pointing to a different repo.
    const { workspaceId: wsA } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Clean WS', { workingDir: repoA })
    const { workspaceId: wsB } = await createWorkspaceWithAgentsViaAPI(leapmuxServer, 'Dirty WS', { workingDir: repoB })

    await loginViaToken(page, adminToken)

    // Navigate to workspace B first to load its diff stats.
    await openWorkspace(page, wsB)

    // The DiffStatsBadge is rendered inside the workspace-item div.
    const wsAItem = workspaceRow(page, wsA)

    // Wait for workspace B's diff stats badge to appear on the workspace item.
    // The git status refresh is triggered when the active tab context is set.
    // First wait for the agent tab to be visible (confirms restore is done).
    await agentTabs(page).first().waitFor()

    // Wait for any git-diff-stats badge on the page (workspace item or tab tree).
    await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible()

    // Now switch to workspace A.
    await workspaceRowTitle(page, wsA).click()
    await waitForWorkspaceReady(page)

    // After switching, workspace A should still have no diff stats.
    // Before the fix, workspace B's diff stats would leak into workspace A
    // because the reactive effect applied stale git data during the switch.
    await page.waitForTimeout(3000) // Allow time for any stale effect to fire
    await expect(wsAItem.locator('[data-testid="git-diff-stats"]')).not.toBeVisible()

    // Switch back to workspace B — diff stats should reappear.
    await workspaceRowTitle(page, wsB).click()
    await waitForWorkspaceReady(page)
    await expect(page.locator('[data-testid="git-diff-stats"]').first()).toBeVisible()

    // Switch to workspace A one more time — still no diff stats.
    await workspaceRowTitle(page, wsA).click()
    await waitForWorkspaceReady(page)
    await page.waitForTimeout(3000)
    await expect(wsAItem.locator('[data-testid="git-diff-stats"]')).not.toBeVisible()
  })
})
