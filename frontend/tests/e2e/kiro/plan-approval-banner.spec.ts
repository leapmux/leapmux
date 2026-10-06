import { expect } from '@playwright/test'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { kiroToolResult } from '../helpers/kiroToolResult'
import { expectNoNativeControl } from '../helpers/nativeControlObservation'
import { kiroSwitchToExecutionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { kiroTest, openKiroAgent } from '../kiro-fixtures'

kiroTest('hands the actual native plan to execution without a plan review banner', async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
  await openKiroAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { permissionMode: 'plan' })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Plan')
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KIRO }
  const plan = '1. Read the parser.\n2. Test the parser.'
  await expectNoNativeControl(context, { testId: 'plan-approve-btn', relatedControl: () => expectNoNativeControl(context, { testId: 'plan-reject-btn', relatedControl: async () => {
    await modelScript.queue(
      { toolCalls: [kiroSwitchToExecutionToolCall('no-review-switch', plan)] },
      { text: 'NATIVE_PLAN_HANDED_OFF' },
      { text: 'NATIVE_PLAN_EXECUTION_STARTED' },
    )
    await sendMessage(page, modelScript.prompt('Finish the actual native plan and enter execution.'))
    const status = await modelScript.waitForSteps(3)
    await waitForAgentIdle(page)
    const planned = status.requests.find(request => request.stepIndex === 1)
    const executed = status.requests.find(request => request.stepIndex === 2)
    expect(JSON.stringify(planned?.body)).toContain('"agentMode":"plan"')
    expect(JSON.stringify(executed?.body)).toContain('"agentMode":"vibe"')
    if (!planned)
      throw new Error('The native plan switch reached no follow-up request.')
    expect(kiroToolResult(planned, 'no-review-switch').text.length).toBeGreaterThan(0)
    await expectSettingsChip(page, 'Default')
    await expect(assistantBubbles(page).filter({ hasText: 'NATIVE_PLAN_HANDED_OFF' })).toHaveCount(1)
    await expect(assistantBubbles(page).filter({ hasText: 'NATIVE_PLAN_EXECUTION_STARTED' })).toHaveCount(1)
  } }) })
  await page.reload()
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Default')
  await expect(page.locator('[data-testid="plan-approve-btn"]:visible')).toHaveCount(0)
  await expect(page.locator('[data-testid="plan-reject-btn"]:visible')).toHaveCount(0)
})
