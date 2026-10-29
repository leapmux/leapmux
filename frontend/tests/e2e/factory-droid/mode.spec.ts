import type { MockModelRequestRecord } from '../helpers/mockModelScript'
import { expect } from '@playwright/test'
import { DROID_MODE } from '../../../src/generated/contracts/droid-protocol'
import { droidTest } from '../droid-fixtures'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { chooseNativeModeWithNotice } from '../helpers/nativeModeChange'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeOption, exerciseRestoredNativeOption, expectSettingsOptionsOffered } from '../helpers/nativeSettings'
import { closeComposerMenus, expectSettingsChip, openPlusMenu, settingsGroupTrigger, waitForNativeSettingsHydrated, waitForSettingsHydrated } from '../helpers/ui'
import { expectDroidNativeSettings } from './settingsUpdates'

droidTest.describe('Factory Droid settings', () => {
  droidTest('offers the model, effort and permission-mode groups', async ({ authenticatedDroidWorkspace, page }) => {
    void authenticatedDroidWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  droidTest('applies native Spec mode before and after reload', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'permissionMode',
      value: 'spec',
      nativeProof: async (request) => {
        expect(nativeModelToolNames(request)).toContain('ExitSpecMode')
        await expectDroidNativeSettings(native, { interactionMode: 'spec', autonomyLevel: 'off' })
      },
    })
  })

  droidTest('returns from Spec to Default and proves the native settings after reload', async ({ native }) => {
    await waitForNativeSettingsHydrated(native.page)
    await expectSettingsOptionsOffered(native.page, 'permissionMode', Object.values(DROID_MODE))
    await expectSettingsChip(native.page, 'Auto (High)')
    await sendNativeAnswer(native, 'Reply before the mode sequence.', 'The Droid mode sequence started.')
    for (const mode of [
      {
        value: DROID_MODE.Spec,
        label: 'Spec',
        previousLabel: 'Auto (High)',
        interactionMode: 'spec',
      },
      {
        value: DROID_MODE.Default,
        label: 'Default',
        previousLabel: 'Spec',
        interactionMode: 'auto',
      },
    ]) {
      const notice = await chooseNativeModeWithNotice(native, { groupId: 'permissionMode', ...mode, noticeLabel: 'Permissions' })
      const nativeProof = async (request: MockModelRequestRecord) => {
        expect(request.protocol).toBe('openai-chat-completions')
        // Droid advertises ExitSpecMode in Default also. Its native settings event states the active mode.
        expect(nativeModelToolNames(request)).toContain('ExitSpecMode')
        await expectDroidNativeSettings(native, { interactionMode: mode.interactionMode, autonomyLevel: 'off' })
      }
      await nativeProof(await sendNativeAnswer(native, `Reply in ${mode.label} mode.`, `Droid used ${mode.label} mode.`))
      await exerciseRestoredNativeOption(native, { groupId: 'permissionMode', value: mode.value, nativeProof })
      await expectSettingsChip(native.page, mode.label)
      await notice.assertStored()
    }
  })
})
