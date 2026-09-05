import type { DevServerHandle } from './helpers/devServer'
/**
 * Verifies the in-tab startup-error UX. We don't want to disturb the
 * shared dev fixture (which other specs reuse with a real `claude` on
 * PATH), so this spec spawns its own `leapmux dev` with:
 *   - SHELL=/usr/bin/false so the agent subprocess exits 1 immediately
 *     and the initialize handshake never completes
 *   - LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT_SECONDS=5 to bound the test.
 */
import { expect, test } from '@playwright/test'
import {
  createWorkspaceViaAPI,
  deleteWorkspaceViaAPI,
  openAgentViaAPI,
} from './helpers/api'
import { startDevServer, stopDevServer } from './helpers/devServer'
import { loginViaToken, openWorkspace } from './helpers/ui'

function startServerWithFailingClaude(): Promise<DevServerHandle> {
  return startDevServer({
    dataDirPrefix: 'leapmux-startup-err',
    env: {
      LEAPMUX_WORKER_NAME: 'Local',
      LEAPMUX_CLAUDE_DEFAULT_MODEL: 'sonnet',
      LEAPMUX_CLAUDE_DEFAULT_EFFORT: 'low',
      LEAPMUX_WORKER_AGENT_STARTUP_TIMEOUT_SECONDS: '5',
      // /usr/bin/false ignores all args and exits 1 — the shell
      // "exec claude ..." never runs.
      SHELL: '/usr/bin/false',
    },
  })
}

test.describe('Claude Code agent startup error', () => {
  let srv: DevServerHandle

  test.beforeAll(async () => {
    srv = await startServerWithFailingClaude()
  })

  test.afterAll(async () => {
    if (srv)
      await stopDevServer(srv)
  })

  test('shows in-tab error and rejects subsequent sends', async ({ browser }) => {
    const context = await browser.newContext({ baseURL: srv.hubUrl })
    const page = await context.newPage()

    const workspaceId = await createWorkspaceViaAPI(
      srv.hubUrl,
      srv.adminToken,
      `startup-err-${Date.now()}`,
    )
    await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)
    await loginViaToken(page, srv.adminToken)
    await openWorkspace(page, workspaceId)

    // The startup-error panel must appear with the formatted error.
    const errorPanel = page.locator('[data-testid="agent-startup-error"]')
    await expect(errorPanel).toBeVisible()
    await expect(errorPanel.locator('h2')).toContainText('failed to start')
    await expect(errorPanel.locator('pre code')).toBeVisible()

    // The Worker retains the input as a failed queue item.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('hello')
    await page.keyboard.press('Meta+Enter')
    await expect(page.getByTestId('agent-input-queue')).toContainText('Failed')
    await expect(page.getByTestId('agent-input-queue')).toContainText('hello')

    await deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId).catch(() => {})
    await context.close()
  })
})
