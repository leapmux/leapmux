import { expect, test } from './fixtures'
import { enterAndExitPlanMode, enterPlanMode, exitPlanMode } from './helpers/plan-mode'
import { expectSettingsChip, measureBubbleEdges, settingsBar, userBubbles, waitForAgentIdle } from './helpers/ui'

test.describe('Plan Mode', () => {
  test('enter plan mode, reject exit, then approve exit', async ({ page, authenticatedWorkspace, modelScript }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Verify initial state: Default mode
    await expectSettingsChip(page, 'Default')

    // ── Step 1: Enter plan mode ──
    await enterPlanMode(page, modelScript, { testId: 'plan-mode' })

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expectSettingsChip(page, 'Plan Mode')

    // ── Step 2: Exit plan mode (produces control_request banner) ──
    const exitBanner1 = await exitPlanMode(page, modelScript, { testId: 'plan-mode' })
    await expect(exitBanner1.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 3: Reject the plan with a comment ──
    const editorForReject = page.locator('[data-testid="composer-editor"] .ProseMirror')
    await editorForReject.click()
    await page.keyboard.type('not ready yet', { delay: 100 })
    const rejectBtn = page.locator('[data-testid="plan-reject-btn"]')
    await expect(rejectBtn).toBeEnabled()
    await rejectBtn.click()

    // Verify we are still in Plan Mode after rejection
    await expectSettingsChip(page, 'Plan Mode')

    // Wait for the control banner to disappear (rejection was processed)
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // The rejection returns to the model, which the fallback answers.
    await waitForAgentIdle(page, 60_000)

    // ── Step 4: Exit plan mode again ──
    const exitBanner2 = await exitPlanMode(page, modelScript, { testId: 'plan-mode-again' })
    await expect(exitBanner2.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 5: Verify clear context checkbox is visible and unchecked ──
    const clearContextCheckbox = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await expect(clearContextCheckbox).toBeVisible()
    await expect(clearContextCheckbox).not.toBeChecked()

    // ── Step 6: Approve the plan (without clearing context) ──
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await expect(page.getByRole('radiogroup', { name: 'Permissions' }).getByRole('radio', { name: 'Smart' })).toBeChecked()
    await approveBtn.click()

    // Claude's Smart preset selects Auto Mode.
    await expectSettingsChip(page, 'Auto Mode')

    // Without clear context, the agent continues in current context —
    // no plan_execution notification, so no plan file row in the popover.
  })

  test('approve with clear context checkbox checked', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Enter plan mode, then exit — get the approval banner.
    const banner = await enterAndExitPlanMode(page, modelScript, 'clear-ctx')
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify checkbox is visible and unchecked by default.
    const clearContextCheckbox = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await expect(clearContextCheckbox).toBeVisible()
    await expect(clearContextCheckbox).not.toBeChecked()

    // Check the clear context checkbox.
    await clearContextCheckbox.check()
    await expect(clearContextCheckbox).toBeChecked()

    // Approve the plan.
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

    // Verify control banner disappears.
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()

    // Verify context_cleared notification appears in the chat.
    await expect(page.locator('text=Context cleared')).toBeVisible()

    // The worker persists the plan hand-off with a USER source, so it wears the
    // same end-of-line card a typed message wears and takes the same rule to the
    // right panel edge. Only a real browser resolves that rule: the bleed is CSS
    // var arithmetic that cancels the list gutter, and it works around paint
    // containment on the virtual row. The sibling assertion for a typed message
    // lives in 040-chat-message-rendering.spec.ts.
    const planCard = userBubbles(page).filter({ hasText: 'Execute plan' }).first()
    await expect(planCard).toBeVisible()
    const edges = await measureBubbleEdges(planCard)
    expect(Math.abs(edges.rightGap)).toBeLessThanOrEqual(1)
    // Still a card on the left: inset by at least the gutter, not stretched across.
    expect(edges.leftGap).toBeGreaterThan(20)
    // A rounded corner flush against the edge would read as a mistake.
    expect(edges.radius).toBe('0px')
    // Top edge on the row's, like every other bubble -- the row places it.
    expect(Math.abs(edges.topGapInRow)).toBeLessThanOrEqual(1)

    // Verify Plan File is shown in the popover (plan_execution fires on clear context).
    const infoTrigger = page.locator('[data-testid="agent-info-trigger"]')
    await expect(infoTrigger).toBeVisible()
    await infoTrigger.click()
    const popover = page.locator('[data-testid="agent-info-popover"]')
    await expect(popover).toBeVisible()
    await expect(popover.locator('[data-testid="info-row-plan-file"]')).toBeVisible()
  })
})
