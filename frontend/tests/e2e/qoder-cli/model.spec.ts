import { MOCK_MODELS, QODER_ALTERNATE_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { exerciseRestoredNativeOption } from '../helpers/nativeSettings'
import { chooseSettingsOption, expectSettingsOptionChosen, sendMessage, waitForAgentIdle, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'
import { expect, QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest.describe('Qoder CLI settings', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('switches the model used by the next request', async ({ qoderWorkspace, page, modelScript, leapmuxServer }) => {
    void qoderWorkspace
    await waitForSettingsHydrated(page, 'model')
    await chooseSettingsOption(page, `model-${QODER_ALTERNATE_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${QODER_ALTERNATE_MODEL_ID}`)

    await modelScript.queue({ text: 'The alternate model answered.' })
    await sendMessage(page, modelScript.prompt('Answer with the selected model.'))
    const status = await modelScript.waitForSteps()
    await waitForAgentIdle(page)

    const request = status.requests.find(record => record.stepIndex === 0)
    expect(request?.body).toMatchObject({ model: MOCK_MODELS.qoder })
    const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: qoderWorkspace.workspaceId })
    await exerciseRestoredNativeOption(context, {
      groupId: 'model',
      value: QODER_ALTERNATE_MODEL_ID,
      nativeProof: (request) => {
        expect(request.body).toMatchObject({ model: MOCK_MODELS.qoder })
      },
    })
  })
})
