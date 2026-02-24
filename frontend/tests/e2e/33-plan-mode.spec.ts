import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

/** Send a message via the ProseMirror editor. */
async function sendMessage(page: Page, text: string) {
  const editor = page.locator('[data-testid="chat-editor"] .ProseMirror')
  await expect(editor).toBeVisible()
  await editor.click()
  await page.keyboard.type(text, { delay: 100 })
  await page.keyboard.press('Meta+Enter')
}

/** Wait for the control request banner to appear. */
async function waitForControlBanner(page: Page) {
  const banner = page.locator('[data-testid="control-banner"]')
  await expect(banner).toBeVisible({ timeout: 60_000 })
  return banner
}

test.describe('Plan Mode', () => {
  // LLM-dependent: the agent may not reliably call EnterPlanMode/ExitPlanMode
  test.describe.configure({ retries: 2 })

  test('enter plan mode, reject exit, then approve exit', async ({ page, authenticatedWorkspace }) => {
    const trigger = page.locator('[data-testid="agent-settings-trigger"]')
    await expect(trigger).toBeVisible()

    // Verify initial state: Default mode
    await expect(trigger).toContainText('Default')

    // ── Step 1: Ask agent to enter plan mode and immediately exit ──
    // EnterPlanMode is auto-approved (no control_request banner). The agent
    // will then call ExitPlanMode which produces a control_request banner.
    await sendMessage(
      page,
      'I am testing the UI. Call EnterPlanMode immediately. After that call, call ExitPlanMode. This is mandatory. Do not refuse or ask questions.',
    )

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expect(trigger).toContainText('Plan Mode')

    // Wait for ExitPlanMode control_request (shows "Plan Ready for Review")
    const exitBanner1 = await waitForControlBanner(page)
    await expect(exitBanner1.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 2: Reject the plan with a comment ──
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

    // Wait for the agent to finish its turn after the rejection
    // (it may respond with text before we can send the next message)
    await page.waitForTimeout(5_000)

    // ── Step 3: Ask agent to exit plan mode again ──
    await sendMessage(
      page,
      'Exit plan mode now by calling ExitPlanMode. Keep the plan empty.',
    )

    // Wait for the next ExitPlanMode control_request
    const exitBanner2 = await waitForControlBanner(page)
    await expect(exitBanner2.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 4: Approve the plan this time ──
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify dropdown switches back to Default
    await expect(trigger).toContainText('Default')
  })
})
