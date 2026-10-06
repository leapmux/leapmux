import { droidTest, expect } from '../droid-fixtures'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'
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
})
