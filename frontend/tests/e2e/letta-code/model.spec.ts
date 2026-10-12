import { expect } from '@playwright/test'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, expectSettingsChip, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'
import { lettaTest } from '../letta-fixtures'

lettaTest.describe('Letta Code settings', () => {
  lettaTest('offers the configured model and the permission modes', async ({ authenticatedLettaWorkspace, page }) => {
    void authenticatedLettaWorkspace
    await waitForSettingsHydrated(page)
    await expectSettingsChip(page, MOCK_MODELS.letta)

    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'permissionMode')).toBeVisible()
    await closeComposerMenus(page)
  })

  lettaTest('sends a selected model on the next request and keeps it after reload', async ({ native }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: `openai/${MOCK_MODELS.openai}`,
      nativeProof: request => expect(request.body).toMatchObject({ model: MOCK_MODELS.openai }),
    })
  })
})
