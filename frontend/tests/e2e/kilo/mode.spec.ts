import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { kiloTest } from '../kilo-fixtures'
import { exercisePlanAndEffort } from '../opencode/settingsScenario'
import { KILO_PLAN_REMINDER } from './scenarios'

kiloTest('mode: keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'mode', KILO_PLAN_REMINDER)
})

kiloTest('returns from Plan to Code and keeps each native mode after reload', async ({ native, page }) => {
  await waitForSettingsHydrated(page, 'primaryAgent')
  await expectSettingsOptionsOffered(page, 'primaryAgent', ['code', 'plan'])
  await expectSettingsChip(page, 'Code')

  const notices: NativeModeNotice[] = []
  for (const mode of [
    { value: 'plan', label: 'Plan', previousLabel: 'Code' },
    { value: 'code', label: 'Code', previousLabel: 'Plan' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { ...mode, groupId: 'primaryAgent', noticeLabel: 'Primary Agent' })
    notices.push(notice)
    const nativeProof = (request: MockModelRequestRecord) => {
      expect(request.protocol).toBe('openai-chat-completions')
      if (mode.value === 'plan')
        expect(nativeModelLastUserText(request)).toContain(KILO_PLAN_REMINDER)
      else
        expect(nativeModelLastUserText(request)).not.toContain(KILO_PLAN_REMINDER)
    }
    nativeProof(await sendNativeAnswer(native, 'Reply once in the selected Kilo mode.', `The native ${mode.label} mode answered.`))
    await exerciseRestoredNativeOption(native, { groupId: 'primaryAgent', value: mode.value, nativeProof })
    await expectSettingsChip(page, mode.label)
    await notice.assertStored()
  }
  for (const notice of notices)
    await notice.assertStored()
})
