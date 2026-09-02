import { expect, test } from './fixtures'
import { ENTER_PLAN_PROMPT, enterAndExitPlanMode, EXIT_PLAN_PROMPT } from './helpers/plan-mode'
import { expectSettingsChip, sendMessage, settingsBar, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from './helpers/ui'

test.describe('Plan Mode - Bypass Permissions', () => {
  test('bypass permissions from ExitPlanMode banner', async ({ page, authenticatedWorkspace }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()
    await expectSettingsChip(page, 'Default')

    // Step 1: Enter plan mode and write a dummy plan
    await sendMessage(page, ENTER_PLAN_PROMPT)

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expectSettingsChip(page, 'Plan Mode')
    await waitForAgentIdle(page)

    // Step 2: Exit plan mode (produces control_request banner)
    await sendMessage(page, EXIT_PLAN_PROMPT)
    const banner = await waitForControlBanner(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify both switches are visible and unchecked.
    const clearContextSwitch = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await expect(clearContextSwitch).toBeVisible()
    await expect(clearContextSwitch).not.toBeChecked()

    const bypassSwitch = page.locator('[data-testid="plan-bypass-permissions-checkbox"] input[type="checkbox"]')
    await expect(bypassSwitch).toBeVisible()
    await expect(bypassSwitch).not.toBeChecked()

    // Enable bypass permissions, then approve.
    await bypassSwitch.check()
    await expect(bypassSwitch).toBeChecked()

    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify control banner disappears (plan was approved)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Verify permission mode changed to Bypass Permissions
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Bypass Permissions')
  })

  test('approve and switches toggle with feedback on editor content', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // The empty editor shows Reject, Approve, and both switches.
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-clear-context-checkbox"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-bypass-permissions-checkbox"]')).toBeVisible()

    // Type rejection text in the editor
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('needs changes', { delay: 100 })

    // With editor content: Send feedback is visible and Approve is hidden.
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toHaveText('Send feedback')
    await expect(page.locator('[data-testid="plan-approve-btn"]')).not.toBeVisible()

    // Clear the editor
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')

    // Reject and Approve visible again
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
  })
})
