import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { COPILOT_MODE, COPILOT_OPTION, COPILOT_TOOL } from '../../../src/generated/contracts/copilot-protocol'
import { copilotTest } from '../copilot-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { captureStoredNativeModeNotice, chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelToolNames, nativeOptionValue } from '../helpers/nativeScenario'
import { exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, visibleOnly, waitForNativeSettingsHydrated } from '../helpers/ui'
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
  const nativeProof = (request: Awaited<ReturnType<typeof sendNativeAnswer>>): void => {
    expect(request.protocol).toBe('openai-chat-completions')
    expect(nativeModelToolNames(request)).not.toContain(COPILOT_TOOL.ExitPlanMode)
    expect(request.body).toMatchObject({ reasoning_effort: 'low' })
  }
  nativeProof(await sendNativeAnswer(native, 'Reply after the return to Agent mode.', 'The restored Agent mode answered.'))
  await exerciseRestoredNativeOption(native, { groupId: COPILOT_OPTION.SessionMode, value: COPILOT_MODE.Interactive, nativeProof })
  await expectSettingsChip(native.page, 'Agent')
  await notice.assertStored()
  await planNotice.assertStored()
})
