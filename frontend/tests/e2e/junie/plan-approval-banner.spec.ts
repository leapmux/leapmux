import { KIND_REJECT_ONCE } from '../../../src/components/chat/model/controlPrompt'
import { ACP_PERMISSION_OUTCOME, ACP_UPDATE } from '../../../src/generated/contracts/acp-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { isObject, pickObject, pickString } from '../../../src/lib/jsonPick'
import { withCleanup } from '../helpers/cleanup'
import { watchNativeControls } from '../helpers/nativeControlWatch'
import { readNativeMessageSnapshot, readNativeToolOutputRecord } from '../helpers/nativeMessages'
import { waitForNativeOptionApplied } from '../helpers/nativeSettings'
import { onlyObservedNativeControl, readNativeStoredControlDecision } from '../helpers/nativeStoredControlDecision'
import { junieSubmitPlanToolCall } from '../helpers/providerToolCalls'
import { chooseSettingsOption, controlBanner, expectSettingsChip, expectSettingsOptionChosen, savedControlAnswer, sendMessage, waitForAgentIdle, waitForSettingsHydrated } from '../helpers/ui'
import { expect, junieTest } from '../junie-fixtures'
import { exerciseNativePlanReview } from './planScenarios'

junieTest.describe('Junie plan review', () => {
  junieTest('a plan raises a review request and the plan entries in the to-do sidebar', async ({ authenticatedJunieWorkspace, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await exerciseNativePlanReview({ page, modelScript, provider: AgentProvider.JUNIE })
  })

  // LeapMux draws Junie's plan review as a permission with Junie's own options. Deny
  // selects the first reject option, `revise`. Junie then reports the review as
  // "Plan not implemented.", starts no work, and ends the turn with no further model
  // request (SessionEventEmitter.requestPlanApproval and handlePlanReview in the
  // installed jar). A revision settles no plan, so the session stays in plan mode.
  junieTest('denies a plan review, ends the turn without implementing the plan, and stays in plan mode', async ({ authenticatedJunieWorkspace, leapmuxServer, page, modelScript }) => {
    void authenticatedJunieWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, 'permissionMode-plan')
    await expectSettingsChip(page, 'Plan')
    // Junie applies the mode with a live config-option write, so the native session that the later
    // assertions compare stays the one that this read returns.
    const agent = await waitForNativeOptionApplied({ page, leapmuxServer }, 'permissionMode', 'plan')
    const watch = await watchNativeControls(leapmuxServer, agent.id)
    await withCleanup(async () => {
      await modelScript.queue({
        toolCalls: [junieSubmitPlanToolCall(
          'junie-denied-plan',
          'deny-the-plan',
          [{ name: 'Requirements', content: '- Keep this plan unimplemented.\n' }],
          [{ name: 'Keep this plan unimplemented', description: 'Change no files.' }],
        )],
      })
      await sendMessage(page, modelScript.prompt('Plan the change.'))
      await modelScript.waitForSteps(1)
      const banner = controlBanner(page)
      await expect(banner).toContainText('Implement this plan?')
      await expect(banner).toContainText('Keep this plan unimplemented')
      await expect.poll(() => watch.controls().length).toBeGreaterThan(0)
      const observed = onlyObservedNativeControl(watch.controls())
      await page.getByTestId('control-deny-btn').filter({ visible: true }).click()

      await expect(banner).toHaveCount(0)
      // The browser draws the saved answer from the Worker row alone, so the row exists now.
      await expect(savedControlAnswer(page)).toHaveCount(1)
      await waitForAgentIdle(page)
      await expectSettingsOptionChosen(page, 'permissionMode-plan')
      const status = await modelScript.status()
      expect(status.unexpectedRequests).toEqual([])
      expect(status.requests.filter(request => request.stepIndex !== undefined)).toHaveLength(1)
      expect(onlyObservedNativeControl(watch.controls())).toBe(observed)

      const snapshot = await readNativeMessageSnapshot({ leapmuxServer }, agent.id)
      expect(snapshot.agentSessionId).toBe(agent.agentSessionId)
      const decision = readNativeStoredControlDecision(snapshot, observed.requestId)
      expect(decision.request).toEqual(observed.payload)
      expect(decision.request.method).toBe('session/request_permission')
      const params = pickObject(decision.request, 'params')
      const toolCallId = pickString(pickObject(params, 'toolCall'), 'toolCallId')
      expect(toolCallId).not.toBe('')
      const options = Array.isArray(params?.options) ? params.options.filter(isObject) : []
      const revise = options.find(option => option.optionId === 'revise')
      expect(revise).toMatchObject({ kind: KIND_REJECT_ONCE })
      expect(options.findIndex(option => option.kind === KIND_REJECT_ONCE)).toBe(options.indexOf(revise ?? {}))
      expect(decision.response).toEqual({
        jsonrpc: '2.0',
        id: observed.payload.id,
        result: { outcome: { outcome: ACP_PERMISSION_OUTCOME.Selected, optionId: 'revise' } },
      })
      // The saved row repeats the words of the option, which Junie states in the request.
      const reviseLabel = pickString(revise, 'name')
      expect(reviseLabel).not.toBe('')
      await expect(savedControlAnswer(page)).toHaveText(reviseLabel)
      const refusal = readNativeToolOutputRecord(snapshot, {
        callId: toolCallId,
        spanId: toolCallId,
        accepts: frame => frame.sessionUpdate === ACP_UPDATE.ToolCallUpdate && frame.toolCallId === toolCallId && frame.status === 'completed',
      })
      expect(refusal.frame.content).toMatchObject([{ type: 'content', content: { type: 'text', text: 'Plan not implemented.' } }])

      await page.reload()
      await expect(savedControlAnswer(page)).toHaveText(reviseLabel)
      await expectSettingsChip(page, 'Plan')
    }, async () => watch.cancel())
  })
})
