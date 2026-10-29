import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_TOOL } from '../../../src/generated/contracts/copilot-protocol'
import { copilotTest } from '../copilot-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { captureStoredNativeModeNotice, chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelToolNames, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { runNativeToolTurn } from '../helpers/nativeToolExecution'
import { nativeToolResult } from '../helpers/nativeToolResult'
import { exitPlanModeToolCall } from '../helpers/providerToolCalls'
import { expectSettingsChip, visibleOnly, waitForNativeSettingsHydrated } from '../helpers/ui'
import { COPILOT_KEPT_MODE_TOOL_RESULT, COPILOT_PLAN_ENDED_NOTICE, isCopilotPlanTurn, latestCopilotModeNotice } from './modeSignals'
import { exerciseCopilotPlanAndEffort } from './settingsScenario'

copilotTest('mode: keeps Plan mode and low effort after a turn and reload', async ({ native }) => {
  await waitForNativeSettingsHydrated(native.page)
  await expectSettingsOptionsOffered(native.page, COPILOT_OPTION.SessionMode, Object.values(COPILOT_MODE))
  let planNotice: NativeModeNotice | undefined
  await exerciseCopilotPlanAndEffort(native, 'mode', async (ownerBefore) => {
    const oldValue = nativeOptionValue(ownerBefore, COPILOT_OPTION.SessionMode)
    if (!oldValue)
      throw new Error('The native Copilot mode notice requires the previous session mode.')
    expect(oldValue).toBe(COPILOT_MODE.Interactive)
    const locator = visibleOnly(native.page.getByText(/(?:^|,\s*)Mode \(Agent → Plan\)(?=,|$)/))
    await expect(locator).toHaveCount(1)
    planNotice = await captureStoredNativeModeNotice(native, ownerBefore, {
      groupId: COPILOT_OPTION.SessionMode,
      oldValue,
      value: COPILOT_MODE.Plan,
      label: 'Plan',
      previousLabel: 'Agent',
      noticeLabel: 'Mode',
    }, locator)
  })
  await expectSettingsChip(native.page, 'Plan')
  if (!planNotice)
    throw new Error('The native Copilot Plan selection produced no captured notice.')
  const notice = await chooseNativeModeWithNotice(native, {
    groupId: COPILOT_OPTION.SessionMode,
    value: COPILOT_MODE.Interactive,
    label: 'Agent',
    previousLabel: 'Plan',
    noticeLabel: 'Mode',
  })
  // The tool list cannot prove the return to Agent mode, because Copilot keeps the Plan tools (see ./modeSignals.ts).
  const nativeProof = (request: MockModelRequestRecord): void => {
    expect(request.protocol).toBe('openai-chat-completions')
    expect(isCopilotPlanTurn(request), 'Copilot handles the turn in Agent mode').toBe(false)
    expect(latestCopilotModeNotice(request), 'the last mode notice states that Plan ended').toContain(COPILOT_PLAN_ENDED_NOTICE)
    expect(request.body).toMatchObject({ reasoning_effort: 'low' })
  }
  nativeProof(await sendNativeAnswer(native, 'Reply after the return to Agent mode.', 'The restored Agent mode answered.'))

  // The model can still call the kept Plan exit tool. Agent mode raises no plan review for it, and the runtime refuses it.
  const callId = 'copilot-kept-plan-exit'
  const kept = await runNativeToolTurn(native, {
    toolCalls: [exitPlanModeToolCall(native.provider, callId, 'A plan after the return to Agent mode.')],
    prompt: 'Call the Plan exit tool in Agent mode.',
    answer: 'The Agent mode refused the Plan exit tool.',
    permissions: 'none',
  })
  expect(nativeModelToolNames(kept.toolRequest), 'Copilot keeps its Plan exit tool in Agent mode').toContain(COPILOT_TOOL.ExitPlanMode)
  nativeProof(kept.toolRequest)
  expect(nativeToolResult(kept.resultRequest, callId)).toContain(COPILOT_KEPT_MODE_TOOL_RESULT)

  await exerciseRestoredNativeOption(native, { groupId: COPILOT_OPTION.SessionMode, value: COPILOT_MODE.Interactive, nativeProof })
  await expectSettingsChip(native.page, 'Agent')
  await notice.assertStored()
  await planNotice.assertStored()
})
