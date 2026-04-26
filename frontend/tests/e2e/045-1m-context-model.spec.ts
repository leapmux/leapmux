import { expect, test } from './fixtures'
import { lastAssistantBubble, openSettingsMenu, waitForSettingsIdle } from './helpers/ui'

// The e2e account can't bill Sonnet's 1M-context tier, so this suite uses
// Opus[1m] instead. The underlying coverage — bracketed model IDs and the
// settings-change notification — is the same.
const MODEL_CHANGE_PATTERN = /Model.*Sonnet.*Opus.*1M/

test.describe('1m-context model', () => {
  test('switch to opus[1m] and exchange messages', async ({ authenticatedWorkspace, page }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Default model should be Sonnet
    await expect(trigger).toContainText('Sonnet')

    // Switch to Opus[1m]
    await openSettingsMenu(page)
    await page.locator('[data-testid="model-opus\\[1m\\]"]').click()
    await expect(trigger).toContainText('Opus (1M context)')

    // Verify the settings change notification appears in chat
    await expect(page.getByText(MODEL_CHANGE_PATTERN)).toBeVisible()

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

    // Verify the model is still shown as Opus[1m] after exchanging messages
    await expect(trigger).toContainText('Opus (1M context)')
  })
})
