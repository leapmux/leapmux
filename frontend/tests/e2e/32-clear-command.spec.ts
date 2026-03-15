import { expect, test } from './fixtures'
import { lastAssistantBubble } from './helpers/ui'

test.describe('Clear Command', () => {
  test('slash clear clears context and shows notification', async ({ page, authenticatedWorkspace }) => {
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()

    // Send a message to establish a session
    await editor.click()
    await page.keyboard.type('What is 1+1? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    const lastAssistant1 = lastAssistantBubble(page)
    await expect(lastAssistant1).toContainText('2', { timeout: 30000 })

    // Send /clear
    await editor.click()
    await page.keyboard.type('/clear')
    await page.keyboard.press('Meta+Enter')

    // Verify notification bubble appears
    await expect(page.getByText('Context cleared')).toBeVisible()

    // Verify agent is still responsive (new session)
    await editor.click()
    await page.keyboard.type('What is 3+3? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')
    const lastAssistant2 = lastAssistantBubble(page)
    await expect(lastAssistant2).toContainText('6', { timeout: 30000 })

    // After /clear and a new response, context usage is repopulated by the
    // new session's system prompt tokens.  Verify the grid is visible again
    // (indicating the new session has active context).
    const grid = page.locator('svg[viewBox="0 0 11 11"]')
    await expect(grid).toBeVisible()
  })
})
