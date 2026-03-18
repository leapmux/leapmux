import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { lastAssistantBubble } from './helpers/ui'

/** Open the settings menu, retrying if it was caught mid-close animation. */
async function openSettingsMenu(page: Page) {
  const trigger = page.locator('[data-testid="agent-settings-trigger"]')
  const menu = page.locator('[data-testid="agent-settings-menu"]')
  await expect(async () => {
    if (!await menu.isVisible()) {
      await trigger.click()
    }
    await expect(menu).toBeVisible()
  }).toPass({ timeout: 5000 })
}

/** Wait for the settings loading spinner to disappear. */
async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
}

test.describe('1m-context model', () => {
  test('switch to sonnet[1m] and exchange messages', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Default model should be Sonnet
    await expect(trigger).toContainText('Sonnet')

    // Switch to Sonnet[1m]
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-sonnet\\[1m\\]"]').click()
    await expect(trigger).toContainText('Sonnet[1m]')

    // Verify the settings change notification appears in chat
    await expect(page.getByText('Model (Sonnet \u2192 Sonnet[1m])')).toBeVisible()

    // Wait for agent restart to complete
    await waitForSettingsIdle(page)

    // Send a message and verify the agent responds
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await expect(editor).toBeVisible()
    await editor.click()
    await page.keyboard.type('What is 5+3? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    const lastAssistant = lastAssistantBubble(page)
    await expect(lastAssistant).toContainText('8', { timeout: 30000 })

    // Send a follow-up to confirm the agent session is stable
    await editor.click()
    await page.keyboard.type('What is 10-4? Reply with just the number, nothing else.')
    await page.keyboard.press('Meta+Enter')

    const lastAssistant2 = lastAssistantBubble(page)
    await expect(lastAssistant2).toContainText('6', { timeout: 30000 })

    // Verify the model is still shown as Sonnet[1m] after exchanging messages
    await expect(trigger).toContainText('Sonnet[1m]')
  })
})
