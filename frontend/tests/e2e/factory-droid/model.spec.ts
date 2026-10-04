import { DROID_E2E_SKIP_REASON, DROID_TITLE_RULE, droidTest, expect } from '../droid-fixtures'
import { droidNativeSettingsUpdates } from '../helpers/droidNativeSettings'
import { DROID_MOCK_MODEL_IDS, MOCK_MODELS } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { assistantBubbles, chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { nativeContext } from './scenarios'

droidTest.describe('Factory Droid settings', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('sends a selected custom model to the mock and keeps it after reload', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)
    await waitForSettingsIdle(page)
    await expect.poll(async () => (await droidNativeSettingsUpdates(leapmuxServer, authenticatedDroidWorkspace.workspaceId)).some(update =>
      update.requestId?.startsWith('leapmux-') && update.modelId === DROID_MOCK_MODEL_IDS.alternate)).toBe(true)
    await expectSettingsOptionChosen(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)

    await modelScript.rule(DROID_TITLE_RULE)
    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Reply through the selected model.'))
    await modelScript.waitForSteps()
    await waitForAgentIdle(page)
    const request = (await modelScript.status()).requests.find(record => record.stepIndex === 0)
    expect(request?.body).toMatchObject({ model: MOCK_MODELS.droidAlt })
    await expect(assistantBubbles(page).filter({ hasText: 'The alternate model answered.' }).first()).toBeVisible()
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'model',
      value: DROID_MOCK_MODEL_IDS.alternate,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.droidAlt })
      },
    })
    await waitForSettingsHydrated(page)
    await expectSettingsOptionChosen(page, `model-${DROID_MOCK_MODEL_IDS.alternate}`)
  })
})
