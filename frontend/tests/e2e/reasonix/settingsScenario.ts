import type { ManagedNativeScenarioContext } from '../helpers/nativeScenario'
import { expect } from '@playwright/test'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { applyPermissionPreset, chooseSettingsOption, expectSettingsChip, openPlusMenu, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

/** Keep Reasonix's effort, mode, and approval settings through a native turn and reload. */
export async function exerciseReasonixSessionSettings(context: ManagedNativeScenarioContext): Promise<void> {
  const { page } = context
  await waitForSettingsHydrated(page)
  await chooseSettingsOption(page, 'permissionMode-plan')
  await expectSettingsChip(page, 'Plan')
  await waitForSettingsIdle(page)
  await chooseSettingsOption(page, 'effort-low')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Low')
  const request = await sendNativeAnswer(context, 'Reply once after the effort switch.', 'Reasonix answered at low effort.')
  expect(request.body).toMatchObject({ reasoning_effort: 'low' })
  const menu = await openPlusMenu(page)
  await expect(menu.getByTestId('composer-smart-permissions')).toHaveCount(0)
  await expect(menu.getByTestId('composer-bypass-permissions')).toBeVisible()
  await page.keyboard.press('Escape')
  await applyPermissionPreset(page, 'bypass')
  await waitForSettingsIdle(page)
  await exerciseRestoredNativeOption(context, {
    groupId: 'effort',
    value: 'low',
    nativeProof(restored) {
      expect(restored.body).toMatchObject({ reasoning_effort: 'low' })
    },
  })
  await expectSettingsChip(page, 'Plan')
  await expectSettingsChip(page, 'Low')
  await chooseSettingsOption(page, 'permissionMode-normal')
  await chooseSettingsOption(page, 'tool_approval-ask')
  await waitForSettingsIdle(page)
  await expectSettingsChip(page, 'Normal')
}
