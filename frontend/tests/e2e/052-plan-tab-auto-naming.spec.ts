import { expect, test } from './fixtures'
import { enterAndExitPlanMode } from './helpers/plan-mode'
import { waitForAgentIdle, waitForWorkspaceReady } from './helpers/ui'
import { listAgentsViaAPI } from './helpers/worktree'

test.describe('Plan Mode Tab Auto-Naming', () => {
  test.setTimeout(300_000)
  test('auto-names tab from plan title, respects manual rename', async ({ page, authenticatedWorkspace, leapmuxServer }) => {
    const agentTab = page.locator('[data-testid="tab"][data-tab-type="agent"]').first()

    // ── Step 1: Verify initial tab name contains "Agent" ──
    await expect(agentTab).toBeVisible()
    await expect(agentTab).toContainText('Agent')

    // ── Step 2: Enter plan mode, write the plan file, and exit ──
    // The plan body includes "Never execute this plan." so that after
    // approval the plan execution restart finishes quickly instead of
    // the agent spending minutes exploring the codebase.
    const exitBanner = await enterAndExitPlanMode(page, 'first')

    // Tab should be renamed by now (plan_updated with update_agent_title:true fires on Write).
    await expect(agentTab).toContainText('Dummy plan first')

    // ── Step 3: Approve the plan ──
    await expect(exitBanner.getByText('Plan Ready for Review')).toBeVisible()
    const approveBtn = page.locator('[data-testid="plan-approve-btn"]')
    await expect(approveBtn).toBeEnabled()
    await approveBtn.click()

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
    await expect.poll(async () => {
      const agents = await listAgentsViaAPI(hubUrl, adminToken, workerId, authenticatedWorkspace.workspaceId)
      return agents.map(a => a.title)
    }).toContain('My Custom Name')

    await page.reload()
    await waitForWorkspaceReady(page)
    await expect(agentTab).toContainText('My Custom Name')
  })
})
