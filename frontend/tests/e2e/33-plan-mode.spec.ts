import { expect, test } from './fixtures'
import { enterPlanPrompt, EXIT_PLAN_PROMPT } from './helpers/plan-mode'
import { sendMessage, waitForAgentIdle, waitForControlBanner } from './helpers/ui'

test.describe('Plan Mode', () => {
  test('enter plan mode, reject exit, then approve exit', async ({ page, authenticatedWorkspace }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Verify initial state: Default mode
    await expect(trigger).toContainText('Default')

    // ── Step 1: Enter plan mode and write a dummy plan ──
    await sendMessage(page, enterPlanPrompt('plan-mode'))

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expect(trigger).toContainText('Plan Mode')
    await waitForAgentIdle(page)

    // ── Step 2: Exit plan mode (produces control_request banner) ──
    await sendMessage(page, EXIT_PLAN_PROMPT)

    const exitBanner1 = await waitForControlBanner(page)
    await expect(exitBanner1.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 3: Reject the plan with a comment ──
    const editorForReject = page.locator('[data-testid="chat-editor"] .ProseMirror')
    await editorForReject.click()
    await page.keyboard.type('not ready yet', { delay: 100 })
    const rejectBtn = page.locator('[data-testid="plan-reject-btn"]')
    await expect(rejectBtn).toBeEnabled()
    await rejectBtn.click()

    // Verify we are still in Plan Mode after rejection
    await expect(trigger).toContainText('Plan Mode')

    // Wait for the control banner to disappear (rejection was processed)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Wait for the agent to finish its turn after the rejection.
    await waitForAgentIdle(page, 60_000)

    // ── Step 4: Exit plan mode again ──
    // The agent might call ExitPlanMode again on its own after rejection,
    // or we may need to ask it explicitly.
    const bannerAlreadyVisible = await page.locator('[data-testid="control-banner"]').isVisible()
    if (!bannerAlreadyVisible) {
      await sendMessage(page, EXIT_PLAN_PROMPT)
    }

    const exitBanner2 = await waitForControlBanner(page)
    await expect(exitBanner2.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 5: Approve the plan this time ──
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify dropdown switches to Accept Edits (plan approval sets acceptEdits mode)
    await expect(trigger).toContainText('Accept Edits')

    // ── Step 6: Verify Plan File is shown in the popover ──
    const infoTrigger = page.locator('[data-testid="session-id-trigger"]')
    await expect(infoTrigger).toBeVisible({ timeout: 30_000 })
    await infoTrigger.click()
    const popover = page.locator('[data-testid="session-id-popover"]')
    await expect(popover).toBeVisible()
    await expect(popover.locator('[data-testid="info-row-plan-file"]')).toBeVisible()
  })
})
