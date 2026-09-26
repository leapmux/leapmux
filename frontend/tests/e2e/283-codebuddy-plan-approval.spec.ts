import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEBUDDY_E2E_SKIP_REASON, codebuddyTest, expect } from './codebuddy-fixtures'
import { enterPlanModeToolCall, exitPlanModeToolCall } from './helpers/providerToolCalls'
import { expectSettingsChip, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from './helpers/ui'

/**
 * 283 — CodeBuddy Code plan approval.
 *
 * `EnterPlanMode` switches the session to Plan without asking. `ExitPlanMode`
 * raises the plan for review: the banner shows the plan controls, a rejection
 * keeps plan mode, and an approval implements the plan and switches the session
 * to Accept Edits (CodeBuddy's own plan-exit mode).
 */
codebuddyTest.skip(!!CODEBUDDY_E2E_SKIP_REASON, CODEBUDDY_E2E_SKIP_REASON || '')

const PROVIDER = AgentProvider.CODEBUDDY

/** The plan text the exit call raises for approval. `testId` keeps two runs apart. */
function planText(testId: string): string {
  return `# Dummy plan ${testId}\n\nNever execute this plan.`
}

codebuddyTest.describe('CodeBuddy Code plan approval', () => {
  codebuddyTest('raises the plan for review, keeps plan mode on reject, and implements it on approve', async ({ codebuddyWorkspace, page, modelScript }) => {
    void codebuddyWorkspace
    await waitForSettingsHydrated(page)
    // Plan mode drives the agent past what a test can count: entering it starts
    // a mode the provider keeps working in, and each verdict starts more turns.
    await modelScript.fallback({ text: 'Working through the plan.' })

    await modelScript.queue(
      { toolCalls: [enterPlanModeToolCall(PROVIDER, 'enter-plan')] },
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'exit-plan-1', modelScript.prompt(planText('first')))] },
    )
    await sendMessage(page, modelScript.prompt('Enter plan mode and present the plan.'))
    await modelScript.waitForSteps(2)

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await expectSettingsChip(page, 'Plan')

    // A typed reply rejects the plan with feedback, and the session stays in
    // plan mode.
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()
    await page.keyboard.type('not ready yet', { delay: 50 })
    await page.getByTestId('plan-reject-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await waitForAgentIdle(page, 180_000)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue({ toolCalls: [exitPlanModeToolCall(PROVIDER, 'exit-plan-2', modelScript.prompt(planText('second')))] })
    await sendMessage(page, modelScript.prompt('Present the plan again.'))
    await modelScript.waitForSteps(1)
    const banner2 = await waitForControlBanner(page)
    await expect(banner2).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await waitForAgentIdle(page, 180_000)
    // An approved plan exit switches the session to Accept Edits.
    await expectSettingsChip(page, 'Accept Edits')
  })
})
