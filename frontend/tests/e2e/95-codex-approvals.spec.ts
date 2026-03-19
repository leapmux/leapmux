import { codexTest, expect } from './codex-fixtures'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Approvals', () => {
  // Note: The default test fixtures use approvalPolicy: "never" (bypassPermissions),
  // so approval requests won't appear. These tests verify the basic flow works
  // and can be expanded when approval-mode fixtures are added.

  codexTest('agent runs commands without approval prompts in bypass mode', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Run: echo "approval-test-bypass"')
    await waitForAgentIdle(page, 120_000)

    // The command should have executed without any approval prompt.
    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    expect(joined).toContain('approval-test-bypass')
  })

  codexTest('no control banner appears in bypass mode', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Run: echo "no-approval-needed"')
    await waitForAgentIdle(page, 120_000)

    // The control banner should NOT appear in bypass mode.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).not.toBeVisible({ timeout: 3000 })
  })

  codexTest('agent writes files without approval in bypass mode', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    await sendMessage(page, 'Create a file /tmp/codex-approval-test.txt with content "test"')
    await waitForAgentIdle(page, 120_000)

    // Should have completed without approval prompt.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).not.toBeVisible({ timeout: 3000 })
  })
})
