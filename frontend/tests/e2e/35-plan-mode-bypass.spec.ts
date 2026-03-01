import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { ENTER_PLAN_PROMPT, enterAndExitPlanMode, EXIT_PLAN_PROMPT } from './helpers/plan-mode'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'

/** Wait for the settings loading spinner to disappear. */
async function waitForSettingsIdle(page: Page) {
  await expect(page.locator('[data-testid="settings-loading-spinner"]')).not.toBeVisible()
}

test.describe('Plan Mode - Bypass Permissions', () => {
  test('bypass permissions from ExitPlanMode banner', async ({ page, authenticatedWorkspace }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()
    await expect(trigger).toContainText('Default')

    // Step 1: Enter plan mode and write a dummy plan
    await sendMessage(page, ENTER_PLAN_PROMPT)

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expect(trigger).toContainText('Plan Mode')
    await waitForAgentIdle(page)

    // Step 2: Exit plan mode (produces control_request banner)
    await sendMessage(page, EXIT_PLAN_PROMPT)
    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify bypass button is visible
    const bypassBtn = page.locator('[data-testid="control-bypass-btn"]')
    await expect(bypassBtn).toBeVisible()
    await expect(bypassBtn).toHaveAttribute('title', 'Approve this plan and stop asking for permissions')

    // Click bypass permissions
    await bypassBtn.click()

    // Verify control banner disappears (plan was approved)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Verify permission mode changed to Bypass Permissions
    await waitForSettingsIdle(page)
    await expect(trigger).toContainText('Bypass Permissions')
  })

  test('approve and bypass buttons toggle with reject on editor content', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // With empty editor: Reject, Approve and Bypass all visible
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-bypass-btn"]')).toBeVisible()

    // Type rejection text in the editor
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('needs changes', { delay: 100 })

    // With editor content: Reject visible, Approve and Bypass hidden
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).not.toBeVisible()
    await expect(page.locator('[data-testid="control-bypass-btn"]')).not.toBeVisible()

    // Clear the editor
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')

    // Reject, Approve and Bypass all visible again
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="control-bypass-btn"]')).toBeVisible()
  })
})
