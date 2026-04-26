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
import { loginViaToken, waitForWorkspaceReady } from './helpers/ui'

function startServerWithFailingClaude(): Promise<DevServerHandle> {
  return startDevServer({
    dataDirPrefix: 'leapmux-startup-err',
    env: {
      LEAPMUX_WORKER_NAME: 'Local',
      LEAPMUX_HUB_SIGNUP_ENABLED: 'true',
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
      srv.adminOrgId,
    )
    await openAgentViaAPI(srv.hubUrl, srv.adminToken, srv.workerId, workspaceId)
    await loginViaToken(page, srv.adminToken)
    await page.goto(`/o/admin/workspace/${workspaceId}`)
    await waitForWorkspaceReady(page)

    // The startup-error panel must appear with the formatted error.
    const errorPanel = page.locator('[data-testid="agent-startup-error"]')
    await expect(errorPanel).toBeVisible({ timeout: 30_000 })
    await expect(errorPanel.locator('h2')).toContainText('failed to start')
    await expect(errorPanel.locator('pre code')).toBeVisible()

    // Sends from a stale UI must not produce a network round-trip that
    // succeeds — type a message and submit; the message should appear
    // with an error label rather than being delivered.
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('hello')
    await page.keyboard.press('Meta+Enter')
    await expect(page.locator('[data-testid="message-error"]')).toBeVisible({ timeout: 10_000 })

    await deleteWorkspaceViaAPI(srv.hubUrl, srv.adminToken, workspaceId).catch(() => {})
    await context.close()
  })
})
