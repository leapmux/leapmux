import { expect } from '@playwright/test'
import { WS_CHANNEL_ROUTE } from '../../src/generated/contracts/wire'
import { deleteWorkspaceViaAPI } from './helpers/api'
import { withCleanup } from './helpers/cleanup'
import { treeRow, waitForWorkspaceReady } from './helpers/ui'
import { showWorkspaceWithAgents } from './helpers/workspace'
import { createGitRepo } from './helpers/worktree'
import { ensureWorkerOnline, restartWorker, stopWorker, processTest as test, waitForWorkerOffline } from './process-control-fixtures'

test('keeps an unpersisted file tab unpublished across Worker restart', async ({ page, separateHubWorker }) => {
  await ensureWorkerOnline(separateHubWorker)
  const repoDir = createGitRepo(separateHubWorker.dataDir, 'payload-publication')
  let workspaceId: string | undefined
  let hold = false
  let held = 0
  await page.routeWebSocket(url => url.pathname === WS_CHANNEL_ROUTE, (browser) => {
    const server = browser.connectToServer()
    browser.onMessage((message) => {
      if (hold)
        held += 1
      else
        server.send(message)
    })
  })
  await withCleanup(async () => {
    ;({ workspaceId } = await showWorkspaceWithAgents(page, separateHubWorker, 'Payload publication', { workingDir: repoDir }))
    await expect(treeRow(page, 'README.md')).toBeVisible()
    hold = true
    await treeRow(page, 'README.md').click()
    await expect.poll(() => held).toBeGreaterThan(0)
    const files = page.locator('[data-testid="tab"][data-tab-type="file"]:visible')
    await expect(files).toHaveCount(0)
    await stopWorker(separateHubWorker)
    await waitForWorkerOffline(separateHubWorker)
    await restartWorker(separateHubWorker)
    await expect(files).toHaveCount(0)
    // The old channel lost the held request. Reload opens a new channel and
    // retries the real registration without replaying its obsolete ciphertext.
    hold = false
    await page.reload()
    await waitForWorkspaceReady(page)
    await treeRow(page, 'README.md').click()
    await expect(files).toHaveCount(1)
    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(files).toHaveCount(1)
  }, async () => {
    hold = false
    await ensureWorkerOnline(separateHubWorker)
    if (workspaceId)
      await deleteWorkspaceViaAPI(separateHubWorker.hubUrl, separateHubWorker.adminToken, workspaceId)
  })
})
