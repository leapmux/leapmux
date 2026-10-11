import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelLastUserText } from '../helpers/nativeScenario'
import { exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, waitForSettingsHydrated } from '../helpers/ui'
import { opencodeTest } from '../opencode-fixtures'
import { exercisePlanAndEffort, OPENCODE_PLAN_REMINDER } from './settingsScenario'

opencodeTest('mode: keeps its Plan mode and effort after a turn and reload', async ({ native }) => {
  await exercisePlanAndEffort(native, 'mode')
})

opencodeTest('returns from Plan to Build and keeps each native mode after reload', async ({ native, page }) => {
  await waitForSettingsHydrated(page, 'primaryAgent')
  await expectSettingsOptionsOffered(page, 'primaryAgent', ['build', 'plan'])
  await expectSettingsChip(page, 'Build')

  const notices: NativeModeNotice[] = []
  for (const mode of [
    { value: 'plan', label: 'Plan', previousLabel: 'Build' },
    { value: 'build', label: 'Build', previousLabel: 'Plan' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { ...mode, groupId: 'primaryAgent', noticeLabel: 'Primary Agent' })
    notices.push(notice)
    const nativeProof = (request: MockModelRequestRecord) => {
      expect(request.protocol).toBe('openai-chat-completions')
      if (mode.value === 'plan')
        expect(nativeModelLastUserText(request)).toContain(OPENCODE_PLAN_REMINDER)
      else
        expect(nativeModelLastUserText(request)).not.toContain(OPENCODE_PLAN_REMINDER)
    }
    nativeProof(await sendNativeAnswer(native, 'Reply once in the selected OpenCode mode.', `The native ${mode.label} mode answered.`))
    await exerciseRestoredNativeOption(native, { groupId: 'primaryAgent', value: mode.value, nativeProof })
    await expectSettingsChip(page, mode.label)
    await notice.assertStored()
  }
  for (const notice of notices)
    await notice.assertStored()
})
