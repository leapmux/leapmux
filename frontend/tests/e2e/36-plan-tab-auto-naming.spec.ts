import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { planModePrompt } from './helpers'

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

    // ── Step 2: Enter plan mode and exit with a plan ──
    // The plan body includes "Never execute this plan." so that after
    // approval the plan execution restart finishes quickly instead of
    // the agent spending minutes exploring the codebase.
    await sendMessage(page, planModePrompt('first'))

    // Wait for the tab title to update from "Agent 1" to the plan's heading.
    await expect(agentTab).toContainText('Dummy plan first', { timeout: 60_000 })

    // ── Step 3: Approve the plan ──
    const exitBanner = await waitForControlBanner(page)
    await expect(exitBanner.getByText('Plan Ready for Review')).toBeVisible()
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Wait for plan execution to start and then finish. The agent sees
    // "Never execute this plan." in the plan content and finishes quickly.
    await expect(page.getByText('Executing plan')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-testid="thinking-indicator"]')).not.toBeVisible({ timeout: 120_000 })

    // ── Step 4: Manually rename the tab ──
    await agentTab.dblclick()
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await editInput.fill('My Custom Name')
    await page.keyboard.press('Enter')

    // Verify manual rename took effect.
    await expect(agentTab).toContainText('My Custom Name')

    // ── Step 5: Enter plan mode again with a different plan heading ──
    await sendMessage(page, planModePrompt('second'))

    // Wait for ExitPlanMode banner so we know the plan file was written.
    const exitBanner2 = await waitForControlBanner(page)
    await expect(exitBanner2.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 6: Verify the tab title did NOT change ──
    // Manual rename should be respected; auto-rename is skipped.
    await expect(agentTab).toContainText('My Custom Name')
    await expect(agentTab).not.toContainText('Dummy plan second')
  })
})
