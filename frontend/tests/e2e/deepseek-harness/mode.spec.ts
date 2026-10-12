import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelInstructionText } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { expectSettingsChip, waitForNativeSettingsHydrated } from '../helpers/ui'

deepseekHarnessTest('preserves the selected native plan mode before and after reload', async ({ native: context }) => {
  await exerciseNativeOption(context, {
    groupId: 'permissionMode',
    value: 'plan',
    nativeProof: request => expect(nativeModelInstructionText(request)).toContain('You are in plan mode.'),
  })
})

deepseekHarnessTest('returns from Plan to Act and keeps each native mode after reload', async ({ native }) => {
  const { page } = native
  await waitForNativeSettingsHydrated(page)
  await expectSettingsOptionsOffered(page, 'permissionMode', ['act', 'plan'])
  await expectSettingsChip(page, 'Act')
  const notices: NativeModeNotice[] = []
  for (const change of [
    { value: 'plan', label: 'Plan', previousLabel: 'Act' },
    { value: 'act', label: 'Act', previousLabel: 'Plan' },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...change, noticeLabel: 'Mode' })
    notices.push(notice)
    const nativeProof = (request: MockModelRequestRecord) => {
      const instruction = nativeModelInstructionText(request)
      if (change.value === 'plan')
        expect(instruction).toContain('You are in plan mode.')
      else
        expect(instruction).not.toContain('You are in plan mode.')
    }
    nativeProof(await sendNativeAnswer(native, `Reply in ${change.label} mode.`, `The native ${change.label} mode answered.`))
    await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: change.value, nativeProof })
    await expectSettingsChip(page, change.label)
    await notice.assertStored()
  }
  for (const notice of notices)
    await notice.assertStored()
})
