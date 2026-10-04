import { DROID_E2E_SKIP_REASON, droidTest, expect } from '../droid-fixtures'
import { droidNativeSettingsUpdates } from '../helpers/droidNativeSettings'
import { nativeModelToolNames } from '../helpers/nativeScenario'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid settings', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('offers the model, effort and permission-mode groups', async ({ authenticatedDroidWorkspace, page }) => {
    void authenticatedDroidWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  droidTest('applies native Spec mode before and after reload', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
    await exerciseNativeOption(context, {
      groupId: 'permissionMode',
      value: 'spec',
      nativeProof: async (request) => {
        expect(nativeModelToolNames(request)).toContain('ExitSpecMode')
        await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, authenticatedDroidWorkspace.workspaceId)).some(update =>
          update.requestId?.startsWith('leapmux-') && update.interactionMode === 'spec' && update.autonomyLevel === 'off')).toBe(true)
      },
    })
  })
})
