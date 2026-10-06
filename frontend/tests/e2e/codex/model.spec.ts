import { expect } from '@playwright/test'
import { codexTest } from '../codex-fixtures'
import { MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, offeredSettingsOptions, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'

codexTest.describe('applies Codex session settings', () => {
  codexTest('a model switch reaches the next request', async ({ native, page }) => {
    await waitForSettingsHydrated(page)
    // Codex offers the models of the mock catalog. Select one that differs from the pinned default.
    const selectedModel = (await offeredSettingsOptions(page, 'model')).find(value => value !== 'default' && value !== MOCK_MODELS.openai)
    if (!selectedModel)
      throw new Error('The Codex model menu offers no model besides the pinned default.')
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: selectedModel,
      nativeProof: (request) => {
        expect(request.protocol).toBe('openai-responses')
        expect(request.body).toMatchObject({ model: selectedModel })
      },
    })
  })

  codexTest('the plus menu offers the settings groups the matrix lists', async ({ authenticatedCodexWorkspace, page }) => {
    void authenticatedCodexWorkspace
    await waitForSettingsHydrated(page)
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toBeVisible()
    await closeComposerMenus(page)
  })
})
