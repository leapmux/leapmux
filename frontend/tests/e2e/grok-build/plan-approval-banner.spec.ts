import { expect } from '@playwright/test'

import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { GROK_METHOD, GROK_MODE, GROK_PLAN_OUTCOME, GROK_REPLY_FIELD } from '../../../src/generated/contracts/grok-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { grokTest, openGrokAgent } from '../grok-fixtures'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { readNativeMessageSnapshot } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, savedControlAnswer, sendMessage, visibleControlBanner, waitForAgentIdle } from '../helpers/ui'

const PROVIDER = AgentProvider.GROK_BUILD

grokTest.describe('Grok Build control requests', () => {
  // Grok's plan approval is a request of its own. Approve takes the shared plan
  // surface, and Grok leaves plan mode itself, which the chip then follows.
  grokTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'grok-plan', '')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Finish planning and ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
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
    await openGrokAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')
    const agent = await currentNativeAgent({ page, leapmuxServer })
    const watch = await watchNativeControls(leapmuxServer, agent.id)
    await withCleanup(async () => {
      await modelScript.queue(
        { toolCalls: [exitPlanModeToolCall(PROVIDER, 'grok-rejected-plan', '')] },
        { text: 'Still planning after the rejection.' },
      )
      await sendMessage(page, modelScript.prompt('Finish planning and ask for approval.'))
      await modelScript.waitForSteps(1)
      const banner = visibleControlBanner(page)
      await expect(banner).toContainText('Plan Ready for Review')
      await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
      const observed = onlyObservedNativeControl(watch.controls())
      await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()

      await expect(banner).toHaveCount(0)
      const status = await modelScript.waitForSteps(2)
      const continuation = status.requests.find(request => request.stepIndex === 1)
      expect(JSON.stringify(continuation?.body)).toContain('The user does not want to exit plan mode. Continue planning and ask the user what they would like to do.')
      await waitForAgentIdle(page)
      await expect(assistantBubbles(page).filter({ hasText: 'Still planning after the rejection.' })).toBeVisible()
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectSettingsOptionChosen(page, `${OPTION_ID_PERMISSION_MODE}-${GROK_MODE.Plan}`)
      expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

      const snapshot = await readNativeMessageSnapshot({ leapmuxServer }, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
      expect(decision.request).toEqual(observed.payload)
      expect(decision.request.method).toBe(GROK_METHOD.ExitPlanMode)
      // A bare rejection carries no feedback field, which is what selects Grok's own refusal text.
      expect(decision.response).toEqual({ jsonrpc: '2.0', id: observed.payload.id, result: { [GROK_REPLY_FIELD.Outcome]: GROK_PLAN_OUTCOME.Cancelled } })

      await page.reload()
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectSettingsChip(page, 'Plan')
    }, async () => watch.cancel())
  })
})
