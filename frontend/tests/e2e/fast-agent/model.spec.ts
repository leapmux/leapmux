import { expect, fastAgentTest } from '../fastagent-fixtures'
import { FAST_AGENT_MOCK_MODEL } from '../helpers/mockAgentEnvironment'
import { sendNativeAnswer } from '../helpers/nativeConversation'
import { closeComposerMenus, openPlusMenu, settingsGroupTrigger, waitForSettingsHydrated } from '../helpers/ui'
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'

fastAgentTest.describe('Fast Agent settings apply', () => {
  fastAgentTest('omits a model setting while the launch model answers a turn', async ({ native, page }) => {
    await waitForSettingsHydrated(page, 'permissionMode')
    await openPlusMenu(page)
    await expect(settingsGroupTrigger(page, 'model')).toHaveCount(0)
    await closeComposerMenus(page)
    await expectMissingSetting(native, {
      feature: 'model',
      relatedProof: async () => {
        const request = await sendNativeAnswer(native, 'Reply once with the launch model.', 'The launch model answered.')
        expect(request.body).toMatchObject({ model: FAST_AGENT_MOCK_MODEL })
        expect((await native.modelScript.status()).requests.filter(record => record.stepIndex === request.stepIndex)).toHaveLength(1)
      },
    })
  })
})
