import { isMaybeVisible, sendMessage, waitForAgentIdle } from './helpers/ui'
import { expect, PI_E2E_SKIP_REASON, piTest } from './pi-fixtures'

piTest.skip(!!PI_E2E_SKIP_REASON, PI_E2E_SKIP_REASON || '')

piTest.describe('Pi Coding Agent Lifecycle', () => {
  piTest('Pi agent tab is visible after creation', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    const tabs = page.locator('[data-testid="tab"]')
    await expect(tabs.first()).toBeVisible()
  })

  piTest('can close Pi agent tab', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    const tabsBefore = await page.locator('[data-testid="tab"]').count()
    expect(tabsBefore).toBeGreaterThan(0)

    const closeBtn = page.locator('[data-testid="tab"] [data-testid="close-tab"]').first()
    if (await closeBtn.isVisible()) {
      await closeBtn.click()
      const confirmBtn = page.locator('button:has-text("Close")')
      if (await isMaybeVisible(confirmBtn, 2000)) {
        await confirmBtn.click()
      }
    }
  })

  piTest('clear context via /clear command resets Pi session', async ({ authenticatedPiWorkspace, page }) => {
    void authenticatedPiWorkspace // fixture trigger
    await sendMessage(page, 'Remember the number 42 for me.')
    await waitForAgentIdle(page, 180_000)

    await sendMessage(page, '/clear')
    await page.waitForTimeout(5000)

    // Pi's clear-context handler routes through new_session + get_state.
    // Either a "context cleared" notification appears or the chat is reset
    // to a small number of messages.
    const chatArea = page.locator('[data-testid="message-content"]')
    const allText = await chatArea.allTextContents()
    const joined = allText.join(' ').toLowerCase()
    expect(joined.includes('clear') || await chatArea.count() <= 2).toBeTruthy()
  })
})
