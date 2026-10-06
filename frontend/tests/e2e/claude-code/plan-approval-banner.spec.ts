import { expect } from '@playwright/test'
import { claudeTest } from '../claude-fixtures'
import { enterAndExitPlanMode, enterPlanMode, exitPlanMode } from '../helpers/plan-mode'
import { retryUntilPass } from '../helpers/retryUntilPass'
import { agentTabs, answerPlanReview, composerEditor, enterControlFeedback, expectNoControlBanner, expectSettingsChip, measureBubbleEdges, openAgentInfoCard, PLATFORM_MOD, settingsBar, userBubbles, visibleOnly, waitForAgentIdle, waitForControlBanner, waitForEditorDraft, waitForWorkspaceReady } from '../helpers/ui'
import { listAgentsViaAPI } from '../helpers/workerTabs'
import { CLAUDE_AGENT } from './scenarios'

claudeTest.describe('Control Request Draft Persistence', () => {
  claudeTest('ExitPlanMode draft survives page reload', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    // Enter plan mode, write a dummy plan, and exit.
    const banner = await enterAndExitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider })
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Type a rejection reason in the editor.
    await enterControlFeedback(page, 'draft rejection reason')

    // Wait for the debounced save to actually land, not for a fixed margin.
    await waitForEditorDraft(page, leapmuxServer.adminUserId, 'draft rejection reason')

    // Reload the page.
    await page.reload()

    // Wait for the control banner to reappear (control requests are persisted server-side).
    await waitForControlBanner(page)

    // Verify the editor still contains the rejection reason.
    await expect(composerEditor(page)).toContainText('draft rejection reason')
  })
})

claudeTest.describe('Plan Mode', () => {
  claudeTest('enter plan mode, reject exit, then approve exit', async ({ page, authenticatedWorkspace, modelScript }) => {
    const trigger = settingsBar(page)
    await expect(trigger).toBeVisible()

    // Verify initial state: Default mode
    await expectSettingsChip(page, 'Default')

    // ── Step 1: Enter plan mode ──
    await enterPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider }, { testId: 'plan-mode' })

    // Verify dropdown switches to Plan Mode (EnterPlanMode is auto-approved)
    await expectSettingsChip(page, 'Plan Mode')

    // ── Step 2: Exit plan mode (produces control_request banner) ──
    const exitBanner1 = await exitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider }, { testId: 'plan-mode' })
    await expect(exitBanner1.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 3: Reject the plan with a comment ──
    await enterControlFeedback(page, 'not ready yet')
    // The click waits until the button is enabled.
    await answerPlanReview(page, 'reject')

    // Verify we are still in Plan Mode after rejection
    await expectSettingsChip(page, 'Plan Mode')

    // Wait for the control banner to disappear (rejection was processed)
    await expectNoControlBanner(page)

    // The rejection returns to the model, which the fallback answers.
    await waitForAgentIdle(page)

    // ── Step 4: Exit plan mode again ──
    const exitBanner2 = await exitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider }, { testId: 'plan-mode-again' })
    await expect(exitBanner2.getByText('Plan Ready for Review')).toBeVisible()

    // ── Step 5: Verify clear context checkbox is visible and unchecked ──
    const clearContextCheckbox = page.locator('[data-testid="plan-clear-context-checkbox"]:visible input[type="checkbox"]')
    await expect(clearContextCheckbox).toBeVisible()
    await expect(clearContextCheckbox).not.toBeChecked()

    // ── Step 6: Approve the plan (without clearing context) ──
    await expect(page.getByRole('radiogroup', { name: 'Permissions' }).getByRole('radio', { name: 'Smart' })).toBeChecked()
    await answerPlanReview(page, 'approve')

    // Claude's Smart preset selects Auto Mode.
    await expectSettingsChip(page, 'Auto Mode')

    // Without clear context, the agent continues in current context —
    // no plan_execution notification, so no plan file row in the popover.
  })

  claudeTest('approve with clear context checkbox checked', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Enter plan mode, then exit — get the approval banner.
    const banner = await enterAndExitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider }, { testId: 'clear-ctx' })
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // Verify checkbox is visible and unchecked by default.
    const clearContextCheckbox = page.locator('[data-testid="plan-clear-context-checkbox"]:visible input[type="checkbox"]')
    await expect(clearContextCheckbox).toBeVisible()
    await expect(clearContextCheckbox).not.toBeChecked()

    // Check the clear context checkbox.
    await clearContextCheckbox.check()
    await expect(clearContextCheckbox).toBeChecked()

    // Approve the plan. The click waits until the button is enabled.
    await answerPlanReview(page, 'approve')

    // Verify control banner disappears.
    await expectNoControlBanner(page)

    // Verify context_cleared notification appears in the chat.
    //
    // `visibleOnly`, because this is a transcript row. ChatView keeps a hidden
    // premeasure copy of a row until its height is known, and the real row is
    // hidden in place while it measures, so an unscoped locator matches TWO
    // elements in that window. Playwright does not retry a strict-mode
    // violation, so this failed at once instead of waiting for the row to settle.
    //
    // A SUBSTRING match, never `exact`. `notificationRenderers.tsx` joins
    // adjacent text notifications into one element, so this row reads
    // "Context cleared" until the plan hand-off lands and then
    // "Context cleared, Executing plan" -- an exact match stops matching the
    // moment the row grows, and waits out its whole timeout on a row that is
    // plainly on screen.
    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()

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
    const popover = await openAgentInfoCard(page)
    await expect(popover.locator('[data-testid="info-row-plan-file"]')).toBeVisible()
  })
})

claudeTest.describe('plan mode - bypass permissions', () => {
  claudeTest('approve and switches toggle with feedback on editor content', async ({ page, authenticatedWorkspace, modelScript }) => {
    // Enter plan mode, write a dummy plan, and exit
    const banner = await enterAndExitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider })
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    // The visible copy of each control. A count of zero visible copies proves that no copy of the control is visible.
    const control = (testId: string) => page.getByTestId(testId).filter({ visible: true })

    // The empty editor shows Reject, Approve, the Clear Context switch, and the permission pills.
    await expect(control('plan-reject-btn')).toBeVisible()
    await expect(control('plan-approve-btn')).toBeVisible()
    await expect(control('plan-clear-context-checkbox')).toBeVisible()
    await expect(control('control-permissions-pill-group')).toBeVisible()

    // Type rejection text in the editor
    await enterControlFeedback(page, 'needs changes')

    // With editor content: Send feedback is visible and Approve is hidden.
    await expect(control('plan-reject-btn')).toHaveText('Send feedback')
    await expect(control('plan-approve-btn')).toHaveCount(0)
    await expect(control('plan-clear-context-checkbox')).toHaveCount(0)
    await expect(control('control-permissions-pill-group')).toHaveCount(0)

    // Clear the editor
    await page.keyboard.press(`${PLATFORM_MOD}+a`)
    await page.keyboard.press('Backspace')

    // Reject and Approve visible again
    await expect(control('plan-reject-btn')).toBeVisible()
    await expect(control('plan-approve-btn')).toBeVisible()
  })

  claudeTest('lays the pill radios and their moving copies out identically', async ({ page, authenticatedWorkspace, modelScript }) => {
    const banner = await enterAndExitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider })
    await expect(banner.getByText('Plan Ready for Review')).toBeVisible()

    const group = page.getByRole('radiogroup', { name: 'Permissions' })
    await expect(group).toBeVisible()

    // A serif face, far from the UA control font. A `<button>` takes its family
    // from the UA stylesheet unless the rule states `inherit`, and on some
    // platforms that family and `system-ui` resolve to the SAME face -- which
    // would let a font divergence pass unseen here. The write goes through the
    // CSSOM, which the page's content security policy does not restrict.
    await page.evaluate(() => {
      document.documentElement.style.setProperty('--font-sans', '"Times New Roman", serif')
    })
    await expect.poll(async () => group.evaluate(el =>
      getComputedStyle(el.querySelector('[role="radio"]')!).fontFamily)).toContain('Times New Roman')

    const measured = await group.evaluate((element) => {
      const metrics = (el: Element | null | undefined) => {
        if (!el)
          return undefined
        const rect = el.getBoundingClientRect()
        const style = getComputedStyle(el)
        return {
          x: rect.x,
          width: rect.width,
          font: style.fontFamily,
          fontSize: style.fontSize,
          padding: style.padding,
        }
      }
      const radios = [...element.querySelectorAll('[role="radio"]')]
      const copies = [...element.querySelectorAll('[data-pill-selection-labels] [data-label]')]
      return radios.map((radio, index) => ({
        label: radio.textContent ?? '',
        radio: metrics(radio),
        copy: metrics(copies[index]),
      }))
    })

    // The group paints each label twice: the real radio carries the text, and
    // the sliding overlay repeats it as a copy that must cover that radio
    // exactly. The fill is clipped to the RADIO, so a copy that lays out to a
    // different width drags its 1px divider off the boundary and the primary
    // window appears to spill into the next option. Every metric that decides
    // the width is compared, so a rule that reaches one row and not the other
    // fails here whatever property it sets.
    expect(measured.length).toBeGreaterThan(1)
    for (const option of measured) {
      expect(option.copy?.font).toBe(option.radio?.font)
      expect(option.copy?.fontSize).toBe(option.radio?.fontSize)
      expect(option.copy?.padding).toBe(option.radio?.padding)
      expect(Math.abs((option.copy?.x ?? 0) - (option.radio?.x ?? 0))).toBeLessThanOrEqual(0.5)
      expect(Math.abs((option.copy?.width ?? 0) - (option.radio?.width ?? 0))).toBeLessThanOrEqual(0.5)
    }
  })
})

claudeTest.describe('Plan Mode Tab Auto-Naming', () => {
  claudeTest('auto-names tab from plan title, respects manual rename', async ({ page, authenticatedWorkspace, leapmuxServer, modelScript }) => {
    const agentTab = agentTabs(page).first()

    // ── Step 1: Verify initial tab name contains "Agent" ──
    await expect(agentTab).toBeVisible()
    await expect(agentTab).toContainText('Agent')

    // ── Step 2: Enter plan mode, write the plan file, and exit ──
    // The plan body includes "Never execute this plan." so that after
    // approval the plan execution restart finishes quickly instead of
    // the agent spending minutes exploring the codebase.
    const exitBanner = await enterAndExitPlanMode({ page, modelScript, provider: CLAUDE_AGENT.provider }, { testId: 'first' })

    // Tab should be renamed by now (plan_updated with update_agent_title:true fires on Write).
    await expect(agentTab).toContainText('Dummy plan first')

    // ── Step 3: Approve the plan ──
    await expect(exitBanner.getByText('Plan Ready for Review')).toBeVisible()
    await answerPlanReview(page, 'approve')

    // Wait for plan execution to finish. The agent sees
    // "Never execute this plan." in the plan content and finishes quickly.
    // The "Executing plan" text may appear too briefly to catch, so wait for
    // the turn to end. Asserting the thinking indicator is absent RIGHT after
    // the approve click passes against a turn that has not started yet;
    // waitForAgentIdle gives it a bounded chance to appear first.
    await waitForAgentIdle(page)

    // ── Step 4: Manually rename the tab ──
    await agentTab.dblclick()
    const editInput = agentTab.locator('input')
    await expect(editInput).toBeVisible()
    await editInput.fill('My Custom Name')
    await page.keyboard.press('Enter')

    // Verify manual rename took effect.
    await expect(agentTab).toContainText('My Custom Name')

    // ── Step 5: Verify manual rename persists after page reload ──
    // Instead of entering plan mode again (which is LLM-dependent and fragile),
    // verify that the manual rename persists across a page reload.
    //
    // Wait for the WORKER to hold the new title before reloading. The rename
    // handler patches local metadata and fires renameAgent without awaiting it,
    // so the tabbar shows the new name immediately while the RPC is still in
    // flight -- and a reload started in that window cancels it, leaving the
    // agent named after the plan. Polling the server here is what makes the
    // assertion below about PERSISTENCE rather than about timing.
    const { hubUrl, adminToken, workerId } = leapmuxServer
    await retryUntilPass(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)
      expect(agents.map(a => a.title), 'the Worker stores the new agent name').toContain('My Custom Name')
    })

    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(agentTab).toContainText('My Custom Name')
  })
})
