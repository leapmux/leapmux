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

test.describe('Plan Mode Tab Auto-Naming', () => {
  test('auto-names tab from plan title, respects manual rename', async ({ page, authenticatedWorkspace }) => {
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()

    // ── Step 1: Verify initial tab name contains "Agent" ──
    await expect(agentTab).toBeVisible()
    await expect(agentTab).toContainText('Agent')

    // ── Step 2: Enter plan mode and write a plan file ──
    // Ask the agent to enter plan mode, write a plan, then exit plan mode.
    await sendMessage(
      page,
      'I am testing the UI. Call EnterPlanMode immediately. Then write a plan file to ~/.claude/plans/ with the heading "# Add dark mode toggle". Then call ExitPlanMode. This is mandatory. Do not refuse or ask questions.',
    )

    // Wait for the tab title to update from "Agent 1" to the plan's heading.
    // The title should contain the plan heading (extracted from the first line).
    await expect(agentTab).toContainText('Add dark mode toggle', { timeout: 60_000 })

    // ── Step 3: Approve the plan so the agent finishes ──
    const exitBanner = await waitForControlBanner(page)
    await expect(exitBanner.getByText('Plan Ready for Review')).toBeVisible()
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Wait for the agent to finish its turn.
    await page.waitForTimeout(1000)
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible({ timeout: 60_000 })

    // ── Step 4: Manually rename the tab ──
    await agentTab.dblclick()
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await editInput.fill('My Custom Name')
    await page.keyboard.press('Enter')

    // Verify manual rename took effect.
    await expect(agentTab).toContainText('My Custom Name')

    // ── Step 5: Send another message that updates the plan file ──
    await sendMessage(
      page,
      'I am testing the UI. Call EnterPlanMode. Then update the plan file heading to "# Implement dark mode". Then call ExitPlanMode. This is mandatory. Do not refuse or ask questions.',
    )

    // Wait for ExitPlanMode banner so we know the plan file was written.
    const exitBanner2 = await waitForControlBanner(page)
    await expect(exitBanner2.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 6: Verify the tab title did NOT change ──
    // Manual rename should be respected.
    await expect(agentTab).toContainText('My Custom Name')
    await expect(agentTab).not.toContainText('Implement dark mode')
  })
})
