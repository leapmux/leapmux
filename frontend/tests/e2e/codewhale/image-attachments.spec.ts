import { expect } from '@playwright/test'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseAttachmentDelivery } from '../helpers/attachmentModelProbe'
import { CODEWHALE_VISION_MODEL_ID } from '../helpers/mockAgentEnvironment'
import { chooseSettingsOption, expectSettingsOptionChosen, waitForSettingsHydrated, waitForSettingsIdle } from '../helpers/ui'

codewhaleTest.describe('Codewhale attachments', () => {
  codewhaleTest('delivers an image attachment to the model', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await waitForSettingsHydrated(page)
    await chooseSettingsOption(page, `model-${CODEWHALE_VISION_MODEL_ID}`)
    await waitForSettingsIdle(page)
    await expectSettingsOptionChosen(page, `model-${CODEWHALE_VISION_MODEL_ID}`)
    await exerciseAttachmentDelivery(page, modelScript, 'image', 'codewhale-shot.png')
    const request = (await modelScript.status()).requests.find(record => record.stepIndex === 0)
    if (!request?.body || typeof request.body !== 'object' || !('model' in request.body))
      throw new Error('the Codewhale image request must state its selected model')
    expect(request.body.model).toBe(CODEWHALE_VISION_MODEL_ID)
  })
})
