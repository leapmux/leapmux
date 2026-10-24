import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { expect } from '@playwright/test'
import { CODEBUDDY_MODE } from '../../../src/generated/contracts/codebuddy-protocol'
import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codebuddyTest } from '../codebuddy-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { exerciseNativePermissionDecision } from '../helpers/nativePermission'
import { addedInstructionLines } from '../helpers/nativePlanMode'
import { currentNativeAgent, nativeModelContextText, nativeModelInstructionText, nativeModelLastUserText } from '../helpers/nativeScenario'
import { exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exitPlanModeToolCall, writeToolCall } from '../helpers/providerToolCalls'
import { answerPlanReview, chooseSettingsOption, expectSettingsChip, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForControlBanner, waitForNativeSettingsHydrated, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codebuddyTest.describe('CodeBuddy Code settings', () => {
  codebuddyTest('the mode menu lists the four advertised modes', async ({ authenticatedCodebuddyWorkspace, page }) => {
    void authenticatedCodebuddyWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsOptionsOffered(page, 'permissionMode', Object.values(CODEBUDDY_MODE))
    // The fixture opens the agent in Bypass Permissions.
    await expectSettingsOptionChosen(page, `permissionMode-${CODEBUDDY_MODE.BypassPermissions}`)
  })

  codebuddyTest('keeps selected Plan mode for the next native turn', async ({ native }) => {
    const { page, modelScript } = native
    const baseline = 'The first mode probe answered.'
    await sendNativeAnswer(native, 'Reply before I change mode.', baseline)

    const original = await currentNativeAgent(native)
    await chooseSettingsOption(page, `permissionMode-${CODEBUDDY_MODE.Plan}`)
    await waitForSettingsIdle(page)
    await expectSettingsChip(page, 'Plan')
    const planStep = await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.CODEBUDDY, 'exit-selected-plan', '# Probe plan')] })
    await modelScript.fallback({ text: 'The mode probe ended.' })
    await sendMessage(page, modelScript.prompt('Present the plan for review after the mode change.'))
    const next = await modelScript.requestAt(planStep)
    expect(next.protocol).toBe('openai-chat-completions')
    expect(JSON.stringify(next.body)).toContain(baseline)
    const banner = await waitForControlBanner(page)
    await expect(banner).toContainText('Plan Ready for Review')
    await answerPlanReview(page, 'reject')
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
    const revised = await sendNativeAnswer(native, 'Revise the rejected plan.', 'The revised plan retains the conversation.')
    expect(nativeModelContextText(revised)).toContain(baseline)
    expect((await currentNativeAgent(native)).agentSessionId).toBe(original.agentSessionId)

    // This process starts in Plan, so rejection must restore input even when its launch mode matches.
    const repeated = await modelScript.queue({ toolCalls: [exitPlanModeToolCall(AgentProvider.CODEBUDDY, 'exit-repeated-plan', '# Revised probe plan')] })
    await sendMessage(page, modelScript.prompt('Present the revised plan for review.'))
    await modelScript.requestAt(repeated)
    await waitForControlBanner(page)
    await answerPlanReview(page, 'reject')
    await waitForAgentIdle(page)
    await expectSettingsChip(page, 'Plan')
    const final = await sendNativeAnswer(native, 'Revise the plan once more.', 'The second revision retains the conversation.')
    expect(nativeModelContextText(final)).toContain(baseline)
    expect(nativeModelContextText(final)).toContain('The revised plan retains the conversation.')
    expect((await currentNativeAgent(native)).agentSessionId).toBe(original.agentSessionId)
  })

  codebuddyTest('returns from Plan to Default and proves both native modes after reload', async ({ native }) => {
    await waitForNativeSettingsHydrated(native.page)
    await expectSettingsOptionsOffered(native.page, 'permissionMode', Object.values(CODEBUDDY_MODE))
    await chooseNativeModeWithNotice(native, {
      groupId: 'permissionMode',
      value: CODEBUDDY_MODE.Default,
      label: 'Default',
      previousLabel: 'Bypass Permissions',
      noticeLabel: 'Permissions',
    })
    const baseline = await sendNativeAnswer(native, 'Reply once with the current native setting.', 'CODEBUDDY_MODE_SEQUENCE_CONTEXT')
    const normalInstruction = nativeModelInstructionText(baseline)
    const agent = await currentNativeAgent(native)
    let writeProbe = 0
    for (const mode of [
      { value: CODEBUDDY_MODE.Plan, label: 'Plan', previousLabel: 'Default' },
      { value: CODEBUDDY_MODE.Default, label: 'Default', previousLabel: 'Plan' },
    ]) {
      const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...mode, noticeLabel: 'Permissions' })
      const nativeProof = async (request: MockModelRequestRecord) => {
        expect(request.protocol).toBe('openai-chat-completions')
        expect(nativeModelContextText(request)).toContain('CODEBUDDY_MODE_SEQUENCE_CONTEXT')
        const instruction = nativeModelInstructionText(request)
        if (mode.value === CODEBUDDY_MODE.Plan) {
          expect(addedInstructionLines(normalInstruction, instruction)).toMatch(/\bplan(?:ning)?\b|read-only|\bread only\b/i)
          expect(nativeModelLastUserText(request)).toContain('Plan mode is active.')
        }
        else {
          // Earlier Plan reminders stay in the conversation. The current prompt states the active mode.
          expect(nativeModelLastUserText(request)).not.toContain('Plan mode is active.')
          const callId = `codebuddy-default-mode-write-${writeProbe++}`
          const filename = `${callId}.txt`
          const path = join(agent.workingDir, filename)
          const content = `The native Default write ${writeProbe} ran after approval.\n`
          expect(existsSync(path)).toBe(false)
          await exerciseNativePermissionDecision(native, {
            toolCall: writeToolCall(AgentProvider.CODEBUDDY, callId, { path, content }),
            decision: 'allow',
            beforeDecision: async (banner) => {
              await expect(banner).toContainText(filename)
              expect(existsSync(path)).toBe(false)
            },
            nativeProof: (result) => {
              expect(readFileSync(path, 'utf8')).toBe(content)
              expect(nativeToolResult(result, callId)).toContain(filename)
            },
          })
        }
      }
      await nativeProof(await sendNativeAnswer(native, 'Reply once with the current native setting.', `CodeBuddy used ${mode.label} mode.`))
      await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: mode.value, nativeProof })
      await expectSettingsChip(native.page, mode.label)
      await notice.assertStored()
    }
  })
})
