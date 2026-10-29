import type { NativeModeNotice } from '../helpers/nativeModeChange'
import { expect } from '@playwright/test'
import { cursorTest } from '../cursor-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { expectSettingsChip, offeredSettingsOptions, waitForNativeSettingsHydrated } from '../helpers/ui'
import { exerciseCursorNativePlanRPC, exerciseCursorSelectedPlanSettings } from './settingsScenario'

cursorTest('mode: keeps a selected model and Plan mode after a turn and reload', async ({ native }) => {
  await exerciseCursorSelectedPlanSettings(native, 'permissionMode')
})

cursorTest('confirms Plan mode through the native set-mode RPC', async ({ native }) => {
  await exerciseCursorNativePlanRPC(native)
})

cursorTest('returns from Plan and Ask to Agent with one notice per native mode change', async ({ native }) => {
  const { page } = native
  await waitForNativeSettingsHydrated(page)
  expect(await offeredSettingsOptions(page, 'permissionMode')).toEqual(['agent', 'plan', 'ask'])
  await expectSettingsChip(page, 'Agent')

  const notices: NativeModeNotice[] = []
  for (const change of [
    {
      value: 'plan',
      label: 'Plan',
      previous: 'Agent',
      mode: 3,
    },
    {
      value: 'ask',
      label: 'Ask',
      previous: 'Plan',
      mode: 2,
    },
    {
      value: 'agent',
      label: 'Agent',
      previous: 'Ask',
      mode: 1,
    },
  ]) {
    const notice = await chooseNativeModeWithNotice(native, {
      groupId: 'permissionMode',
      ...change,
      previousLabel: change.previous,
      noticeLabel: 'Mode',
    })
    notices.push(notice)
    const request = await sendNativeAnswer(native, `Reply in ${change.label} mode.`, `The ${change.label} mode reached the native turn.`)
    expect(request.nativeRequest).toMatchObject({ mode: change.mode })
    await notice.assertStored()
  }

  await exerciseRestoredNativeOption(native, {
    groupId: 'permissionMode',
    value: 'agent',
    nativeProof: request => expect(request.nativeRequest).toMatchObject({ mode: 1 }),
  })
  await expectSettingsChip(page, 'Agent')
  for (const notice of notices)
    await notice.assertStored()
})
