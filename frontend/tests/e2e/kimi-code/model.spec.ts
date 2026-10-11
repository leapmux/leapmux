import { expect } from '@playwright/test'
import { KIMI_MOCK_MODELS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseNativeOption } from '../helpers/nativeSettings'
import { closeComposerMenus, expectNoSettingsChip, expectSettingsChip, openPlusMenu, settingsGroupTrigger } from '../helpers/ui'
import { kimiTest } from '../kimi-fixtures'

kimiTest.describe('applies Kimi Code session settings', () => {
  // The second model thinks at no level, so the effort axis leaves with it.
  kimiTest('a model switch reaches the next request and drops the effort axis', async ({ native, page }) => {
    await exerciseNativeOption(native, {
      groupId: 'model',
      value: KIMI_MOCK_MODELS.plain,
      nativeProof: (request) => {
        expect(request.body).toHaveProperty('model', MOCK_MODELS.pi)
        expect(request.body).not.toHaveProperty('reasoning_effort')
      },
    })
    // Every request of the test asked for the plain model: the selected turn and the restored turn.
    expect((await native.modelScript.status()).requests.map(request => (request.body as { model?: unknown }).model)).toEqual([MOCK_MODELS.pi, MOCK_MODELS.pi])

    await expectSettingsChip(page, 'GLM-5.3')
    await expectNoSettingsChip(page, 'GLM-5.3 Flash')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toBeVisible()
    await expect(settingsGroupTrigger(page, 'effort')).toHaveCount(0)
    await closeComposerMenus(page)
  })
})
