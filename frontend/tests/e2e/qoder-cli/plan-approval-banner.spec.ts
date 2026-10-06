import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { enterPlanModeToolCall, exitPlanModeFromFileToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { sendMessage, waitForAgentIdle, waitForControlBanner, waitForSettingsHydrated } from '../helpers/ui'
import { expect, expectQoderModeChip, qoderTest } from '../qoder-fixtures'

qoderTest.describe('Qoder CLI plan approval', () => {
  const PROVIDER = AgentProvider.QODER

  function planText(revision: string): string {
    return `# Dummy plan ${revision}\n\nNever execute this plan.`
  }

  const PLAN_FILE_CAPTURE = { planFile: '(?:create your plan at|already exists at) (\\S+?\\.md)' }

  function planSteps(revision: string) {
    return [
      {
        toolCalls: [writeToolCall(PROVIDER, `write-plan-${revision}`, { path: '{{planFile}}', content: planText(revision) })],
        captures: PLAN_FILE_CAPTURE,
      },
      { toolCalls: [exitPlanModeFromFileToolCall(PROVIDER, `exit-plan-${revision}`, [])] },
    ]
  }

  qoderTest('reviews the plan, retains it on reject, and applies the selected mode on approve', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page)
    // Plan mode can start additional model turns.
    // Each plan decision can start another turn.
    await modelScript.fallback({ text: 'Working through the plan.' })

    await modelScript.queue({ toolCalls: [enterPlanModeToolCall(PROVIDER, 'enter-plan')] })
    await sendMessage(page, modelScript.prompt('Enter plan mode and present the plan.'))
    await modelScript.waitForSteps()
    const enterBanner = await waitForControlBanner(page)
    await expect(enterBanner).toContainText('EnterPlanMode')
    await page.getByTestId('control-allow-btn').filter({ visible: true }).click()
    await waitForAgentIdle(page)
    await expectQoderModeChip(page, 'Plan')

    await modelScript.queue(...planSteps('first'))
    await sendMessage(page, modelScript.prompt('Write the plan file, then present it for review.'))
    await modelScript.waitForSteps()

    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await expectQoderModeChip(page, 'Plan')

    // A typed reply rejects the plan with feedback, and the session stays in
    // plan mode.
    await page.locator('[data-testid="composer-editor"] .ProseMirror').click()
    await page.keyboard.type('not ready yet', { delay: 50 })
    await page.getByTestId('plan-reject-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await waitForAgentIdle(page)
    await expectQoderModeChip(page, 'Plan')

    await modelScript.queue(...planSteps('second'))
    await sendMessage(page, modelScript.prompt('Revise the plan file and present it again.'))
    await modelScript.waitForSteps()
    const banner2 = await waitForControlBanner(page)
    await expect(banner2).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').click()
    await expect(page.locator('[data-testid="control-banner"]')).not.toBeVisible()
    await waitForAgentIdle(page)
    // The review control selects Auto. The next turn keeps the approved plan.
    await expectQoderModeChip(page, 'Auto')
    await modelScript.queue({ text: 'The approved mode stayed active.' })
    await sendMessage(page, modelScript.prompt('Reply after the approved plan.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const nextRequest = status.requests.find(request => request.stepIndex === status.stepCount - 1)
    expect(nextRequest).toBeDefined()
    const nextBody = JSON.stringify(nextRequest?.body)
    expect(nextBody.includes('# Dummy plan second')).toBe(true)
    expect(nextBody.includes('Exited Plan Mode')).toBe(true)
  })
})
