import { expect } from '@playwright/test'

import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { GROK_METHOD, GROK_MODE, GROK_PLAN_OUTCOME, GROK_REPLY_FIELD } from '../../../src/generated/contracts/grok-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { GROK_AGENT, grokTest } from '../grok-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { readObservedNativeDecision, waitForOneNativeControl } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, assistantBubbles, controlBanner, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, savedControlAnswer, sendMessage, waitForAgentIdle } from '../helpers/ui'
import { openProviderAgent } from '../helpers/workspace'

const PROVIDER = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build control requests', () => {
  // Grok's plan approval is a request of its own. Approve takes the shared plan
  // surface, and Grok leaves plan mode itself, which the chip then follows.
  grokTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    const start = await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'grok-plan', '')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and ask for approval.'))
    await modelScript.waitForSteps(start + 1)
    const banner = controlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await answerPlanReview(page, 'approve')
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps(start + 2)
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Plan approved; starting.' })).toBeVisible()
    await expectSettingsChip(page, 'Default')
  })

  // Reject answers Grok's plan approval with the outcome `cancelled`. Grok gives the
  // refusal to the model as the tool's result, and the turn goes on, so the model
  // answers once more in the same turn (tool_calls.rs, PlanApprovalOutcome::Cancelled).
  // The mock writes no plan file, so Grok's text is its no-plan refusal. Grok stays in
  // plan mode and sends no mode change.
  grokTest('rejects a plan, returns the refusal to the model, and stays in plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openProviderAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, GROK_AGENT, { optionValues: { [OPTION_ID_PERMISSION_MODE]: 'plan' } })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')
    const agent = await currentNativeAgent({ page, leapmuxServer })
    const watch = await watchNativeControls(leapmuxServer, agent.id)
    await withCleanup(async () => {
      const start = await modelScript.queue(
        { toolCalls: [exitPlanModeToolCall(PROVIDER, 'grok-rejected-plan', '')] },
        { text: 'Still planning after the rejection.' },
      )
      await sendMessage(page, modelScript.prompt('Finish planning and ask for approval.'))
      await modelScript.waitForSteps(start + 1)
      const banner = controlBanner(page)
      await expect(banner).toContainText('Plan Ready for Review')
      const observed = await waitForOneNativeControl(watch)
      await answerPlanReview(page, 'reject')

      await expect(banner).toHaveCount(0)
      const continuation = await modelScript.requestAt(start + 1)
      expect(JSON.stringify(continuation.body)).toContain('The user does not want to exit plan mode. Continue planning and ask the user what they would like to do.')
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'Still planning after the rejection.' })).toBeVisible()
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectSettingsOptionChosen(page, `${OPTION_ID_PERMISSION_MODE}-${GROK_MODE.Plan}`)

      const { decision } = await readObservedNativeDecision({ leapmuxServer }, agent, watch, observed)
      expect(decision.request.method).toBe(GROK_METHOD.ExitPlanMode)
      // A bare rejection carries no feedback field, which is what selects Grok's own refusal text.
      expect(decision.response).toEqual({ jsonrpc: '2.0', id: observed.payload.id, result: { [GROK_REPLY_FIELD.Outcome]: GROK_PLAN_OUTCOME.Cancelled } })

      await page.reload()
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectSettingsChip(page, 'Plan')
    }, async () => watch.cancel())
  })
})
