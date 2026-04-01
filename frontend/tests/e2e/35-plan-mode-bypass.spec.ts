import { expect, test } from './fixtures'
import { ENTER_PLAN_PROMPT, enterAndExitPlanMode, EXIT_PLAN_PROMPT } from './helpers/plan-mode'
import { sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsIdle } from './helpers/ui'

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

    // Verify both checkboxes are visible and unchecked
    const clearContextCheckbox = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await expect(clearContextCheckbox).toBeVisible()
    await expect(clearContextCheckbox).not.toBeChecked()

    const bypassCheckbox = page.locator('[data-testid="plan-bypass-permissions-checkbox"] input[type="checkbox"]')
    await expect(bypassCheckbox).toBeVisible()
    await expect(bypassCheckbox).not.toBeChecked()

    // Check bypass permissions, then approve
    await bypassCheckbox.check()
    await expect(bypassCheckbox).toBeChecked()

    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify control banner disappears (plan was approved)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Verify permission mode changed to Bypass Permissions
    await waitForSettingsIdle(page)
    await expect(trigger).toContainText('Bypass Permissions')
  })

  test('approve and checkboxes toggle with reject on editor content', async ({ page, authenticatedWorkspace }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode(page)
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // With empty editor: Reject, Approve, and checkboxes all visible
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-clear-context-checkbox"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-bypass-permissions-checkbox"]')).toBeVisible()

    // Type rejection text in the editor
    const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editor.click()
    await page.keyboard.type('needs changes', { delay: 100 })

    // With editor content: Reject visible, Approve hidden
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).not.toBeVisible()

    // Clear the editor
    await page.keyboard.press('Meta+a')
    await page.keyboard.press('Backspace')

    // Reject and Approve visible again
    await expect(page.locator('[data-testid="plan-reject-btn"]')).toBeVisible()
    await expect(page.locator('[data-testid="plan-approve-btn"]')).toBeVisible()
  })
})
