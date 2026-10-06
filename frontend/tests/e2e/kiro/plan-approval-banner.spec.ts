import { expect } from '@playwright/test'
import { kiroSwitchToExecutionToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, openWorkspace, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { expectNoPlanReview } from '../helpers/unsupportedPlanMode'
import { openProviderAgent } from '../helpers/workspace'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_AGENT, nativeContext } from './scenarios'
import { kiroToolResult } from './toolResult'

kiroTest('hands the actual native plan to execution without a plan review banner', async ({ page, modelScript, leapmuxServer, authenticatedEmptyWorkspace }) => {
  await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, KIRO_AGENT, { optionValues: { permissionMode: 'plan' } })
  await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
  await waitForSettingsHydrated(page)
  await expectSettingsChip(page, 'Plan')
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
  const plan = '1. Read the parser.\n2. Test the parser.'
  await expectNoPlanReview(context, {
    relatedProof: async () => {
      const start = await modelScript.queue(
        { toolCalls: [kiroSwitchToExecutionToolCall('no-review-switch', plan)] },
        { text: 'NATIVE_PLAN_HANDED_OFF' },
        { text: 'NATIVE_PLAN_EXECUTION_STARTED' },
      )
      await sendMessage(page, modelScript.prompt('Finish the actual native plan and enter execution.'))
      await modelScript.waitForSteps(start + 3)
      await waitForAgentIdle(page)
      const planned = await modelScript.requestAt(start + 1)
      const executed = await modelScript.requestAt(start + 2)
      expect(JSON.stringify(planned.body)).toContain('"agentMode":"plan"')
      expect(JSON.stringify(executed.body)).toContain('"agentMode":"vibe"')
      expect(kiroToolResult(planned, 'no-review-switch').text.length).toBeGreaterThan(0)
      await expectSettingsChip(page, 'Default')
      await expect(assistantBubbles(page).filter({ hasText: 'NATIVE_PLAN_HANDED_OFF' })).toHaveCount(1)
      await expect(assistantBubbles(page).filter({ hasText: 'NATIVE_PLAN_EXECUTION_STARTED' })).toHaveCount(1)
    },
    afterReload: async () => {
      await waitForSettingsHydrated(page)
      await expectSettingsChip(page, 'Default')
    },
  })
})
