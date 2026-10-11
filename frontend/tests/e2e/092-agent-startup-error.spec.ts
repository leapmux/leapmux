import type { TimingWorker } from './helpers/timingFixture'
import { expect } from '@playwright/test'
import { test } from './fixtures'
import { openAgentViaAPI } from './helpers/api'
import { withTimingWorker } from './helpers/timingFixture'
import { loginViaToken, openWorkspace, sendMessage } from './helpers/ui'
import { withTestWorkspace } from './helpers/workspace'

// A worker whose every shell is /usr/bin/false cannot launch its agent, so the
// startup error panel is the only outcome its tab can show.
const startupErrorTest = test.extend<{ failingWorker: TimingWorker }>({
  failingWorker: async ({ leapmuxServer }, use) => withTimingWorker(leapmuxServer, {
    dataDirPrefix: 'leapmux-startup-err',
    env: { SHELL: '/usr/bin/false', LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT_SECONDS: '5' },
  }, use),
})

startupErrorTest.describe('agent startup error', () => {
  startupErrorTest('shows in-tab error and rejects subsequent sends', async ({ page, failingWorker }) => {
    const srv = failingWorker.server

    await withTestWorkspace(srv, 'startup-err', async ({ workspaceId }) => {
      await openAgentViaAPI(srv, workspaceId)
      await loginViaToken(page, srv.adminToken)
      await openWorkspace(page, workspaceId)

      // The startup-error panel must appear with the formatted error.
      const errorPanel = page.locator('[data-testid="agent-startup-error"]')
      await expect(errorPanel).toBeVisible()
      await expect(errorPanel.locator('h2')).toContainText('failed to start')
      await expect(errorPanel.locator('pre code')).toBeVisible()

      // The Worker retains the input as a failed queue item.
      await sendMessage(page, 'hello')
      await expect(page.getByTestId('agent-input-queue')).toContainText('Failed')
      await expect(page.getByTestId('agent-input-queue')).toContainText('hello')
    })
  })
})
