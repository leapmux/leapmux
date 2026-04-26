import { waitForWorkspaceReady } from './helpers/ui'
import { expect, OPENCODE_E2E_SKIP_REASON, opencodeTest } from './opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest.describe('OpenCode Agent Lifecycle', () => {
  opencodeTest('agent starts and shows ready state', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // The editor should be visible and ready for input.
    const editor = page.locator('[data-testid="chat-editor"]')
    await expect(editor).toBeVisible({ timeout: 30_000 })
  })

  opencodeTest('agent reconnects after page reload', async ({ authenticatedOpencodeWorkspace, page }) => {
    void authenticatedOpencodeWorkspace // fixture trigger

    // Reload the page.
    await page.reload()
    await waitForWorkspaceReady(page)

    // The editor should be visible again after reconnecting.
    const editor = page.locator('[data-testid="chat-editor"]')
    await expect(editor).toBeVisible({ timeout: 30_000 })
  })
})
