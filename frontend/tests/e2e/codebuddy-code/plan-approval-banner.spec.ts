import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest, expect } from '../codebuddy-fixtures'
import { enterPlanModeToolCall, exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code plan approval', () => {
  const PROVIDER = AgentProvider.CODEBUDDY

  function planText(testId: string): string {
    return `# Dummy plan ${testId}\n\nNever execute this plan.`
  }

  codebuddyTest('raises review again after rejection and resumes in Default after approval', async ({ authenticatedCodebuddyWorkspace, page, modelScript }) => {
    void authenticatedCodebuddyWorkspace
    await waitForSettingsHydrated(page)
    // Plan mode can start additional model turns.
    // Each plan decision can start another turn.
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
    await waitForAgentIdle(page)
    await expectSettingsOptionChosen(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue({ toolCalls: [exitPlanModeToolCall(PROVIDER, 'exit-plan-2', modelScript.prompt(planText('second')))] })
    await modelScript.rule({
      name: 'the approved plan continues in the native model',
      when: { body: 'Exited plan mode' },
      respond: { text: 'The approved plan continued.' },
      once: true,
    })
    await sendMessage(page, modelScript.prompt('Present the plan again.'))
    await modelScript.waitForSteps(1)
    const banner2 = await waitForControlBanner(page)
    await expect(banner2).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await expect.poll(async () => (await modelScript.status()).ruleMatches['the approved plan continues in the native model'] ?? 0).toBe(1)
    await waitForAgentIdle(page)
    // The installed CodeBuddy CLI returns to Default after an approved exit.
    await expectSettingsOptionChosen(page, 'permissionMode-default')
  })
})
