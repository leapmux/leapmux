import { expect } from '@playwright/test'

import { OPTION_ID_PERMISSION_MODE } from '../../../src/components/chat/settingsGroups'
import { ACP_PERMISSION_OUTCOME, ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { QWEN_MODE, QWEN_PERMISSION_OPTION, QWEN_TOOL } from '../../../src/generated/contracts/qwen-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { pickObject, pickString } from '../../../src/lib/jsonPick'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { readNativeMessageSnapshot, readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { currentNativeAgent } from '../helpers/nativeScenario'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { assistantBubbles, expectSettingsChip, expectSettingsOptionChosen, openWorkspace, savedControlAnswer, sendMessage, visibleControlBanner, visibleOnly, waitForAgentIdle } from '../helpers/ui'

import { openQwenAgent, qwenTest } from '../qwen-fixtures'

const PROVIDER = AgentProvider.QWEN_CODE

qwenTest.describe('Qwen Code control requests', () => {
  qwenTest('approves a plan and leaves plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    await modelScript.queue(
      { toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-plan', '# Qwen probe plan\n\n1. Change no files.')] },
      { text: 'Plan approved; starting.' },
    )
    await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    // The request carries the plan itself, so the banner draws it.
    await expect(banner).toContainText('Proposed Plan')
    await expect(banner).toContainText('Change no files.')
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    await expect(assistantBubbles(page).filter({ hasText: 'Plan approved; starting.' })).toBeVisible()
    // Qwen reports the mode it left plan mode for, and the chip follows it.
    await expectSettingsChip(page, 'Default')
  })

  // Approval with a fresh context replaces the session in place.
  // The old turn waits for plan approval. Clear answers that request and cancels the old turn before the new session opens.
  // The approved plan runs in the new session. No old-session output reaches the reader after that replacement.
  qwenTest('approves a plan with a fresh context and runs it in the new session', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')

    // The clear answers the old approval `cancelled`, which Qwen reads as "keep
    // planning", and the cancel of the old turn follows it. Qwen 0.24 ends a turn after a
    // cancelled plan approval without a model request (Session.ts, exit_plan_mode cancel).
    // The fallback stays so that a Qwen version that asks the model once more cannot fail
    // this test with an unscripted turn.
    await modelScript.fallback({ text: 'Still planning in the old session.' })
    // The new session receives the stored plan, with the marker that the plan
    // carries, so its request reaches this script.
    await modelScript.rule({
      name: 'the approved plan runs in the new session',
      when: { body: 'Execute the following plan' },
      respond: { text: 'Running the approved plan in a fresh context.' },
    })
    await modelScript.queue({
      toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-fresh-plan', modelScript.prompt('# Qwen fresh plan\n\n1. Change no files.'))],
    })
    await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
    await modelScript.waitForSteps(1)
    const banner = visibleControlBanner(page)
    await expect(banner).toContainText('Proposed Plan')
    const clearContext = page.locator('[data-testid="plan-clear-context-checkbox"] input[type="checkbox"]')
    await clearContext.check()
    await expect(clearContext).toBeChecked()
    await page.getByTestId('plan-approve-btn').filter({ visible: true }).click()
    await expect(banner).toHaveCount(0)

    await expect(visibleOnly(page.getByText('Context cleared'))).toBeVisible()
    await expect(assistantBubbles(page).filter({ hasText: 'Running the approved plan in a fresh context.' })).toBeVisible()
    await waitForAgentIdle(page)
    await expect(banner).toHaveCount(0)
    await expect(assistantBubbles(page).filter({ hasText: 'Still planning in the old session.' })).toHaveCount(0)
  })

  // Reject answers Qwen's plan approval with the option `cancel` ("No, keep planning").
  // Over the Agent Client Protocol, Qwen then cancels the tool call, reports it as
  // failed, and ends the turn with no further model request. It keeps the refusal in
  // its history for the next prompt and stays in plan mode (Session.ts,
  // stopAfterPermissionCancel). So this script queues the tool call alone.
  qwenTest('rejects a plan, ends the turn without a model request, and stays in plan mode', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
    await openQwenAgent(leapmuxServer, authenticatedEmptyWorkspace.workspaceId, { [OPTION_ID_PERMISSION_MODE]: 'plan' })
    await openWorkspace(page, authenticatedEmptyWorkspace.workspaceId)
    await expectSettingsChip(page, 'Plan')
    const agent = await currentNativeAgent({ page, leapmuxServer })
    const watch = await watchNativeControls(leapmuxServer, agent.id)
    await withCleanup(async () => {
      await modelScript.queue({
        toolCalls: [exitPlanModeToolCall(PROVIDER, 'qwen-rejected-plan', '# Qwen rejected plan\n\n1. Keep this plan unapproved.')],
      })
      await sendMessage(page, modelScript.prompt('Plan the probe, then ask for approval.'))
      await modelScript.waitForSteps(1)
      const banner = visibleControlBanner(page)
      await expect(banner).toContainText('Proposed Plan')
      await expect(banner).toContainText('Keep this plan unapproved.')
      await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
      const observed = onlyObservedNativeControl(watch.controls())
      await page.getByTestId('plan-reject-btn').filter({ visible: true }).click()

      await expect(banner).toHaveCount(0)
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await waitForAgentIdle(page)
      await expectSettingsOptionChosen(page, `${OPTION_ID_PERMISSION_MODE}-${QWEN_MODE.Plan}`)
      const status = await modelScript.status()
      expect(status.unexpectedRequests).toEqual([])
      expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(1)
      expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

      const snapshot = await readNativeMessageSnapshot({ leapmuxServer }, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
      expect(decision.request).toEqual(observed.payload)
      expect(decision.response).toEqual({
        jsonrpc: '2.0',
        id: observed.payload.id,
        result: { outcome: { outcome: ACP_PERMISSION_OUTCOME.Selected, optionId: QWEN_PERMISSION_OPTION.Cancel } },
      })
      const toolCallId = pickString(pickObject(pickObject(observed.payload, 'params'), 'toolCall'), 'toolCallId')
      expect(toolCallId).not.toBe('')
      const refusal = readNativeToolOutputRecord(snapshot, {
        callId: toolCallId,
        spanId: toolCallId,
        accepts: frame => frame.sessionUpdate === ACP_UPDATE.ToolCallUpdate && frame.toolCallId === toolCallId && frame.status === 'failed',
      })
      expect(refusal.frame.content).toEqual([{ type: 'content', content: { type: 'text', text: `Tool "${QWEN_TOOL.ExitPlanMode}" was canceled by the user.` } }])

      await page.reload()
      await expect(savedControlAnswer(page)).toHaveText('Reject')
      await expectSettingsChip(page, 'Plan')
    }, async () => watch.cancel())
  })
})
