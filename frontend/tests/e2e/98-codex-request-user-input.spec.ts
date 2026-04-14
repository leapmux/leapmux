import { codexTest, expect } from './codex-fixtures'
import { isMaybeVisible, openSettingsMenu, sendMessage, waitForAgentIdle, waitForSettingsIdle } from './helpers/ui'

codexTest.describe('Codex requestUserInput', () => {
  codexTest('approval flow works with on-request policy', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace

    // Switch to on-request approval policy so approval prompts appear.
    await openSettingsMenu(page)
    const onRequestRadio = page.locator('[data-testid="permission-mode-on-request"]')
    await expect(onRequestRadio).toBeVisible()
    await onRequestRadio.click()
    await waitForSettingsIdle(page)

    // Close the menu by clicking elsewhere.
    await page.locator('[data-testid="chat-editor"] .ProseMirror').click()

    // Send a command that will trigger an approval request.
    // Use rm which should always require approval in on-request mode.
    await sendMessage(page, 'Run this exact command: rm -rf /tmp/codex-approval-test-dir-nonexistent')

    // Wait for the control banner to appear.
    const banner = page.locator('[data-testid="control-banner"]')
    await expect(banner).toBeVisible({ timeout: 120_000 })

    // Click the Allow button (Codex uses decision-based buttons).
    const decisionAllowBtn = page.locator('[data-testid="control-decision-accept"]')
    const legacyAllowBtn = page.locator('[data-testid="control-allow-btn"]')
    const allowBtn = await isMaybeVisible(decisionAllowBtn) ? decisionAllowBtn : legacyAllowBtn
    await expect(allowBtn).toBeVisible()
    await allowBtn.click()

    // Wait for the agent to finish and verify the command ran.
    await waitForAgentIdle(page, 120_000)
    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ')
    expect(joined).toContain('codex-approval-test-dir-nonexistent')
  })
})
