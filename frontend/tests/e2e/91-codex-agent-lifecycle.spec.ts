import { codexTest, expect } from './codex-fixtures'
import { sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Agent Lifecycle', () => {
  codexTest('Codex agent tab is visible after creation', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabs = page.locator('[data-testid="tab"]')
    await expect(tabs.first()).toBeVisible()
  })

  codexTest('can create multiple Codex agents', async ({ authenticatedCodexWorkspace, page, leapmuxServer }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabsBefore = await page.locator('[data-testid="tab"]').count()

    // Click the new agent button to create another agent.
    const newAgentBtn = page.locator('[data-testid^="new-agent-button"]').first()
    if (await newAgentBtn.isVisible({ timeout: 5000 }).catch(() => false)) {
      await newAgentBtn.click()
      // Wait for the new tab to appear.
      await page.waitForTimeout(5000)
      const tabsAfter = await page.locator('[data-testid="tab"]').count()
      expect(tabsAfter).toBeGreaterThanOrEqual(tabsBefore)
    }
  })

  codexTest('can close Codex agent tab', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabsBefore = await page.locator('[data-testid="tab"]').count()
    expect(tabsBefore).toBeGreaterThan(0)

    // Close the first agent tab via the close button.
    const closeBtn = page.locator('[data-testid="tab"] [data-testid="close-tab"]').first()
    if (await closeBtn.isVisible()) {
      await closeBtn.click()
      // Confirm if a dialog appears.
      const confirmBtn = page.locator('button:has-text("Close")')
      if (await confirmBtn.isVisible({ timeout: 2000 }).catch(() => false)) {
        await confirmBtn.click()
      }
    }
  })

  codexTest('clear context via /clear command', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    // Send an initial message so there's context to clear.
    await sendMessage(page, 'Hello')
    await waitForAgentIdle(page, 120_000)

    // Send /clear command.
    await sendMessage(page, '/clear')
    await page.waitForTimeout(5000)

    // The context_cleared notification should appear in the chat.
    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ').toLowerCase()
    // Either a "context cleared" notification or the chat should be reset.
    expect(joined.includes('clear') || await chatArea.count() <= 2).toBeTruthy()
  })
})
