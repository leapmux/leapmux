import { DIRAC_E2E_SKIP_REASON, diracTest, expect, openDiracAgent } from './dirac-fixtures'
import { diracRespondToolCall } from './helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, messageBubbles, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from './helpers/ui'

diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

/**
 * 272 — Dirac plan mode.
 *
 * Dirac's plan card resolves at the next prompt and raises no approval banner
 * (matrix note 8): there is no **Approve** to press. The `respond plan` call
 * ends the plan turn; the next prompt answers it.
 */
diracTest.describe('Dirac plan mode', () => {
  diracTest('the plan card resolves at the next prompt and raises no approval', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openDiracAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [diracRespondToolCall('dirac-plan', 'plan', '1. Write the parser.\n2. Test it.')] },
      { toolCalls: [diracRespondToolCall('dirac-plan-done', 'complete', 'The plan is done.')] },
    )
    await sendMessage(page, modelScript.prompt('Plan the work.'))
    await modelScript.waitForSteps(1)
    await waitForAgentIdle(page, 120_000)

    await expect(messageBubbles(page).filter({ hasText: 'Write the parser.' }).first()).toBeVisible()
    // The plan raises no approval banner: nothing named Approve exists.
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
    await expect(page.locator('[data-testid="control-allow-btn"]')).toHaveCount(0)

    await sendMessage(page, modelScript.prompt('Proceed with the plan.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page, 120_000)
    await expect(assistantBubbles(page).filter({ hasText: 'The plan is done.' }).first()).toBeVisible()
    await expect(page.locator('[data-testid="control-banner"]')).toHaveCount(0)
  })
})
