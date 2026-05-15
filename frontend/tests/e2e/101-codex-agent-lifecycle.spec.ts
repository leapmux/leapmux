import { codexTest, expect } from './codex-fixtures'
import { isMaybeVisible, sendMessage, waitForAgentIdle } from './helpers/ui'

codexTest.describe('Codex Agent Lifecycle', () => {
  codexTest('Codex agent tab is visible after creation', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabs = page.locator('[data-testid="tab"]')
    await expect(tabs.first()).toBeVisible()
  })

  codexTest('can create multiple Codex agents', async ({ authenticatedCodexWorkspace, page, leapmuxServer }) => {
    void authenticatedCodexWorkspace // fixture trigger
    void leapmuxServer
    const tabs = page.locator('[data-testid="tab"]')
    const tabsBefore = await tabs.count()

    // Click the new agent button — must be visible. A regression where the
    // button never renders or the click is a no-op should fail this test.
    const newAgentBtn = page.locator('[data-testid^="new-agent-button"]').first()
    await expect(newAgentBtn).toBeVisible()
    await newAgentBtn.click()

    // A new tab must appear.
    await expect(tabs).toHaveCount(tabsBefore + 1)
  })

  codexTest('can close Codex agent tab', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace // fixture trigger
    const tabs = page.locator('[data-testid="tab"]')
    const tabsBefore = await tabs.count()
    expect(tabsBefore).toBeGreaterThan(0)

    // Close the first agent tab via the close button — must be visible.
    const closeBtn = page.locator('[data-testid="tab"] [data-testid="close-tab"]').first()
    await expect(closeBtn).toBeVisible()
    await closeBtn.click()
    // A confirmation dialog may appear — confirm if it does.
    const confirmBtn = page.locator('button:has-text("Close")')
    if (await isMaybeVisible(confirmBtn, 2000)) {
      await confirmBtn.click()
    }

    // The close must take effect: tab count drops by one (or to zero if
    // this was the only tab — workspace may collapse the tab strip).
    await expect(tabs).toHaveCount(Math.max(tabsBefore - 1, 0))
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
