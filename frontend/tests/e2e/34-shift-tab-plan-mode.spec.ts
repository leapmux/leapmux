import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/** Click the settings trigger to open the dropdown menu. */
async function openSettingsMenu(page: Page) {
  await page.locator('[data-testid="agent-settings-trigger"]').click()
  await expect(page.locator('[data-testid="agent-settings-menu"]')).toBeVisible()
}

/** Wait for the settings loading spinner to disappear. */
async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
}

test.describe('Shift-Tab Plan Mode Toggle', () => {
  test('toggle plan mode with Shift+Tab', async ({ page, authenticatedWorkspace }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()
    await expect(trigger).toContainText('Default')

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()

    // Shift+Tab → Plan Mode
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    // Shift+Tab → back to Default
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Default')
  })

  test('consecutive mode toggles are merged in chat history', async ({ page, authenticatedWorkspace }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()
    await expect(trigger).toContainText('Default')

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    const chatContainer = page.locator('[data-testid="chat-container"]')
    const modeNotifications = chatContainer.getByText(/Mode \(/)
    await editor.click()

    // Toggle Default → Plan → Default (round-trip should cancel out)
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Default')
    await waitForSettingsIdle(page)

    // Verify no notification remains (round-trip merges to a no-op)
    await expect(modeNotifications).toHaveCount(0)

    // Toggle once more → Plan Mode; exactly one notification should appear
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)
    await expect(modeNotifications).toHaveCount(1)
    await expect(modeNotifications.first()).toContainText('Default')
    await expect(modeNotifications.first()).toContainText('Plan Mode')
  })

  test('toggle back to non-default mode after dropdown change', async ({ page, authenticatedWorkspace }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Switch to Accept Edits via dropdown
    await openSettingsMenu(page)
    await page.locator('[data-testid="permission-mode-acceptEdits"]').click()
    await expect(trigger).toContainText('Accept Edits')
    // Close the dropdown before continuing
    await page.keyboard.press('Escape')
    await expect(page.locator('[data-testid="agent-settings-menu"]')).not.toBeVisible()
    await waitForSettingsIdle(page)

    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()

    // Shift+Tab → Plan Mode
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Plan Mode')
    await waitForSettingsIdle(page)

    // Shift+Tab → back to Accept Edits (not Default)
    await page.keyboard.press('Shift+Tab')
    await expect(trigger).toContainText('Accept Edits')
  })
})
